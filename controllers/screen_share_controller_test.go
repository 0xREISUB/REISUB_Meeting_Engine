package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/twitchtv/twirp"
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
	assertAPIError(t, shareRequest(r, "start", "guest-token", testParticipantIdentity), 403, apierror.ScreenShareNotAllowed, "Oda sahibi katılımcıların ekran paylaşmasına izin vermiyor")
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
	assertAPIError(t, shareRequest(r, "stop", otherToken, other), 409, apierror.ScreenShareBusy, "Başka bir oturum ekran paylaşıyor")
	assertAPIError(t, shareRequest(r, "start", otherToken, other), 409, apierror.ScreenShareBusy, "Başka bir katılımcı ekranını paylaşıyor")
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

func publishScreen(p *livekit.ParticipantInfo, sources ...livekit.TrackSource) {
	for _, source := range sources {
		sid := "screen"
		if source == livekit.TrackSource_SCREEN_SHARE_AUDIO {
			sid = "screen-audio"
		}
		p.Tracks = append(p.Tracks, &livekit.TrackInfo{Sid: sid, Source: source})
	}
}

func stateRequest(r *gin.Engine, token string) *httptest.ResponseRecorder {
	return jsonRequest(r, http.MethodGet, "/rooms/123-456-789/state", token, "")
}

func expireShareLease(t *testing.T) {
	t.Helper()
	if err := database.DB.Model(&models.Room{}).Where("room_id = ?", "123-456-789").
		Update("screen_share_lease_until", time.Now().Add(-time.Second).Unix()).Error; err != nil {
		t.Fatal(err)
	}
}

func assertMuted(t *testing.T, f *fakeModeration, want ...string) {
	t.Helper()
	if !slices.Equal(f.mutedTracks, want) {
		t.Fatalf("muted tracks = %v, want %v", f.mutedTracks, want)
	}
}

func TestDisablingGuestSharingMutesScreenAndScreenAudioTracks(t *testing.T) {
	r, f := moderationTestRouter(t)
	allowSharing(t, r)
	if w := shareRequest(r, "start", "guest-token", testParticipantIdentity); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	guest := f.participants[0]
	publishScreen(guest, livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO)
	w := moderationRequest(r, "/rooms/123-456-789/settings", "owner-token", `{"allow_participant_screen_share":false}`)
	if w.Code != 200 || w.Body.String() != `{"allow_participant_screen_share":false,"screen_share_identity":""}` {
		t.Fatal(w.Code, w.Body.String())
	}
	assertMuted(t, f, "screen", "screen-audio")
	if hasSource(guest, livekit.TrackSource_SCREEN_SHARE) || hasSource(guest, livekit.TrackSource_SCREEN_SHARE_AUDIO) {
		t.Fatal("screen sources still granted")
	}
	for _, track := range guest.Tracks {
		if track.Source == livekit.TrackSource_MICROPHONE && track.Muted {
			t.Fatal("revoking screen share muted the microphone")
		}
	}
}

func TestOwnerStoppingGuestShareMutesItsTracks(t *testing.T) {
	r, f := moderationTestRouter(t)
	allowSharing(t, r)
	shareRequest(r, "start", "guest-token", testParticipantIdentity)
	publishScreen(f.participants[0], livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO)
	if w := shareRequest(r, "stop", "owner-token", testParticipantIdentity); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	assertMuted(t, f, "screen", "screen-audio")
}

func TestStartingShareMutesLeftoverScreenTracksOfOthers(t *testing.T) {
	r, f := moderationTestRouter(t)
	addScreenOwner(f)
	// A misbehaving guest publishes screen tracks without holding the slot.
	publishScreen(f.participants[0], livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO)
	if w := shareRequest(r, "start", "owner-token", ownerIdentity); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	assertMuted(t, f, "screen", "screen-audio")
}

