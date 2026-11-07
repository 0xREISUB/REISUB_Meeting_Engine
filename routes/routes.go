package routes

import (
	"reisub-backend/apierror"
	"reisub-backend/controllers"
	"reisub-backend/middleware"

	"github.com/gin-gonic/gin"
)

// SetupRoutes registers the public HTTP API.
func SetupRoutes(r *gin.Engine) {
	r.NoRoute(apierror.NoRoute)

	// Health check used by clients to verify the server address.
	r.GET("/ping", controllers.Ping)

	r.POST("/register", controllers.Register)
	r.POST("/login", controllers.Login)
	r.POST("/logout", controllers.Logout)

	roomRoutes := r.Group("/rooms")
	roomRoutes.Use(middleware.RequireAuth())
	{
		roomRoutes.POST("", controllers.CreateRoom)
		roomRoutes.POST("/join", controllers.JoinRoom)
		roomRoutes.POST("/:room_id/close", controllers.CloseRoom)
		roomRoutes.GET("/:room_id/state", controllers.GetMeetingState)
		roomRoutes.POST("/:room_id/settings", controllers.UpdateMeetingSettings)
		roomRoutes.POST("/:room_id/screen-share/start", controllers.StartScreenShare)
		roomRoutes.POST("/:room_id/screen-share/stop", controllers.StopScreenShare)
		roomRoutes.POST("/:room_id/participants/:identity/microphone", controllers.SetParticipantMicrophone)
		roomRoutes.GET("/:room_id/messages", controllers.ListRoomMessages)
		roomRoutes.POST("/:room_id/messages", controllers.SendRoomMessage)
	}
}
