package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"reisub-backend/database"
	"reisub-backend/middleware"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

const testParticipantIdentity = "user-22-90b39d7a-5745-4c23-a697-e1e85fe145e0"

type fakeModeration struct {
	participants []*livekit.ParticipantInfo
	updates      int
	mutes        int
	creates      int
	deletes      int
	deleteError  error
	onDelete     func()
	onCreate     func()
}

func (f *fakeModeration) CreateRoom(_ context.Context, r *livekit.CreateRoomRequest) (*livekit.Room, error) {
	f.creates++
	if f.onCreate != nil {
		f.onCreate()
	}
	return &livekit.Room{Name: r.Name}, nil
}
func (f *fakeModeration) DeleteRoom(_ context.Context, _ *livekit.DeleteRoomRequest) (*livekit.DeleteRoomResponse, error) {
	f.deletes++
	if f.onDelete != nil {
		f.onDelete()
	}
	return &livekit.DeleteRoomResponse{}, f.deleteError
}
func (f *fakeModeration) ListParticipants(_ context.Context, _ *livekit.ListParticipantsRequest) (*livekit.ListParticipantsResponse, error) {
	return &livekit.ListParticipantsResponse{Participants: f.participants}, nil
}
func (f *fakeModeration) UpdateParticipant(_ context.Context, r *livekit.UpdateParticipantRequest) (*livekit.ParticipantInfo, error) {
	f.updates++
	for _, p := range f.participants {
		if p.Identity == r.Identity {
			p.Permission = proto.Clone(r.Permission).(*livekit.ParticipantPermission)
			if p.Attributes == nil {
				p.Attributes = map[string]string{}
			}
			for k, v := range r.Attributes {
				p.Attributes[k] = v
			}
			return p, nil
		}
	}
	return nil, errors.New("participant not found")
}
func (f *fakeModeration) MutePublishedTrack(_ context.Context, r *livekit.MuteRoomTrackRequest) (*livekit.MuteRoomTrackResponse, error) {
	if !r.Muted {
		return nil, errors.New("remote unmute must never be used")
	}
	f.mutes++
	return &livekit.MuteRoomTrackResponse{}, nil
}

