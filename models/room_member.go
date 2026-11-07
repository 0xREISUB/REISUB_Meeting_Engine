package models

import "time"

// RoomMember remembers the display name a user gave when creating or joining
// a room. Chat shows it instead of the account nick, which for guests is a
// generated "guest-..." value that must never be shown.
type RoomMember struct {
	ID          uint   `gorm:"primaryKey"`
	RoomID      string `gorm:"uniqueIndex:room_user_member;not null"`
	UserID      uint   `gorm:"uniqueIndex:room_user_member;not null"`
	DisplayName string `gorm:"not null"`
	UpdatedAt   time.Time
}
