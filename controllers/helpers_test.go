package controllers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// useTestDB points database.DB at a fresh in-memory database for one test.
func useTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previous := database.DB
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() {
		database.DB = previous
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err = db.AutoMigrate(&models.User{}, &models.Room{}, &models.ChatMessage{}, &models.RoomMicrophoneLock{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func jsonRequest(handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

// assertAPIError checks the status, the unchanged client-visible message and
// the machine-readable code of an error response.
func assertAPIError(t *testing.T, w *httptest.ResponseRecorder, status int, code apierror.Code, message string) {
	t.Helper()
	var body apierror.Body
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("non-JSON error body %q", w.Body.String())
	}
	if w.Code != status || body.Code != code || body.Error != message {
		t.Fatalf("got %d %+v, want %d {Error:%s Code:%s}", w.Code, body, status, message, code)
	}
}
