package middleware

import (
	"log"
	"net/http"
	"strings"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"
	"reisub-backend/policy"

	"github.com/gin-gonic/gin"
)

// BearerToken returns the session token from the Authorization header. The
// "Bearer " prefix is optional and case-insensitive.
func BearerToken(c *gin.Context) string {
	token := strings.TrimSpace(c.GetHeader("Authorization"))
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		token = strings.TrimSpace(token[len("Bearer "):])
	}
	return token
}

// RequireAuth rejects requests without a valid session token (HTTP 401,
// which clients treat as "session expired") and stores the user under "user".
// UserForToken finds the user whose session token this is, as RequireAuth
// does, and records their activity. False when the token is unknown or
// expired.
func UserForToken(token string) (models.User, bool) {
	if token == "" {
		return models.User{}, false
	}
	var users []models.User
	if err := database.DB.Where("token = ?", token).Limit(1).Find(&users).Error; err != nil || len(users) == 0 {
		return models.User{}, false
	}
	user := users[0]
	now := time.Now()
	if user.TokenExpiresAt != nil && !now.Before(*user.TokenExpiresAt) {
		return models.User{}, false
	}
	touchLastSeen(user, now)
	if !user.IsGuest {
		touchActive(user, now)
	}
	return user, true
}

func RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := BearerToken(c)

		if token == "" {
			apierror.Abort(c, http.StatusUnauthorized, apierror.AuthRequired, "Kimlik doğrulaması gerekli")
			return
		}

		user, ok := UserForToken(token)
		if !ok {
			apierror.Abort(c, http.StatusUnauthorized, apierror.InvalidToken, "Geçersiz token")
			return
		}
		if user.IsGuest {
			if allowed, _ := policy.GuestsAllowed(); !allowed {
				respondGuestsNotAllowed(c)
				return
			}
		}

		c.Set("user", user)
		c.Next()
	}
}

func respondGuestsNotAllowed(c *gin.Context) {
	apierror.Abort(c, http.StatusForbidden, apierror.GuestsNotAllowed, "Bu sunucu hesap gerektiriyor; lütfen giriş yapın")
}

// RequireGuestsAllowed refuses the request with 403 guests_not_allowed while
// the server does not allow guests.
func RequireGuestsAllowed() gin.HandlerFunc {
	return func(c *gin.Context) {
		if allowed, _ := policy.GuestsAllowed(); !allowed {
			respondGuestsNotAllowed(c)
			return
		}
		c.Next()
	}
}

// lastSeenResolution limits LastSeenAt writes to one per user per hour, so
// clients polling every two seconds do not write on every request.
const lastSeenResolution = time.Hour

// activeResolution is how often ActiveAt is written, well inside the
// timeout after which friends see a user as offline.
const activeResolution = 20 * time.Second

// touchActive records that the user is around, for their friends.
func touchActive(user models.User, now time.Time) {
	if user.ActiveAt != nil && now.Sub(*user.ActiveAt) < activeResolution {
		return
	}
	if err := database.DB.Model(&models.User{}).Where("id = ?", user.ID).UpdateColumn("active_at", now).Error; err != nil {
		log.Printf("warning: could not record activity for user %d: %v", user.ID, err)
	}
}

// RequireAccount keeps guests out: friends, teams and profiles belong to
// accounts. It runs after RequireAuth.
func RequireAccount() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.MustGet("user").(models.User).IsGuest {
			apierror.Abort(c, http.StatusForbidden, apierror.AccountRequired, "Bu özellik için giriş yapmalısınız")
			return
		}
		c.Next()
	}
}

func touchLastSeen(user models.User, now time.Time) {
	if user.LastSeenAt != nil && now.Sub(*user.LastSeenAt) < lastSeenResolution {
		return
	}
	if err := database.DB.Model(&models.User{}).Where("id = ?", user.ID).UpdateColumn("last_seen_at", now).Error; err != nil {
		log.Printf("warning: could not record activity for user %d: %v", user.ID, err)
	}
}
