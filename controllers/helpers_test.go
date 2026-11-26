package controllers

import (
	"bytes"
	"encoding/json"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/middleware"

	"gorm.io/gorm"
)

// useTestDB points database.DB at a fresh in-memory database for one test.
func useTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previous := database.DB
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := openTestDB(t, name)
	if err != nil {
		t.Fatal(err)
	}
	database.DB = db
	// Nobody connected, nobody just left: each test starts afresh.
	hub = newRealtimeHub()
	hub.onPresence = presenceChanged
	watchers = &roomWatchers{rooms: map[string]map[*realtimeClient]struct{}{}}
	RoomMisses = middleware.NewKeyLimiter(30, 10*time.Minute)
	loginFailures = middleware.NewKeyLimiter(20, 15*time.Minute)
	// Quick passwords: the work factor is the same everywhere in a test.
	passwordCost = bcrypt.MinCost
	missingUserHash, _ = bcrypt.GenerateFromPassword([]byte("no such user"), passwordCost)
	t.Cleanup(func() {
		database.DB = previous
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err = database.Migrate(db); err != nil {
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
