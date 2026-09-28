package models

import "time"

type ChatMessage struct {
	ID        uint      `gorm:"primaryKey"`
	RoomID    string    `gorm:"not null;index:idx_chat_room_created"`
	SenderID  uint      `gorm:"not null;index"`
	Sender    string    `gorm:"not null"`
	Content   string    `gorm:"not null"`
	CreatedAt time.Time `gorm:"index:idx_chat_room_created"`
}
