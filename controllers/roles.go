package controllers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/livekit/protocol/livekit"
	"gorm.io/gorm"
)

// roleAttribute carries each participant's room role to every client
// through LiveKit: "owner", "moderator", or empty for a participant.
const roleAttribute = "meeting.role"

func roleRank(role string) int {
	switch role {
	case models.RoleOwner:
		return 2
	case models.RoleModerator:
		return 1
	}
	return 0
}

func roleAttributeValue(role string) string {
	if role == models.RoleParticipant {
		return ""
	}
	return role
}

// canActOn reports whether a user with role actor may moderate a user with
// role target: owners act on moderators and participants, moderators only
// on participants, and nobody acts on an owner.
func canActOn(actor, target string) bool {
	if target == models.RoleOwner {
		return false
	}
	return actor == models.RoleOwner || (actor == models.RoleModerator && target == models.RoleParticipant)
}

// roleRow returns the user's role row in the room, or nil if they have none.
func roleRow(db *gorm.DB, roomID string, userID uint) (*models.RoomRole, error) {
	// Find instead of First: a missing row is normal and must not be logged.
	var rows []models.RoomRole
	if err := db.Where("room_id = ? AND user_id = ?", roomID, userID).Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// effectiveRole is the role a user currently holds. A suspended owner has no
// powers until they rejoin.
func effectiveRole(roomID string, userID uint) (string, error) {
	row, err := roleRow(database.DB, roomID, userID)
	if err != nil {
		return "", err
	}
	if row == nil || row.Suspended {
		return models.RoleParticipant, nil
	}
	return row.Role, nil
}

// requireRoomRole loads the room named in the path and checks that the caller
// holds at least minRole in it. It writes the error response itself and
// reports whether to continue.
func requireRoomRole(c *gin.Context, minRole string, allowClosed bool) (models.Room, string, bool) {
	var room models.Room
	if err := database.DB.Where("room_id = ?", c.Param("room_id")).First(&room).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			apierror.Respond(c, http.StatusNotFound, apierror.RoomNotFound, "Oda bulunamadı")
		} else {
			apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
		}
		return room, "", false
	}
	role, err := effectiveRole(room.RoomID, c.MustGet("user").(models.User).ID)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
		return room, "", false
	}
	if roleRank(role) < roleRank(minRole) {
		if minRole == models.RoleOwner {
			apierror.Respond(c, http.StatusForbidden, apierror.OwnerRequired, "Bu işlem yalnızca oda sahiplerine açık")
		} else {
			apierror.Respond(c, http.StatusForbidden, apierror.NotRoomOwner, "Bu işlem yalnızca oda yöneticisine açık")
		}
		return room, "", false
	}
	if !allowClosed && room.Status != models.RoomStatusActive {
		apierror.Respond(c, http.StatusConflict, apierror.RoomNotActive, "Oda aktif değil")
		return room, "", false
	}
	return room, role, true
}

// respondTargetProtected rejects acting on an owner, or a moderator acting
// on another moderator.
func respondTargetProtected(c *gin.Context) {
	apierror.Respond(c, http.StatusForbidden, apierror.TargetProtected, "Bu katılımcı üzerinde işlem yapma yetkiniz yok")
}

// connectedParticipants lists the room's LiveKit participants; a media room
// that no longer exists simply has nobody connected.
func connectedParticipants(ctx context.Context, svc liveKitRoomService, roomID string) ([]*livekit.ParticipantInfo, error) {
	list, err := svc.ListParticipants(ctx, &livekit.ListParticipantsRequest{Room: roomID})
	if isTwirpNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return list.Participants, nil
}

// sessionsOf returns the connected sessions of one user.
func sessionsOf(ps []*livekit.ParticipantInfo, userID uint) []*livekit.ParticipantInfo {
	var sessions []*livekit.ParticipantInfo
	for _, p := range ps {
		if id, err := participantUserID(p.Identity); err == nil && id == userID {
			sessions = append(sessions, p)
		}
	}
	return sessions
}

