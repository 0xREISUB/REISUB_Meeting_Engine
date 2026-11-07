package controllers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Ping lets clients verify that the configured address is a REISUB server.
func Ping(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"message": "pong",
		"status":  "active",
	})
}