func TestStaleReleaseTreatsScreenAudioAsAccompanimentOnly(t *testing.T) {
	r, f := moderationTestRouter(t)
	addScreenOwner(f)
	owner := f.participants[1]
	shareRequest(r, "start", "owner-token", ownerIdentity)
	publishScreen(owner, livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO)
	expireShareLease(t)

	// Video and audio live: the share is active and nothing is touched.
	if w := stateRequest(r, "guest-token"); w.Code != 200 || !strings.Contains(w.Body.String(), ownerIdentity) {
		t.Fatal(w.Code, w.Body.String())
	}
	assertMuted(t, f)

	// The video track ended but screen audio is still live: release the slot
	// and mute the leftover audio rather than letting it hold the room.
	owner.Tracks[0].Muted = true
	if w := stateRequest(r, "guest-token"); w.Code != 200 || w.Body.String() != `{"allow_participant_screen_share":false,"screen_share_identity":""}` {
		t.Fatal(w.Code, w.Body.String())
	}
	assertMuted(t, f, "screen", "screen-audio")
	if hasSource(owner, livekit.TrackSource_SCREEN_SHARE_AUDIO) {
		t.Fatal("stale holder kept screen audio permission")
	}
}

func TestRevokeToleratesUnpublishedTracksAndReportsMuteFailures(t *testing.T) {
	r, f := moderationTestRouter(t)
	allowSharing(t, r)
	shareRequest(r, "start", "guest-token", testParticipantIdentity)
	publishScreen(f.participants[0], livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO)

	f.muteErr = errors.New("network unavailable")
	assertAPIError(t, moderationRequest(r, "/rooms/123-456-789/settings", "owner-token", `{"allow_participant_screen_share":false}`),
		502, apierror.MediaServerError, "İzin kapatıldı fakat paylaşım durdurulamadı. Tekrar deneyin.")
	var room models.Room
	database.DB.First(&room)
	if room.AllowParticipantScreenShare || room.ScreenShareIdentity == "" {
		t.Fatal("failed revoke must keep the slot for a retry but persist the restriction")
	}

	// The track may already be gone once the source permission is revoked.
	f.muteErr = twirp.NotFoundError("track not found")
	if w := moderationRequest(r, "/rooms/123-456-789/settings", "owner-token", `{"allow_participant_screen_share":false}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	database.DB.First(&room)
	if room.ScreenShareIdentity != "" {
		t.Fatal("slot not released")
	}
}

func TestStatePollSkipsLiveKitWhenNobodyShares(t *testing.T) {
	r, f := moderationTestRouter(t)
	for i := 0; i < 5; i++ {
		w := stateRequest(r, "guest-token")
		if w.Code != 200 || w.Body.String() != `{"allow_participant_screen_share":false,"screen_share_identity":""}` {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if f.lists != 0 || f.gets != 0 {
		t.Fatalf("idle polls reached LiveKit: %d lists, %d gets", f.lists, f.gets)
	}
}

func TestStatePollAsksOnlyForHolderAndReusesRecentConfirmation(t *testing.T) {
	r, f := moderationTestRouter(t)
	shareCheckInterval = time.Hour
	addScreenOwner(f)
	shareRequest(r, "start", "owner-token", ownerIdentity)
	publishScreen(f.participants[1], livekit.TrackSource_SCREEN_SHARE)
	f.lists, f.gets = 0, 0
	want := `{"allow_participant_screen_share":false,"screen_share_identity":"` + ownerIdentity + `"}`
	for _, token := range []string{"guest-token", "owner-token", "guest-token", "owner-token"} {
		if w := stateRequest(r, token); w.Code != 200 || w.Body.String() != want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if f.lists != 0 || f.gets != 1 {
		t.Fatalf("got %d lists, %d gets; want 0 lists, 1 get", f.lists, f.gets)
	}
	// A slot change invalidates the confirmation immediately.
	if w := shareRequest(r, "stop", "owner-token", ownerIdentity); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := stateRequest(r, "guest-token"); w.Body.String() != `{"allow_participant_screen_share":false,"screen_share_identity":""}` {
		t.Fatal("stale cached holder reported after stop:", w.Body.String())
	}
}

func TestStatePollReleasesHolderWhoLeftTheRoom(t *testing.T) {
	r, f := moderationTestRouter(t)
	addScreenOwner(f)
	shareRequest(r, "start", "owner-token", ownerIdentity)
	f.participants = f.participants[:1] // the owner disconnected within the grace period
	if w := stateRequest(r, "guest-token"); w.Code != 200 || w.Body.String() != `{"allow_participant_screen_share":false,"screen_share_identity":""}` {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestStatePollReportsLiveKitFailure(t *testing.T) {
	r, f := moderationTestRouter(t)
	addScreenOwner(f)
	shareRequest(r, "start", "owner-token", ownerIdentity)
	f.getErr = errors.New("network unavailable")
	assertAPIError(t, stateRequest(r, "guest-token"), 502, apierror.MediaServerError, "Katılımcılar alınamadı")
}
