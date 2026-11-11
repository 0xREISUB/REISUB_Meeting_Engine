package models

import (
	"strings"
	"time"
)

// Per-room roles. A room has one full admin, its owner; everyone else is a
// participant with the permissions granted to them. A user without a
// RoomRole row is a participant without permissions.
const (
	RoleOwner       = "owner"
	RoleParticipant = "participant"
)

// Permissions, in their fixed wire order. The owner implicitly has all of
// them.
const (
	PermissionShareScreen      = "share_screen"
	PermissionMuteOthers       = "mute_others"
	PermissionRequestUnmute    = "request_unmute"
	PermissionAdmitMembers     = "admit_members"
	PermissionRemoveMembers    = "remove_members"
	PermissionEndMeeting       = "end_meeting"
	PermissionGrantPermissions = "grant_permissions"
)

// AllPermissions lists every permission in wire order.
var AllPermissions = []string{
	PermissionShareScreen,
	PermissionMuteOthers,
	PermissionRequestUnmute,
	PermissionAdmitMembers,
	PermissionRemoveMembers,
	PermissionEndMeeting,
	PermissionGrantPermissions,
}

// NormalizePermissions returns the known names among names, deduplicated and
// in wire order. Unknown names are ignored.
func NormalizePermissions(names []string) []string {
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[strings.TrimSpace(name)] = true
	}
	normalized := []string{}
	for _, permission := range AllPermissions {
		if wanted[permission] {
			normalized = append(normalized, permission)
		}
	}
	return normalized
}

// ParsePermissions reads a stored comma-separated permission list.
func ParsePermissions(value string) []string {
	if value == "" {
		return []string{}
	}
	return NormalizePermissions(strings.Split(value, ","))
}

// JoinPermissions is the stored and LiveKit attribute form of permissions.
func JoinPermissions(permissions []string) string {
	return strings.Join(NormalizePermissions(permissions), ",")
}

// RoomRole holds a user's role and permissions in one room. It is keyed by
// user ID, so both survive reconnects and new devices.
type RoomRole struct {
	ID     uint   `gorm:"primaryKey"`
	RoomID string `gorm:"uniqueIndex:room_user_role;not null"`
	UserID uint   `gorm:"uniqueIndex:room_user_role;not null"`
	Role   string `gorm:"not null"`
	// Permissions is a participant's comma-separated permission list in wire
	// order; empty for owners, who have all of them.
	Permissions string `gorm:"not null;default:''"`
	// GrantedAt is when the role, or a participant's first permission, was
	// granted. Succession prefers the earliest.
	GrantedAt time.Time
	// GrantedBy is the user who granted the role; 0 means the system
	// (succession or a migration).
	GrantedBy uint
	// Suspended marks an owner who was away when succession ran. A suspended
	// owner has no powers until they rejoin, when they become owner again
	// and the acting owner gets PreviousPermissions back.
	Suspended bool `gorm:"not null;default:false"`
	// Acting marks an owner appointed by succession rather than by another
	// owner. PreviousPermissions are restored when they step back.
	Acting              bool `gorm:"not null;default:false"`
	PreviousPermissions string
}
