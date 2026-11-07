// Package apierror writes the API's JSON error responses.
//
// Every error body has the form {"error": "<message>", "code": "<code>"}.
// "error" is a human-readable (Turkish) message that clients may show
// verbatim; "code" is a stable, machine-readable identifier clients should
// branch on or localize from. Codes are never renamed once released; new
// codes may be added.
package apierror

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Code is a stable machine-readable error identifier.
type Code string

// General.
const (
	// InvalidRequest: the body is missing or malformed.
	InvalidRequest Code = "invalid_request"
	// NotFound: no such route.
	NotFound Code = "not_found"
	// Internal: an unexpected server-side failure (usually the database).
	Internal Code = "internal_error"
)

// Authentication and accounts.
const (
	// MissingCredentials: nick or password missing, or nick blank after trimming.
	MissingCredentials Code = "missing_credentials"
	// PasswordTooLong: password exceeds bcrypt's 72-byte limit.
	PasswordTooLong Code = "password_too_long"
	// NickTaken: another account already uses this nick.
	NickTaken Code = "nick_taken"
	// UserNotFound: no account with this nick.
	UserNotFound Code = "user_not_found"
	// WrongPassword: the password does not match.
	WrongPassword Code = "wrong_password"
	// AuthRequired: no bearer token was sent (HTTP 401).
	AuthRequired Code = "auth_required"
	// InvalidToken: the token is unknown or expired (HTTP 401 on /rooms).
	InvalidToken Code = "invalid_token"
)

// Rooms and participants.
const (
	// NameRequired: the display name is missing.
	NameRequired Code = "name_required"
	// InvalidRoomRequest: display name or a NNN-NNN-NNN room code is missing.
	InvalidRoomRequest Code = "invalid_room_request"
	// RoomNotFound: the room does not exist or is not active.
	RoomNotFound Code = "room_not_found"
	// RoomNotActive: the room exists but is closing or closed.
	RoomNotActive Code = "room_not_active"
	// NotRoomOwner: only the room owner may do this.
	NotRoomOwner Code = "not_room_owner"
	// InvalidIdentity: the participant identity is malformed.
	InvalidIdentity Code = "invalid_identity"
	// IdentityNotOwned: the identity belongs to another user.
	IdentityNotOwned Code = "identity_not_owned"
	// ParticipantNotFound: the identity is not connected to the media room.
	ParticipantNotFound Code = "participant_not_found"
	// MediaServerNotConfigured: LiveKit credentials or URL are missing.
	MediaServerNotConfigured Code = "media_server_not_configured"
	// MediaServerError: a LiveKit call failed; the request may be retried.
	MediaServerError Code = "media_server_error"
)

// Screen sharing.
const (
	// ScreenShareNotAllowed: the owner has not allowed participants to share.
	ScreenShareNotAllowed Code = "screen_share_not_allowed"
	// ScreenShareBusy: another session holds the room's screen-share slot.
	ScreenShareBusy Code = "screen_share_busy"
	// PublishNotPermitted: the session may not publish media at all.
	PublishNotPermitted Code = "publish_not_permitted"
)

// Chat.
const (
	// InvalidMessage: the message is empty or longer than 1000 characters.
	InvalidMessage Code = "invalid_message"
)

// Body is the JSON shape of every error response.
type Body struct {
	Error string `json:"error"`
	Code  Code   `json:"code"`
}

// Respond writes an error response.
func Respond(c *gin.Context, status int, code Code, message string) {
	c.JSON(status, Body{Error: message, Code: code})
}

// Abort writes an error response and stops the handler chain.
func Abort(c *gin.Context, status int, code Code, message string) {
	c.AbortWithStatusJSON(status, Body{Error: message, Code: code})
}

// NoRoute answers requests for unknown paths.
func NoRoute(c *gin.Context) {
	Respond(c, http.StatusNotFound, NotFound, "İstek yolu bulunamadı")
}

// Recovery turns a handler panic into a JSON 500 instead of an empty body.
func Recovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, _ any) {
		Abort(c, http.StatusInternalServerError, Internal, "Sunucu hatası")
	})
}
