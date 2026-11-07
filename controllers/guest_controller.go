package controllers

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
)

// defaultGuestTokenTTL is how long a guest token stays valid (GUEST_TOKEN_TTL).
const defaultGuestTokenTTL = 30 * 24 * time.Hour

// envDuration reads a Go duration such as "720h" from the environment,
// falling back to def when it is unset or invalid.
func envDuration(key string, def time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return def
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		log.Printf("warning: invalid %s=%q, using %s", key, value, def)
		return def
	}
	return d
}

// CreateGuest starts an anonymous session: a new guest user with a random
// nick, no password and a token that expires after GUEST_TOKEN_TTL. The token
// works for every /rooms endpoint like a login token.
func CreateGuest(c *gin.Context) {
	suffix := make([]byte, 8)
	_, _ = rand.Read(suffix)
	now := time.Now()
	expires := now.Add(envDuration("GUEST_TOKEN_TTL", defaultGuestTokenTTL))
	user := models.User{
		Nick:           models.GuestNickPrefix + hex.EncodeToString(suffix),
		Password:       "",
		Token:          generateToken(),
		IsGuest:        true,
		TokenExpiresAt: &expires,
		LastSeenAt:     &now,
	}
	if err := database.DB.Create(&user).Error; err != nil {
		// Never 404/405: clients read those as "this server has no guests".
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Misafir oturumu oluşturulamadı")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"token": user.Token, "guest": true})
}

// defaultGuestInactiveAfter is how long an unused guest is kept
// (GUEST_INACTIVE_AFTER).
const defaultGuestInactiveAfter = 14 * 24 * time.Hour

// StartGuestCleanup deletes inactive or expired guests now and then hourly.
func StartGuestCleanup() {
	inactiveAfter := envDuration("GUEST_INACTIVE_AFTER", defaultGuestInactiveAfter)
	go func() {
		for {
			deleted, err := database.DeleteInactiveGuests(database.DB, time.Now().Add(-inactiveAfter))
			if err != nil {
				log.Printf("warning: guest cleanup failed: %v", err)
			} else if deleted > 0 {
				log.Printf("guest cleanup: deleted %d inactive guest(s)", deleted)
			}
			time.Sleep(time.Hour)
		}
	}()
}
