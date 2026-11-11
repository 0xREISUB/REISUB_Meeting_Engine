package controllers

import (
	"context"
	"crypto/rand"
	"math/big"
	"net/http"
	"sort"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/livekit/protocol/livekit"
	"gorm.io/gorm"
)

// Succession keeps a room with a connected owner. When the owner leaves:
//
//  1. the connected participant with the most permissions (ties: granted
//     earliest, then joined earliest), otherwise
//  2. a uniformly random connected participant becomes the acting owner;
//  3. if nobody is connected, the next user to join becomes acting owner.
//
// An owner who was away is suspended rather than demoted: when they rejoin
// they are owner again and the acting owner gets back the permissions they
// had before. An acting owner who leaves also steps back. All of this runs
// under the room lifecycle lock and is idempotent: once a connected owner
// exists, running it again changes nothing.

// randomIndex picks a uniformly random index below n. Tests may replace it.
var randomIndex = func(n int) (int, error) {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, err
	}
	return int(v.Int64()), nil
}

// stepBack is a former acting owner whose permissions were restored.
type stepBack struct {
	userID      uint
	permissions string
}

// runSuccession appoints an acting owner if no owner remains connected.
// gone reports sessions that are leaving and must not count as connected.
func runSuccession(ctx context.Context, svc liveKitRoomService, room *models.Room, ps []*livekit.ParticipantInfo, gone func(*livekit.ParticipantInfo) bool) error {
	var connected []*livekit.ParticipantInfo
	for _, p := range ps {
		if !gone(p) {
			connected = append(connected, p)
		}
	}
	if exists, err := connectedOwnerExists(room.RoomID, connected, 0); err != nil || exists {
		return err
	}

	var successor uint
	var unlocked bool
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		// The active owner is away: suspend a real owner, an acting one steps back.
		var owners []models.RoomRole
		if err := tx.Where("room_id = ? AND role = ? AND suspended = ?", room.RoomID, models.RoleOwner, false).Find(&owners).Error; err != nil {
			return err
		}
		for _, owner := range owners {
			if owner.Acting {
				if err := restoreActingOwner(tx, owner); err != nil {
					return err
				}
				continue
			}
			if err := tx.Model(&owner).Update("suspended", true).Error; err != nil {
				return err
			}
		}

		var previous string
		var err error
		successor, previous, err = pickSuccessor(tx, room.RoomID, connected)
		if err != nil || successor == 0 {
			return err
		}
		unlocked, err = writeOwner(tx, room.RoomID, successor, 0, true, previous)
		return err
	})
	if err != nil || successor == 0 {
		return err
	}
	return syncAccess(ctx, svc, room, connected, successor, ownerAccess(), unlocked)
}

// pickSuccessor returns the connected participant with the most permissions
// (ties: granted earliest, joined earliest, lowest ID), otherwise a random
// connected user, otherwise 0. previous are the successor's permissions, to
// restore when they step back.
func pickSuccessor(tx *gorm.DB, roomID string, connected []*livekit.ParticipantInfo) (uint, string, error) {
	var granted []models.RoomRole
	if err := tx.Where("room_id = ? AND role = ? AND permissions <> ''", roomID, models.RoleParticipant).Find(&granted).Error; err != nil {
		return 0, "", err
	}
	joinedAt := func(userID uint) int64 {
		earliest := int64(0)
		for i, p := range sessionsOf(connected, userID) {
			if i == 0 || p.JoinedAt < earliest {
				earliest = p.JoinedAt
			}
		}
		return earliest
	}
	var best *models.RoomRole
	for i := range granted {
		row := &granted[i]
		if len(sessionsOf(connected, row.UserID)) == 0 {
			continue
		}
		if best == nil || moreSenior(row, best, joinedAt) {
			best = row
		}
	}
	if best != nil {
		return best.UserID, best.Permissions, nil
	}

	seen := map[uint]bool{}
	var candidates []uint
	for _, p := range connected {
		if id, err := participantUserID(p.Identity); err == nil && !seen[id] {
			seen[id] = true
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return 0, "", nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })
	i, err := randomIndex(len(candidates))
	if err != nil {
		return 0, "", err
	}
	return candidates[i], "", nil
}

