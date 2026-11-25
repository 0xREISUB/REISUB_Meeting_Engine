package models

import "time"

// GuestNickPrefix starts every guest nick; registration may not use it.
const GuestNickPrefix = "guest-"

type User struct {
	ID       uint   `gorm:"primaryKey"`
	Nick     string `gorm:"uniqueIndex;not null"`
	Password string `gorm:"not null"`
	Token    string `gorm:"index"`
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
