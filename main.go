package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"reisub-backend/database"
	"reisub-backend/routes"

	"github.com/gin-gonic/gin"
)

func main() {
	database.ConnectDB()

	r := gin.Default()
	if err := r.SetTrustedProxies(nil); err != nil {
		log.Fatal("Güvenilir proxy ayarı yapılandırılamadı: ", err)
	}
	r.Use(corsMiddleware())

	routes.SetupRoutes(r)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	if err := r.Run(":" + port); err != nil {
		log.Fatal(err)
	}
}

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		isAllowedOrigin := isCorsOriginAllowed(origin)

		if isAllowedOrigin {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
			c.Header("Access-Control-Allow-Private-Network", "true")
		}

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

func isCorsOriginAllowed(origin string) bool {
	if origin == "" {
		return false
	}

	if os.Getenv("CORS_ALLOW_ALL") == "true" {
		return true
	}

	if strings.HasPrefix(origin, "http://localhost:") ||
		strings.HasPrefix(origin, "http://127.0.0.1:") {
		return true
	}

	for _, allowedOrigin := range strings.Split(os.Getenv("CORS_ALLOWED_ORIGINS"), ",") {
		if strings.TrimSpace(allowedOrigin) == origin {
			return true
		}
	}

	return false
}
