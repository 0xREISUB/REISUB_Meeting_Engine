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

func roleRequest(r *gin.Engine, token string, target uint, role string) *httptest.ResponseRecorder {
	return moderationRequest(r, "/rooms/123-456-789/participants/"+identityFor(target)+"/role", token, `{"role":"`+role+`"}`)
}

func storedRole(t *testing.T, userID uint) string {
	t.Helper()
	role, err := effectiveRole("123-456-789", userID)
	if err != nil {
		t.Fatal(err)
	}
	return role
}

func TestOnlyOwnersChangeRoles(t *testing.T) {
	r, _ := rolesTestRouter(t)
	for _, actor := range []uint{modID, 22} {
		assertAPIError(t, roleRequest(r, tokenFor(actor), 22, "moderator"), 403, apierror.OwnerRequired, "Bu işlem yalnızca oda sahiplerine açık")
		assertAPIError(t, moderationRequest(r, "/rooms/123-456-789/transfer-ownership", tokenFor(actor), `{"identity":"`+identityFor(22)+`"}`),
			403, apierror.OwnerRequired, "Bu işlem yalnızca oda sahiplerine açık")
	}
	assertAPIError(t, roleRequest(r, "owner-token", 22, "admin"), 400, apierror.InvalidRole, "Geçerli bir rol gerekli")
	assertAPIError(t, moderationRequest(r, "/rooms/123-456-789/participants/bogus/role", "owner-token", `{"role":"moderator"}`),
		400, apierror.InvalidIdentity, "Katılımcı kimliği geçersiz")
	assertAPIError(t, roleRequest(r, "owner-token", 999, "moderator"), 404, apierror.ParticipantNotFound, "Kullanıcı bulunamadı")
}

func TestOwnerPromotesAndDemotesWithLiveAttribute(t *testing.T) {
	r, f := rolesTestRouter(t)
	guest := f.participants[0]
	w := roleRequest(r, "owner-token", 22, "moderator")
	if w.Code != 200 || w.Body.String() != `{"identity":"`+identityFor(22)+`","role":"moderator"}` {
		t.Fatal(w.Code, w.Body.String())
	}
	row, _ := roleRow(database.DB, "123-456-789", 22)
	if row == nil || row.Role != models.RoleModerator || row.GrantedBy != 11 || guest.Attributes[roleAttribute] != "moderator" {
		t.Fatalf("promotion not stored or published: %+v %v", row, guest.Attributes)
	}
	if w := roleRequest(r, "owner-token", 22, "participant"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if storedRole(t, 22) != models.RoleParticipant || guest.Attributes[roleAttribute] != "" {
		t.Fatal("demotion not stored or published")
	}
	// Owners may change other owners' roles.
	if w := roleRequest(r, "owner-token", owner2ID, "moderator"); w.Code != 200 || storedRole(t, owner2ID) != models.RoleModerator {
		t.Fatal("owner could not demote another owner", w.Code)
	}
}

func TestPromotionToOwnerLiftsMicrophoneLock(t *testing.T) {
	r, f := rolesTestRouter(t)
	if w := micRequest(r, "owner-token", 22, `{"locked":true}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := roleRequest(r, "owner-token", 22, "owner"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	locked, _ := microphoneLocked("123-456-789", 22)
	guest := f.participants[0]
	if locked || !hasSource(guest, livekit.TrackSource_MICROPHONE) || guest.Attributes[microphoneLockAttribute] != "false" || guest.Attributes[roleAttribute] != "owner" {
		t.Fatalf("lock survived promotion: locked=%v attrs=%v", locked, guest.Attributes)
	}
}

func TestOwnerStepsDownOnlyWhileAnotherOwnerIsConnected(t *testing.T) {
	t.Run("connected co-owner", func(t *testing.T) {
		r, _ := rolesTestRouter(t)
		if w := roleRequest(r, "owner-token", 11, "moderator"); w.Code != 200 {
			t.Fatal("step down with a connected co-owner:", w.Code, w.Body.String())
		}
		assertAPIError(t, roleRequest(r, tokenFor(owner2ID), owner2ID, "participant"), 409, apierror.LastOwner, "Toplantıda bağlı başka bir oda sahibi kalmalı")
	})
	t.Run("co-owner only on record", func(t *testing.T) {
		r, f := rolesTestRouter(t)
		f.participants = f.participants[:4] // owner2 disconnects
		assertAPIError(t, roleRequest(r, "owner-token", 11, "moderator"), 409, apierror.LastOwner, "Toplantıda bağlı başka bir oda sahibi kalmalı")
	})
}

func TestDemotedModeratorLosesShareWhileParticipantSharingIsOff(t *testing.T) {
	r, f := rolesTestRouter(t)
	shareRequest(r, "start", tokenFor(modID), identityFor(modID))
	publishScreen(f.participants[2], livekit.TrackSource_SCREEN_SHARE)
	if w := roleRequest(r, "owner-token", modID, "participant"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var room models.Room
	database.DB.First(&room)
	if room.ScreenShareIdentity != "" || hasSource(f.participants[2], livekit.TrackSource_SCREEN_SHARE) {
		t.Fatal("demoted moderator kept sharing although participants may not share")
	}
}

func TestTransferOwnership(t *testing.T) {
	r, f := rolesTestRouter(t)
	path := "/rooms/123-456-789/transfer-ownership"
	assertAPIError(t, moderationRequest(r, path, "owner-token", `{"identity":"`+identityFor(11)+`"}`), 400, apierror.InvalidRequest, "Sahipliği kendinize devredemezsiniz")
	f.participants[0].Identity = "user-22-00000000-0000-0000-0000-000000000000" // guest reconnected elsewhere
	assertAPIError(t, moderationRequest(r, path, "owner-token", `{"identity":"`+identityFor(22)+`"}`), 404, apierror.ParticipantNotFound, "Katılımcı artık odada değil")
	f.participants[0].Identity = identityFor(22)

	w := moderationRequest(r, path, "owner-token", `{"identity":"`+identityFor(22)+`"}`)
	if w.Code != 200 || w.Body.String() != `{"caller_role":"moderator","identity":"`+identityFor(22)+`","role":"owner"}` {
		t.Fatal(w.Code, w.Body.String())
	}
	if storedRole(t, 22) != models.RoleOwner || storedRole(t, 11) != models.RoleModerator {
		t.Fatal("roles not swapped")
	}
	if f.participants[0].Attributes[roleAttribute] != "owner" || f.participants[1].Attributes[roleAttribute] != "moderator" {
		t.Fatal("attributes not published")
	}
	// The former owner is now a moderator.
	assertAPIError(t, roleRequest(r, "owner-token", modID, "participant"), 403, apierror.OwnerRequired, "Bu işlem yalnızca oda sahiplerine açık")
}