// writeRole stores a role chosen by an owner (grantedBy) or by succession
// (acting). It reports whether the user's microphone lock was lifted
// because they became an owner, whom nobody may lock.
func writeRole(db *gorm.DB, roomID string, userID uint, role string, grantedBy uint, acting bool, previousRole string) (unlocked bool, err error) {
	row, err := roleRow(db, roomID, userID)
	if err != nil {
		return false, err
	}
	if role == models.RoleParticipant {
		if row != nil {
			err = db.Delete(row).Error
		}
		return false, err
	}
	if row == nil {
		row = &models.RoomRole{RoomID: roomID, UserID: userID}
	}
	// Keep the original grant time when the role itself does not change, so
	// "moderator longest" survives unrelated updates.
	if row.Role != role || row.Suspended || row.GrantedAt.IsZero() {
		row.GrantedAt = time.Now()
	}
	row.Role, row.GrantedBy, row.Suspended, row.Acting, row.PreviousRole = role, grantedBy, false, acting, previousRole
	if err = db.Save(row).Error; err != nil {
		return false, err
	}
	if role != models.RoleOwner {
		return false, nil
	}
	result := db.Model(&models.RoomMicrophoneLock{}).Where("room_id = ? AND user_id = ? AND locked = ?", roomID, userID, true).Update("locked", false)
	return result.RowsAffected > 0, result.Error
}

// syncRole pushes a user's role to every connected session: the
// meeting.role attribute, a lifted microphone lock, and, for a participant
// while participant sharing is off, the end of their screen share.
func syncRole(ctx context.Context, svc liveKitRoomService, room *models.Room, ps []*livekit.ParticipantInfo, userID uint, role string, unlocked bool) error {
	for _, p := range sessionsOf(ps, userID) {
		_, err := svc.UpdateParticipant(ctx, &livekit.UpdateParticipantRequest{Room: room.RoomID, Identity: p.Identity, Attributes: map[string]string{roleAttribute: roleAttributeValue(role)}})
		if err != nil && !isTwirpNotFound(err) {
			return err
		}
		if unlocked {
			if err := applyMicrophonePolicy(ctx, svc, *room, p, false, false); err != nil {
				return err
			}
		}
		if role == models.RoleParticipant && !room.AllowParticipantScreenShare && p.Identity == room.ScreenShareIdentity {
			if err := revokeScreenShare(ctx, svc, room.RoomID, p); err != nil {
				return err
			}
			if err := saveShare(room, "", 0); err != nil {
				return err
			}
		}
	}
	return nil
}

