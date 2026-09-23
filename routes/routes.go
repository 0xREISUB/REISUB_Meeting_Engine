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
	r.GET("/users", controllers.GetUsers) // SİLİNECEK!

	roomRoutes := r.Group("/rooms")
	roomRoutes.Use(middleware.RequireAuth())
	{
		roomRoutes.POST("", controllers.CreateRoom)
		roomRoutes.POST("/join", controllers.JoinRoom)
	}
}
