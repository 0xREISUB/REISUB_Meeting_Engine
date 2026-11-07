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
}