// connectedOwnerExists reports whether a user other than except holds an
// active owner role and has a connected session.
func connectedOwnerExists(roomID string, ps []*livekit.ParticipantInfo, except uint) (bool, error) {
	var owners []models.RoomRole
	if err := database.DB.Where("room_id = ? AND role = ? AND suspended = ?", roomID, models.RoleOwner, false).Find(&owners).Error; err != nil {
		return false, err
	}
	for _, owner := range owners {
		if owner.UserID != except && len(sessionsOf(ps, owner.UserID)) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// roomParticipants connects to LiveKit and lists the room, writing the error
// response itself on failure.
func roomParticipants(c *gin.Context, room models.Room) (context.Context, liveKitRoomService, []*livekit.ParticipantInfo, bool) {
	ctx, svc, err := connectLiveKit(c.Request.Context(), room.RoomID)
	if err != nil {
		apierror.Respond(c, http.StatusServiceUnavailable, apierror.MediaServerNotConfigured, "Toplantı sunucusu yapılandırılmamış")
		return ctx, nil, nil, false
	}
	ps, err := connectedParticipants(ctx, svc, room.RoomID)
	if err != nil {
		apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Katılımcılar sunucudan alınamadı")
		return ctx, nil, nil, false
	}
	return ctx, svc, ps, true
}

// SetParticipantRole lets an owner make any user an owner, moderator or
// participant. An owner may step down only while another owner is connected.
func SetParticipantRole(c *gin.Context) {
	release := lockRoomLifecycle(c.Param("room_id"))
	defer release()
	room, _, ok := requireRoomRole(c, models.RoleOwner, false)
	if !ok {
		return
	}
	var input struct {
		Role string `json:"role"`
	}
	if c.ShouldBindJSON(&input) != nil || roleRank(input.Role) == 0 && input.Role != models.RoleParticipant {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidRole, "Geçerli bir rol gerekli")
		return
	}
	identity := c.Param("identity")
	targetID, err := participantUserID(identity)
	if err != nil {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidIdentity, "Katılımcı kimliği geçersiz")
		return
	}
	var users int64
	if err = database.DB.Model(&models.User{}).Where("id = ?", targetID).Count(&users).Error; err != nil || users == 0 {
		apierror.Respond(c, http.StatusNotFound, apierror.ParticipantNotFound, "Kullanıcı bulunamadı")
		return
	}
	ctx, svc, ps, ok := roomParticipants(c, room)
	if !ok {
		return
	}
	caller := c.MustGet("user").(models.User)
	if targetID == caller.ID && input.Role != models.RoleOwner {
		another, err := connectedOwnerExists(room.RoomID, ps, caller.ID)
		if err != nil {
			apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
			return
		}
		if !another {
			apierror.Respond(c, http.StatusConflict, apierror.LastOwner, "Toplantıda bağlı başka bir oda sahibi kalmalı")
			return
		}
	}
	unlocked, err := writeRole(database.DB, room.RoomID, targetID, input.Role, caller.ID, false, "")
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Rol kaydedilemedi")
		return
	}
	if err = syncRole(ctx, svc, &room, ps, targetID, input.Role, unlocked); err != nil {
		apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Rol kaydedildi fakat tüm oturumlara uygulanamadı. Tekrar deneyin.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"identity": identity, "role": input.Role})
}

// TransferOwnership makes a connected participant an owner and the calling
// owner a moderator.
func TransferOwnership(c *gin.Context) {
	release := lockRoomLifecycle(c.Param("room_id"))
	defer release()
	room, _, ok := requireRoomRole(c, models.RoleOwner, false)
	if !ok {
		return
	}
	var input struct {
		Identity string `json:"identity"`
	}
	if c.ShouldBindJSON(&input) != nil {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidRequest, "Katılımcı kimliği gerekli")
		return
	}
	targetID, err := participantUserID(input.Identity)
	if err != nil {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidIdentity, "Katılımcı kimliği geçersiz")
		return
	}
	caller := c.MustGet("user").(models.User)
	if targetID == caller.ID {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidRequest, "Sahipliği kendinize devredemezsiniz")
		return
	}
	ctx, svc, ps, ok := roomParticipants(c, room)
	if !ok {
		return
	}
	connected := false
	for _, p := range ps {
		connected = connected || p.Identity == input.Identity
	}
	if !connected {
		apierror.Respond(c, http.StatusNotFound, apierror.ParticipantNotFound, "Katılımcı artık odada değil")
		return
	}
	var unlocked bool
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		if unlocked, err = writeRole(tx, room.RoomID, targetID, models.RoleOwner, caller.ID, false, ""); err != nil {
			return err
		}
		_, err = writeRole(tx, room.RoomID, caller.ID, models.RoleModerator, caller.ID, false, "")
		return err
	})
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Sahiplik devredilemedi")
		return
	}
	if err = syncRole(ctx, svc, &room, ps, targetID, models.RoleOwner, unlocked); err == nil {
		err = syncRole(ctx, svc, &room, ps, caller.ID, models.RoleModerator, false)
	}
	if err != nil {
		apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Sahiplik devredildi fakat tüm oturumlara uygulanamadı. Tekrar deneyin.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"identity": input.Identity, "role": models.RoleOwner, "caller_role": models.RoleModerator})
}
