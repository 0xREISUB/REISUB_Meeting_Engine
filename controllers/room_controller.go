package controllers

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"gorm.io/gorm"
)

var roomIDPattern = regexp.MustCompile(`^\d{3}-\d{3}-\d{3}$`)

type roomRequest struct {
	Name   string `json:"name"`
	RoomID string `json:"room_id"`
}

type roomResponse struct {
	RoomID      string `json:"room_id"`
	LiveKitURL  string `json:"livekit_url"`
	Token       string `json:"token"`
	Participant string `json:"participant"`
	// IsHost is true for owners and moderators so older clients still show
	// the moderation controls; Role is the precise room role.
	IsHost bool   `json:"is_host"`
	Role   string `json:"role"`
	// InviteURL is PUBLIC_WEB_URL + "/j/" + room ID; omitted when
	// PUBLIC_WEB_URL is not configured.
	InviteURL string `json:"invite_url,omitempty"`
}

func CreateRoom(c *gin.Context) {
	var input roomRequest
	if err := c.ShouldBindJSON(&input); err != nil || strings.TrimSpace(input.Name) == "" {
		apierror.Respond(c, http.StatusBadRequest, apierror.NameRequired, "İsim gerekli")
		return
	}

	user := c.MustGet("user").(models.User)
	if os.Getenv("LIVEKIT_API_KEY") == "" || os.Getenv("LIVEKIT_API_SECRET") == "" {
		apierror.Respond(c, http.StatusInternalServerError, apierror.MediaServerNotConfigured, "LiveKit yapılandırması eksik")
		return
	}
	roomID, err := newRoomID()
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda oluşturulamadı")
		return
	}
	room := models.Room{
		RoomID:  roomID,
		Name:    strings.TrimSpace(input.Name),
		OwnerID: user.ID,
		Status:  models.RoomStatusCreating,
	}
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&room).Error; err != nil {
			return err
		}
		return tx.Create(&models.RoomRole{RoomID: room.RoomID, UserID: user.ID, Role: models.RoleOwner, GrantedAt: time.Now(), GrantedBy: user.ID}).Error
	})
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda oluşturulamadı")
		return
	}

	ctx, service, err := connectLiveKit(c.Request.Context(), room.RoomID)
	if err != nil {
		database.DB.Delete(&room)
		apierror.Respond(c, http.StatusServiceUnavailable, apierror.MediaServerNotConfigured, "Toplantı sunucusu yapılandırılmamış")
		return
	}
	if _, err = service.CreateRoom(ctx, &livekit.CreateRoomRequest{Name: room.RoomID}); err != nil {
		database.DB.Delete(&room)
		apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Toplantı sunucusunda oda oluşturulamadı")
		return
	}
	if err = database.DB.Model(&room).Update("status", models.RoomStatusActive).Error; err != nil {
		service.DeleteRoom(ctx, &livekit.DeleteRoomRequest{Room: room.RoomID})
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda etkinleştirilemedi")
		return
	}
	response, err := makeRoomResponse(room, user.ID, strings.TrimSpace(input.Name), models.RoleOwner)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, err.Error())
		return
	}
	c.JSON(http.StatusCreated, response)
}

func JoinRoom(c *gin.Context) {
	var input roomRequest
	if err := c.ShouldBindJSON(&input); err != nil || strings.TrimSpace(input.Name) == "" || !roomIDPattern.MatchString(input.RoomID) {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidRoomRequest, "İsim ve geçerli oda kodu gerekli")
		return
	}

	releaseRoom := lockRoomLifecycle(input.RoomID)
	defer releaseRoom()
	var room models.Room
	if err := database.DB.Where("room_id = ? AND status = ?", input.RoomID, models.RoomStatusActive).First(&room).Error; err != nil {
		apierror.Respond(c, http.StatusNotFound, apierror.RoomNotFound, "Oda bulunamadı")
		return
	}

	ctx, service, err := connectLiveKit(c.Request.Context(), room.RoomID)
	if err != nil {
		apierror.Respond(c, http.StatusServiceUnavailable, apierror.MediaServerNotConfigured, "Toplantı sunucusu yapılandırılmamış")
		return
	}
	// Empty LiveKit rooms expire independently of the persisted room code.
	// This is idempotent for an existing media session, and serialized with close.
	if _, err = service.CreateRoom(ctx, &livekit.CreateRoomRequest{Name: room.RoomID}); err != nil {
		apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Toplantı sunucusunda oda hazırlanamadı")
		return
	}
	user := c.MustGet("user").(models.User)
	if err = claimOwnershipOnJoin(ctx, service, &room, user.ID); err != nil {
		apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Oda sahipliği güncellenemedi. Tekrar deneyin.")
		return
	}
	role, err := effectiveRole(room.RoomID, user.ID)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
		return
	}
	response, err := makeRoomResponse(room, user.ID, strings.TrimSpace(input.Name), role)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, err.Error())
		return
	}
	c.JSON(http.StatusOK, response)
}

