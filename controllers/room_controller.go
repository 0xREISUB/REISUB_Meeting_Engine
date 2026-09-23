package controllers

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/livekit/protocol/auth"
)

var roomIDPattern = regexp.MustCompile(`^\d{3}-\d{3}-\d{3}$`)

type roomRequest struct {
	Name   string `json:"name"`
	RoomID string `json:"room_id"`
}

type roomResponse struct {
	RoomID      string `json:"room_id"`
	LiveKitURL  string `json:"livekit_url"`
	Token       string `json:"token"`
	Participant string `json:"participant"`
	IsHost      bool   `json:"is_host"`
}

func CreateRoom(c *gin.Context) {
	var input roomRequest
	if err := c.ShouldBindJSON(&input); err != nil || strings.TrimSpace(input.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "İsim gerekli"})
		return
	}

	user := c.MustGet("user").(models.User)
	if os.Getenv("LIVEKIT_API_KEY") == "" || os.Getenv("LIVEKIT_API_SECRET") == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "LiveKit yapılandırması eksik"})
		return
	}
	room := models.Room{
		RoomID:  newRoomID(),
		Name:    strings.TrimSpace(input.Name),
		OwnerID: user.ID,
		Status:  "active",
	}
	if err := database.DB.Create(&room).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Oda oluşturulamadı"})
		return
	}

	response, err := makeRoomResponse(room, user.ID, user.Nick, true)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, response)
}

func JoinRoom(c *gin.Context) {
	var input roomRequest
	if err := c.ShouldBindJSON(&input); err != nil || strings.TrimSpace(input.Name) == "" || !roomIDPattern.MatchString(input.RoomID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "İsim ve geçerli oda kodu gerekli"})
		return
	}

	var room models.Room
	if err := database.DB.Where("room_id = ? AND status = ?", input.RoomID, "active").First(&room).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Oda bulunamadı"})
		return
	}

	user := c.MustGet("user").(models.User)
	response, err := makeRoomResponse(room, user.ID, strings.TrimSpace(input.Name), room.OwnerID == user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, response)
}

func newRoomID() string {
	var roomID string
	for {
		roomID = fmt.Sprintf("%03d-%03d-%03d", time.Now().UnixNano()%1000, time.Now().UnixNano()/1000%1000, time.Now().UnixNano()/1000000%1000)
		var count int64
		database.DB.Model(&models.Room{}).Where("room_id = ?", roomID).Count(&count)
		if count == 0 {
			return roomID
		}
	}
}

func makeRoomResponse(room models.Room, userID uint, participant string, isHost bool) (roomResponse, error) {
	apiKey := os.Getenv("LIVEKIT_API_KEY")
	apiSecret := os.Getenv("LIVEKIT_API_SECRET")
	if apiKey == "" || apiSecret == "" {
		return roomResponse{}, fmt.Errorf("LiveKit yapılandırması eksik")
	}

	grant := &auth.VideoGrant{
		Room:           room.RoomID,
		RoomJoin:       true,
		RoomAdmin:      isHost,
		CanPublish:     boolPtr(true),
		CanSubscribe:   boolPtr(true),
		CanPublishData: boolPtr(true),
	}
	token, err := auth.NewAccessToken(apiKey, apiSecret).
		SetIdentity(fmt.Sprintf("user-%d-%s", userID, uuid.NewString())).
		SetName(participant).
		SetValidFor(2 * time.Hour).
		SetVideoGrant(grant).
		ToJWT()
	if err != nil {
		return roomResponse{}, err
	}

	return roomResponse{
		RoomID:      room.RoomID,
		LiveKitURL:  liveKitURL(),
		Token:       token,
		Participant: participant,
		IsHost:      isHost,
	}, nil
}

func boolPtr(value bool) *bool { return &value }

func liveKitURL() string {
	if value := os.Getenv("LIVEKIT_PUBLIC_URL"); value != "" {
		return value
	}
	return "ws://localhost:7880"
}
