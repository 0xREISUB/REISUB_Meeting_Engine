package controllers

import (
	"errors"
	"net/http"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
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
	var row models.RoomRole
	err := db.Where("room_id = ? AND user_id = ?", roomID, userID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
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
