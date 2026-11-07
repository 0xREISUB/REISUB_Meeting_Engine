package middleware

import (
	"net/http"
	"strings"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
)

func RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := strings.TrimSpace(c.GetHeader("Authorization"))
		if strings.HasPrefix(strings.ToLower(token), "bearer ") {
			token = strings.TrimSpace(token[len("Bearer "):])
		}

		if token == "" {
			apierror.Abort(c, http.StatusUnauthorized, apierror.AuthRequired, "Kimlik doğrulaması gerekli")
			return
		}

		var user models.User
		if err := database.DB.Where("token = ?", token).First(&user).Error; err != nil {
			apierror.Abort(c, http.StatusUnauthorized, apierror.InvalidToken, "Geçersiz token")
			return
		}

		c.Set("user", user)
		c.Next()
	}
}
