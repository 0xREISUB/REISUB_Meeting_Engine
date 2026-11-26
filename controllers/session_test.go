package controllers

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"reisub-backend/database"
	"reisub-backend/middleware"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
)

// sessionRouter is the auth router with an endpoint that needs a session.
func sessionRouter(t *testing.T) *gin.Engine {
	t.Helper()
	r := authTestRouter(t)
	r.GET("/me", middleware.RequireAuth(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"user": c.MustGet("user").(models.User).ID, "session": c.MustGet("session").(models.Session).ID})
	})
	jsonRequest(r, http.MethodPost, "/register", "", `{"nick":"alice","password":"right"}`)
	return r
}

func login(t *testing.T, r *gin.Engine) string {
	t.Helper()
	w := jsonRequest(r, http.MethodPost, "/login", "", `{"nick":"alice","password":"right"}`)
	var body struct{ Token string }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Token == "" {
		t.Fatal("login:", w.Code, w.Body.String())
	}
	return body.Token
}

func signedIn(r *gin.Engine, token string) bool {
	return jsonRequest(r, http.MethodGet, "/me", token, "").Code == http.StatusOK
}

func TestSeveralDevicesStaySignedIn(t *testing.T) {
	r := sessionRouter(t)
	phone, computer := login(t, r), login(t, r)
	if !signedIn(r, phone) || !signedIn(r, computer) {
		t.Fatal("signing in on the computer signed the phone out")
	}
	// Signing out on one device leaves the other.
	database.DB.Create(&models.PushDevice{SessionID: 1, UserID: 1, Token: "fcm-phone", Platform: "android"})
	if w := jsonRequest(r, http.MethodPost, "/logout", phone, ""); w.Code != http.StatusOK {
		t.Fatal("logout:", w.Code, w.Body.String())
	}
	if signedIn(r, phone) || !signedIn(r, computer) {
		t.Fatal("logout ended the wrong sessions")
	}
	// Its push device went with it.
	var devices int64
	database.DB.Model(&models.PushDevice{}).Count(&devices)
	if devices != 0 {
		t.Fatal("a signed-out device still gets pushes")
	}
	if w := jsonRequest(r, http.MethodPost, "/logout", phone, ""); w.Code != http.StatusUnauthorized {
		t.Fatal("second logout:", w.Code)
	}
}

func TestTheOldestSessionsEndBeyondTheLimit(t *testing.T) {
	r := sessionRouter(t)
	first := login(t, r)
	var last string
	for i := 0; i < models.MaxSessionsPerUser; i++ {
		last = login(t, r)
	}
	if signedIn(r, first) || !signedIn(r, last) {
		t.Fatal("the oldest session did not end")
	}
	var count int64
	database.DB.Model(&models.Session{}).Count(&count)
	if count != models.MaxSessionsPerUser {
		t.Fatalf("%d sessions kept", count)
	}
}

func TestSessionsFromBeforeMoveOnFirstUse(t *testing.T) {
	r := sessionRouter(t)
	// A session the way it was kept before: on the user.
	database.DB.Model(&models.User{}).Where("nick = ?", "alice").UpdateColumn("token_hash", models.HashToken("old-device-token-0123456789abcdef0123"))
	newer := login(t, r)
	if !signedIn(r, "old-device-token-0123456789abcdef0123") || !signedIn(r, newer) {
		t.Fatal("an old session or the new one does not work")
	}
	var user models.User
	database.DB.Where("nick = ?", "alice").First(&user)
	var moved int64
	database.DB.Model(&models.Session{}).Where("token_hash = ?", models.HashToken("old-device-token-0123456789abcdef0123")).Count(&moved)
	if user.TokenHash != "" || moved != 1 {
		t.Fatalf("not moved: user hash %q, sessions %d", user.TokenHash, moved)
	}
	// It signs out like any other.
	if w := jsonRequest(r, http.MethodPost, "/logout", "old-device-token-0123456789abcdef0123", ""); w.Code != http.StatusOK || signedIn(r, "old-device-token-0123456789abcdef0123") {
		t.Fatal("old session did not sign out:", w.Code)
	}
	// One never used since signs out too.
	database.DB.Model(&models.User{}).Where("nick = ?", "alice").UpdateColumn("token_hash", models.HashToken("unused-device-token-0123456789abcdef01"))
	if w := jsonRequest(r, http.MethodPost, "/logout", "unused-device-token-0123456789abcdef01", ""); w.Code != http.StatusOK || signedIn(r, "unused-device-token-0123456789abcdef01") {
		t.Fatal("unused old session did not sign out:", w.Code)
	}
}

func TestOldSessionsGetAnEnd(t *testing.T) {
	r := sessionRouter(t)
	const old = "never-used-device-token-0123456789abcdef"
	database.DB.Model(&models.User{}).Where("nick = ?", "alice").UpdateColumn("token_hash", models.HashToken(old))
	if n, err := middleware.BoundLegacySessions(database.DB); err != nil || n != 1 {
		t.Fatalf("bounded %d: %v", n, err)
	}
	var user models.User
	database.DB.Where("nick = ?", "alice").First(&user)
	if user.TokenExpiresAt == nil || time.Until(*user.TokenExpiresAt) < middleware.AccountTokenTTL()-time.Minute {
		t.Fatalf("end %v", user.TokenExpiresAt)
	}
	// A second start changes nothing.
	if n, _ := middleware.BoundLegacySessions(database.DB); n != 0 {
		t.Fatalf("bounded again: %d", n)
	}
	// Still unused when it comes, it is over.
	database.DB.Model(&models.User{}).Where("nick = ?", "alice").UpdateColumn("token_expires_at", time.Now().Add(-time.Minute))
	if signedIn(r, old) {
		t.Fatal("an old session past its end still works")
	}
}
