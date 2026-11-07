package controllers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
)

func authTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	useTestDB(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/register", Register)
	r.POST("/login", Login)
	r.POST("/logout", Logout)
	return r
}

func TestRegisterAndLoginTrimNick(t *testing.T) {
	r := authTestRouter(t)
	if w := jsonRequest(r, http.MethodPost, "/register", "", `{"nick":"  alice\t","password":" secret "}`); w.Code != http.StatusOK {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	var user models.User
	if err := database.DB.First(&user).Error; err != nil || user.Nick != "alice" {
		t.Fatalf("stored nick = %q, want %q (err %v)", user.Nick, "alice", err)
	}
	for _, nick := range []string{"alice", " alice ", "\nalice"} {
		w := jsonRequest(r, http.MethodPost, "/login", "", `{"nick":`+quote(nick)+`,"password":" secret "}`)
		var body struct{ Token string }
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != http.StatusOK || body.Token == "" {
			t.Fatalf("login %q: %d %s", nick, w.Code, w.Body.String())
		}
	}
	// The password is not trimmed.
	if w := jsonRequest(r, http.MethodPost, "/login", "", `{"nick":"alice","password":"secret"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("trimmed password accepted: %d", w.Code)
	}
}

func TestRegisterRejectsBlankNickAndTrimmedDuplicate(t *testing.T) {
	r := authTestRouter(t)
	for _, body := range []string{`{"nick":"   ","password":"x"}`, `{"nick":"\t\n","password":"x"}`, `{"nick":"","password":"x"}`, `{"nick":"bob"}`} {
		if w := jsonRequest(r, http.MethodPost, "/register", "", body); w.Code != http.StatusBadRequest {
			t.Fatalf("register %s: %d", body, w.Code)
		}
		if w := jsonRequest(r, http.MethodPost, "/login", "", body); w.Code != http.StatusBadRequest {
			t.Fatalf("login %s: %d", body, w.Code)
		}
	}
	var count int64
	database.DB.Model(&models.User{}).Count(&count)
	if count != 0 {
		t.Fatalf("blank nick stored: %d users", count)
	}
	if w := jsonRequest(r, http.MethodPost, "/register", "", `{"nick":"bob","password":"x"}`); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	if w := jsonRequest(r, http.MethodPost, "/register", "", `{"nick":" bob ","password":"y"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("duplicate after trimming accepted: %d", w.Code)
	}
}

func TestRegisterRejectsPasswordBcryptCannotHash(t *testing.T) {
	r := authTestRouter(t)
	long := strings.Repeat("a", maxPasswordBytes+1)
	if w := jsonRequest(r, http.MethodPost, "/register", "", `{"nick":"carol","password":"`+long+`"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("over-long password: %d %s", w.Code, w.Body.String())
	}
	var count int64
	database.DB.Model(&models.User{}).Count(&count)
	if count != 0 {
		t.Fatal("account created with an unusable password hash")
	}
	if w := jsonRequest(r, http.MethodPost, "/register", "", `{"nick":"carol","password":"`+long[:maxPasswordBytes]+`"}`); w.Code != http.StatusOK {
		t.Fatalf("72-byte password: %d", w.Code)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
