package controllers

import (
	"context"
	"net/http"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/livekit/protocol/livekit"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// isBanned reports whether the user was removed from the room.
func isBanned(roomID string, userID uint) (bool, error) {
	var count int64
	err := database.DB.Model(&models.RoomBan{}).Where("room_id = ? AND user_id = ?", roomID, userID).Count(&count).Error
	return count > 0, err
}

// rejectBanned writes 403 removed_from_room for a removed caller, or 500 if
// the ban cannot be read, and reports whether the request may continue.
func rejectBanned(c *gin.Context, roomID string) bool {
	banned, err := isBanned(roomID, c.MustGet("user").(models.User).ID)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
		return false
	}
	if banned {
		apierror.Respond(c, http.StatusForbidden, apierror.RemovedFromRoom, "Bu toplantıdan çıkarıldınız ve tekrar katılamazsınız")
		return false
	}
	return true
}

// removeSessions disconnects every session of a user from the media room.
// LiveKit reports PARTICIPANT_REMOVED to each of them.
func removeSessions(ctx context.Context, svc liveKitRoomService, roomID string, sessions []*livekit.ParticipantInfo) error {
	for _, p := range sessions {
		_, err := svc.RemoveParticipant(ctx, &livekit.RoomParticipantIdentity{Room: roomID, Identity: p.Identity})
		if err != nil && !isTwirpNotFound(err) {
			return err
		}
	}
	return nil
}

// RemoveMember removes a user from the room for good: it records a ban,
// drops their permissions, releases the screen share if they hold it, and
// disconnects all their sessions. It works on users who are not connected
// too. Needs remove_members; nobody may remove the owner or themselves.
func RemoveMember(c *gin.Context) {
	release := lockRoomLifecycle(c.Param("room_id"))
	defer release()
	room, _, ok := requirePermission(c, models.PermissionRemoveMembers, false)
	if !ok {
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
	caller := c.MustGet("user").(models.User)
	protected, err := isProtectedTarget(room.RoomID, caller.ID, targetID)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
		return
	}
	if protected {
		respondTargetProtected(c)
		return
	}
	ctx, svc, ps, ok := roomParticipants(c, room)
	if !ok {
		return
	}
	sessions := sessionsOf(ps, targetID)
	holdsShare := false
	for _, p := range sessions {
		holdsShare = holdsShare || p.Identity == room.ScreenShareIdentity
	}
	if uid, err := participantUserID(room.ScreenShareIdentity); err == nil && uid == targetID {
		holdsShare = true // a reservation from a session that already left
	}
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		ban := models.RoomBan{RoomID: room.RoomID, UserID: targetID, RemovedBy: caller.ID, RemovedAt: time.Now()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&ban).Error; err != nil {
			return err
		}
		return writePermissions(tx, room.RoomID, targetID, nil, caller.ID)
	})
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Katılımcı çıkarılamadı")
		return
	}
	if holdsShare {
		for _, p := range sessions {
			if err := revokeScreenShare(ctx, svc, room.RoomID, p); err != nil {
				apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Katılımcı çıkarıldı fakat paylaşımı durdurulamadı. Tekrar deneyin.")
				return
			}
		}
		if err := saveShare(&room, "", 0); err != nil {
			apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Paylaşım durumu kaydedilemedi")
			return
		}
	}
	if err := removeSessions(ctx, svc, room.RoomID, sessions); err != nil {
		apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Katılımcı çıkarıldı fakat bağlantısı kesilemedi. Tekrar deneyin.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"identity": identity, "removed": true})
}
