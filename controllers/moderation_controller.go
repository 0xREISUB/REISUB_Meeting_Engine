package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/twitchtv/twirp"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const microphoneLockAttribute = "meeting.microphone_locked"
const microphoneRequestAttribute = "meeting.microphone_request"
const microphoneSourcesAttribute = "meeting.microphone_original_permission"

type microphonePermissionBackup struct {
	CanPublish bool                  `json:"can_publish"`
	Sources    []livekit.TrackSource `json:"sources"`
}

type roomModerationService interface {
	CreateRoom(context.Context, *livekit.CreateRoomRequest) (*livekit.Room, error)
	DeleteRoom(context.Context, *livekit.DeleteRoomRequest) (*livekit.DeleteRoomResponse, error)
	ListParticipants(context.Context, *livekit.ListParticipantsRequest) (*livekit.ListParticipantsResponse, error)
	UpdateParticipant(context.Context, *livekit.UpdateParticipantRequest) (*livekit.ParticipantInfo, error)
	MutePublishedTrack(context.Context, *livekit.MuteRoomTrackRequest) (*livekit.MuteRoomTrackResponse, error)
}

var newModerationService = func(endpoint string) roomModerationService {
	return livekit.NewRoomServiceProtobufClient(endpoint, &http.Client{Timeout: 10 * time.Second})
}

func moderationService(ctx context.Context, roomID string) (context.Context, roomModerationService, error) {
	key, secret := os.Getenv("LIVEKIT_API_KEY"), os.Getenv("LIVEKIT_API_SECRET")
	if key == "" || secret == "" {
		return ctx, nil, errors.New("LiveKit credentials missing")
	}
	endpoint := os.Getenv("LIVEKIT_INTERNAL_URL")
	if endpoint == "" {
		endpoint = liveKitURL()
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return ctx, nil, err
	}
	if parsed.Scheme == "ws" {
		parsed.Scheme = "http"
	}
	if parsed.Scheme == "wss" {
		parsed.Scheme = "https"
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return ctx, nil, errors.New("invalid LiveKit service URL")
	}
	token, err := auth.NewAccessToken(key, secret).SetValidFor(time.Minute).
		SetVideoGrant(&auth.VideoGrant{Room: roomID, RoomAdmin: true, RoomCreate: true}).ToJWT()
	if err != nil {
		return ctx, nil, err
	}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)
	ctx, err = twirp.WithHTTPRequestHeaders(ctx, headers)
	if err != nil {
		return ctx, nil, err
	}
	return ctx, newModerationService(strings.TrimRight(parsed.String(), "/")), nil
}

func ownerRoom(c *gin.Context, allowClosed bool) (models.Room, bool) {
	var room models.Room
	if err := database.DB.Where("room_id = ?", c.Param("room_id")).First(&room).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Oda bulunamadı"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Oda okunamadı"})
		}
		return room, false
	}
	if room.OwnerID != c.MustGet("user").(models.User).ID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Bu işlem yalnızca oda yöneticisine açık"})
		return room, false
	}
	if !allowClosed && room.Status != "active" {
		c.JSON(http.StatusConflict, gin.H{"error": "Oda aktif değil"})
		return room, false
	}
	return room, true
}

func CloseRoom(c *gin.Context) {
	room, ok := ownerRoom(c, true)
	if !ok {
		return
	}
	if room.Status == "closed" {
		c.JSON(http.StatusOK, gin.H{"status": "closed"})
		return
	}
	ctx, service, err := moderationService(c.Request.Context(), room.RoomID)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Toplantı sunucusu yapılandırılmamış"})
		return
	}
	// Deny new join tokens before terminating active media sessions.
	if err = database.DB.Model(&room).Update("status", "closing").Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Oda kapatma işlemi başlatılamadı"})
		return
	}
	if _, err = service.DeleteRoom(ctx, &livekit.DeleteRoomRequest{Room: room.RoomID}); err != nil {
		var rpcErr twirp.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code() != twirp.NotFound {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Oda katılıma kapatıldı fakat mevcut bağlantılar sonlandırılamadı. Tekrar deneyin."})
			return
		}
	}
	if err = database.DB.Model(&room).Update("status", "closed").Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Oda kapandı fakat durum kaydedilemedi. Tekrar deneyin."})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "closed"})
}

func nonMicrophoneSources() []livekit.TrackSource {
	return []livekit.TrackSource{livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO}
}

func microphoneLocked(roomID string, userID uint) (bool, error) {
	var lock models.RoomMicrophoneLock
	err := database.DB.Where("room_id = ? AND user_id = ?", roomID, userID).First(&lock).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return lock.Locked, err
}

func participantUserID(identity string) (uint, error) {
	parts := strings.SplitN(identity, "-", 3)
	if len(parts) != 3 || parts[0] != "user" {
		return 0, errors.New("invalid identity")
	}
	id, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil || id == 0 {
		return 0, errors.New("invalid user")
	}
	if _, err = uuid.Parse(parts[2]); err != nil {
		return 0, err
	}
	return uint(id), nil
}

