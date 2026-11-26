package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"
	"reisub-backend/policy"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
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

// UserForToken finds the user and session this token belongs to, as
// RequireAuth does, and records their activity. False when the token is
// unknown or its session has ended.
func UserForToken(token string) (models.User, models.Session, bool) {
	if token == "" {
		return models.User{}, models.Session{}, false
	}
	hash := models.HashToken(token)
	session, ok := sessionByHash(hash)
	if !ok {
		if session, ok = adoptLegacySession(hash); !ok {
			return models.User{}, models.Session{}, false
		}
	}
	now := time.Now()
	if session.ExpiresAt != nil && !now.Before(*session.ExpiresAt) {
		return models.User{}, models.Session{}, false
	}
	var users []models.User
	if err := database.DB.Where("id = ?", session.UserID).Limit(1).Find(&users).Error; err != nil || len(users) == 0 {
		return models.User{}, models.Session{}, false
	}
	user := users[0]
	// A guest ends when their user does, whatever the session says.
	if user.IsGuest && user.TokenExpiresAt != nil && !now.Before(*user.TokenExpiresAt) {
		return models.User{}, models.Session{}, false
	}
	touchLastSeen(user, now)
	if !user.IsGuest {
		touchActive(user, now)
		extendSession(&session, now)
	}
	return user, session, true
}

func sessionByHash(hash string) (models.Session, bool) {
	var sessions []models.Session
	if err := database.DB.Where("token_hash = ?", hash).Limit(1).Find(&sessions).Error; err != nil || len(sessions) == 0 {
		return models.Session{}, false
	}
	return sessions[0], true
}

// adoptLegacySession moves a session from before users could have several
// (users.token_hash) to a Session, on its first use; it keeps its end.
func adoptLegacySession(hash string) (models.Session, bool) {
	var users []models.User
	if err := database.DB.Where("token_hash = ?", hash).Limit(1).Find(&users).Error; err != nil || len(users) == 0 {
		return models.Session{}, false
	}
	user := users[0]
	session := models.Session{UserID: user.ID, TokenHash: hash, ExpiresAt: user.TokenExpiresAt}
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&session).Error; err != nil {
			return err
		}
		return tx.Model(&models.User{}).Where("id = ? AND token_hash = ?", user.ID, hash).UpdateColumn("token_hash", "").Error
	})
	if err != nil {
		// Another request moved it at the same moment.
		return sessionByHash(hash)
	}
	return session, true
}

// NewSession signs the user in on one more device and returns its token.
// Beyond MaxSessionsPerUser the oldest sessions end.
func NewSession(db *gorm.DB, userID uint, expiresAt *time.Time) (string, error) {
	token := GenerateToken()
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&models.Session{UserID: userID, TokenHash: models.HashToken(token), ExpiresAt: expiresAt}).Error; err != nil {
			return err
		}
		var old []uint
		if err := tx.Model(&models.Session{}).Where("user_id = ?", userID).Order("id DESC").Offset(models.MaxSessionsPerUser).Pluck("id", &old).Error; err != nil {
			return err
		}
		return EndSessions(tx, old)
	})
	return token, err
}

// EndSessions deletes the sessions and the push devices that go with them.
func EndSessions(db *gorm.DB, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	if err := db.Where("session_id IN ?", ids).Delete(&models.PushDevice{}).Error; err != nil {
		return err
	}
	return db.Where("id IN ?", ids).Delete(&models.Session{}).Error
}

// GenerateToken makes a new session token: 256 random bits, in hex.
func GenerateToken() string {
	bytes := make([]byte, 32)
	// crypto/rand.Read never returns an error on supported platforms.
	_, _ = rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// defaultAccountTokenTTL is how long an account session lasts unused.
const defaultAccountTokenTTL = 30 * 24 * time.Hour

// AccountTokenTTL is how long an account session lasts unused:
// ACCOUNT_TOKEN_TTL (a Go duration), else 30 days.
func AccountTokenTTL() time.Duration {
	if ttl, err := time.ParseDuration(os.Getenv("ACCOUNT_TOKEN_TTL")); err == nil && ttl > 0 {
		return ttl
	}
	return defaultAccountTokenTTL
}

// sessionRenewal is how often a session in use has its end moved on.
const sessionRenewal = 24 * time.Hour

// extendSession moves an account session's end on while it is used, at most
// daily, so only sessions left unused for AccountTokenTTL end. Sessions from
// before sessions ended get their end here.
func extendSession(session *models.Session, now time.Time) {
	ttl := AccountTokenTTL()
	if session.ExpiresAt != nil && session.ExpiresAt.After(now.Add(ttl-sessionRenewal)) {
		return
	}
	until := now.Add(ttl)
	if err := database.DB.Model(&models.Session{}).Where("id = ?", session.ID).UpdateColumn("expires_at", until).Error; err != nil {
		log.Printf("warning: could not extend session %d: %v", session.ID, err)
		return
	}
	session.ExpiresAt = &until
}

// RequireAuth rejects requests without a valid session token (HTTP 401,
// which clients treat as "session expired") and stores the user under
// "user" and their session under "session".
func RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := BearerToken(c)

		if token == "" {
			apierror.Abort(c, http.StatusUnauthorized, apierror.AuthRequired, "Kimlik doğrulaması gerekli")
			return
		}

		user, session, ok := UserForToken(token)
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
		c.Set("session", session)
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
