package main

import (
	"log"
	"net/http"
	"os"
	"reisub-backend/database"
	"reisub-backend/routes"
	"strings"

	"github.com/gin-gonic/gin"
)

func main() {
	// Veritabanını başlat
	database.ConnectDB()

	// Web sunucusunu başlat
	r := gin.Default()
	r.Use(corsMiddleware())

	// Rotaları (Endpointleri) yükle
	routes.SetupRoutes(r)

	// PRODUCTION UYARISI
	log.Println("==================================================================")
	log.Println(" UYARI: /users UÇ NOKTASI ŞU AN AKTİF DURUMDA.")
	log.Println(" PRODUCTION'A ÇIKMADAN ÖNCE routes.go İÇİNDEN KESİNLİKLE SİLİN!")
	log.Println("==================================================================")

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	r.Run(":" + port)
}

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		isLocalOrigin := strings.HasPrefix(origin, "http://localhost:") ||
			strings.HasPrefix(origin, "http://127.0.0.1:")

		if isLocalOrigin {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
