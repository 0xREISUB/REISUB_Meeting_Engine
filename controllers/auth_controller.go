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

func generateToken() string {
	bytes := make([]byte, 32)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

func Register(c *gin.Context) {
	var input struct {
		Nick     string `json:"nick" binding:"required"`
		Password string `json:"password" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Eksik bilgi gönderildi"})
		return
	}

	hashedPassword, _ := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	user := models.User{Nick: input.Nick, Password: string(hashedPassword)}

	if err := database.DB.Create(&user).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Bu nick kullanımda"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Kayıt başarılı"})
}

func Login(c *gin.Context) {
	var input struct {
		Nick     string `json:"nick" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
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
	database.DB.Model(&user).Update("token", newToken)
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

	// Boş token açığına karşı rastgele yeni token atıyoruz
	database.DB.Model(&user).Update("token", generateToken())
	c.JSON(http.StatusOK, gin.H{"message": "Başarıyla çıkış yapıldı"})
}

func GetUsers(c *gin.Context) {
	var users []models.User
	database.DB.Find(&users)
	c.JSON(http.StatusOK, gin.H{"users": users})
}
