package controllers

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/proto"
	"net/http"
	"reisub-backend/database"
	"reisub-backend/models"
	"time"
)

// All admission, permission updates, settings and closure share the room lock.
// Join JWTs never grant screen sources; only the selected live identity gets them.
func restrictScreenShare(permission *livekit.ParticipantPermission, allowed bool) {
	sources := permission.CanPublishSources
	if len(sources) == 0 && permission.CanPublish {
		sources = []livekit.TrackSource{livekit.TrackSource_CAMERA, livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO}
	}
	filtered := []livekit.TrackSource{}
	for _, s := range sources {
		if s != livekit.TrackSource_SCREEN_SHARE && s != livekit.TrackSource_SCREEN_SHARE_AUDIO {
			filtered = append(filtered, s)
		}
	}
	if allowed && permission.CanPublish {
		filtered = append(filtered, livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO)
	}
	permission.CanPublishSources = filtered
	if len(filtered) == 0 {
		permission.CanPublish = false
	}
	permission.CanUpdateMetadata = false
}

func setScreenPermission(ctx context.Context, svc roomModerationService, roomID string, p *livekit.ParticipantInfo, allowed bool) error {
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

func activeMeeting(c *gin.Context) (models.Room, bool) {
	var room models.Room
	if err := database.DB.Where("room_id = ? AND status = ?", c.Param("room_id"), "active").First(&room).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Oda bulunamadı veya kapatıldı"})
		return room, false
	}
	return room, true
}
func screenParticipants(c *gin.Context, room models.Room) (context.Context, roomModerationService, []*livekit.ParticipantInfo, bool) {
	ctx, svc, err := moderationService(c.Request.Context(), room.RoomID)
	if err != nil {
		c.JSON(503, gin.H{"error": "Toplantı sunucusu yapılandırılmamış"})
		return ctx, svc, nil, false
	}
	list, err := svc.ListParticipants(ctx, &livekit.ListParticipantsRequest{Room: room.RoomID})
	if err != nil {
		c.JSON(502, gin.H{"error": "Katılımcılar alınamadı"})
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
func releaseStaleShare(ctx context.Context, svc roomModerationService, room *models.Room, ps []*livekit.ParticipantInfo) error {
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
	room, ok := activeMeeting(c)
	if !ok {
		return
	}
	ctx, svc, ps, ok := screenParticipants(c, room)
	if !ok {
		return
	}
	if err := releaseStaleShare(ctx, svc, &room, ps); err != nil {
		c.JSON(502, gin.H{"error": "Paylaşım durumu güncellenemedi"})
		return
	}
	c.JSON(200, meetingState(room))
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
		c.JSON(400, gin.H{"error": "Paylaşım izni gerekli"})
		return
	}
	// Save the restriction before touching media so failures never admit new guests.
	if err := database.DB.Model(&room).Update("allow_participant_screen_share", *input.Allowed).Error; err != nil {
		c.JSON(500, gin.H{"error": "Ayar kaydedilemedi"})
		return
	}
	room.AllowParticipantScreenShare = *input.Allowed
	if !*input.Allowed && room.ScreenShareIdentity != "" {
		uid, _ := participantUserID(room.ScreenShareIdentity)
		if uid != room.OwnerID {
			ctx, svc, ps, ok := screenParticipants(c, room)
			if !ok {
				return
			}
			for _, p := range ps {
				if p.Identity == room.ScreenShareIdentity {
					if err := setScreenPermission(ctx, svc, room.RoomID, p, false); err != nil {
						c.JSON(502, gin.H{"error": "İzin kapatıldı fakat paylaşım durdurulamadı. Tekrar deneyin."})
						return
					}
				}
			}
			if err := saveShare(&room, "", 0); err != nil {
				c.JSON(500, gin.H{"error": "Paylaşım durumu kaydedilemedi"})
				return
			}
		}
	}
	c.JSON(200, meetingState(room))
}
func StartScreenShare(c *gin.Context) {
	release := lockRoomLifecycle(c.Param("room_id"))
	defer release()
	room, ok := activeMeeting(c)
	if !ok {
		return
	}
	user := c.MustGet("user").(models.User)
	if user.ID != room.OwnerID && !room.AllowParticipantScreenShare {
		c.JSON(403, gin.H{"error": "Oda sahibi katılımcıların ekran paylaşmasına izin vermiyor"})
		return
	}
	var input struct {
		Identity string `json:"identity"`
	}
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(400, gin.H{"error": "Katılımcı kimliği gerekli"})
		return
	}
	uid, err := participantUserID(input.Identity)
	if err != nil || uid != user.ID {
		c.JSON(403, gin.H{"error": "Yalnızca kendi oturumunuzdan paylaşabilirsiniz"})
		return
	}
	ctx, svc, ps, ok := screenParticipants(c, room)
	if !ok {
		return
	}
	if err := releaseStaleShare(ctx, svc, &room, ps); err != nil {
		c.JSON(502, gin.H{"error": "Paylaşım durumu alınamadı"})
		return
	}
	if room.ScreenShareIdentity != "" && room.ScreenShareIdentity != input.Identity {
		c.JSON(409, gin.H{"error": "Başka bir katılımcı ekranını paylaşıyor"})
		return
	}
	var target *livekit.ParticipantInfo
	for _, p := range ps {
		if p.Identity == input.Identity {
			target = p
		}
	}
	if target == nil {
		c.JSON(404, gin.H{"error": "Önce toplantıya bağlanın"})
		return
	}
	if target.Permission == nil || !target.Permission.CanPublish {
		c.JSON(403, gin.H{"error": "Bu oturumun yayın izni yok"})
		return
	}
	if err := saveShare(&room, input.Identity, time.Now().Add(30*time.Second).Unix()); err != nil {
		c.JSON(500, gin.H{"error": "Paylaşım başlatılamadı"})
		return
	}
	// Revoke every other publisher before granting the selected one.
	for _, p := range ps {
		if p.Identity != input.Identity {
			if err := setScreenPermission(ctx, svc, room.RoomID, p, false); err != nil {
				c.JSON(502, gin.H{"error": "Paylaşım izinleri güncellenemedi"})
				return
			}
		}
	}
	if err := setScreenPermission(ctx, svc, room.RoomID, target, true); err != nil {
		c.JSON(502, gin.H{"error": "Paylaşım izni verilemedi. Tekrar deneyin."})
		return
	}
	c.JSON(200, meetingState(room))
}
func StopScreenShare(c *gin.Context) {
	release := lockRoomLifecycle(c.Param("room_id"))
	defer release()
	room, ok := activeMeeting(c)
	if !ok {
		return
	}
	var input struct {
		Identity string `json:"identity"`
	}
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(400, gin.H{"error": "Katılımcı kimliği gerekli"})
		return
	}
	user := c.MustGet("user").(models.User)
	uid, err := participantUserID(input.Identity)
	if err != nil || (uid != user.ID && user.ID != room.OwnerID) {
		c.JSON(403, gin.H{"error": "Bu paylaşımı durdurma yetkiniz yok"})
		return
	}
	if room.ScreenShareIdentity == "" {
		c.JSON(200, meetingState(room))
		return
	}
	if input.Identity != room.ScreenShareIdentity {
		c.JSON(409, gin.H{"error": "Başka bir oturum ekran paylaşıyor"})
		return
	}
	ctx, svc, ps, ok := screenParticipants(c, room)
	if !ok {
		return
	}
	for _, p := range ps {
		if p.Identity == input.Identity {
			if err := setScreenPermission(ctx, svc, room.RoomID, p, false); err != nil {
				c.JSON(502, gin.H{"error": "Paylaşım durdurulamadı"})
				return
			}
		}
	}
	if err := saveShare(&room, "", 0); err != nil {
		c.JSON(500, gin.H{"error": "Paylaşım durumu kaydedilemedi"})
		return
	}
	c.JSON(200, meetingState(room))
}