// moreSenior orders succession candidates: most permissions, then granted
// earliest, then joined earliest, then lowest user ID.
func moreSenior(a, b *models.RoomRole, joinedAt func(uint) int64) bool {
	ap, bp := len(models.ParsePermissions(a.Permissions)), len(models.ParsePermissions(b.Permissions))
	switch {
	case ap != bp:
		return ap > bp
	case !a.GrantedAt.Equal(b.GrantedAt):
		return a.GrantedAt.Before(b.GrantedAt)
	case joinedAt(a.UserID) != joinedAt(b.UserID):
		return joinedAt(a.UserID) < joinedAt(b.UserID)
	}
	return a.UserID < b.UserID
}

// restoreActingOwner makes an acting owner a participant again with the
// permissions they had before.
func restoreActingOwner(tx *gorm.DB, row models.RoomRole) error {
	if row.PreviousPermissions == "" {
		return tx.Delete(&row).Error
	}
	return tx.Model(&row).Updates(map[string]interface{}{
		"role": models.RoleParticipant, "permissions": row.PreviousPermissions, "acting": false, "previous_permissions": "",
	}).Error
}

// claimOwnershipOnJoin runs when a user joins. A suspended owner becomes
// owner again and the acting owner steps back; if the room has no active
// owner at all (succession found nobody connected), the joiner becomes
// acting owner.
func claimOwnershipOnJoin(ctx context.Context, svc liveKitRoomService, room *models.Room, userID uint) error {
	row, err := roleRow(database.DB, room.RoomID, userID)
	if err != nil {
		return err
	}
	var activeOwners int64
	if err = database.DB.Model(&models.RoomRole{}).Where("room_id = ? AND role = ? AND suspended = ?", room.RoomID, models.RoleOwner, false).Count(&activeOwners).Error; err != nil {
		return err
	}
	returning := row != nil && row.Role == models.RoleOwner && row.Suspended
	if !returning && activeOwners > 0 {
		return nil
	}

	var restored []stepBack
	var unlocked bool
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		if !returning {
			previous := ""
			if row != nil && row.Role == models.RoleParticipant {
				previous = row.Permissions
			}
			var err error
			unlocked, err = writeOwner(tx, room.RoomID, userID, 0, true, previous)
			return err
		}
		if err := tx.Model(row).Update("suspended", false).Error; err != nil {
			return err
		}
		var acting []models.RoomRole
		if err := tx.Where("room_id = ? AND acting = ?", room.RoomID, true).Find(&acting).Error; err != nil {
			return err
		}
		for _, owner := range acting {
			if err := restoreActingOwner(tx, owner); err != nil {
				return err
			}
			restored = append(restored, stepBack{owner.UserID, owner.PreviousPermissions})
		}
		return nil
	})
	if err != nil {
		return err
	}

	ps, err := connectedParticipants(ctx, svc, room.RoomID)
	if err != nil {
		return err
	}
	if err = syncAccess(ctx, svc, room, ps, userID, ownerAccess(), unlocked); err != nil {
		return err
	}
	for _, back := range restored {
		if err = syncAccess(ctx, svc, room, ps, back.userID, participantAccess(back.permissions), false); err != nil {
			return err
		}
	}
	return nil
}

// LeaveRoom is called by a client that leaves on purpose. If the caller was
// the last connected owner, succession runs at once instead of waiting for
// LiveKit to report the departure.
func LeaveRoom(c *gin.Context) {
	release := lockRoomLifecycle(c.Param("room_id"))
	defer release()
	var room models.Room
	if err := database.DB.Where("room_id = ?", c.Param("room_id")).First(&room).Error; err != nil {
		apierror.Respond(c, http.StatusNotFound, apierror.RoomNotFound, "Oda bulunamadı")
		return
	}
	if room.Status != models.RoomStatusActive {
		c.JSON(http.StatusOK, gin.H{"status": "left"})
		return
	}
	ctx, svc, ps, ok := roomParticipants(c, room)
	if !ok {
		return
	}
	caller := c.MustGet("user").(models.User)
	leaving := func(p *livekit.ParticipantInfo) bool {
		id, err := participantUserID(p.Identity)
		return err == nil && id == caller.ID
	}
	if err := runSuccession(ctx, svc, &room, ps, leaving); err != nil {
		apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Yeni oda sahibi belirlenemedi. Tekrar deneyin.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "left"})
}
