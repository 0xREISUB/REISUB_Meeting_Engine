package middleware

import (
	"log"
	"net/http"
	"strings"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

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
func RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := BearerToken(c)

		if token == "" {
			apierror.Abort(c, http.StatusUnauthorized, apierror.AuthRequired, "Kimlik doğrulaması gerekli")
			return
		}

		var users []models.User
		if err := database.DB.Where("token = ?", token).Limit(1).Find(&users).Error; err != nil || len(users) == 0 {
			apierror.Abort(c, http.StatusUnauthorized, apierror.InvalidToken, "Geçersiz token")
			return
		}
		user := users[0]
		now := time.Now()
		// Guest tokens expire; the client then creates a new guest session.
		if user.TokenExpiresAt != nil && !now.Before(*user.TokenExpiresAt) {
			apierror.Abort(c, http.StatusUnauthorized, apierror.InvalidToken, "Geçersiz token")
			return
		}
		touchLastSeen(user, now)

		c.Set("user", user)
		c.Next()
	}
}

// lastSeenResolution limits LastSeenAt writes to one per user per hour, so
// clients polling every two seconds do not write on every request.
const lastSeenResolution = time.Hour

func touchLastSeen(user models.User, now time.Time) {
	if user.LastSeenAt != nil && now.Sub(*user.LastSeenAt) < lastSeenResolution {
		return
	}
	if err := database.DB.Model(&models.User{}).Where("id = ?", user.ID).UpdateColumn("last_seen_at", now).Error; err != nil {
		log.Printf("warning: could not record activity for user %d: %v", user.ID, err)
	}
}
