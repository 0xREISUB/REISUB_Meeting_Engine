package controllers

import (
	"net/http"
	"strings"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
)

const (
	maxDisplayNameRunes   = 40
	maxStatusMessageRunes = 80
)

// profileResponse is the user's own profile, with what they chose rather
// than what friends see.
type profileResponse struct {
	ID            uint   `json:"id"`
	Nick          string `json:"nick"`
	DisplayName   string `json:"display_name"`
	StatusMessage string `json:"status_message"`
	Presence      string `json:"presence"`
}

func makeProfile(user models.User) profileResponse {
	presence := user.Presence
	if presence == "" {
		presence = models.PresenceOnline
	}
	return profileResponse{
		ID:            user.ID,
		Nick:          user.Nick,
		DisplayName:   user.Name(),
		StatusMessage: user.StatusMessage,
		Presence:      presence,
	}
}

// GetProfile answers the logged-in user's profile.
func GetProfile(c *gin.Context) {
	c.JSON(http.StatusOK, makeProfile(c.MustGet("user").(models.User)))
}

type profileUpdate struct {
	DisplayName   *string `json:"display_name"`
	StatusMessage *string `json:"status_message"`
	Presence      *string `json:"presence"`
}

// UpdateProfile changes the fields given; the others stay. An empty display
// name goes back to the nick.
func UpdateProfile(c *gin.Context) {
	var input profileUpdate
	if err := c.ShouldBindJSON(&input); err != nil {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidProfile, "Profil bilgileri geçersiz")
		return
	}
	user := c.MustGet("user").(models.User)
	changes := map[string]any{}
	if input.DisplayName != nil {
		name := strings.TrimSpace(*input.DisplayName)
		if len([]rune(name)) > maxDisplayNameRunes {
			apierror.Respond(c, http.StatusBadRequest, apierror.InvalidProfile, "Görünen ad en fazla 40 karakter olabilir")
			return
		}
		changes["display_name"] = name
		user.DisplayName = name
	}
	if input.StatusMessage != nil {
		status := strings.TrimSpace(*input.StatusMessage)
		if len([]rune(status)) > maxStatusMessageRunes {
			apierror.Respond(c, http.StatusBadRequest, apierror.InvalidProfile, "Durum mesajı en fazla 80 karakter olabilir")
			return
		}
		changes["status_message"] = status
		user.StatusMessage = status
	}
	if input.Presence != nil {
		switch *input.Presence {
		case models.PresenceOnline, models.PresenceBusy, models.PresenceInvisible:
		default:
			apierror.Respond(c, http.StatusBadRequest, apierror.InvalidProfile, "Geçersiz durum")
			return
		}
		changes["presence"] = *input.Presence
		user.Presence = *input.Presence
	}
	if len(changes) > 0 {
		if err := database.DB.Model(&models.User{}).Where("id = ?", user.ID).Updates(changes).Error; err != nil {
			apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Profil kaydedilemedi")
			return
		}
	}
	c.JSON(http.StatusOK, makeProfile(user))
}
