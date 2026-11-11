package routes

import (
	"os"
	"time"

	"reisub-backend/controllers"
	"reisub-backend/middleware"
	"reisub-backend/webapp"

	"github.com/gin-gonic/gin"
)

// POST /guest is limited to guestsPerWindow new sessions per client IP per
// guestWindow, enough for a group joining from one office or school network.
const (
	guestsPerWindow = 30
	guestWindow     = 10 * time.Minute
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
	// Android App Links verification for invite links; public, no auth.
	assetLinks := webapp.AssetLinks()
	r.GET("/.well-known/assetlinks.json", assetLinks)
	r.HEAD("/.well-known/assetlinks.json", assetLinks)

	// Health check used by clients to verify the server address.
	r.GET("/ping", controllers.Ping)

	r.POST("/register", controllers.Register)
	r.POST("/login", controllers.Login)
	r.POST("/logout", controllers.Logout)
	// Anonymous guest session; no auth.
	r.POST("/guest", middleware.RateLimit(guestsPerWindow, guestWindow), controllers.CreateGuest)

	// Called by LiveKit; authenticated by its signed JWT, not a user token.
	r.POST("/livekit/webhook", controllers.LiveKitWebhook)

	roomRoutes := r.Group("/rooms")
	roomRoutes.Use(middleware.RequireAuth())
	{
		roomRoutes.POST("", controllers.CreateRoom)
		roomRoutes.POST("/join", controllers.JoinRoom)
		roomRoutes.POST("/:room_id/close", controllers.CloseRoom)
		roomRoutes.GET("/:room_id/state", controllers.GetMeetingState)
		roomRoutes.POST("/:room_id/screen-share/start", controllers.StartScreenShare)
		roomRoutes.POST("/:room_id/screen-share/stop", controllers.StopScreenShare)
		roomRoutes.POST("/:room_id/participants/:identity/microphone", controllers.SetParticipantMicrophone)
		roomRoutes.PUT("/:room_id/participants/:identity/permissions", controllers.SetParticipantPermissions)
		roomRoutes.POST("/:room_id/participants/:identity/remove", controllers.RemoveMember)
		roomRoutes.POST("/:room_id/transfer-ownership", controllers.TransferOwnership)
		roomRoutes.POST("/:room_id/leave", controllers.LeaveRoom)
		roomRoutes.GET("/:room_id/messages", controllers.ListRoomMessages)
		roomRoutes.POST("/:room_id/messages", controllers.SendRoomMessage)
	}
}