func moderationTestRouter(t *testing.T) (*gin.Engine, *fakeModeration) {
	t.Helper()
	previous := database.DB
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = previous; sql, _ := db.DB(); sql.Close() })
	if err = db.AutoMigrate(&models.User{}, &models.Room{}, &models.RoomMicrophoneLock{}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []models.User{{ID: 11, Nick: "owner", Password: "unused", Token: "owner-token"}, {ID: 22, Nick: "guest", Password: "unused", Token: "guest-token"}} {
		if err = db.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Create(&models.Room{RoomID: "123-456-789", Name: "test", OwnerID: 11, Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}
	f := &fakeModeration{participants: []*livekit.ParticipantInfo{{Identity: testParticipantIdentity, Permission: &livekit.ParticipantPermission{CanPublish: true, CanSubscribe: true, CanPublishData: true}, Tracks: []*livekit.TrackInfo{{Sid: "mic", Source: livekit.TrackSource_MICROPHONE}, {Sid: "camera", Source: livekit.TrackSource_CAMERA}}}}}
	oldFactory := newModerationService
	newModerationService = func(string) roomModerationService { return f }
	t.Cleanup(func() { newModerationService = oldFactory })
	t.Setenv("LIVEKIT_API_KEY", "test-key")
	t.Setenv("LIVEKIT_API_SECRET", "test-secret-000000000000000000000000")
	t.Setenv("LIVEKIT_INTERNAL_URL", "http://livekit:7880")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/rooms")
	g.Use(middleware.RequireAuth())
	g.POST("", CreateRoom)
	g.POST("/join", JoinRoom)
	g.POST("/:room_id/close", CloseRoom)
	g.POST("/:room_id/participants/:identity/microphone", SetParticipantMicrophone)
	return r, f
}
func moderationRequest(r *gin.Engine, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
func microphonePath() string {
	return "/rooms/123-456-789/participants/" + testParticipantIdentity + "/microphone"
}

func TestModerationRequiresAuthenticatedOwner(t *testing.T) {
	r, f := moderationTestRouter(t)
	for _, path := range []string{"/rooms/123-456-789/close", microphonePath()} {
		if w := moderationRequest(r, path, "", `{"locked":true}`); w.Code != 401 {
			t.Fatalf("unauthenticated %s: %d", path, w.Code)
		}
		if w := moderationRequest(r, path, "guest-token", `{"locked":true}`); w.Code != 403 {
			t.Fatalf("non-owner %s: %d", path, w.Code)
		}
	}
	if f.updates != 0 || f.deletes != 0 {
		t.Fatal("unauthorized request reached LiveKit")
	}
}
func TestMicrophoneLockPersistsAcrossRejoinAndPreservesCamera(t *testing.T) {
	r, f := moderationTestRouter(t)
	if w := moderationRequest(r, microphonePath(), "owner-token", `{"locked":true}`); w.Code != 200 {
		t.Fatalf("lock: %d %s", w.Code, w.Body.String())
	}
	p := f.participants[0]
	if p.Attributes[microphoneLockAttribute] != "true" || f.mutes != 1 {
		t.Fatal("microphone was not muted and locked")
	}
	for _, source := range p.Permission.CanPublishSources {
		if source == livekit.TrackSource_MICROPHONE {
			t.Fatal("locked participant can still publish mic")
		}
	}
	if !p.Permission.CanPublish || !p.Permission.CanSubscribe || len(p.Permission.CanPublishSources) != 3 {
		t.Fatal("camera or receive permissions were lost")
	}
	w := moderationRequest(r, "/rooms/join", "guest-token", `{"room_id":"123-456-789","name":"guest"}`)
	if w.Code != 200 {
		t.Fatalf("rejoin: %d %s", w.Code, w.Body.String())
	}
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
	if grant.Attributes[microphoneLockAttribute] != "true" || grant.Video.GetCanPublishSource(livekit.TrackSource_MICROPHONE) {
		t.Fatal("rejoin bypassed microphone lock")
	}
}
func TestUnlockRequestsConsentWithoutRemoteUnmute(t *testing.T) {
	r, f := moderationTestRouter(t)
	moderationRequest(r, microphonePath(), "owner-token", `{"locked":true}`)
	w := moderationRequest(r, microphonePath(), "owner-token", `{"locked":false,"request_unmute":true}`)
	if w.Code != 200 {
		t.Fatalf("unlock: %d %s", w.Code, w.Body.String())
	}
	p := f.participants[0]
	if p.Attributes[microphoneLockAttribute] != "false" || p.Attributes[microphoneRequestAttribute] == "" {
		t.Fatal("unlock/request not applied")
	}
	if f.mutes != 1 {
		t.Fatal("unlock must not remotely activate the microphone")
	}
	locked, err := microphoneLocked("123-456-789", 22)
	if err != nil || locked {
		t.Fatal("persistent lock not cleared")
	}
}
func TestCloseRoomStopsMediaAndRejectsNewJoins(t *testing.T) {
	r, f := moderationTestRouter(t)
	f.onDelete = func() {
		var room models.Room
		database.DB.Where("room_id = ?", "123-456-789").First(&room)
		if room.Status != "closing" {
			t.Error("new joins not blocked before deleting LiveKit room")
		}
	}
	w := moderationRequest(r, "/rooms/123-456-789/close", "owner-token", `{}`)
	if w.Code != 200 || f.deletes != 1 {
		t.Fatalf("close: %d %s", w.Code, w.Body.String())
	}
	if w = moderationRequest(r, "/rooms/join", "guest-token", `{"room_id":"123-456-789","name":"guest"}`); w.Code != 404 {
		t.Fatal("closed room can still be joined")
	}
	if w = moderationRequest(r, "/rooms/123-456-789/close", "owner-token", `{}`); w.Code != 200 || f.deletes != 1 {
		t.Fatal("close is not idempotent")
	}
}
func TestCloseFailureCanBeRetriedWithoutIssuingNewTokens(t *testing.T) {
	r, f := moderationTestRouter(t)
	f.deleteError = errors.New("network unavailable")
	if w := moderationRequest(r, "/rooms/123-456-789/close", "owner-token", `{}`); w.Code != 502 {
		t.Fatalf("failure status %d", w.Code)
	}
	if w := moderationRequest(r, "/rooms/join", "guest-token", `{"room_id":"123-456-789","name":"guest"}`); w.Code != 404 {
		t.Fatal("closing room issued a new token")
	}
	f.deleteError = nil
	if w := moderationRequest(r, "/rooms/123-456-789/close", "owner-token", `{}`); w.Code != 200 {
		t.Fatal("close retry failed")
	}
}
func TestCreateRoomExplicitlyCreatesLiveKitRoomWithoutClientAdminGrant(t *testing.T) {
	r, f := moderationTestRouter(t)
	w := moderationRequest(r, "/rooms", "owner-token", `{"name":"owner"}`)
	if w.Code != 201 || f.creates != 1 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var response roomResponse
	json.Unmarshal(w.Body.Bytes(), &response)
	verifier, _ := auth.ParseAPIToken(response.Token)
	_, grant, err := verifier.Verify("test-secret-000000000000000000000000")
	if err != nil || !response.IsHost || grant.Video.RoomAdmin {
		t.Fatal("owner UI role or server-only admin authorization is incorrect")
	}
}

func TestMicrophoneLockDoesNotGrantAdditionalSources(t *testing.T) {
	r, f := moderationTestRouter(t)
	f.participants[0].Permission.CanPublishSources = []livekit.TrackSource{livekit.TrackSource_MICROPHONE}
	if w := moderationRequest(r, microphonePath(), "owner-token", `{"locked":true}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	p := f.participants[0]
	if p.Permission.CanPublish || len(p.Permission.CanPublishSources) != 0 {
		t.Fatal("locking an audio-only participant granted another media source")
	}
	if w := moderationRequest(r, microphonePath(), "owner-token", `{"locked":false}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if !p.Permission.CanPublish || len(p.Permission.CanPublishSources) != 1 || p.Permission.CanPublishSources[0] != livekit.TrackSource_MICROPHONE {
		t.Fatal("unlock did not restore original permissions")
	}
}

func TestJoinRecreatesOnlyActiveMediaRooms(t *testing.T) {
	r, f := moderationTestRouter(t)
	if w := moderationRequest(r, "/rooms/join", "guest-token", `{"room_id":"123-456-789","name":"guest"}`); w.Code != 200 || f.creates != 1 {
		t.Fatalf("active room recreation: %d %s", w.Code, w.Body.String())
	}
	moderationRequest(r, "/rooms/123-456-789/close", "owner-token", `{}`)
	if w := moderationRequest(r, "/rooms/join", "guest-token", `{"room_id":"123-456-789","name":"guest"}`); w.Code != 404 || f.creates != 1 {
		t.Fatal("closed room was recreated")
	}
}

func TestCloseWaitsForAnInflightJoinAndRoomLockIsReleased(t *testing.T) {
	r, f := moderationTestRouter(t)
	started, finish := make(chan struct{}), make(chan struct{})
	joined, closed := make(chan int, 1), make(chan int, 1)
	f.onCreate = func() { close(started); <-finish }
	go func() {
		joined <- moderationRequest(r, "/rooms/join", "guest-token", `{"room_id":"123-456-789","name":"guest"}`).Code
	}()
	<-started
	go func() { closed <- moderationRequest(r, "/rooms/123-456-789/close", "owner-token", `{}`).Code }()
	close(finish)
	if code := <-joined; code != 200 {
		t.Fatalf("join status %d", code)
	}
	if code := <-closed; code != 200 {
		t.Fatalf("close status %d", code)
	}
	if f.creates != 1 || f.deletes != 1 {
		t.Fatal("unexpected media room lifecycle")
	}
	roomLifecycle.Lock()
	remaining := len(roomLifecycle.entries)
	roomLifecycle.Unlock()
	if remaining != 0 {
		t.Fatal("room lifecycle lock was not cleaned up")
	}
}
