package controllers

import (
	"net/http"
	"strings"

	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
)

type chatMessageRequest struct {
	Content string `json:"content" binding:"required"`
}

type chatMessageResponse struct {
	ID        uint   `json:"id"`
	RoomID    string `json:"room_id"`
	Sender    string `json:"sender"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
	IsMine    bool   `json:"is_mine"`
}

func ListRoomMessages(c *gin.Context) {
	user := c.MustGet("user").(models.User)
	roomID := c.Param("room_id")
	if !activeRoomExists(roomID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Oda bulunamadı"})
		return
	}

	var messages []models.ChatMessage
	if err := database.DB.Where("room_id = ?", roomID).Order("id DESC").Limit(200).Find(&messages).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Mesajlar alınamadı"})
		return
	}

	response := make([]chatMessageResponse, len(messages))
	for index, message := range messages {
		response[len(messages)-1-index] = formatChatMessage(message, user.ID)
	}
	c.JSON(http.StatusOK, response)
}

func SendRoomMessage(c *gin.Context) {
	var input chatMessageRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Mesaj gerekli"})
		return
	}
	content := strings.TrimSpace(input.Content)
	if content == "" || len([]rune(content)) > 1000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Mesaj 1-1000 karakter olmalı"})
		return
	}

	roomID := c.Param("room_id")
	if !activeRoomExists(roomID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Oda bulunamadı"})
		return
	}

	user := c.MustGet("user").(models.User)
	message := models.ChatMessage{
		RoomID:   roomID,
		SenderID: user.ID,
		Sender:   user.Nick,
		Content:  content,
	}
	if err := database.DB.Create(&message).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Mesaj gönderilemedi"})
		return
	}
	c.JSON(http.StatusCreated, formatChatMessage(message, user.ID))
}

func activeRoomExists(roomID string) bool {
	var count int64
	database.DB.Model(&models.Room{}).Where("room_id = ? AND status = ?", roomID, "active").Count(&count)
	return count > 0
}

func formatChatMessage(message models.ChatMessage, userID uint) chatMessageResponse {
	return chatMessageResponse{
		ID:        message.ID,
		RoomID:    message.RoomID,
		Sender:    message.Sender,
		Content:   message.Content,
		CreatedAt: message.CreatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
		IsMine:    message.SenderID == userID,
	}
}
