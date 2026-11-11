package models

import "time"

// MeetingCooldown holds a user back from creating rooms until Until. It is
// set for the creator and the owner of a room that ended at its time limit.
type MeetingCooldown struct {
	ID     uint      `gorm:"primaryKey"`
	UserID uint      `gorm:"uniqueIndex;not null"`
	Until  time.Time `gorm:"not null"`
}
