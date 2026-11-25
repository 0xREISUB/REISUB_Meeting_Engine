package controllers

import (
	"net/http"
	"strconv"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// notificationsPageSize is how many new messages one poll returns at most;
// the client asks again at once for the rest.
const notificationsPageSize = 50

// conversationInfo is what a conversation is to one user: with which
// friend, or in which team, and how to name it.
type conversationInfo struct {
	Kind     string
	TargetID uint
	Title    string
}

// userConversations answers the conversations the user takes part in, by
// their key: one per friend and one per team.
func userConversations(userID uint) (map[string]conversationInfo, error) {
	result := map[string]conversationInfo{}
	var rows []models.Friendship
	if err := database.DB.Where("(user_id = ? OR friend_id = ?) AND status = ?", userID, userID, models.FriendshipAccepted).Find(&rows).Error; err != nil {
		return nil, err
	}
	friendIDs := make([]uint, 0, len(rows))
	for _, row := range rows {
		friendIDs = append(friendIDs, otherSide(row, userID))
	}
	friends, err := usersByID(friendIDs)
	if err != nil {
		return nil, err
	}
	for _, id := range friendIDs {
		if friend, ok := friends[id]; ok {
			result[friendConversation(userID, id)] = conversationInfo{Kind: "friend", TargetID: id, Title: friend.Name()}
		}
	}
	var teams []models.Team
	err = database.DB.Joins("JOIN team_members ON team_members.team_id = teams.id AND team_members.user_id = ?", userID).Find(&teams).Error
	if err != nil {
		return nil, err
	}
	for _, team := range teams {
		result[teamConversation(team.ID)] = conversationInfo{Kind: "team", TargetID: team.ID, Title: team.Name}
	}
	return result, nil
}

func conversationKeys(conversations map[string]conversationInfo) []string {
	keys := make([]string, 0, len(conversations))
	for key := range conversations {
		keys = append(keys, key)
	}
	return keys
}

// unreadCounts answers, by conversation, how many messages others sent
// that the user has not read.
func unreadCounts(userID uint, conversations []string) (map[string]int64, error) {
	result := map[string]int64{}
	if len(conversations) == 0 {
		return result, nil
	}
	var rows []struct {
		Conversation string
		Count        int64
	}
	err := database.DB.Table("social_messages AS m").
		Select("m.conversation AS conversation, COUNT(*) AS count").
		Joins("LEFT JOIN conversation_reads AS r ON r.conversation = m.conversation AND r.user_id = ?", userID).
		Where("m.conversation IN ? AND m.sender_id <> ? AND m.id > COALESCE(r.last_read_id, 0)", conversations, userID).
		Group("m.conversation").Scan(&rows).Error
	for _, row := range rows {
		result[row.Conversation] = row.Count
	}
	return result, err
}

type notificationMessage struct {
	socialMessageResponse
	// Kind is "friend" or "team", and TargetID the friend's user id or the
	// team's id: where the message was sent.
	Kind     string `json:"kind"`
	TargetID uint   `json:"target_id"`
	// Title names the conversation: the friend, or the team.
	Title string `json:"title"`
}

type notificationsResponse struct {
	// LatestID is what to send as ?after= next time.
	LatestID uint                  `json:"latest_id"`
	Messages []notificationMessage `json:"messages"`
}

// GetNotifications answers the messages others sent to the user's
// conversations since ?after=<message id>, oldest first. Without it, it
// answers only where to start, so opening the app does not announce old
// messages.
func GetNotifications(c *gin.Context) {
	user := c.MustGet("user").(models.User)
	conversations, err := userConversations(user.ID)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Bildirimler alınamadı")
		return
	}
	keys := conversationKeys(conversations)
	response := notificationsResponse{Messages: []notificationMessage{}}
	after, parseErr := strconv.ParseUint(c.Query("after"), 10, 64)
	if parseErr != nil {
		// No starting point yet: answer the latest message, announce none.
		if len(keys) > 0 {
			var latest *uint
			if err := database.DB.Model(&models.SocialMessage{}).Where("conversation IN ?", keys).Select("MAX(id)").Scan(&latest).Error; err != nil {
				apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Bildirimler alınamadı")
				return
			}
			if latest != nil {
				response.LatestID = *latest
			}
		}
		c.JSON(http.StatusOK, response)
		return
	}
	response.LatestID = uint(after)
	if len(keys) == 0 {
		c.JSON(http.StatusOK, response)
		return
	}
	var messages []models.SocialMessage
	if err := database.DB.Where("conversation IN ? AND id > ? AND sender_id <> ?", keys, after, user.ID).
		Order("id").Limit(notificationsPageSize).Find(&messages).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Bildirimler alınamadı")
		return
	}
	formatted, err := formatSocialMessages(messages, user.ID)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Bildirimler alınamadı")
		return
	}
	for i, message := range messages {
		info := conversations[message.Conversation]
		response.Messages = append(response.Messages, notificationMessage{
			socialMessageResponse: formatted[i],
			Kind:                  info.Kind,
			TargetID:              info.TargetID,
			Title:                 info.Title,
		})
		response.LatestID = message.ID
	}
	c.JSON(http.StatusOK, response)
}

type readInput struct {
	// LastID is the last message the user saw; without it, all are read.
	LastID uint `json:"last_id"`
}

// MarkConversationRead records that the user read the conversation up to
// the message they last saw. It never moves backwards.
func MarkConversationRead(c *gin.Context) {
	var input readInput
	_ = c.ShouldBindJSON(&input)
	user := c.MustGet("user").(models.User)
	conversation, ok := conversationOf(c, user, c.Param("kind"))
	if !ok {
		return
	}
	var latest *uint
	if err := database.DB.Model(&models.SocialMessage{}).Where("conversation = ?", conversation).Select("MAX(id)").Scan(&latest).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Okundu bilgisi kaydedilemedi")
		return
	}
	if latest == nil {
		c.Status(http.StatusNoContent)
		return
	}
	readTo := *latest
	if input.LastID > 0 && input.LastID < readTo {
		readTo = input.LastID
	}
	row := models.ConversationRead{UserID: user.ID, Conversation: conversation, LastReadID: readTo}
	err := database.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "conversation"}},
		DoUpdates: clause.Assignments(map[string]any{"last_read_id": gorm.Expr("MAX(last_read_id, ?)", readTo)}),
	}).Create(&row).Error
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Okundu bilgisi kaydedilemedi")
		return
	}
	// The user's other devices take the unread badge down too.
	socialChanged(user.ID)
	c.Status(http.StatusNoContent)
}
