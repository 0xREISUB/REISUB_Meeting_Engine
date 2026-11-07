package models

import "time"

// Per-room roles. A user without a RoomRole row is a plain participant.
const (
	RoleOwner       = "owner"
	RoleModerator   = "moderator"
	RoleParticipant = "participant"
)

// RoomRole grants a user a role in one room. It is keyed by user ID, so the
// role survives reconnects and new devices.
type RoomRole struct {
	ID        uint   `gorm:"primaryKey"`
	RoomID    string `gorm:"uniqueIndex:room_user_role;not null"`
	UserID    uint   `gorm:"uniqueIndex:room_user_role;not null"`
	Role      string `gorm:"not null"`
	GrantedAt time.Time
	// GrantedBy is the user who granted the role; 0 means the system
	// (succession or the single-owner migration).
	GrantedBy uint
	// Suspended marks an owner who was away when succession ran. A suspended
	// owner has no powers until they rejoin, when they become owner again
	// and every acting owner reverts to PreviousRole.
	Suspended bool `gorm:"not null;default:false"`
	// Acting marks an owner appointed by succession rather than by another
	// owner. PreviousRole ("moderator" or "") is restored when a suspended
	// owner returns.
	Acting       bool `gorm:"not null;default:false"`
	PreviousRole string
}
