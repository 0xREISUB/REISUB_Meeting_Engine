package models

import "time"

type User struct {
	ID        uint   `gorm:"primaryKey"`
	Nick      string `gorm:"uniqueIndex;not null"`
	Password  string `gorm:"not null"`
	Token     string
	CreatedAt time.Time
}
