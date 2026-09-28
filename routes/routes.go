package routes

import (
	"reisub-backend/controllers"
	"reisub-backend/middleware"

	"github.com/gin-gonic/gin"
)

func SetupRoutes(r *gin.Engine) {
	// Sistem Test Rotası
	r.GET("/ping", controllers.Ping)

	r.POST("/register", controllers.Register)
	r.POST("/login", controllers.Login)
	r.POST("/logout", controllers.Logout)

	roomRoutes := r.Group("/rooms")
	roomRoutes.Use(middleware.RequireAuth())
	{
		roomRoutes.POST("", controllers.CreateRoom)
		roomRoutes.POST("/join", controllers.JoinRoom)
		roomRoutes.GET("/:room_id/messages", controllers.ListRoomMessages)
		roomRoutes.POST("/:room_id/messages", controllers.SendRoomMessage)
	}
}
