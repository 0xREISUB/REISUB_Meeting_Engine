package controllers

import (
	"context"
	"net/http"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/proto"
)

// Screen sharing is exclusive per room. Join JWTs never grant screen sources;
// only the live identity holding the room's slot receives them through
// UpdateParticipant. Admission, permission updates, settings and closure all
// run under the room lifecycle lock.

// screenShareStartGrace is how long a new holder has to publish its screen
// track before the reservation counts as abandoned.
const screenShareStartGrace = 30 * time.Second

func allPublishSources() []livekit.TrackSource {
	return []livekit.TrackSource{
		livekit.TrackSource_CAMERA,
		livekit.TrackSource_MICROPHONE,
		livekit.TrackSource_SCREEN_SHARE,
		livekit.TrackSource_SCREEN_SHARE_AUDIO,
	}
}

func isScreenSource(source livekit.TrackSource) bool {
	return source == livekit.TrackSource_SCREEN_SHARE || source == livekit.TrackSource_SCREEN_SHARE_AUDIO
}

// restrictScreenShare adds or removes both screen sources while preserving
// every other publish permission.
func restrictScreenShare(permission *livekit.ParticipantPermission, allowed bool) {
	sources := permission.CanPublishSources
	if len(sources) == 0 && permission.CanPublish {
		sources = allPublishSources()
	}
	filtered := []livekit.TrackSource{}
	for _, s := range sources {
		if !isScreenSource(s) {
			filtered = append(filtered, s)
		}
	}
	if allowed && permission.CanPublish {
		filtered = append(filtered, livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO)
	}
	permission.CanPublishSources = filtered
	// An empty source list means unrestricted when CanPublish remains true.
	if len(filtered) == 0 {
		permission.CanPublish = false
	}
	permission.CanUpdateMetadata = false
}

func setScreenPermission(ctx context.Context, svc liveKitRoomService, roomID string, p *livekit.ParticipantInfo, allowed bool) error {
	if p.Permission == nil {
		return nil
	}
	permission := proto.Clone(p.Permission).(*livekit.ParticipantPermission)
	restrictScreenShare(permission, allowed)
	if proto.Equal(permission, p.Permission) {
		return nil
	}
	_, err := svc.UpdateParticipant(ctx, &livekit.UpdateParticipantRequest{Room: roomID, Identity: p.Identity, Permission: permission})
	return err
}

// activeRoom loads the active room named in the path. It writes the error
// response itself and reports whether to continue.
func activeRoom(c *gin.Context) (models.Room, bool) {
	var room models.Room
	if err := database.DB.Where("room_id = ? AND status = ?", c.Param("room_id"), models.RoomStatusActive).First(&room).Error; err != nil {
		apierror.Respond(c, http.StatusNotFound, apierror.RoomNotFound, "Oda bulunamadı veya kapatıldı")
		return room, false
	}
	return room, true
}

func listRoomParticipants(c *gin.Context, room models.Room) (context.Context, liveKitRoomService, []*livekit.ParticipantInfo, bool) {
	ctx, svc, err := connectLiveKit(c.Request.Context(), room.RoomID)
	if err != nil {
		apierror.Respond(c, http.StatusServiceUnavailable, apierror.MediaServerNotConfigured, "Toplantı sunucusu yapılandırılmamış")
		return ctx, svc, nil, false
	}
	list, err := svc.ListParticipants(ctx, &livekit.ListParticipantsRequest{Room: room.RoomID})
	if err != nil {
		apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Katılımcılar alınamadı")
		return ctx, svc, nil, false
	}
	return ctx, svc, list.Participants, true
}

func saveShare(room *models.Room, identity string, until int64) error {
	if err := database.DB.Model(room).Updates(map[string]interface{}{"screen_share_identity": identity, "screen_share_lease_until": until}).Error; err != nil {
		return err
	}
	room.ScreenShareIdentity = identity
	room.ScreenShareLeaseUntil = until
	return nil
}

// releaseStaleShare frees the slot when its holder has left the room, or has
// no live screen track after the start grace period.
func releaseStaleShare(ctx context.Context, svc liveKitRoomService, room *models.Room, ps []*livekit.ParticipantInfo) error {
	if room.ScreenShareIdentity == "" {
		return nil
	}
	for _, p := range ps {
		if p.Identity != room.ScreenShareIdentity {
			continue
		}
		for _, t := range p.Tracks {
			if t.Source == livekit.TrackSource_SCREEN_SHARE && !t.Muted {
				return nil
			}
		}
		if time.Now().Unix() < room.ScreenShareLeaseUntil {
			return nil
		}
		if err := setScreenPermission(ctx, svc, room.RoomID, p, false); err != nil {
			return err
		}
	}
	return saveShare(room, "", 0)
}

func meetingState(room models.Room) gin.H {
	return gin.H{"allow_participant_screen_share": room.AllowParticipantScreenShare, "screen_share_identity": room.ScreenShareIdentity}
}

func GetMeetingState(c *gin.Context) {
	release := lockRoomLifecycle(c.Param("room_id"))
	defer release()
	room, ok := activeRoom(c)
	if !ok {
		return
	}
	ctx, svc, ps, ok := listRoomParticipants(c, room)
	if !ok {
		return
	}
	if err := releaseStaleShare(ctx, svc, &room, ps); err != nil {
		apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Paylaşım durumu güncellenemedi")
		return
	}
	c.JSON(http.StatusOK, meetingState(room))
}

