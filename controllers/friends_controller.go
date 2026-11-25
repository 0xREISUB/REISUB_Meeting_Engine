package controllers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
)

type friendRequestInput struct {
	Nick string `json:"nick" binding:"required"`
}

// SendFriendRequest asks the user with the given nick to be friends. If
// they had already asked this user, the two become friends at once.
func SendFriendRequest(c *gin.Context) {
	var input friendRequestInput
	if err := c.ShouldBindJSON(&input); err != nil || strings.TrimSpace(input.Nick) == "" {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidRequest, "Kullanıcı adı gerekli")
		return
	}
	user := c.MustGet("user").(models.User)
	var targets []models.User
	if err := database.DB.Where("nick = ? AND is_guest = ?", strings.TrimSpace(input.Nick), false).Limit(1).Find(&targets).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Kullanıcı aranamadı")
		return
	}
	if len(targets) == 0 {
		apierror.Respond(c, http.StatusNotFound, apierror.UserNotFound, "Kullanıcı bulunamadı")
		return
	}
	target := targets[0]
	if target.ID == user.ID {
		apierror.Respond(c, http.StatusBadRequest, apierror.CannotAddSelf, "Kendinizi arkadaş olarak ekleyemezsiniz")
		return
	}
	existing, err := friendship(user.ID, target.ID)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Arkadaşlık okunamadı")
		return
	}
	switch {
	case existing == nil:
		row := models.Friendship{UserID: user.ID, FriendID: target.ID, Status: models.FriendshipPending}
		if err := database.DB.Create(&row).Error; err != nil {
			apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "İstek gönderilemedi")
			return
		}
		c.JSON(http.StatusCreated, gin.H{"status": models.FriendshipPending})
	case existing.Status == models.FriendshipAccepted:
		apierror.Respond(c, http.StatusConflict, apierror.AlreadyFriends, "Zaten arkadaşsınız")
	case existing.UserID == user.ID:
		apierror.Respond(c, http.StatusConflict, apierror.RequestPending, "İstek zaten gönderildi")
	default:
		// They asked first: asking back says yes.
		if err := acceptFriendship(existing); err != nil {
			apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "İstek kabul edilemedi")
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": models.FriendshipAccepted})
	}
}

func acceptFriendship(row *models.Friendship) error {
	now := time.Now()
	return database.DB.Model(row).Updates(map[string]any{"status": models.FriendshipAccepted, "accepted_at": now}).Error
}

// pendingRequestTo finds the pending request :request_id sent to the user.
// It writes the error response itself.
func pendingRequestTo(c *gin.Context, userID uint) (*models.Friendship, bool) {
	id, err := strconv.ParseUint(c.Param("request_id"), 10, 64)
	if err != nil {
		apierror.Respond(c, http.StatusNotFound, apierror.FriendRequestNotFound, "Arkadaşlık isteği bulunamadı")
		return nil, false
	}
	var rows []models.Friendship
	if err := database.DB.Where("id = ? AND friend_id = ? AND status = ?", id, userID, models.FriendshipPending).Limit(1).Find(&rows).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Arkadaşlık okunamadı")
		return nil, false
	}
	if len(rows) == 0 {
		apierror.Respond(c, http.StatusNotFound, apierror.FriendRequestNotFound, "Arkadaşlık isteği bulunamadı")
		return nil, false
	}
	return &rows[0], true
}

func AcceptFriendRequest(c *gin.Context) {
	user := c.MustGet("user").(models.User)
	row, ok := pendingRequestTo(c, user.ID)
	if !ok {
		return
	}
	if err := acceptFriendship(row); err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "İstek kabul edilemedi")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": models.FriendshipAccepted})
}

func DeclineFriendRequest(c *gin.Context) {
	user := c.MustGet("user").(models.User)
	row, ok := pendingRequestTo(c, user.ID)
	if !ok {
		return
	}
	if err := database.DB.Delete(row).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "İstek reddedilemedi")
		return
	}
	c.Status(http.StatusNoContent)
}

// RemoveFriend ends a friendship, or takes back a request the user sent.
func RemoveFriend(c *gin.Context) {
	user := c.MustGet("user").(models.User)
	otherID, err := strconv.ParseUint(c.Param("user_id"), 10, 64)
	if err != nil {
		apierror.Respond(c, http.StatusNotFound, apierror.NotFriends, "Arkadaş değilsiniz")
		return
	}
	row, err := friendship(user.ID, uint(otherID))
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Arkadaşlık okunamadı")
		return
	}
	// Someone else's request is answered with decline, not removed here.
	if row == nil || (row.Status == models.FriendshipPending && row.UserID != user.ID) {
		apierror.Respond(c, http.StatusNotFound, apierror.NotFriends, "Arkadaş değilsiniz")
		return
	}
	if err := database.DB.Delete(row).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Arkadaşlık silinemedi")
		return
	}
	c.Status(http.StatusNoContent)
}
