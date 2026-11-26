package models

import "time"

// Call rings the people asked to a meeting: a friend called, or a team
// whose meeting started. It rings until EndsAt unless it ends sooner.
type Call struct {
	ID       uint   `gorm:"primaryKey"`
	RoomID   string `gorm:"not null;index"`
	CallerID uint   `gorm:"not null"`
	// Conversation is where the call was made: "friend:a:b" or "team:n".
	Conversation string    `gorm:"not null"`
	EndsAt       time.Time `gorm:"not null"`
	// EndedAt is set once nobody's phone rings any more.
	EndedAt   *time.Time
	CreatedAt time.Time
}

// Call states of one person called.
const (
	CallRinging  = "ringing"
	CallAnswered = "answered"
	CallDeclined = "declined"
	CallMissed   = "missed"
)

// CallInvitee is one person a call rings.
type CallInvitee struct {
	ID     uint   `gorm:"primaryKey"`
	CallID uint   `gorm:"not null;uniqueIndex:call_invitee_pair"`
	UserID uint   `gorm:"not null;uniqueIndex:call_invitee_pair;index"`
	State  string `gorm:"not null"`
	// SecretHash is the SHA-256 of a secret sent only to this person's
	// devices, with which a phone may answer the call from its
	// notification without the app (and without a session token).
	SecretHash string `gorm:"not null"`
	UpdatedAt  time.Time
}