type microphoneModerationRequest struct {
	Locked        *bool `json:"locked"`
	RequestUnmute bool  `json:"request_unmute"`
}

func SetParticipantMicrophone(c *gin.Context) {
	room, ok := ownerRoom(c, false)
	if !ok {
		return
	}
	var input microphoneModerationRequest
	if err := c.ShouldBindJSON(&input); err != nil || input.Locked == nil || (*input.Locked && input.RequestUnmute) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Geçerli bir mikrofon kilidi işlemi gerekli"})
		return
	}
	targetID, err := participantUserID(c.Param("identity"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Katılımcı kimliği geçersiz"})
		return
	}
	ctx, service, err := moderationService(c.Request.Context(), room.RoomID)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Toplantı sunucusu yapılandırılmamış"})
		return
	}
	list, err := service.ListParticipants(ctx, &livekit.ListParticipantsRequest{Room: room.RoomID})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Katılımcılar sunucudan alınamadı"})
		return
	}
	found := false
	for _, p := range list.Participants {
		if p.Identity == c.Param("identity") {
			found = true
			break
		}
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "Katılımcı artık odada değil"})
		return
	}
	lock := models.RoomMicrophoneLock{RoomID: room.RoomID, UserID: targetID, Locked: *input.Locked}
	if err = database.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "room_id"}, {Name: "user_id"}}, DoUpdates: clause.AssignmentColumns([]string{"locked"})}).Create(&lock).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Mikrofon kilidi kaydedilemedi"})
		return
	}
	// Apply to every current session of the same user to prevent device/rejoin bypass.
	for _, p := range list.Participants {
		id, parseErr := participantUserID(p.Identity)
		if parseErr != nil || id != targetID {
			continue
		}
		if err = applyMicrophonePolicy(ctx, service, room.RoomID, p, *input.Locked, input.RequestUnmute); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Mikrofon politikası kaydedildi fakat tüm oturumlara uygulanamadı. Tekrar deneyin."})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"locked": *input.Locked, "unmute_requested": input.RequestUnmute})
}

func applyMicrophonePolicy(ctx context.Context, service roomModerationService, roomID string, p *livekit.ParticipantInfo, locked, requestUnmute bool) error {
	if p.Permission == nil {
		return errors.New("participant permissions unavailable")
	}
	permission := proto.Clone(p.Permission).(*livekit.ParticipantPermission)
	attrs := map[string]string{microphoneLockAttribute: strconv.FormatBool(locked), microphoneRequestAttribute: ""}
	if locked {
		if p.Attributes[microphoneLockAttribute] != "true" {
			original, err := json.Marshal(microphonePermissionBackup{
				CanPublish: permission.CanPublish, Sources: permission.CanPublishSources,
			})
			if err != nil {
				return err
			}
			attrs[microphoneSourcesAttribute] = string(original)
		}
		sources := permission.CanPublishSources
		if len(sources) == 0 && permission.CanPublish {
			sources = nonMicrophoneSources()
		}
		permission.CanPublishSources = nil
		for _, source := range sources {
			if source != livekit.TrackSource_MICROPHONE {
				permission.CanPublishSources = append(permission.CanPublishSources, source)
			}
		}
		// An empty source list means unrestricted when CanPublish remains true.
		if len(permission.CanPublishSources) == 0 {
			permission.CanPublish = false
		}
	} else {
		if p.Attributes[microphoneLockAttribute] == "true" {
			original := microphonePermissionBackup{CanPublish: true}
			if encoded := p.Attributes[microphoneSourcesAttribute]; encoded != "" {
				if err := json.Unmarshal([]byte(encoded), &original); err != nil {
					return err
				}
			}
			permission.CanPublish = original.CanPublish
			permission.CanPublishSources = original.Sources
		}
		attrs[microphoneSourcesAttribute] = ""
		if requestUnmute {
			attrs[microphoneRequestAttribute] = uuid.NewString()
		}
	}
	permission.CanUpdateMetadata = false
	if _, err := service.UpdateParticipant(ctx, &livekit.UpdateParticipantRequest{Room: roomID, Identity: p.Identity, Permission: permission, Attributes: attrs}); err != nil {
		return err
	}
	if locked {
		for _, track := range p.Tracks {
			if track.Source != livekit.TrackSource_MICROPHONE {
				continue
			}
			if _, err := service.MutePublishedTrack(ctx, &livekit.MuteRoomTrackRequest{Room: roomID, Identity: p.Identity, TrackSid: track.Sid, Muted: true}); err != nil {
				// Source revocation may already have unpublished the microphone.
				var rpcErr twirp.Error
				if !errors.As(err, &rpcErr) || rpcErr.Code() != twirp.NotFound {
					return fmt.Errorf("mute microphone: %w", err)
				}
			}
		}
	}
	return nil
}
