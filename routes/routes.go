package routes

import (
	"net/http"
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

// Failed logins per client IP per window; then 429 until it ends. New
// accounts per client IP per window.
const (
	loginFailuresPerWindow = 10
	loginFailureWindow     = 15 * time.Minute
	registrationsPerWindow = 10
	registrationWindow     = time.Hour
)

// Requests under /rooms that answer 404 (an unknown room code, mostly) are
// limited to roomMissesPerWindow per client IP per roomMissWindow; then that
// address gets 429 for the rest of the window.
const (
	roomMissesPerWindow = 30
	roomMissWindow      = 10 * time.Minute
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
	// The server's public rules, e.g. whether guests may use meetings.
	r.GET("/policy", controllers.Policy)

	// Wrong passwords count per address (and per nick, in Login); new
	// accounts are limited per address too.
	r.POST("/register", middleware.RateLimit(registrationsPerWindow, registrationWindow), controllers.Register)
	r.POST("/login", middleware.LimitStatus(loginFailuresPerWindow, loginFailureWindow, http.StatusUnauthorized), controllers.Login)
	r.POST("/logout", controllers.Logout)
	// Anonymous guest session; no auth.
	r.POST("/guest", middleware.RequireGuestsAllowed(), middleware.RateLimit(guestsPerWindow, guestWindow), controllers.CreateGuest)

	// Called by LiveKit; authenticated by its signed JWT, not a user token.
	r.POST("/livekit/webhook", controllers.LiveKitWebhook)

	roomRoutes := r.Group("/rooms")
	// A room code is the only secret needed to join, and a 404 tells a wrong
	// code apart: cap wrong guesses per address. Meetings the realtime
	// connection refuses to watch count in the same window.
	roomMisses := middleware.NewKeyLimiter(roomMissesPerWindow, roomMissWindow)
	controllers.RoomMisses = roomMisses
	roomRoutes.Use(middleware.LimitStatusWith(roomMisses, http.StatusNotFound), middleware.RequireAuth())
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
		roomRoutes.PUT("/:room_id/settings", controllers.UpdateMeetingSettings)
		roomRoutes.GET("/:room_id/waiting/:request_id", controllers.GetWaitingRequest)
		roomRoutes.DELETE("/:room_id/waiting/:request_id", controllers.CancelWaitingRequest)
		roomRoutes.POST("/:room_id/waiting/:request_id/admit", controllers.AdmitWaitingRequest)
		roomRoutes.POST("/:room_id/waiting/:request_id/reject", controllers.RejectWaitingRequest)
		roomRoutes.POST("/:room_id/transfer-ownership", controllers.TransferOwnership)
		roomRoutes.POST("/:room_id/leave", controllers.LeaveRoom)
		roomRoutes.GET("/:room_id/messages", controllers.ListRoomMessages)
		roomRoutes.POST("/:room_id/messages", controllers.SendRoomMessage)
	}

	// The app's realtime connection; it says who it is in its first
	// message, not in the URL.
	r.GET("/ws", controllers.RealtimeSocket)

	// Profile pictures; public, under names nobody can guess.
	r.GET("/avatars/:name", controllers.ServeAvatar)
	r.HEAD("/avatars/:name", controllers.ServeAvatar)

	// Profiles, friends, teams and their chats belong to accounts; guests
	// are turned away.
	social := r.Group("", middleware.RequireAuth(), middleware.RequireAccount())
	{
		social.GET("/me", controllers.GetProfile)
		social.PATCH("/me", controllers.UpdateProfile)
		social.PUT("/me/avatar", controllers.UploadAvatar)
		social.DELETE("/me/avatar", controllers.DeleteAvatar)
		social.PUT("/me/push-device", controllers.PutPushDevice)
		social.DELETE("/me/push-device", controllers.DeletePushDevice)
		social.GET("/social", controllers.GetSocialOverview)
		social.GET("/notifications", controllers.GetNotifications)

		social.POST("/friends/requests", controllers.SendFriendRequest)
		social.POST("/friends/requests/:request_id/accept", controllers.AcceptFriendRequest)
		social.POST("/friends/requests/:request_id/decline", controllers.DeclineFriendRequest)
		social.DELETE("/friends/:user_id", controllers.RemoveFriend)

		social.POST("/teams", controllers.CreateTeam)
		social.GET("/teams/:team_id", controllers.GetTeam)
		social.DELETE("/teams/:team_id", controllers.DeleteTeam)
		social.POST("/teams/:team_id/members", controllers.AddTeamMember)
		social.DELETE("/teams/:team_id/members/:user_id", controllers.RemoveTeamMember)
		social.PUT("/teams/:team_id/members/:user_id/role", controllers.SetTeamMemberRole)
		social.POST("/teams/:team_id/meeting", controllers.StartTeamMeeting)

		social.GET("/chats/:kind/:id/messages", controllers.ListConversationMessages)
		social.POST("/chats/:kind/:id/messages", controllers.SendConversationMessage)
		social.POST("/chats/:kind/:id/read", controllers.MarkConversationRead)
		social.POST("/chats/friend/:id/call", controllers.CallFriend)
	}
}
