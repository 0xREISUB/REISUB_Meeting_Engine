package models

import "time"

type Room struct {
	ID        uint   `gorm:"primaryKey"`
	RoomID    string `gorm:"uniqueIndex;not null"`
	Name      string `gorm:"not null"`
	OwnerID   uint   `gorm:"not null;index"`
	Status    string `gorm:"not null;default:active"`
	CreatedAt time.Time
	UpdatedAt time.Time
}
