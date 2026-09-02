package controllers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func CreateRoom(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "Oda oluşturma apisi hazır olacak"})
}