// newRoomID returns an unused room code of the form NNN-NNN-NNN. The code is
// the only secret needed to join a room, so it must not be predictable.
func newRoomID() (string, error) {
	for attempt := 0; attempt < 10; attempt++ {
		n, err := rand.Int(rand.Reader, big.NewInt(1_000_000_000))
		if err != nil {
			return "", err
		}
		v := n.Int64()
		roomID := fmt.Sprintf("%03d-%03d-%03d", v/1_000_000, v/1_000%1_000, v%1_000)
		var count int64
		if err = database.DB.Model(&models.Room{}).Where("room_id = ?", roomID).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return roomID, nil
		}
	}
	return "", errors.New("no free room code found")
}

func makeRoomResponse(room models.Room, userID uint, participant, role string) (roomResponse, error) {
	apiKey := os.Getenv("LIVEKIT_API_KEY")
	apiSecret := os.Getenv("LIVEKIT_API_SECRET")
	if apiKey == "" || apiSecret == "" {
		return roomResponse{}, fmt.Errorf("LiveKit yapılandırması eksik")
	}

	grant := &auth.VideoGrant{
		Room:           room.RoomID,
		RoomJoin:       true,
		RoomAdmin:      false,
		CanPublish:     boolPtr(true),
		CanSubscribe:   boolPtr(true),
		CanPublishData: boolPtr(true),
	}
	locked, err := microphoneLocked(room.RoomID, userID)
	if err != nil {
		return roomResponse{}, fmt.Errorf("Mikrofon yetkisi okunamadı")
	}
	// Screen sources are never part of a join token; see StartScreenShare.
	sources := []livekit.TrackSource{livekit.TrackSource_CAMERA, livekit.TrackSource_MICROPHONE}
	if locked {
		sources = []livekit.TrackSource{livekit.TrackSource_CAMERA}
	}
	grant.SetCanPublishSources(sources)
	attrs := map[string]string{microphoneLockAttribute: "false", roleAttribute: roleAttributeValue(role)}
	if locked {
		attrs[microphoneLockAttribute] = "true"
		attrs[microphoneSourcesAttribute] = `{"can_publish":true,"sources":null}`
	}
	grant.SetCanUpdateOwnMetadata(false)
	token, err := auth.NewAccessToken(apiKey, apiSecret).
		SetIdentity(fmt.Sprintf("user-%d-%s", userID, uuid.NewString())).
		SetName(participant).
		SetValidFor(5 * time.Minute).
		SetAttributes(attrs).
		SetVideoGrant(grant).
		ToJWT()
	if err != nil {
		return roomResponse{}, err
	}

	return roomResponse{
		RoomID:      room.RoomID,
		LiveKitURL:  liveKitURL(),
		Token:       token,
		Participant: participant,
		IsHost:      roleRank(role) >= roleRank(models.RoleModerator),
		Role:        role,
		InviteURL:   inviteURL(room.RoomID),
	}, nil
}

func boolPtr(value bool) *bool { return &value }

// inviteURL is the short invite link for a room, served by the web app at
// PUBLIC_WEB_URL (e.g. https://meeting.reisub.xyz/j/123-456-789).
func inviteURL(roomID string) string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_WEB_URL")), "/")
	if base == "" {
		return ""
	}
	return base + "/j/" + roomID
}
