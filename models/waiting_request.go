package models

import "time"

// Waiting request states.
const (
	WaitingStatusWaiting  = "waiting"
	WaitingStatusAdmitted = "admitted"
	WaitingStatusRejected = "rejected"
)

// WaitingRequest is a user asking to be let into a room whose waiting room
// is on. A user has at most one request per room. Requests whose requester
// stopped polling expire; all of them end with the room.
type WaitingRequest struct {
	ID        uint   `gorm:"primaryKey"`
	RequestID string `gorm:"uniqueIndex;not null"`
	RoomID    string `gorm:"uniqueIndex:room_user_waiting;not null"`
	UserID    uint   `gorm:"uniqueIndex:room_user_waiting;not null"`
	// Name is the display name given when asking to join.
	Name   string `gorm:"not null"`
	Status string `gorm:"not null"`
	// LastPolledAt is refreshed whenever the requester asks to join or polls.
	LastPolledAt time.Time `gorm:"not null;index"`
	CreatedAt    time.Time
}
