package models

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// GuestNickPrefix starts every guest nick; registration may not use it.
const GuestNickPrefix = "guest-"

type User struct {
	ID       uint   `gorm:"primaryKey"`
	Nick     string `gorm:"uniqueIndex;not null"`
	Password string `gorm:"not null"`
	// TokenHash is the SHA-256 of the user's session token (see HashToken);
	// the token itself is never stored. Empty without a session.
	TokenHash string `gorm:"index"`
	// IsGuest marks an anonymous session created by POST /guest. Guests have
	// no password and can never log in; their nick is never shown to others.
	IsGuest bool `gorm:"not null;default:false;index"`
	// TokenExpiresAt ends a guest token; nil means the token does not expire
	// (login tokens).
	TokenExpiresAt *time.Time
	// LastSeenAt is refreshed at most hourly by authenticated requests and
	// drives the cleanup of inactive guests.
	LastSeenAt *time.Time
	CreatedAt  time.Time

	// DisplayName is how the user shows to friends and teams; empty means
	// the nick.
	DisplayName string `gorm:"not null;default:''"`
	// StatusMessage is a short line shown under the name.
	StatusMessage string `gorm:"not null;default:''"`
	// Presence is what the user chose to show: PresenceOnline, PresenceBusy
	// or PresenceInvisible. Whether they are actually around comes from
	// ActiveAt.
	Presence string `gorm:"not null;default:'online'"`
	// ActiveAt is refreshed every few seconds by authenticated requests;
	// friends see a user as offline once it is older than a short timeout.
	ActiveAt *time.Time
	// AvatarFile is the profile picture's file name in the avatar directory;
	// empty without one.
	AvatarFile string `gorm:"not null;default:''"`
}

// Presence a user can choose.
const (
	PresenceOnline    = "online"
	PresenceBusy      = "busy"
	PresenceInvisible = "invisible"
)

// Name is how the user shows to others: the display name, or the nick.
func (u User) Name() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Nick
}

// HashToken is how a session token is stored and looked up: its SHA-256.
// Tokens are 256 random bits, so a fast hash is enough; a stolen database
// gives no usable session.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
