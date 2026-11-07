package controllers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
)

// newGuest calls POST /guest and returns the token and stored user.
func newGuest(t *testing.T, r *gin.Engine) (string, models.User) {
	t.Helper()
	w := jsonRequest(r, http.MethodPost, "/guest", "", "")
	var body struct {
		Token string `json:"token"`
		Guest bool   `json:"guest"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusCreated || !body.Guest || body.Token == "" {
		t.Fatalf("POST /guest = %d %s", w.Code, w.Body.String())
	}
	var user models.User
	if err := database.DB.Where("token = ?", body.Token).First(&user).Error; err != nil {
		t.Fatal(err)
	}
	return body.Token, user
}

// connectWith adds the LiveKit session described by a join/create response
// to the fake room and returns it.
func connectWith(t *testing.T, f *fakeModeration, raw []byte) *livekit.ParticipantInfo {
	t.Helper()
	var response roomResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.ParseAPIToken(response.Token)
	if err != nil {
		t.Fatal(err)
	}
	p := &livekit.ParticipantInfo{
		Identity:   verifier.Identity(),
		Permission: &livekit.ParticipantPermission{CanPublish: true, CanSubscribe: true, CanPublishData: true},
		Tracks:     []*livekit.TrackInfo{{Sid: "mic-" + verifier.Identity(), Source: livekit.TrackSource_MICROPHONE}},
	}
	f.participants = append(f.participants, p)
	return p
}

func TestGuestSessionIsCreatedWithExpiringToken(t *testing.T) {
	r, _ := moderationTestRouter(t)
	t.Setenv("GUEST_TOKEN_TTL", "48h")
	before := time.Now()
	token, user := newGuest(t, r)
	if !user.IsGuest || !strings.HasPrefix(user.Nick, models.GuestNickPrefix) || user.Password != "" || len(token) != 64 {
		t.Fatalf("unexpected guest: %+v", user)
	}
	if user.TokenExpiresAt == nil || user.TokenExpiresAt.Before(before.Add(47*time.Hour)) || user.TokenExpiresAt.After(time.Now().Add(48*time.Hour)) {
		t.Fatalf("token expiry %v, want about 48h", user.TokenExpiresAt)
	}
	// Each call is a new guest.
	if other, second := newGuest(t, r); other == token || second.ID == user.ID || second.Nick == user.Nick {
		t.Fatal("guest sessions are not distinct")
	}
}

func TestGuestTokenIsRejectedAfterExpiry(t *testing.T) {
	r, _ := moderationTestRouter(t)
	token, user := newGuest(t, r)
	if w := jsonRequest(r, http.MethodGet, "/rooms/123-456-789/state", token, ""); w.Code != 200 {
		t.Fatal("fresh guest token rejected:", w.Code, w.Body.String())
	}
	database.DB.Model(&user).Update("token_expires_at", time.Now().Add(-time.Second))
	assertAPIError(t, jsonRequest(r, http.MethodGet, "/rooms/123-456-789/state", token, ""), 401, apierror.InvalidToken, "Geçersiz token")
	// Login tokens still never expire.
	if w := jsonRequest(r, http.MethodGet, "/rooms/123-456-789/state", "owner-token", ""); w.Code != 200 {
		t.Fatal("login token rejected:", w.Code)
	}
}

func TestGuestsCannotLogInAndGuestNicksAreReserved(t *testing.T) {
	r, _ := moderationTestRouter(t)
	r.POST("/login", Login)
	r.POST("/register", Register)
	_, user := newGuest(t, r)
	assertAPIError(t, jsonRequest(r, http.MethodPost, "/login", "", `{"nick":"`+user.Nick+`","password":"anything"}`),
		401, apierror.UserNotFound, "Kullanıcı bulunamadı")
	assertAPIError(t, jsonRequest(r, http.MethodPost, "/login", "", `{"nick":"`+user.Nick+`","password":""}`),
		400, apierror.MissingCredentials, "Eksik bilgi")
	for _, nick := range []string{"guest-abc", "GUEST-x", "  Guest-y "} {
		assertAPIError(t, jsonRequest(r, http.MethodPost, "/register", "", `{"nick":"`+nick+`","password":"pw"}`),
			400, apierror.NickReserved, "Bu nick kullanılamaz")
	}
	if w := jsonRequest(r, http.MethodPost, "/register", "", `{"nick":"guesthouse","password":"pw"}`); w.Code != 200 {
		t.Fatal("ordinary nick starting with 'guest' rejected:", w.Code)
	}
}

func TestLastSeenIsRecordedAtMostHourly(t *testing.T) {
	r, _ := moderationTestRouter(t)
	token, user := newGuest(t, r)
	old := time.Now().Add(-2 * time.Hour)
	database.DB.Model(&user).Update("last_seen_at", old)
	jsonRequest(r, http.MethodGet, "/rooms/123-456-789/state", token, "")
	database.DB.First(&user, user.ID)
	if user.LastSeenAt == nil || !user.LastSeenAt.After(old.Add(time.Hour)) {
		t.Fatalf("activity not recorded: %v", user.LastSeenAt)
	}
	seen := *user.LastSeenAt
	jsonRequest(r, http.MethodGet, "/rooms/123-456-789/state", token, "")
	database.DB.First(&user, user.ID)
	if !user.LastSeenAt.Equal(seen) {
		t.Fatal("activity written on every request")
	}
}

func TestGuestsCreateJoinModerateAndSucceed(t *testing.T) {
	r, f := moderationTestRouter(t)
	f.participants = nil
	hostToken, host := newGuest(t, r)
	guestToken, guest := newGuest(t, r)

	created := jsonRequest(r, http.MethodPost, "/rooms", hostToken, `{"name":"Ayşe"}`)
	if created.Code != 201 || !strings.Contains(created.Body.String(), `"role":"owner"`) {
		t.Fatal("guest could not create a room:", created.Code, created.Body.String())
	}
	var room roomResponse
	_ = json.Unmarshal(created.Body.Bytes(), &room)
	hostSession := connectWith(t, f, created.Body.Bytes())
	joined := jsonRequest(r, http.MethodPost, "/rooms/join", guestToken, `{"room_id":"`+room.RoomID+`","name":"Mehmet"}`)
	if joined.Code != 200 || !strings.Contains(joined.Body.String(), `"role":"participant"`) {
		t.Fatal("guest could not join:", joined.Code, joined.Body.String())
	}
	guestSession := connectWith(t, f, joined.Body.Bytes())
	if id, err := participantUserID(guestSession.Identity); err != nil || id != guest.ID {
		t.Fatalf("guest identity %q does not map to user %d", guestSession.Identity, guest.ID)
	}

	base := "/rooms/" + room.RoomID
	// The guest owner moderates the guest participant.
	if w := jsonRequest(r, http.MethodPost, base+"/participants/"+guestSession.Identity+"/microphone", hostToken, `{"locked":true}`); w.Code != 200 {
		t.Fatal("guest owner could not lock a microphone:", w.Code, w.Body.String())
	}
	if w := jsonRequest(r, http.MethodPost, base+"/participants/"+guestSession.Identity+"/role", hostToken, `{"role":"moderator"}`); w.Code != 200 {
		t.Fatal("guest owner could not appoint a moderator:", w.Code, w.Body.String())
	}
	if guestSession.Attributes[roleAttribute] != "moderator" {
		t.Fatal("guest moderator attribute not published")
	}
	// A guest moderator still cannot act on the owner.
	assertAPIError(t, jsonRequest(r, http.MethodPost, base+"/participants/"+hostSession.Identity+"/microphone", guestToken, `{"locked":true}`),
		403, apierror.TargetProtected, "Bu katılımcı üzerinde işlem yapma yetkiniz yok")
	if w := jsonRequest(r, http.MethodPost, base+"/screen-share/start", guestToken, `{"identity":"`+guestSession.Identity+`"}`); w.Code != 200 {
		t.Fatal("guest moderator could not share:", w.Code, w.Body.String())
	}
	if w := jsonRequest(r, http.MethodPost, base+"/messages", guestToken, `{"content":"merhaba"}`); w.Code != 201 {
		t.Fatal("guest could not chat:", w.Code, w.Body.String())
	}

	// The LiveKit webhook reports the guest owner gone: the guest moderator
	// becomes acting owner.
	f.participants = []*livekit.ParticipantInfo{guestSession}
	event := &livekit.WebhookEvent{Event: "participant_left", Room: &livekit.Room{Name: room.RoomID}, Participant: &livekit.ParticipantInfo{Identity: hostSession.Identity}}
	if w := serve(r, signedWebhook(t, event, "test-key", testSecret, false)); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	if role, _ := effectiveRole(room.RoomID, guest.ID); role != models.RoleOwner {
		t.Fatalf("guest moderator did not succeed the guest owner: %s", role)
	}
	if role, _ := effectiveRole(room.RoomID, host.ID); role != models.RoleParticipant {
		t.Fatalf("departed guest owner kept powers: %s", role)
	}
}
