package controllers

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/middleware"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// bcrypt only accepts passwords up to 72 bytes.
const maxPasswordBytes = 72

// loginFailures counts wrong passwords per nick, from any address.
var loginFailures = middleware.NewKeyLimiter(20, 15*time.Minute)

// missingUserHash is compared against when the nick is unknown, so that
// answer takes as long as a wrong password.
var missingUserHash, _ = bcrypt.GenerateFromPassword([]byte("no such user"), bcrypt.DefaultCost)

type credentials struct {
	Nick     string `json:"nick" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// bindCredentials parses a nick/password body. The nick is trimmed so that
// " alice " and "alice" are the same account; the password is used verbatim.
func bindCredentials(c *gin.Context) (credentials, bool) {
	var input credentials
	if err := c.ShouldBindJSON(&input); err != nil {
		return input, false
	}
	input.Nick = strings.TrimSpace(input.Nick)
	return input, input.Nick != ""
}

func Register(c *gin.Context) {
	input, ok := bindCredentials(c)
	if !ok {
		apierror.Respond(c, http.StatusBadRequest, apierror.MissingCredentials, "Eksik bilgi gönderildi")
		return
	}
	if strings.HasPrefix(strings.ToLower(input.Nick), models.GuestNickPrefix) {
		apierror.Respond(c, http.StatusBadRequest, apierror.NickReserved, "Bu nick kullanılamaz")
		return
	}
	if len(input.Password) > maxPasswordBytes {
		apierror.Respond(c, http.StatusBadRequest, apierror.PasswordTooLong, "Şifre en fazla 72 bayt olabilir")
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Kayıt tamamlanamadı")
		return
	}
	user := models.User{Nick: input.Nick, Password: string(hashedPassword)}

	if err := database.DB.Create(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			apierror.Respond(c, http.StatusBadRequest, apierror.NickTaken, "Bu nick kullanımda")
		} else {
			apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Kayıt tamamlanamadı")
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Kayıt başarılı"})
}

func Login(c *gin.Context) {
	input, ok := bindCredentials(c)
	if !ok {
		apierror.Respond(c, http.StatusBadRequest, apierror.MissingCredentials, "Eksik bilgi")
		return
	}

	// Guessing one account from many addresses is held back too.
	key := strings.ToLower(input.Nick)
	if over, retryAfter := loginFailures.Exhausted(key); over {
		middleware.AbortRateLimited(c, retryAfter)
		return
	}
	var users []models.User
	// Guests have no password and can never log in.
	if err := database.DB.Where("nick = ? AND is_guest = ?", input.Nick, false).Limit(1).Find(&users).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oturum başlatılamadı")
		return
	}
	// The same answer, in about the same time, for an unknown nick and a
	// wrong password: nothing tells which nicks exist.
	hash := missingUserHash
	if len(users) > 0 {
		hash = []byte(users[0].Password)
	}
	if err := bcrypt.CompareHashAndPassword(hash, []byte(input.Password)); err != nil || len(users) == 0 {
		loginFailures.Add(key)
		apierror.Respond(c, http.StatusUnauthorized, apierror.InvalidCredentials, "Kullanıcı adı veya şifre hatalı")
		return
	}
	loginFailures.Reset(key)
	user := users[0]

	// One more signed-in device; the others stay signed in.
	expires := time.Now().Add(middleware.AccountTokenTTL())
	newToken, err := middleware.NewSession(database.DB, user.ID, &expires)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oturum başlatılamadı")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Giriş başarılı", "token": newToken})
}

func Logout(c *gin.Context) {
	token := middleware.BearerToken(c)
	if len(token) < 30 {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidToken, "Token geçersiz")
		return
	}

	// Only this device signs out; the user's others stay signed in.
	hash := models.HashToken(token)
	var sessions []uint
	if err := database.DB.Model(&models.Session{}).Where("token_hash = ?", hash).Pluck("id", &sessions).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Çıkış yapılamadı")
		return
	}
	// A session from before users could have several, never used since.
	legacy := database.DB.Model(&models.User{}).Where("token_hash = ?", hash).UpdateColumn("token_hash", "")
	if legacy.Error != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Çıkış yapılamadı")
		return
	}
	if len(sessions) == 0 && legacy.RowsAffected == 0 {
		apierror.Respond(c, http.StatusUnauthorized, apierror.InvalidToken, "Geçersiz token")
		return
	}
	if err := middleware.EndSessions(database.DB, sessions); err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Çıkış yapılamadı")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Başarıyla çıkış yapıldı"})
}
