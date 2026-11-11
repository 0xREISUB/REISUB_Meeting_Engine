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
	"gorm.io/gorm/clause"
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
	// IsHost is true only for the owner; older clients still read it.
	IsHost bool   `json:"is_host"`
	Role   string `json:"role"`
	// Permissions at join time, in wire order; all of them for the owner.
	// Later changes arrive as the meeting.permissions LiveKit attribute.
	Permissions []string `json:"permissions"`
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
	now := time.Now()
	room := models.Room{
		RoomID:     roomID,
		Name:       strings.TrimSpace(input.Name),
		OwnerID:    user.ID,
		Status:     models.RoomStatusCreating,
		LastUsedAt: &now,
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
	if err = rememberDisplayName(room.RoomID, user.ID, input.Name); err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda oluşturulamadı")
		return
	}
	response, err := makeRoomResponse(room, user.ID, strings.TrimSpace(input.Name), ownerAccess())
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
	// Removed users get no token, and so cannot become acting owner either.
	if !rejectBanned(c, room.RoomID) {
		return
	}
	user := c.MustGet("user").(models.User)
	if room.WaitingRoom {
		direct, err := skipsWaitingRoom(room.RoomID, user.ID)
		if err != nil {
			apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
			return
		}
		if !direct {
			requestID, err := requestToWait(room.RoomID, user.ID, strings.TrimSpace(input.Name))
			if err != nil {
				apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Katılma isteği kaydedilemedi")
				return
			}
			c.JSON(http.StatusAccepted, gin.H{"status": models.WaitingStatusWaiting, "request_id": requestID})
			return
		}
	}
	response, ok := enterRoom(c, &room, user.ID, input.Name)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, response)
}

// enterRoom prepares the media room and issues join credentials to a user
// who may enter. It writes the error response itself on failure.
func enterRoom(c *gin.Context, room *models.Room, userID uint, name string) (roomResponse, bool) {
	ctx, service, err := connectLiveKit(c.Request.Context(), room.RoomID)
	if err != nil {
		apierror.Respond(c, http.StatusServiceUnavailable, apierror.MediaServerNotConfigured, "Toplantı sunucusu yapılandırılmamış")
		return roomResponse{}, false
	}
	// Empty LiveKit rooms expire independently of the persisted room code.
	// This is idempotent for an existing media session, and serialized with close.
	if _, err = service.CreateRoom(ctx, &livekit.CreateRoomRequest{Name: room.RoomID}); err != nil {
		apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Toplantı sunucusunda oda hazırlanamadı")
		return roomResponse{}, false
	}
	markRoomUsed(room, true)
	if err = claimOwnershipOnJoin(ctx, service, room, userID); err != nil {
		apierror.Respond(c, http.StatusBadGateway, apierror.MediaServerError, "Oda sahipliği güncellenemedi. Tekrar deneyin.")
		return roomResponse{}, false
	}
	if err = rememberDisplayName(room.RoomID, userID, name); err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
		return roomResponse{}, false
	}
	userAccess, err := roomAccess(room.RoomID, userID)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Oda okunamadı")
		return roomResponse{}, false
	}
	response, err := makeRoomResponse(*room, userID, strings.TrimSpace(name), userAccess)
	if err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, err.Error())
		return roomResponse{}, false
	}
	return response, true
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

func makeRoomResponse(room models.Room, userID uint, participant string, a access) (roomResponse, error) {
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
	// Screen sources are never part of a join token, even with share_screen:
	// only the holder of the room's single screen-share slot receives them
	// (see StartScreenShare).
	sources := []livekit.TrackSource{livekit.TrackSource_CAMERA, livekit.TrackSource_MICROPHONE}
	if locked {
		sources = []livekit.TrackSource{livekit.TrackSource_CAMERA}
	}
	grant.SetCanPublishSources(sources)
	attrs := a.attributes()
	attrs[microphoneLockAttribute] = "false"
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
		IsHost:      a.Role == models.RoleOwner,
		Role:        a.Role,
		Permissions: a.Permissions,
		InviteURL:   inviteURL(room.RoomID),
	}, nil
}

func boolPtr(value bool) *bool { return &value }

// rememberDisplayName stores the name the user chose for this room; the
// latest create/join wins.
func rememberDisplayName(roomID string, userID uint, name string) error {
	return rememberDisplayNameTx(database.DB, roomID, userID, name)
}

func rememberDisplayNameTx(db *gorm.DB, roomID string, userID uint, name string) error {
	member := models.RoomMember{RoomID: roomID, UserID: userID, DisplayName: strings.TrimSpace(name)}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "room_id"}, {Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"display_name", "updated_at"}),
	}).Create(&member).Error
}

// displayName is how a user appears in a room's chat: the name they joined
// with, otherwise their nick, and never a guest's generated nick.
func displayName(roomID string, user models.User) string {
	var members []models.RoomMember
	database.DB.Where("room_id = ? AND user_id = ?", roomID, user.ID).Limit(1).Find(&members)
	switch {
	case len(members) > 0 && members[0].DisplayName != "":
		return members[0].DisplayName
	case user.IsGuest:
		return "Misafir"
	}
	return user.Nick
}

// inviteURL is the short invite link for a room, served by the web app at
// PUBLIC_WEB_URL (e.g. https://meeting.reisub.xyz/j/123-456-789).
func inviteURL(roomID string) string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_WEB_URL")), "/")
	if base == "" {
		return ""
	}
	return base + "/j/" + roomID
}
