package controllers

import (
	"net/http"
	"os"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/webhook"
)

// maxWebhookBytes bounds the body read before the signature is checked.
const maxWebhookBytes = 1 << 20

// LiveKitWebhook receives LiveKit server events. Every request must carry a
// JWT signed with the LiveKit API key/secret whose sha256 claim matches the
// body; anything else is rejected with 401. participant_joined removes a
// banned user again (a removed client may still hold a valid LiveKit token or
// refresh token); participant_left and room_finished run owner succession;
// other events are acknowledged and ignored.
func LiveKitWebhook(c *gin.Context) {
	key, secret := os.Getenv("LIVEKIT_API_KEY"), os.Getenv("LIVEKIT_API_SECRET")
	if key == "" || secret == "" {
		apierror.Respond(c, http.StatusServiceUnavailable, apierror.MediaServerNotConfigured, "Toplantı sunucusu yapılandırılmamış")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxWebhookBytes)
	event, err := webhook.ReceiveWebhookEvent(c.Request, auth.NewSimpleKeyProvider(key, secret))
	if err != nil {
		apierror.Respond(c, http.StatusUnauthorized, apierror.InvalidWebhook, "Webhook doğrulanamadı")
		return
	}
	if event.Event == webhook.EventParticipantJoined {
		removeIfBanned(c, event.GetRoom().GetName(), event.GetParticipant().GetIdentity())
		return
	}
	if event.Event != webhook.EventParticipantLeft && event.Event != webhook.EventRoomFinished {
		c.Status(http.StatusNoContent)
		return
	}
	roomID := event.GetRoom().GetName()
	release := lockRoomLifecycle(roomID)
	defer release()
	var room models.Room
	if err := database.DB.Where("room_id = ? AND status = ?", roomID, models.RoomStatusActive).Limit(1).Find(&room).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
		return
	}
	if room.ID == 0 {
		// Unknown or closed room: nothing to hand over.
		c.Status(http.StatusNoContent)
		return
	}
	ctx, svc, err := connectLiveKit(c.Request.Context(), room.RoomID)
	if err != nil {
		apierror.Respond(c, http.StatusServiceUnavailable, apierror.MediaServerNotConfigured, "Toplantı sunucusu yapılandırılmamış")
		return
	}
	ps, err := connectedParticipants(ctx, svc, room.RoomID)
	if err != nil {
		apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Katılımcılar sunucudan alınamadı")
		return
	}
	// LiveKit may still list the departing session briefly.
	left := event.GetParticipant().GetIdentity()
	gone := func(p *livekit.ParticipantInfo) bool { return left != "" && p.Identity == left }
	if err := runSuccession(ctx, svc, &room, ps, gone); err != nil {
		apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Yeni oda sahibi belirlenemedi")
		return
	}
	c.Status(http.StatusNoContent)
}

// removeIfBanned disconnects a removed user who reconnected to the media room
// with a token issued before the ban.
func removeIfBanned(c *gin.Context, roomID, identity string) {
	userID, err := participantUserID(identity)
	if err != nil {
		c.Status(http.StatusNoContent)
		return
	}
	banned, err := isBanned(roomID, userID)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
		return
	}
	if !banned {
		c.Status(http.StatusNoContent)
		return
	}
	ctx, svc, err := connectLiveKit(c.Request.Context(), roomID)
	if err != nil {
		apierror.Respond(c, http.StatusServiceUnavailable, apierror.MediaServerNotConfigured, "Toplantı sunucusu yapılandırılmamış")
		return
	}
	if err = removeSessions(ctx, svc, roomID, []*livekit.ParticipantInfo{{Identity: identity}}); err != nil {
		apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Katılımcı çıkarılamadı")
		return
	}
	c.Status(http.StatusNoContent)
}
