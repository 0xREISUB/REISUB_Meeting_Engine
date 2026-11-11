package models

import "time"

// RoomBan keeps a removed user out of a room until the room is closed. It is
// keyed by user ID, so every device and session of that user is refused.
type RoomBan struct {
	ID        uint   `gorm:"primaryKey"`
	RoomID    string `gorm:"uniqueIndex:room_user_ban;not null"`
	UserID    uint   `gorm:"uniqueIndex:room_user_ban;not null"`
	RemovedBy uint   `gorm:"not null"`
	RemovedAt time.Time
}
