package models

import "time"

// Session is one signed-in device: a user may have several at once. Only
// the SHA-256 of its token is kept (see HashToken).
type Session struct {
	ID        uint   `gorm:"primaryKey"`
	UserID    uint   `gorm:"not null;index"`
	TokenHash string `gorm:"not null;uniqueIndex"`
	// ExpiresAt ends the session; account sessions have it moved on while
	// they are used. Nil never ends.
	ExpiresAt *time.Time
	CreatedAt time.Time
}

// MaxSessionsPerUser is how many devices stay signed in at once; signing in
// on one more ends the oldest.
const MaxSessionsPerUser = 10

// PushDevice is where a session's device receives push notifications (an
// FCM registration token). It ends with its session.
type PushDevice struct {
	ID        uint   `gorm:"primaryKey"`
	SessionID uint   `gorm:"not null;uniqueIndex"`
	UserID    uint   `gorm:"not null;index"`
	Token     string `gorm:"not null;uniqueIndex"`
	// Kind is what Token is: "fid", a Firebase Installation ID (apps from
	// now on), or "token", an FCM registration token (older apps).
	Kind string `gorm:"not null;default:'token'"`
	// Platform is "android"; the only one with push for now.
	Platform  string `gorm:"not null"`
	UpdatedAt time.Time
}
