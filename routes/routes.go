package routes

import (
	"reisub-backend/controllers"

	"github.com/gin-gonic/gin"
)

func SetupRoutes(r *gin.Engine) {
	r.POST("/register", controllers.Register)
	r.POST("/login", controllers.Login)
	r.POST("/logout", controllers.Logout)
	r.GET("/users", controllers.GetUsers) // SİLİNECEK!

	r.POST("/rooms", controllers.CreateRoom)
}
