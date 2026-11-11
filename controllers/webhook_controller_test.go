package controllers

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/webhook"
	"google.golang.org/protobuf/encoding/protojson"
)

const testSecret = "test-secret-000000000000000000000000"

// signedWebhook builds a request the way LiveKit sends it: a JWT signed with
// key/secret carrying the body's sha256.
func signedWebhook(t *testing.T, event *livekit.WebhookEvent, key, secret string, tamper bool) *http.Request {
	t.Helper()
	body, err := protojson.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	token, err := auth.NewAccessToken(key, secret).SetValidFor(time.Minute).SetSha256(base64.StdEncoding.EncodeToString(sum[:])).ToJWT()
	if err != nil {
		t.Fatal(err)
	}
	if tamper {
		body = bytes.Replace(body, []byte(identityFor(11)), []byte(identityFor(22)), 1)
	}
	req := httptest.NewRequest(http.MethodPost, "/livekit/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/webhook+json")
	req.Header.Set("Authorization", token)
	return req
}

func ownerLeftEvent() *livekit.WebhookEvent {
	return &livekit.WebhookEvent{
		Event:       webhook.EventParticipantLeft,
		Room:        &livekit.Room{Name: "123-456-789"},
		Participant: &livekit.ParticipantInfo{Identity: identityFor(11)},
	}
}

func serve(r *gin.Engine, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestWebhookRejectsUnsignedOrForgedCalls(t *testing.T) {
	r, f := permissionsTestRouter(t)
	disconnect(f, 11)
	unsigned := httptest.NewRequest(http.MethodPost, "/livekit/webhook", bytes.NewBufferString(`{"event":"participant_left"}`))
	for name, req := range map[string]*http.Request{
		"no signature":  unsigned,
		"wrong secret":  signedWebhook(t, ownerLeftEvent(), "test-key", "another-secret-0000000000000000000", false),
		"unknown key":   signedWebhook(t, ownerLeftEvent(), "other-key", testSecret, false),
		"tampered body": signedWebhook(t, ownerLeftEvent(), "test-key", testSecret, true),
	} {
		t.Run(name, func(t *testing.T) {
			assertAPIError(t, serve(r, req), 401, apierror.InvalidWebhook, "Webhook doğrulanamadı")
		})
	}
	if storedAccess(t, 11).Role != models.RoleOwner || storedAccess(t, modID).Role != models.RoleParticipant || f.updates != 0 {
		t.Fatal("an unauthenticated webhook changed roles")
	}
}

func TestParticipantLeftWebhookRunsSuccessionOnce(t *testing.T) {
	r, f := permissionsTestRouter(t)
	// LiveKit may still list the departing session; the event excludes it.
	if w := serve(r, signedWebhook(t, ownerLeftEvent(), "test-key", testSecret, false)); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	successor := mustRow(t, modID)
	if successor.Role != models.RoleOwner || !successor.Acting || !mustRow(t, 11).Suspended {
		t.Fatalf("webhook did not run succession: %+v", successor)
	}
	// /leave after the event (or a redelivered event) changes nothing.
	updates := f.updates
	disconnect(f, 11)
	serve(r, signedWebhook(t, ownerLeftEvent(), "test-key", testSecret, false))
	leaveRequest(r, 11)
	if again := mustRow(t, modID); again.GrantedAt != successor.GrantedAt || f.updates != updates || storedAccess(t, mod2ID).Role != models.RoleParticipant {
		t.Fatal("repeated departure changed roles again")
	}
}

func TestRoomFinishedWebhookSuspendsOwnerAndOtherEventsAreIgnored(t *testing.T) {
	r, f := permissionsTestRouter(t)
	other := &livekit.WebhookEvent{Event: webhook.EventParticipantJoined, Room: &livekit.Room{Name: "123-456-789"}, Participant: &livekit.ParticipantInfo{Identity: identityFor(22)}}
	if w := serve(r, signedWebhook(t, other, "test-key", testSecret, false)); w.Code != http.StatusNoContent || storedAccess(t, 11).Role != models.RoleOwner {
		t.Fatal("unrelated event changed state", w.Code)
	}
	f.participants = nil // the media room emptied out
	finished := &livekit.WebhookEvent{Event: webhook.EventRoomFinished, Room: &livekit.Room{Name: "123-456-789"}}
	if w := serve(r, signedWebhook(t, finished, "test-key", testSecret, false)); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	if !mustRow(t, 11).Suspended {
		t.Fatal("owner not suspended when the room emptied")
	}
	if joinAs(t, r, 22).Role != models.RoleOwner {
		t.Fatal("first joiner after an empty room is not owner")
	}
}
