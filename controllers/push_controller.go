package controllers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"
	"reisub-backend/push"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm/clause"
)

// pushSender delivers push notifications; nil while push is off (no
// FCM_CREDENTIALS_FILE). Tests put a fake one in.
var pushSender push.Sender

// pushQueue holds notifications waiting to be sent, so a slow FCM never
// holds up a request. Beyond its size new ones are dropped.
var pushQueue = make(chan push.Message, 1024)

// pushWorkers send from the queue side by side.
const pushWorkers = 4

// pushSends counts sends in progress; tests wait for them.
var pushSends sync.WaitGroup

// StartPush turns push notifications on when FCM_CREDENTIALS_FILE names a
// Firebase service account key, and starts sending.
func StartPush() {
	path := strings.TrimSpace(os.Getenv("FCM_CREDENTIALS_FILE"))
	if path == "" {
		log.Print("push notifications off: FCM_CREDENTIALS_FILE is not set")
		return
	}
	sender, err := push.NewFCMFromFile(context.Background(), path)
	if err != nil {
		log.Printf("warning: push notifications off: %v", err)
		return
	}
	pushSender = sender
	startPushWorkers()
	log.Print("push notifications on")
}

var pushWorkersOnce sync.Once

func startPushWorkers() {
	pushWorkersOnce.Do(func() {
		for i := 0; i < pushWorkers; i++ {
			go func() {
				for m := range pushQueue {
					deliverPush(m)
				}
			}()
		}
	})
}

// deliverPush sends one message and forgets a device FCM no longer knows.
func deliverPush(m push.Message) {
	defer pushSends.Done()
	sender := pushSender
	if sender == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := sender.Send(ctx, m)
	switch {
	case errors.Is(err, push.ErrUnregistered):
		if err := database.DB.Where("token = ?", m.Token).Delete(&models.PushDevice{}).Error; err != nil {
			log.Printf("warning: could not forget a push device: %v", err)
		}
	case err != nil:
		log.Printf("warning: push failed: %v", err)
	}
}

// enqueuePush hands a message to the senders, or drops it if they are
// behind.
func enqueuePush(m push.Message) {
	pushSends.Add(1)
	select {
	case pushQueue <- m:
	default:
		pushSends.Done()
		log.Print("warning: push queue full; a notification was dropped")
	}
}

// pushContentLimit keeps a push well inside FCM's 4 KB of data: a longer
// message shows its start.
const pushContentLimit = 500

// pushNewMessage tells the user's signed-in phones about a new message:
// who wrote, where, and what (its start). The text passes through Google
// (FCM) on its way, encrypted in transit.
func pushNewMessage(userID uint, m notificationMessage) {
	// An invitation rings as a call instead (see startCall).
	if m.RoomID != "" {
		return
	}
	content := m.Content
	if runes := []rune(content); len(runes) > pushContentLimit {
		content = string(runes[:pushContentLimit]) + "…"
	}
	pushToUser(userID, map[string]string{
		"content":    content,
		"type":       "message",
		"kind":       m.Kind,
		"target_id":  strconv.FormatUint(uint64(m.TargetID), 10),
		"title":      m.Title,
		"sender":     m.Sender,
		"message_id": strconv.FormatUint(uint64(m.ID), 10),
	}, 0)
}

// pushToUser sends data to the user's phones whose session has not ended;
// ttl zero keeps it an hour for a phone that is off.
func pushToUser(userID uint, data map[string]string, ttl time.Duration) {
	if pushSender == nil {
		return
	}
	var devices []models.PushDevice
	err := database.DB.Model(&models.PushDevice{}).
		Joins("JOIN sessions ON sessions.id = push_devices.session_id").
		Where("push_devices.user_id = ? AND sessions.user_id = push_devices.user_id AND (sessions.expires_at IS NULL OR sessions.expires_at > ?)", userID, time.Now()).
		Find(&devices).Error
	if err != nil {
		log.Printf("warning: could not read push devices of user %d: %v", userID, err)
		return
	}
	for _, d := range devices {
		enqueuePush(push.Message{Token: d.Token, FID: d.Kind == "fid", Data: data, TTL: ttl})
	}
}

// PutPushDevice records where this device gets push notifications. A
// token another session had moves to this one.
func PutPushDevice(c *gin.Context) {
	var input struct {
		Token    string `json:"token"`
		Platform string `json:"platform"`
		// Kind is "fid" for a Firebase Installation ID; empty or "token"
		// for an FCM registration token, from older apps.
		Kind string `json:"kind"`
	}
	bound := c.ShouldBindJSON(&input) == nil
	if input.Kind == "" {
		input.Kind = "token"
	}
	if !bound || strings.TrimSpace(input.Token) == "" || len(input.Token) > 4096 || input.Platform != "android" || (input.Kind != "token" && input.Kind != "fid") {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidRequest, "Geçersiz cihaz")
		return
	}
	user := c.MustGet("user").(models.User)
	session := c.MustGet("session").(models.Session)
	token := strings.TrimSpace(input.Token)
	err := database.DB.Where("token = ? AND session_id <> ?", token, session.ID).Delete(&models.PushDevice{}).Error
	if err == nil {
		err = database.DB.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "session_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"token", "kind", "user_id", "platform", "updated_at"}),
		}).Create(&models.PushDevice{SessionID: session.ID, UserID: user.ID, Token: token, Kind: input.Kind, Platform: input.Platform}).Error
	}
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Cihaz kaydedilemedi")
		return
	}
	c.Status(http.StatusNoContent)
}

// DeletePushDevice stops push notifications to this device.
func DeletePushDevice(c *gin.Context) {
	session := c.MustGet("session").(models.Session)
	if err := database.DB.Where("session_id = ?", session.ID).Delete(&models.PushDevice{}).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Cihaz silinemedi")
		return
	}
	c.Status(http.StatusNoContent)
}
