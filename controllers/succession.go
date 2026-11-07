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

// Succession keeps a room with a connected owner. When the last connected
// owner leaves:
//
//  1. the connected moderator who has been a moderator longest, otherwise
//  2. a uniformly random connected participant becomes an acting owner;
//  3. if nobody is connected, the next user to join becomes acting owner.
//
// Owners who were away are suspended rather than demoted: when any of them
// rejoins they are owner again and every acting owner reverts to the role
// they had before. Acting owners who leave simply revert. All of this runs
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
	var previous string
	var unlocked bool
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		// Every active owner is away: suspend real owners, revert acting ones.
		var owners []models.RoomRole
		if err := tx.Where("room_id = ? AND role = ? AND suspended = ?", room.RoomID, models.RoleOwner, false).Find(&owners).Error; err != nil {
			return err
		}
		for _, owner := range owners {
			if owner.Acting {
				if err := revertActingOwner(tx, owner); err != nil {
					return err
				}
				continue
			}
			if err := tx.Model(&owner).Update("suspended", true).Error; err != nil {
				return err
			}
		}

		var err error
		successor, previous, err = pickSuccessor(tx, room.RoomID, connected)
		if err != nil || successor == 0 {
			return err
		}
		unlocked, err = writeRole(tx, room.RoomID, successor, models.RoleOwner, 0, true, previous)
		return err
	})
	if err != nil || successor == 0 {
		return err
	}
	return syncRole(ctx, svc, room, connected, successor, models.RoleOwner, unlocked)
}

// pickSuccessor returns the longest-serving connected moderator, otherwise a
// random connected user, otherwise 0. previous is the successor's role to
// restore later ("moderator" or "").
func pickSuccessor(tx *gorm.DB, roomID string, connected []*livekit.ParticipantInfo) (uint, string, error) {
	var moderators []models.RoomRole
	if err := tx.Where("room_id = ? AND role = ?", roomID, models.RoleModerator).Order("granted_at, user_id").Find(&moderators).Error; err != nil {
		return 0, "", err
	}
	for _, moderator := range moderators {
		if len(sessionsOf(connected, moderator.UserID)) > 0 {
			return moderator.UserID, models.RoleModerator, nil
		}
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

// revertActingOwner gives an acting owner back the role they had before.
func revertActingOwner(tx *gorm.DB, row models.RoomRole) error {
	if row.PreviousRole == models.RoleModerator {
		return tx.Model(&row).Updates(map[string]interface{}{"role": models.RoleModerator, "acting": false, "previous_role": ""}).Error
	}
	return tx.Delete(&row).Error
}

// claimOwnershipOnJoin runs when a user joins. A suspended owner becomes
// owner again and every acting owner reverts; if the room has no active
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
	returning := row != nil && row.Suspended
	if !returning && activeOwners > 0 {
		return nil
	}

	var reverted []models.RoomRole
	var unlocked bool
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		if !returning {
			previous := ""
			if row != nil && row.Role == models.RoleModerator {
				previous = models.RoleModerator
			}
			var err error
			unlocked, err = writeRole(tx, room.RoomID, userID, models.RoleOwner, 0, true, previous)
			return err
		}
		if err := tx.Model(row).Update("suspended", false).Error; err != nil {
			return err
		}
		if err := tx.Where("room_id = ? AND acting = ?", room.RoomID, true).Find(&reverted).Error; err != nil {
			return err
		}
		for _, acting := range reverted {
			if err := revertActingOwner(tx, acting); err != nil {
				return err
			}
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
	if err = syncRole(ctx, svc, room, ps, userID, models.RoleOwner, unlocked); err != nil {
		return err
	}
	for _, acting := range reverted {
		role := acting.PreviousRole
		if role == "" {
			role = models.RoleParticipant
		}
		if err = syncRole(ctx, svc, room, ps, acting.UserID, role, false); err != nil {
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
