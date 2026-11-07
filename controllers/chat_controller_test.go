package controllers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"reisub-backend/models"

	"github.com/gin-gonic/gin"
)

func TestRoomMessagesPersistAndIdentifySender(t *testing.T) {
	db := useTestDB(t)
	if err := db.Create(&models.Room{RoomID: "123-456-789", Name: "test", Status: "active"}).Error; err != nil {
		t.Fatalf("create test room: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		userID := uint(11)
		nickname := "alice"
		if c.GetHeader("X-Test-User") == "other" {
			userID = 22
			nickname = "bob"
		}
		c.Set("user", models.User{ID: userID, Nick: nickname})
		c.Next()
	})
	router.POST("/rooms/:room_id/messages", SendRoomMessage)
	router.GET("/rooms/:room_id/messages", ListRoomMessages)

	request := httptest.NewRequest(http.MethodPost, "/rooms/123-456-789/messages", bytes.NewBufferString(`{"content":"  hello room  "}`))
	request.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	router.ServeHTTP(created, request)
	if created.Code != http.StatusCreated {
		t.Fatalf("send status = %d, want %d: %s", created.Code, http.StatusCreated, created.Body.String())
	}
	var sent chatMessageResponse
	if err := json.Unmarshal(created.Body.Bytes(), &sent); err != nil {
		t.Fatalf("decode sent message: %v", err)
	}
	if sent.Content != "hello room" || sent.Sender != "alice" || !sent.IsMine {
		t.Fatalf("unexpected sent message: %+v", sent)
	}

	request = httptest.NewRequest(http.MethodGet, "/rooms/123-456-789/messages", nil)
	request.Header.Set("X-Test-User", "other")
	listed := httptest.NewRecorder()
	router.ServeHTTP(listed, request)
	if listed.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d: %s", listed.Code, http.StatusOK, listed.Body.String())
	}
	var messages []chatMessageResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &messages); err != nil {
		t.Fatalf("decode message list: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "hello room" || messages[0].IsMine {
		t.Fatalf("unexpected messages for another participant: %+v", messages)
	}
}
