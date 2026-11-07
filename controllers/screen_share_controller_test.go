package controllers

import (
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
)

const ownerIdentity = "user-11-90b39d7a-5745-4c23-a697-e1e85fe145e0"
const screenPath = "/rooms/123-456-789/screen-share/"

func addScreenOwner(f *fakeModeration) {
	f.participants = append(f.participants, &livekit.ParticipantInfo{Identity: ownerIdentity, Permission: &livekit.ParticipantPermission{CanPublish: true, CanSubscribe: true, CanPublishData: true}})
}
func shareRequest(r *gin.Engine, action, token, identity string) *httptest.ResponseRecorder {
	return moderationRequest(r, screenPath+action, token, `{"identity":"`+identity+`"}`)
}
func hasSource(p *livekit.ParticipantInfo, s livekit.TrackSource) bool {
	if !p.Permission.CanPublish {
		return false
	}
	if len(p.Permission.CanPublishSources) == 0 {
		return true
	}
	for _, source := range p.Permission.CanPublishSources {
		if source == s {
			return true
		}
	}
	return false
}
func allowSharing(t *testing.T, r *gin.Engine) {
	t.Helper()
	w := moderationRequest(r, "/rooms/123-456-789/settings", "owner-token", `{"allow_participant_screen_share":true}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestScreenShareDefaultClosedAndOnlyOwnerChangesSettings(t *testing.T) {
	r, f := moderationTestRouter(t)
	addScreenOwner(f)
	var room models.Room
	database.DB.First(&room)
	if room.AllowParticipantScreenShare {
		t.Fatal("guests allowed by default")
	}
	for _, token := range []string{"", "guest-token"} {
		w := moderationRequest(r, "/rooms/123-456-789/settings", token, `{"allow_participant_screen_share":true}`)
		if w.Code != 401 && w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	if w := shareRequest(r, "start", "guest-token", testParticipantIdentity); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := shareRequest(r, "start", "owner-token", ownerIdentity); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if !hasSource(f.participants[1], livekit.TrackSource_SCREEN_SHARE) || hasSource(f.participants[0], livekit.TrackSource_SCREEN_SHARE) {
		t.Fatal("screen source permissions not exclusive")
	}
}
func TestScreenShareConcurrentAdmissionAllowsOneAndStopFreesSlot(t *testing.T) {
	r, f := moderationTestRouter(t)
	addScreenOwner(f)
	allowSharing(t, r)
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for _, v := range []struct{ token, identity string }{{"owner-token", ownerIdentity}, {"guest-token", testParticipantIdentity}} {
		wg.Add(1)
		go func(token, identity string) { defer wg.Done(); codes <- shareRequest(r, "start", token, identity).Code }(v.token, v.identity)
	}
	wg.Wait()
	close(codes)
	successes, conflicts := 0, 0
	for c := range codes {
		if c == 200 {
			successes++
		} else if c == 409 {
			conflicts++
		} else {
			t.Fatal(c)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("two publishers admitted")
	}
	var room models.Room
	database.DB.First(&room)
	holder := room.ScreenShareIdentity
	token := "guest-token"
	otherToken := "owner-token"
	other := ownerIdentity
	if holder == ownerIdentity {
		token = "owner-token"
		otherToken = "guest-token"
		other = testParticipantIdentity
	}
	if w := shareRequest(r, "stop", otherToken, other); w.Code != 409 {
		t.Fatal("other session released the slot", w.Code)
	}
	if w := shareRequest(r, "stop", token, holder); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := shareRequest(r, "start", otherToken, other); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestScreenSharePermissionCannotBeForgedAndJWTNeverGrantsIt(t *testing.T) {
	r, f := moderationTestRouter(t)
	addScreenOwner(f)
	allowSharing(t, r)
	if w := shareRequest(r, "start", "guest-token", ownerIdentity); w.Code != 403 {
		t.Fatal("identity spoofing", w.Code)
	}
	if w := shareRequest(r, "start", "guest-token", testParticipantIdentity); w.Code != 200 {
		t.Fatal(w.Code)
	}
	w := moderationRequest(r, "/rooms/join", "guest-token", `{"room_id":"123-456-789","name":"new device"}`)
	var response roomResponse
	json.Unmarshal(w.Body.Bytes(), &response)
	verifier, err := auth.ParseAPIToken(response.Token)
	if err != nil {
		t.Fatal(err)
	}
	_, grant, err := verifier.Verify("test-secret-000000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	if grant.Video.GetCanPublishSource(livekit.TrackSource_SCREEN_SHARE) || grant.Video.GetCanPublishSource(livekit.TrackSource_SCREEN_SHARE_AUDIO) {
		t.Fatal("rejoin can bypass singleton")
	}
}
func TestDisablingGuestSharingRevokesCurrentScreenAndPreservesMicLock(t *testing.T) {
	r, f := moderationTestRouter(t)
	allowSharing(t, r)
	if w := moderationRequest(r, microphonePath(), "owner-token", `{"locked":true}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := shareRequest(r, "start", "guest-token", testParticipantIdentity); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if hasSource(f.participants[0], livekit.TrackSource_MICROPHONE) {
		t.Fatal("sharing bypassed mic lock")
	}
	moderationRequest(r, microphonePath(), "owner-token", `{"locked":false}`)
	if !hasSource(f.participants[0], livekit.TrackSource_SCREEN_SHARE) {
		t.Fatal("mic unlock lost active screen")
	}
	w := moderationRequest(r, "/rooms/123-456-789/settings", "owner-token", `{"allow_participant_screen_share":false}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if hasSource(f.participants[0], livekit.TrackSource_SCREEN_SHARE) || !hasSource(f.participants[0], livekit.TrackSource_CAMERA) || !hasSource(f.participants[0], livekit.TrackSource_MICROPHONE) {
		t.Fatal("wrong source revocation")
	}
	var room models.Room
	database.DB.First(&room)
	if room.ScreenShareIdentity != "" {
		t.Fatal("slot retained")
	}
	// Microphone moderation must not restore screen sources from an old backup.
	moderationRequest(r, microphonePath(), "owner-token", `{"locked":true}`)
	moderationRequest(r, microphonePath(), "owner-token", `{"locked":false}`)
	if hasSource(f.participants[0], livekit.TrackSource_SCREEN_SHARE) {
		t.Fatal("mic unlock restored screen permission")
	}
}
func TestStaleScreenReservationExpiresButAnActiveTrackKeepsIt(t *testing.T) {
	r, f := moderationTestRouter(t)
	addScreenOwner(f)
	shareRequest(r, "start", "owner-token", ownerIdentity)
	var room models.Room
	database.DB.First(&room)
	database.DB.Model(&room).Update("screen_share_lease_until", time.Now().Add(-time.Second).Unix())
	state := func() int {
		req := httptest.NewRequest("GET", "/rooms/123-456-789/state", nil)
		req.Header.Set("Authorization", "Bearer owner-token")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	f.participants[1].Tracks = []*livekit.TrackInfo{{Source: livekit.TrackSource_SCREEN_SHARE}}
	if state() != 200 {
		t.Fatal("state failed")
	}
	database.DB.First(&room)
	if room.ScreenShareIdentity == "" {
		t.Fatal("live share expired")
	}
	f.participants[1].Tracks = nil
	if state() != 200 {
		t.Fatal("state failed")
	}
	database.DB.First(&room)
	if room.ScreenShareIdentity != "" {
		t.Fatal("abandoned reservation retained")
	}
	if hasSource(f.participants[1], livekit.TrackSource_SCREEN_SHARE) {
		t.Fatal("expired publisher still allowed")
	}
}
