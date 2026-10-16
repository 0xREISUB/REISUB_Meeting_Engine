package models

// RoomMicrophoneLock follows an authenticated user across room reconnects.
type RoomMicrophoneLock struct {
	ID     uint   `gorm:"primaryKey"`
	RoomID string `gorm:"uniqueIndex:room_user_microphone;not null"`
	UserID uint   `gorm:"uniqueIndex:room_user_microphone;not null"`
	Locked bool   `gorm:"not null"`
}
