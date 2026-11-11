package controllers

import (
	"net/http"

	"reisub-backend/policy"

	"github.com/gin-gonic/gin"
)

// Ping lets clients verify that the configured address is a REISUB server.
func Ping(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"message": "pong",
		"status":  "active",
	})
}

// Policy publishes the server's public rules (see package policy). It needs
// no authentication.
func Policy(c *gin.Context) {
	c.JSON(http.StatusOK, policy.Public())
}
