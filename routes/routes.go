package routes

import (
	"os"

	"reisub-backend/controllers"
	"reisub-backend/middleware"
	"reisub-backend/webapp"

	"github.com/gin-gonic/gin"
)

// SetupRoutes registers the public HTTP API.
//
// When WEB_DIR names a Flutter web build, it is served for every GET that no
// API route matches, and GET /j/:room_id serves its invite page.
func SetupRoutes(r *gin.Engine) {
	web := webapp.Handler{Dir: os.Getenv("WEB_DIR")}
	r.NoRoute(web.NoRoute)
	// Short invite links; public, no auth.
	r.GET("/j/:room_id", web.Invite)
	r.HEAD("/j/:room_id", web.Invite)

	// Health check used by clients to verify the server address.
	r.GET("/ping", controllers.Ping)

	r.POST("/register", controllers.Register)
	r.POST("/login", controllers.Login)
	r.POST("/logout", controllers.Logout)

	// Called by LiveKit; authenticated by its signed JWT, not a user token.
	r.POST("/livekit/webhook", controllers.LiveKitWebhook)

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
		roomRoutes.POST("/:room_id/participants/:identity/role", controllers.SetParticipantRole)
		roomRoutes.POST("/:room_id/transfer-ownership", controllers.TransferOwnership)
		roomRoutes.POST("/:room_id/leave", controllers.LeaveRoom)
		roomRoutes.GET("/:room_id/messages", controllers.ListRoomMessages)
		roomRoutes.POST("/:room_id/messages", controllers.SendRoomMessage)
	}
}
