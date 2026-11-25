package controllers

import (
	"fmt"
	"time"

	"reisub-backend/database"
	"reisub-backend/models"
)

// presenceTimeout is how long after their last request friends still see
// a user as around. Clients refresh the social overview more often than
// this while open.
const presenceTimeout = 45 * time.Second

// Presence as friends see it.
const (
	presenceOnline    = "online"
	presenceBusy      = "busy"
	presenceInMeeting = "in_meeting"
	presenceOffline   = "offline"
)

// userSummary is how a user shows to their friends and teams.
type userSummary struct {
	ID            uint   `json:"id"`
	Nick          string `json:"nick"`
	DisplayName   string `json:"display_name"`
	StatusMessage string `json:"status_message"`
	Presence      string `json:"presence"`
	AvatarURL     string `json:"avatar_url"`
	// LastSeenSeconds is how long ago an offline user was last around;
	// absent when unknown, when they hide, or when they are around.
	LastSeenSeconds *int64 `json:"last_seen_seconds,omitempty"`
	// Unread counts the friend's messages the user has not read; only in
	// the user's own friend list.
	Unread int64 `json:"unread,omitempty"`
}

// visiblePresence is what others see of a user: hidden users and users who
// went quiet are offline; a user in a meeting shows so, unless busy.
func visiblePresence(user models.User, inMeeting bool, now time.Time) string {
	if user.Presence == models.PresenceInvisible {
		return presenceOffline
	}
	if inMeeting {
		return presenceInMeeting
	}
	if user.ActiveAt == nil || now.Sub(*user.ActiveAt) > presenceTimeout {
		return presenceOffline
	}
	if user.Presence == models.PresenceBusy {
		return presenceBusy
	}
	return presenceOnline
}

func summarize(user models.User, inMeeting map[uint]bool, now time.Time) userSummary {
	summary := userSummary{
		ID:            user.ID,
		Nick:          user.Nick,
		DisplayName:   user.Name(),
		StatusMessage: user.StatusMessage,
		Presence:      visiblePresence(user, inMeeting[user.ID], now),
		AvatarURL:     avatarURL(user.AvatarFile),
	}
	if summary.Presence == presenceOffline && user.Presence != models.PresenceInvisible && user.ActiveAt != nil {
		seconds := int64(now.Sub(*user.ActiveAt).Seconds())
		summary.LastSeenSeconds = &seconds
	}
	return summary
}

// usersInMeeting reports which of the given users are connected to an
// active room right now.
func usersInMeeting(userIDs []uint) (map[uint]bool, error) {
	result := map[uint]bool{}
	if len(userIDs) == 0 {
		return result, nil
	}
	var ids []uint
	err := database.DB.Model(&models.MeetingPresence{}).
		Joins("JOIN rooms ON rooms.room_id = meeting_presences.room_id AND rooms.status = ?", models.RoomStatusActive).
		Where("meeting_presences.user_id IN ?", userIDs).
		Distinct().Pluck("meeting_presences.user_id", &ids).Error
	for _, id := range ids {
		result[id] = true
	}
	return result, err
}

// friendship returns the row between two users, in either direction, or
// nil when there is none.
func friendship(a, b uint) (*models.Friendship, error) {
	var rows []models.Friendship
	err := database.DB.Where("(user_id = ? AND friend_id = ?) OR (user_id = ? AND friend_id = ?)", a, b, b, a).
		Limit(1).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

func areFriends(a, b uint) (bool, error) {
	row, err := friendship(a, b)
	return row != nil && row.Status == models.FriendshipAccepted, err
}

// friendConversation names the chat between two users, the same from
// either side.
func friendConversation(a, b uint) string {
	if a > b {
		a, b = b, a
	}
	return fmt.Sprintf("friend:%d:%d", a, b)
}

func teamConversation(teamID uint) string {
	return fmt.Sprintf("team:%d", teamID)
}

// teamMembership returns the user's membership of the team, or nil.
func teamMembership(teamID, userID uint) (*models.TeamMember, error) {
	var rows []models.TeamMember
	err := database.DB.Where("team_id = ? AND user_id = ?", teamID, userID).Limit(1).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}
