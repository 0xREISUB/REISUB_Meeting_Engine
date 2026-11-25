package controllers

import (
	"encoding/json"
	"strings"
	"testing"

	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/livekit/protocol/auth"
)

func TestNewRoomIDIsWellFormedAndUnpredictable(t *testing.T) {
	useTestDB(t)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id, err := newRoomID()
		if err != nil {
			t.Fatal(err)
		}
		if !roomIDPattern.MatchString(id) {
			t.Fatalf("room ID %q does not match NNN-NNN-NNN", id)
		}
		seen[id] = true
	}
	// Back-to-back time-derived codes used to collide or differ only slightly.
	if len(seen) < 49 {
		t.Fatalf("only %d distinct codes out of 50", len(seen))
	}
}

func TestInviteURLFollowsPublicWebURL(t *testing.T) {
	r, _ := moderationTestRouter(t)
	t.Setenv("PUBLIC_WEB_URL", "https://meeting.example/")
	w := moderationRequest(r, "/rooms", "owner-token", `{"name":"owner"}`)
	var created roomResponse
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if w.Code != 201 || created.InviteURL != "https://meeting.example/j/"+created.RoomID {
		t.Fatalf("create invite_url = %q (%d %s)", created.InviteURL, w.Code, w.Body.String())
	}
	w = moderationRequest(r, "/rooms/join", "guest-token", `{"room_id":"123-456-789","name":"guest"}`)
	if !strings.Contains(w.Body.String(), `"invite_url":"https://meeting.example/j/123-456-789"`) {
		t.Fatalf("join response: %s", w.Body.String())
	}

	t.Setenv("PUBLIC_WEB_URL", "")
	w = moderationRequest(r, "/rooms/join", "guest-token", `{"room_id":"123-456-789","name":"guest"}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), "invite_url") {
		t.Fatalf("invite_url present without PUBLIC_WEB_URL: %s", w.Body.String())
	}
	for _, field := range []string{`"room_id"`, `"livekit_url"`, `"token"`, `"participant"`, `"is_host"`, `"role"`} {
		if !strings.Contains(w.Body.String(), field) {
			t.Fatalf("join response lost %s: %s", field, w.Body.String())
		}
	}
}

// joinAttributes joins the test room as token and answers the attributes
// its LiveKit token carries.
func joinAttributes(t *testing.T, r *gin.Engine, token string) map[string]string {
	t.Helper()
	w := moderationRequest(r, "/rooms/join", token, `{"room_id":"123-456-789","name":"x"}`)
	if w.Code != 200 {
		t.Fatalf("join: %d %s", w.Code, w.Body.String())
	}
	var response roomResponse
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	verifier, err := auth.ParseAPIToken(response.Token)
	if err != nil {
		t.Fatal(err)
	}
	_, grant, err := verifier.Verify("test-secret-000000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	return grant.Attributes
}

func TestMeetingTokensCarryTheProfilePicture(t *testing.T) {
	r, _ := moderationTestRouter(t)
	database.DB.Model(&models.User{}).Where("id = 22").Update("avatar_file", "22-0123456789abcdef.jpg")
	if got := joinAttributes(t, r, "guest-token")[avatarAttribute]; got != "/avatars/22-0123456789abcdef.jpg" {
		t.Fatalf("with a picture: %q", got)
	}
	// Without one, no attribute: the app shows initials.
	if _, ok := joinAttributes(t, r, "owner-token")[avatarAttribute]; ok {
		t.Fatal("an attribute without a picture")
	}
}