func UpdateMeetingSettings(c *gin.Context) {
	release := lockRoomLifecycle(c.Param("room_id"))
	defer release()
	room, ok := ownerRoom(c, false)
	if !ok {
		return
	}
	var input struct {
		Allowed *bool `json:"allow_participant_screen_share"`
	}
	if c.ShouldBindJSON(&input) != nil || input.Allowed == nil {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidRequest, "Paylaşım izni gerekli")
		return
	}
	// Save the restriction before touching media so failures never admit new guests.
	if err := database.DB.Model(&room).Update("allow_participant_screen_share", *input.Allowed).Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Ayar kaydedilemedi")
		return
	}
	room.AllowParticipantScreenShare = *input.Allowed
	if !*input.Allowed && room.ScreenShareIdentity != "" {
		uid, _ := participantUserID(room.ScreenShareIdentity)
		if uid != room.OwnerID {
			ctx, svc, ps, ok := listRoomParticipants(c, room)
			if !ok {
				return
			}
			for _, p := range ps {
				if p.Identity == room.ScreenShareIdentity {
					if err := setScreenPermission(ctx, svc, room.RoomID, p, false); err != nil {
						apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "İzin kapatıldı fakat paylaşım durdurulamadı. Tekrar deneyin.")
						return
					}
				}
			}
			if err := saveShare(&room, "", 0); err != nil {
				apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Paylaşım durumu kaydedilemedi")
				return
			}
		}
	}
	c.JSON(http.StatusOK, meetingState(room))
}

func StartScreenShare(c *gin.Context) {
	release := lockRoomLifecycle(c.Param("room_id"))
	defer release()
	room, ok := activeRoom(c)
	if !ok {
		return
	}
	user := c.MustGet("user").(models.User)
	if user.ID != room.OwnerID && !room.AllowParticipantScreenShare {
		apierror.Respond(c, http.StatusForbidden, apierror.ScreenShareNotAllowed, "Oda sahibi katılımcıların ekran paylaşmasına izin vermiyor")
		return
	}
	var input struct {
		Identity string `json:"identity"`
	}
	if c.ShouldBindJSON(&input) != nil {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidRequest, "Katılımcı kimliği gerekli")
		return
	}
	uid, err := participantUserID(input.Identity)
	if err != nil || uid != user.ID {
		apierror.Respond(c, http.StatusForbidden, apierror.IdentityNotOwned, "Yalnızca kendi oturumunuzdan paylaşabilirsiniz")
		return
	}
	ctx, svc, ps, ok := listRoomParticipants(c, room)
	if !ok {
		return
	}
	if err := releaseStaleShare(ctx, svc, &room, ps); err != nil {
		apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Paylaşım durumu alınamadı")
		return
	}
	if room.ScreenShareIdentity != "" && room.ScreenShareIdentity != input.Identity {
		apierror.Respond(c, http.StatusConflict, apierror.ScreenShareBusy, "Başka bir katılımcı ekranını paylaşıyor")
		return
	}
	var target *livekit.ParticipantInfo
	for _, p := range ps {
		if p.Identity == input.Identity {
			target = p
		}
	}
	if target == nil {
		apierror.Respond(c, http.StatusNotFound, apierror.ParticipantNotFound, "Önce toplantıya bağlanın")
		return
	}
	if target.Permission == nil || !target.Permission.CanPublish {
		apierror.Respond(c, http.StatusForbidden, apierror.PublishNotPermitted, "Bu oturumun yayın izni yok")
		return
	}
	if err := saveShare(&room, input.Identity, time.Now().Add(screenShareStartGrace).Unix()); err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Paylaşım başlatılamadı")
		return
	}
	// Revoke every other publisher before granting the selected one.
	for _, p := range ps {
		if p.Identity != input.Identity {
			if err := setScreenPermission(ctx, svc, room.RoomID, p, false); err != nil {
				apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Paylaşım izinleri güncellenemedi")
				return
			}
		}
	}
	if err := setScreenPermission(ctx, svc, room.RoomID, target, true); err != nil {
		apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Paylaşım izni verilemedi. Tekrar deneyin.")
		return
	}
	c.JSON(http.StatusOK, meetingState(room))
}

func StopScreenShare(c *gin.Context) {
	release := lockRoomLifecycle(c.Param("room_id"))
	defer release()
	room, ok := activeRoom(c)
	if !ok {
		return
	}
	var input struct {
		Identity string `json:"identity"`
	}
	if c.ShouldBindJSON(&input) != nil {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidRequest, "Katılımcı kimliği gerekli")
		return
	}
	user := c.MustGet("user").(models.User)
	uid, err := participantUserID(input.Identity)
	if err != nil || (uid != user.ID && user.ID != room.OwnerID) {
		apierror.Respond(c, http.StatusForbidden, apierror.IdentityNotOwned, "Bu paylaşımı durdurma yetkiniz yok")
		return
	}
	if room.ScreenShareIdentity == "" {
		c.JSON(http.StatusOK, meetingState(room))
		return
	}
	if input.Identity != room.ScreenShareIdentity {
		apierror.Respond(c, http.StatusConflict, apierror.ScreenShareBusy, "Başka bir oturum ekran paylaşıyor")
		return
	}
	ctx, svc, ps, ok := listRoomParticipants(c, room)
	if !ok {
		return
	}
	for _, p := range ps {
		if p.Identity == input.Identity {
			if err := setScreenPermission(ctx, svc, room.RoomID, p, false); err != nil {
				apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Paylaşım durdurulamadı")
				return
			}
		}
	}
	if err := saveShare(&room, "", 0); err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Paylaşım durumu kaydedilemedi")
		return
	}
	c.JSON(http.StatusOK, meetingState(room))
}
