package controllers

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
)

// Members of the test room beyond owner (11) and guest (22).
const (
	modID    = 33
	mod2ID   = 44
	owner2ID = 55
)

func identityFor(userID uint) string {
	return fmt.Sprintf("user-%d-90b39d7a-5745-4c23-a697-e1e85fe145e0", userID)
}

func tokenFor(userID uint) string {
	switch userID {
	case 11:
		return "owner-token"
	case 22:
		return "guest-token"
	}
	return fmt.Sprintf("token-%d", userID)
}

// addMember creates a user with the given role (empty for participant) and,
// if connected, a LiveKit session for them.
func addMember(t *testing.T, f *fakeModeration, userID uint, role string, grantedAt time.Time, connected bool) *livekit.ParticipantInfo {
	t.Helper()
	if err := database.DB.Create(&models.User{ID: userID, Nick: fmt.Sprintf("u%d", userID), Password: "x", Token: tokenFor(userID)}).Error; err != nil {
		t.Fatal(err)
	}
	if role != "" {
		if err := database.DB.Create(&models.RoomRole{RoomID: "123-456-789", UserID: userID, Role: role, GrantedAt: grantedAt}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if !connected {
		return nil
	}
	p := &livekit.ParticipantInfo{
		Identity:   identityFor(userID),
		Permission: &livekit.ParticipantPermission{CanPublish: true, CanSubscribe: true, CanPublishData: true},
		Tracks:     []*livekit.TrackInfo{{Sid: fmt.Sprintf("mic-%d", userID), Source: livekit.TrackSource_MICROPHONE}},
	}
	f.participants = append(f.participants, p)
	return p
}

// rolesTestRouter is the moderation router with a second owner, two
// moderators and the guest, all connected. Participants: [guest, owner,
// mod, mod2, owner2].
func rolesTestRouter(t *testing.T) (*gin.Engine, *fakeModeration) {
	t.Helper()
	r, f := moderationTestRouter(t)
	addScreenOwner(f)
	now := time.Now()
	addMember(t, f, modID, models.RoleModerator, now.Add(-time.Hour), true)
	addMember(t, f, mod2ID, models.RoleModerator, now, true)
	addMember(t, f, owner2ID, models.RoleOwner, now, true)
	return r, f
}

func micRequest(r *gin.Engine, token string, target uint, body string) *httptest.ResponseRecorder {
	return moderationRequest(r, "/rooms/123-456-789/participants/"+identityFor(target)+"/microphone", token, body)
}

func TestMicrophonePermissionsFollowRoles(t *testing.T) {
	cases := []struct {
		name   string
		actor  uint
		target uint
		want   int
		code   apierror.Code
	}{
		{"moderator locks participant", modID, 22, 200, ""},
		{"moderator cannot lock moderator", modID, mod2ID, 403, apierror.TargetProtected},
		{"moderator cannot lock owner", modID, 11, 403, apierror.TargetProtected},
		{"owner locks moderator", 11, modID, 200, ""},
		{"owner cannot lock another owner", 11, owner2ID, 403, apierror.TargetProtected},
		{"owner cannot lock themselves", 11, 11, 403, apierror.TargetProtected},
		{"participant cannot lock anyone", 22, modID, 403, apierror.NotRoomOwner},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := rolesTestRouter(t)
			w := micRequest(r, tokenFor(tc.actor), tc.target, `{"locked":true}`)
			if w.Code != tc.want {
				t.Fatalf("lock: %d %s", w.Code, w.Body.String())
			}
			if tc.code != "" {
				var body apierror.Body
				_ = json.Unmarshal(w.Body.Bytes(), &body)
				if body.Code != tc.code {
					t.Fatalf("code %s, want %s", body.Code, tc.code)
				}
				return
			}
			if w := micRequest(r, tokenFor(tc.actor), tc.target, `{"locked":false,"request_unmute":true}`); w.Code != 200 {
				t.Fatalf("unlock with request: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSettingsAndClosePermissionsFollowRoles(t *testing.T) {
	r, _ := rolesTestRouter(t)
	assertAPIError(t, moderationRequest(r, "/rooms/123-456-789/settings", "guest-token", `{"allow_participant_screen_share":true}`),
		403, apierror.NotRoomOwner, "Bu işlem yalnızca oda yöneticisine açık")
	if w := moderationRequest(r, "/rooms/123-456-789/settings", tokenFor(modID), `{"allow_participant_screen_share":true}`); w.Code != 200 {
		t.Fatal("moderator could not change the setting:", w.Code, w.Body.String())
	}
	assertAPIError(t, moderationRequest(r, "/rooms/123-456-789/close", "guest-token", `{}`),
		403, apierror.NotRoomOwner, "Bu işlem yalnızca oda yöneticisine açık")
	if w := moderationRequest(r, "/rooms/123-456-789/close", tokenFor(modID), `{}`); w.Code != 200 {
		t.Fatal("moderator could not end the meeting:", w.Code, w.Body.String())
	}
}

func TestModeratorSharesRegardlessOfSettingAndKeepsShareWhenDisabled(t *testing.T) {
	r, f := rolesTestRouter(t)
	assertAPIError(t, shareRequest(r, "start", "guest-token", testParticipantIdentity),
		403, apierror.ScreenShareNotAllowed, "Oda sahibi katılımcıların ekran paylaşmasına izin vermiyor")
	if w := shareRequest(r, "start", tokenFor(modID), identityFor(modID)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	publishScreen(f.participants[2], livekit.TrackSource_SCREEN_SHARE)
	if w := moderationRequest(r, "/rooms/123-456-789/settings", "owner-token", `{"allow_participant_screen_share":false}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	var room models.Room
	database.DB.First(&room)
	if room.ScreenShareIdentity != identityFor(modID) || len(f.mutedTracks) != 0 {
		t.Fatal("disabling participant sharing stopped a moderator's share")
	}
}

func TestStopSharePermissionsFollowRoles(t *testing.T) {
	cases := []struct {
		name   string
		holder uint
		actor  uint
		want   int
		code   apierror.Code
	}{
		{"moderator stops participant", 22, modID, 200, ""},
		{"moderator cannot stop moderator", mod2ID, modID, 403, apierror.TargetProtected},
		{"owner stops moderator", modID, 11, 200, ""},
		{"owner cannot stop another owner", owner2ID, 11, 403, apierror.TargetProtected},
		{"moderator cannot stop owner", 11, modID, 403, apierror.TargetProtected},
		{"participant cannot stop others", modID, 22, 403, apierror.IdentityNotOwned},
		{"owner stops own share", 11, 11, 200, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := rolesTestRouter(t)
			allowSharing(t, r)
			if w := shareRequest(r, "start", tokenFor(tc.holder), identityFor(tc.holder)); w.Code != 200 {
				t.Fatal("start:", w.Code, w.Body.String())
			}
			w := shareRequest(r, "stop", tokenFor(tc.actor), identityFor(tc.holder))
			if w.Code != tc.want {
				t.Fatalf("stop: %d %s", w.Code, w.Body.String())
			}
			if tc.code != "" {
				var body apierror.Body
				_ = json.Unmarshal(w.Body.Bytes(), &body)
				if body.Code != tc.code {
					t.Fatalf("code %s, want %s", body.Code, tc.code)
				}
			}
		})
	}
}

func TestJoinReportsRoleAndPublishesItAsAttribute(t *testing.T) {
	r, _ := rolesTestRouter(t)
	for _, tc := range []struct {
		user   uint
		role   string
		isHost bool
		attr   string
	}{
		{11, models.RoleOwner, true, "owner"},
		{modID, models.RoleModerator, true, "moderator"},
		{22, models.RoleParticipant, false, ""},
	} {
		w := moderationRequest(r, "/rooms/join", tokenFor(tc.user), `{"room_id":"123-456-789","name":"x"}`)
		var response roomResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if response.Role != tc.role || response.IsHost != tc.isHost {
			t.Fatalf("user %d: role=%q is_host=%v", tc.user, response.Role, response.IsHost)
		}
		verifier, _ := auth.ParseAPIToken(response.Token)
		_, grant, err := verifier.Verify("test-secret-000000000000000000000000")
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := grant.Attributes[roleAttribute]; !ok || got != tc.attr {
			t.Fatalf("user %d: %s attribute = %q (present %v), want %q", tc.user, roleAttribute, got, ok, tc.attr)
		}
	}
}
