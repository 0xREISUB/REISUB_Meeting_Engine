package controllers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// İstemcinin sunucuya erişimini test eden fonksiyon
func Ping(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"message": "pong",
		"status":  "active",
	})
}
