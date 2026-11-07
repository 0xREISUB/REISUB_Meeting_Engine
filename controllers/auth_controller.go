package controllers

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// bcrypt only accepts passwords up to 72 bytes.
const maxPasswordBytes = 72

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

func generateToken() string {
	bytes := make([]byte, 32)
	// crypto/rand.Read never returns an error on supported platforms.
	_, _ = rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

func Register(c *gin.Context) {
	input, ok := bindCredentials(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Eksik bilgi gönderildi"})
		return
	}
	if len(input.Password) > maxPasswordBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Şifre en fazla 72 bayt olabilir"})
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Kayıt tamamlanamadı"})
		return
	}
	user := models.User{Nick: input.Nick, Password: string(hashedPassword)}

	if err := database.DB.Create(&user).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Bu nick kullanımda"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Kayıt başarılı"})
}

func Login(c *gin.Context) {
	input, ok := bindCredentials(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Eksik bilgi"})
		return
	}

	var user models.User
	if err := database.DB.Where("nick = ?", input.Nick).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Kullanıcı bulunamadı"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(input.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Hatalı şifre"})
		return
	}

	newToken := generateToken()
	if err := database.DB.Model(&user).Update("token", newToken).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Oturum başlatılamadı"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Giriş başarılı", "token": newToken})
}

func Logout(c *gin.Context) {
	token := strings.TrimSpace(c.GetHeader("Authorization"))
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		token = strings.TrimSpace(token[len("Bearer "):])
	}
	if token == "" || len(token) < 30 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Token geçersiz"})
		return
	}

	var user models.User
	if err := database.DB.Where("token = ?", token).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Geçersiz token"})
		return
	}

	// Rotate to a fresh random token rather than clearing it, so an empty
	// token can never match a logged-out user.
	database.DB.Model(&user).Update("token", generateToken())
	c.JSON(http.StatusOK, gin.H{"message": "Başarıyla çıkış yapıldı"})
}
