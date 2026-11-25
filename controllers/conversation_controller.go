package controllers

import (
	"net/http"
	"strconv"
	"strings"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
)

const (
	maxSocialMessageRunes = 2000
	// socialPageSize is how many of the latest messages a conversation
	// opens with, and how many newer ones one poll returns at most.
	socialPageSize = 100
)

type socialMessageResponse struct {
	ID       uint   `json:"id"`
	SenderID uint   `json:"sender_id"`
	Sender   string `json:"sender"`
	Content  string `json:"content"`
	// RoomID makes the message an invitation to that meeting room.
	RoomID    string `json:"room_id,omitempty"`
	CreatedAt string `json:"created_at"`
	IsMine    bool   `json:"is_mine"`
}

// conversationOf resolves kind ("friend" or "team") and :id to a
// conversation the user may read and write: a friend's or one of their
// teams'. It writes the error response itself.
func conversationOf(c *gin.Context, user models.User, kind string) (string, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	switch {
	case err != nil:
	case kind == "friend":
		friends, err := areFriends(user.ID, uint(id))
		if err != nil {
			apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Arkadaşlık okunamadı")
			return "", false
		}
		if friends {
			return friendConversation(user.ID, uint(id)), true
		}
		apierror.Respond(c, http.StatusForbidden, apierror.NotFriends, "Arkadaş değilsiniz")
		return "", false
	case kind == "team":
		membership, err := teamMembership(uint(id), user.ID)
		if err != nil {
			apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Takım okunamadı")
			return "", false
		}
		if membership != nil {
			return teamConversation(uint(id)), true
		}
		apierror.Respond(c, http.StatusNotFound, apierror.TeamNotFound, "Takım bulunamadı")
		return "", false
	}
	apierror.Respond(c, http.StatusNotFound, apierror.NotFound, "Sohbet bulunamadı")
	return "", false
}

// ListConversationMessages answers the latest messages, oldest first; with
// ?after=<id>, only those newer than that one.
func ListConversationMessages(c *gin.Context) {
	user := c.MustGet("user").(models.User)
	conversation, ok := conversationOf(c, user, c.Param("kind"))
	if !ok {
		return
	}
	var messages []models.SocialMessage
	query := database.DB.Where("conversation = ?", conversation)
	if after, err := strconv.ParseUint(c.Query("after"), 10, 64); err == nil {
		err = query.Where("id > ?", after).Order("id").Limit(socialPageSize).Find(&messages).Error
		if err != nil {
			apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Mesajlar alınamadı")
			return
		}
	} else {
		if err := query.Order("id DESC").Limit(socialPageSize).Find(&messages).Error; err != nil {
			apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Mesajlar alınamadı")
			return
		}
		for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
			messages[i], messages[j] = messages[j], messages[i]
		}
	}
	response, err := formatSocialMessages(messages, user.ID)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Mesajlar alınamadı")
		return
	}
	c.JSON(http.StatusOK, response)
}

type socialMessageInput struct {
	Content string `json:"content"`
}

func SendConversationMessage(c *gin.Context) {
	var input socialMessageInput
	_ = c.ShouldBindJSON(&input)
	content := strings.TrimSpace(input.Content)
	if content == "" || len([]rune(content)) > maxSocialMessageRunes {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidMessage, "Mesaj 1-2000 karakter olmalı")
		return
	}
	user := c.MustGet("user").(models.User)
	conversation, ok := conversationOf(c, user, c.Param("kind"))
	if !ok {
		return
	}
	message := models.SocialMessage{Conversation: conversation, SenderID: user.ID, Content: content}
	if err := database.DB.Create(&message).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Mesaj gönderilemedi")
		return
	}
	response, err := formatSocialMessages([]models.SocialMessage{message}, user.ID)
	if err != nil || len(response) == 0 {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Mesaj okunamadı")
		return
	}
	c.JSON(http.StatusCreated, response[0])
}

// CallFriend opens a meeting room and leaves an invitation to it in the
// chat with the friend.
func CallFriend(c *gin.Context) {
	user := c.MustGet("user").(models.User)
	conversation, ok := conversationOf(c, user, "friend")
	if !ok {
		return
	}
	room, ok := openRoom(c, user, user.Name())
	if !ok {
		return
	}
	if err := inviteToMeeting(conversation, user.ID, room.RoomID); err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Davet gönderilemedi")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"room_id": room.RoomID})
}

func formatSocialMessages(messages []models.SocialMessage, userID uint) ([]socialMessageResponse, error) {
	senderIDs := make([]uint, 0, len(messages))
	for _, message := range messages {
		senderIDs = append(senderIDs, message.SenderID)
	}
	senders, err := usersByID(senderIDs)
	if err != nil {
		return nil, err
	}
	response := make([]socialMessageResponse, len(messages))
	for i, message := range messages {
		sender := "?"
		if user, ok := senders[message.SenderID]; ok {
			sender = user.Name()
		}
		response[i] = socialMessageResponse{
			ID:        message.ID,
			SenderID:  message.SenderID,
			Sender:    sender,
			Content:   message.Content,
			RoomID:    message.RoomID,
			CreatedAt: message.CreatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
			IsMine:    message.SenderID == userID,
		}
	}
	return response, nil
}
