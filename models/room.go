package models

import "time"

// Room lifecycle states stored in Room.Status.
const (
	RoomStatusCreating = "creating"
	RoomStatusActive   = "active"
	RoomStatusClosing  = "closing"
	RoomStatusClosed   = "closed"
)

type Room struct {
	ID                          uint   `gorm:"primaryKey"`
	RoomID                      string `gorm:"uniqueIndex;not null"`
	Name                        string `gorm:"not null"`
	OwnerID                     uint   `gorm:"not null;index"`
	Status                      string `gorm:"not null;default:active"`
	CreatedAt                   time.Time
	UpdatedAt                   time.Time
	AllowParticipantScreenShare bool `gorm:"not null;default:false"`
	// ScreenShareIdentity is the LiveKit identity holding the room's single
	// screen-share slot; empty when nobody shares.
	ScreenShareIdentity string
	// ScreenShareLeaseUntil (Unix seconds) is the grace period for the holder
	// to start publishing before the reservation counts as abandoned.
	ScreenShareLeaseUntil int64
}
