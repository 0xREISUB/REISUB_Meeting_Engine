package controllers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/middleware"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
)

func loginFrom(r http.Handler, address, nick, password string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewBufferString(fmt.Sprintf(`{"nick":%q,"password":%q}`, nick, password)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = address + ":4000"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestGuessingOneAccountFromManyAddressesIsHeldBack(t *testing.T) {
	r := authTestRouter(t)
	jsonRequest(r, http.MethodPost, "/register", "", `{"nick":"alice","password":"right"}`)
	for i := 0; i < 20; i++ {
		w := loginFrom(r, fmt.Sprintf("198.51.100.%d", i+1), "alice", "wrong")
		assertAPIError(t, w, http.StatusUnauthorized, apierror.InvalidCredentials, "Kullanıcı adı veya şifre hatalı")
	}
	// Now even the right password waits, whatever the address.
	w := loginFrom(r, "203.0.113.7", "Alice", "right")
	assertAPIError(t, w, http.StatusTooManyRequests, apierror.RateLimited, "Çok fazla istek gönderildi. Lütfen biraz sonra tekrar deneyin.")
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
}

func TestALoginClearsTheFailuresBeforeIt(t *testing.T) {
	r := authTestRouter(t)
	jsonRequest(r, http.MethodPost, "/register", "", `{"nick":"alice","password":"right"}`)
	for round := 0; round < 3; round++ {
		for i := 0; i < 15; i++ {
			loginFrom(r, "198.51.100.1", "alice", "wrong")
		}
		if w := loginFrom(r, "198.51.100.1", "alice", "right"); w.Code != http.StatusOK {
			t.Fatalf("round %d: %d %s", round, w.Code, w.Body.String())
		}
	}
}

func TestAccountSessionsEndWhenLeftUnused(t *testing.T) {
	r := authTestRouter(t)
	r.GET("/me", middleware.RequireAuth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	jsonRequest(r, http.MethodPost, "/register", "", `{"nick":"alice","password":"right"}`)
	w := jsonRequest(r, http.MethodPost, "/login", "", `{"nick":"alice","password":"right"}`)
	var body struct{ Token string }
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	read := func() models.Session {
		var session models.Session
		database.DB.Where("token_hash = ?", models.HashToken(body.Token)).First(&session)
		return session
	}
	setEnd := func(end any) {
		database.DB.Model(&models.Session{}).Where("token_hash = ?", models.HashToken(body.Token)).UpdateColumn("expires_at", end)
	}
	// A login lasts AccountTokenTTL.
	expires := read().ExpiresAt
	if expires == nil || time.Until(*expires) < middleware.AccountTokenTTL()-time.Minute {
		t.Fatalf("login set the end to %v", expires)
	}

	// In use, it moves on: an end near is pushed back.
	soon := time.Now().Add(time.Hour)
	setEnd(soon)
	if w := jsonRequest(r, http.MethodGet, "/me", body.Token, ""); w.Code != http.StatusNoContent {
		t.Fatalf("in use: %d", w.Code)
	}
	if moved := read().ExpiresAt; moved == nil || time.Until(*moved) < middleware.AccountTokenTTL()-time.Minute {
		t.Fatalf("not moved on: %v", moved)
	}

	// A session from before sessions ended gets an end on its next use.
	setEnd(nil)
	jsonRequest(r, http.MethodGet, "/me", body.Token, "")
	if read().ExpiresAt == nil {
		t.Fatal("an old session got no end")
	}

	// Past its end, it is over.
	setEnd(time.Now().Add(-time.Minute))
	w = jsonRequest(r, http.MethodGet, "/me", body.Token, "")
	assertAPIError(t, w, http.StatusUnauthorized, apierror.InvalidToken, "Geçersiz token")
}
