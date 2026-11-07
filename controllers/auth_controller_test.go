package controllers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"reisub-backend/apierror"
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
	assertAPIError(t, jsonRequest(r, http.MethodPost, "/login", "", `{"nick":"alice","password":"secret"}`),
		http.StatusUnauthorized, apierror.WrongPassword, "Hatalı şifre")
	assertAPIError(t, jsonRequest(r, http.MethodPost, "/login", "", `{"nick":"nobody","password":"x"}`),
		http.StatusUnauthorized, apierror.UserNotFound, "Kullanıcı bulunamadı")
}

func TestRegisterRejectsBlankNickAndTrimmedDuplicate(t *testing.T) {
	r := authTestRouter(t)
	for _, body := range []string{`{"nick":"   ","password":"x"}`, `{"nick":"\t\n","password":"x"}`, `{"nick":"","password":"x"}`, `{"nick":"bob"}`} {
		assertAPIError(t, jsonRequest(r, http.MethodPost, "/register", "", body), http.StatusBadRequest, apierror.MissingCredentials, "Eksik bilgi gönderildi")
		assertAPIError(t, jsonRequest(r, http.MethodPost, "/login", "", body), http.StatusBadRequest, apierror.MissingCredentials, "Eksik bilgi")
	}
	var count int64
	database.DB.Model(&models.User{}).Count(&count)
	if count != 0 {
		t.Fatalf("blank nick stored: %d users", count)
	}
	if w := jsonRequest(r, http.MethodPost, "/register", "", `{"nick":"bob","password":"x"}`); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	assertAPIError(t, jsonRequest(r, http.MethodPost, "/register", "", `{"nick":" bob ","password":"y"}`),
		http.StatusBadRequest, apierror.NickTaken, "Bu nick kullanımda")
}

func TestRegisterRejectsPasswordBcryptCannotHash(t *testing.T) {
	r := authTestRouter(t)
	long := strings.Repeat("a", maxPasswordBytes+1)
	assertAPIError(t, jsonRequest(r, http.MethodPost, "/register", "", `{"nick":"carol","password":"`+long+`"}`),
		http.StatusBadRequest, apierror.PasswordTooLong, "Şifre en fazla 72 bayt olabilir")
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

func TestLogoutInvalidatesToken(t *testing.T) {
	r := authTestRouter(t)
	jsonRequest(r, http.MethodPost, "/register", "", `{"nick":"dan","password":"pw"}`)
	w := jsonRequest(r, http.MethodPost, "/login", "", `{"nick":"dan","password":"pw"}`)
	var body struct{ Token string }
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w := jsonRequest(r, http.MethodPost, "/logout", body.Token, ""); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	assertAPIError(t, jsonRequest(r, http.MethodPost, "/logout", body.Token, ""), http.StatusUnauthorized, apierror.InvalidToken, "Geçersiz token")
	assertAPIError(t, jsonRequest(r, http.MethodPost, "/logout", "", ""), http.StatusBadRequest, apierror.InvalidToken, "Token geçersiz")
}
