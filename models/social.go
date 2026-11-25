package models

import "time"

// Friendship status.
const (
	FriendshipPending  = "pending"
	FriendshipAccepted = "accepted"
)

// Friendship links two users. While pending, UserID asked FriendID; once
// accepted it holds both ways. There is at most one row per pair, in either
// direction.
type Friendship struct {
	ID         uint   `gorm:"primaryKey"`
	UserID     uint   `gorm:"uniqueIndex:friendship_pair;not null;index"`
	FriendID   uint   `gorm:"uniqueIndex:friendship_pair;not null;index"`
	Status     string `gorm:"not null"`
	CreatedAt  time.Time
	AcceptedAt *time.Time
}

// Team member roles.
const (
	TeamRoleOwner  = "owner"
	TeamRoleAdmin  = "admin"
	TeamRoleMember = "member"
)

// Team is a group of users with its own chat and meeting room.
type Team struct {
	ID      uint   `gorm:"primaryKey"`
	Name    string `gorm:"not null"`
	Color   string `gorm:"not null;default:''"`
	OwnerID uint   `gorm:"not null;index"`
	// RoomID is the code of the team's latest meeting room; a closed room is
	// replaced by a new one when a meeting starts again.
	RoomID    string `gorm:"not null;default:''"`
	CreatedAt time.Time
}

type TeamMember struct {
	ID       uint   `gorm:"primaryKey"`
	TeamID   uint   `gorm:"uniqueIndex:team_user;not null;index"`
	UserID   uint   `gorm:"uniqueIndex:team_user;not null;index"`
	Role     string `gorm:"not null"`
	JoinedAt time.Time
}

// SocialMessage is a message between two friends or within a team.
// Conversation is "friend:<lower id>:<higher id>" or "team:<id>".
type SocialMessage struct {
	ID           uint   `gorm:"primaryKey"`
	Conversation string `gorm:"not null;index:idx_social_conversation_id,priority:1"`
	SenderID     uint   `gorm:"not null"`
	Content      string `gorm:"not null"`
	// RoomID invites to a meeting when set: the message is a call.
	RoomID    string `gorm:"not null;default:''"`
	CreatedAt time.Time
}

// MeetingPresence records that a user is connected to an active room's
// media session. The LiveKit webhook rewrites a room's rows on every join
// and leave.
type MeetingPresence struct {
	ID     uint   `gorm:"primaryKey"`
	RoomID string `gorm:"uniqueIndex:meeting_presence_pair;not null;index"`
	UserID uint   `gorm:"uniqueIndex:meeting_presence_pair;not null;index"`
}
