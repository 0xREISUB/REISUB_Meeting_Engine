package controllers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// Members of the test room beyond owner (11) and guest (22, no permissions).
const (
	modID     = 33 // the former moderator permissions, granted an hour ago
	mod2ID    = 44 // the same permissions, granted now
	granterID = 55 // grant_permissions only
)

// formerModerator are the permissions migrated moderators get.
const formerModerator = "share_screen,mute_others,request_unmute,end_meeting"

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

// addMember creates a user with the given permissions (empty for none) and,
// if connected, a LiveKit session for them.
func addMember(t *testing.T, f *fakeModeration, userID uint, permissions string, grantedAt time.Time, connected bool) *livekit.ParticipantInfo {
	t.Helper()
	if err := database.DB.Create(&models.User{ID: userID, Nick: fmt.Sprintf("u%d", userID), Password: "x", Token: tokenFor(userID)}).Error; err != nil {
		t.Fatal(err)
	}
	if permissions != "" {
		row := models.RoomRole{RoomID: "123-456-789", UserID: userID, Role: models.RoleParticipant, Permissions: permissions, GrantedAt: grantedAt}
		if err := database.DB.Create(&row).Error; err != nil {
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

// permissionsTestRouter is the moderation router with the owner, the guest
// and three participants with permissions, all connected. Participants:
// [guest, owner, mod, mod2, granter].
func permissionsTestRouter(t *testing.T) (*gin.Engine, *fakeModeration) {
	t.Helper()
	r, f := moderationTestRouter(t)
	addScreenOwner(f)
	now := time.Now()
	addMember(t, f, modID, formerModerator, now.Add(-time.Hour), true)
	addMember(t, f, mod2ID, formerModerator, now, true)
	addMember(t, f, granterID, models.PermissionGrantPermissions, now, true)
	return r, f
}

func micRequest(r *gin.Engine, token string, target uint, body string) *httptest.ResponseRecorder {
	return moderationRequest(r, "/rooms/123-456-789/participants/"+identityFor(target)+"/microphone", token, body)
}

func permissionsRequest(r *gin.Engine, token string, target uint, permissions ...string) *httptest.ResponseRecorder {
	if permissions == nil {
		permissions = []string{}
	}
	body, _ := json.Marshal(map[string][]string{"permissions": permissions})
	return jsonRequest(r, http.MethodPut, "/rooms/123-456-789/participants/"+identityFor(target)+"/permissions", token, string(body))
}

// grant gives userID permissions as the owner.
func grant(t *testing.T, r *gin.Engine, userID uint, permissions ...string) {
	t.Helper()
	if w := permissionsRequest(r, "owner-token", userID, permissions...); w.Code != 200 {
		t.Fatal("grant:", w.Code, w.Body.String())
	}
}

func storedAccess(t *testing.T, userID uint) access {
	t.Helper()
	a, err := roomAccess("123-456-789", userID)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func assertCode(t *testing.T, w *httptest.ResponseRecorder, status int, code apierror.Code) {
	t.Helper()
	var body apierror.Body
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != status || body.Code != code {
		t.Fatalf("got %d %s, want %d %s", w.Code, w.Body.String(), status, code)
	}
}

func TestMicrophoneModerationChecksPermissions(t *testing.T) {
	const muteOnly, requestOnly = 66, 77
	cases := []struct {
		name   string
		actor  uint
		target uint
		locked bool // target's microphone is locked first
		body   string
		want   int
		code   apierror.Code
	}{
		{"mute_others locks a participant", modID, 22, false, `{"locked":true}`, 200, ""},
		{"mute_others locks another permission holder", modID, mod2ID, false, `{"locked":true}`, 200, ""},
		{"nobody locks the owner", modID, 11, false, `{"locked":true}`, 403, apierror.TargetProtected},
		{"nobody locks themselves", 11, 11, false, `{"locked":true}`, 403, apierror.TargetProtected},
		{"no permission", 22, modID, false, `{"locked":true}`, 403, apierror.PermissionRequired},
		{"grant_permissions alone", granterID, 22, false, `{"locked":true}`, 403, apierror.PermissionRequired},
		{"request_unmute cannot lock", requestOnly, 22, false, `{"locked":true}`, 403, apierror.PermissionRequired},
		{"request_unmute requests unmute", requestOnly, 22, false, `{"locked":false,"request_unmute":true}`, 200, ""},
		{"unlock and request needs both", requestOnly, 22, true, `{"locked":false,"request_unmute":true}`, 403, apierror.PermissionRequired},
		{"mute_others unlocks", muteOnly, 22, true, `{"locked":false}`, 200, ""},
		{"mute_others cannot request unmute", muteOnly, 22, true, `{"locked":false,"request_unmute":true}`, 403, apierror.PermissionRequired},
		{"both unlock and request", modID, 22, true, `{"locked":false,"request_unmute":true}`, 200, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, f := permissionsTestRouter(t)
			addMember(t, f, muteOnly, models.PermissionMuteOthers, time.Now(), true)
			addMember(t, f, requestOnly, models.PermissionRequestUnmute, time.Now(), true)
			if tc.locked {
				if w := micRequest(r, "owner-token", tc.target, `{"locked":true}`); w.Code != 200 {
					t.Fatal("lock first:", w.Code, w.Body.String())
				}
			}
			w := micRequest(r, tokenFor(tc.actor), tc.target, tc.body)
			if tc.code != "" {
				assertCode(t, w, tc.want, tc.code)
			} else if w.Code != tc.want {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestEndingTheMeetingNeedsEndMeetingAndDropsGrants(t *testing.T) {
	r, _ := permissionsTestRouter(t)
	for _, actor := range []uint{22, granterID} {
		assertAPIError(t, moderationRequest(r, "/rooms/123-456-789/close", tokenFor(actor), `{}`), 403, apierror.PermissionRequired, "Bu işlem için yetkiniz yok")
	}
	if w := moderationRequest(r, "/rooms/123-456-789/close", tokenFor(modID), `{}`); w.Code != 200 {
		t.Fatal("end_meeting could not end the meeting:", w.Code, w.Body.String())
	}
	var rows []models.RoomRole
	database.DB.Find(&rows)
	if len(rows) != 1 || rows[0].UserID != 11 {
		t.Fatalf("grants survived the room: %+v", rows)
	}
}

func TestJoinReportsRolePermissionsAndAttributes(t *testing.T) {
	r, _ := permissionsTestRouter(t)
	all := strings.Join(models.AllPermissions, ",")
	for _, tc := range []struct {
		user        uint
		role        string
		isHost      bool
		permissions string
	}{
		{11, models.RoleOwner, true, all},
		{modID, models.RoleParticipant, false, formerModerator},
		{22, models.RoleParticipant, false, ""},
	} {
		w := moderationRequest(r, "/rooms/join", tokenFor(tc.user), `{"room_id":"123-456-789","name":"x"}`)
		var response roomResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if response.Role != tc.role || response.IsHost != tc.isHost || strings.Join(response.Permissions, ",") != tc.permissions {
			t.Fatalf("user %d: role=%q is_host=%v permissions=%v", tc.user, response.Role, response.IsHost, response.Permissions)
		}
		if tc.permissions == "" && !strings.Contains(w.Body.String(), `"permissions":[]`) {
			t.Fatal("no permissions must be an empty list:", w.Body.String())
		}
		verifier, _ := auth.ParseAPIToken(response.Token)
		_, claims, err := verifier.Verify(testSecret)
		if err != nil {
			t.Fatal(err)
		}
		if claims.Attributes[roleAttribute] != tc.role || claims.Attributes[permissionsAttribute] != tc.permissions {
			t.Fatalf("user %d: attributes %v", tc.user, claims.Attributes)
		}
		if claims.Video.GetCanPublishSource(livekit.TrackSource_SCREEN_SHARE) {
			t.Fatalf("user %d: join token grants screen sharing", tc.user)
		}
	}
}

func TestSetPermissionsChecksCallerAndTarget(t *testing.T) {
	r, _ := permissionsTestRouter(t)
	for _, actor := range []uint{22, modID} {
		assertAPIError(t, permissionsRequest(r, tokenFor(actor), 22, "share_screen"), 403, apierror.PermissionRequired, "Bu işlem için yetkiniz yok")
	}
	for _, tc := range []struct{ actor, target uint }{{11, 11}, {granterID, 11}, {granterID, granterID}} {
		assertAPIError(t, permissionsRequest(r, tokenFor(tc.actor), tc.target), 403, apierror.TargetProtected, "Bu katılımcı üzerinde işlem yapma yetkiniz yok")
	}
	path := "/rooms/123-456-789/participants/" + identityFor(22) + "/permissions"
	assertAPIError(t, jsonRequest(r, http.MethodPut, path, "owner-token", `{}`), 400, apierror.InvalidRequest, "İzin listesi gerekli")
	assertAPIError(t, jsonRequest(r, http.MethodPut, "/rooms/123-456-789/participants/bogus/permissions", "owner-token", `{"permissions":[]}`),
		400, apierror.InvalidIdentity, "Katılımcı kimliği geçersiz")
	assertAPIError(t, permissionsRequest(r, "owner-token", 999), 404, apierror.ParticipantNotFound, "Kullanıcı bulunamadı")
}

func TestSetPermissionsReplacesTheSetAndPublishesIt(t *testing.T) {
	r, f := permissionsTestRouter(t)
	guest := f.participants[0]
	w := permissionsRequest(r, "owner-token", 22, "end_meeting", "bogus", "share_screen", "share_screen")
	if w.Code != 200 || w.Body.String() != `{"identity":"`+identityFor(22)+`","permissions":["share_screen","end_meeting"]}` {
		t.Fatal(w.Code, w.Body.String())
	}
	row := mustRow(t, 22)
	if row.Role != models.RoleParticipant || row.Permissions != "share_screen,end_meeting" || row.GrantedBy != 11 {
		t.Fatalf("not stored: %+v", row)
	}
	if guest.Attributes[roleAttribute] != "participant" || guest.Attributes[permissionsAttribute] != "share_screen,end_meeting" {
		t.Fatalf("not published: %v", guest.Attributes)
	}
	// Anyone with grant_permissions may grant any permission, that one included.
	if w := permissionsRequest(r, tokenFor(granterID), 22, "grant_permissions"); w.Code != 200 || storedAccess(t, 22).Permissions[0] != "grant_permissions" {
		t.Fatal("granter could not grant grant_permissions:", w.Code, w.Body.String())
	}
	// It applies to users who are not connected, and an empty set removes the row.
	addMember(t, f, 88, "", time.Time{}, false)
	if w := permissionsRequest(r, "owner-token", 88, "mute_others"); w.Code != 200 || !storedAccess(t, 88).has("mute_others") {
		t.Fatal("offline grant failed:", w.Code, w.Body.String())
	}
	if w := permissionsRequest(r, "owner-token", 22); w.Code != 200 || w.Body.String() != `{"identity":"`+identityFor(22)+`","permissions":[]}` {
		t.Fatal(w.Code, w.Body.String())
	}
	if row, _ := roleRow(database.DB, "123-456-789", 22); row != nil || guest.Attributes[permissionsAttribute] != "" {
		t.Fatalf("emptied permissions kept: %+v %v", row, guest.Attributes)
	}
}

func TestScreenShareNeedsSharePermission(t *testing.T) {
	r, _ := permissionsTestRouter(t)
	assertAPIError(t, shareRequest(r, "start", "guest-token", testParticipantIdentity), 403, apierror.PermissionRequired, "Bu işlem için yetkiniz yok")
	if w := shareRequest(r, "start", tokenFor(modID), identityFor(modID)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestRevokingShareScreenStopsTheCurrentShare(t *testing.T) {
	r, f := permissionsTestRouter(t)
	shareRequest(r, "start", tokenFor(modID), identityFor(modID))
	mod := session(f, modID)
	publishScreen(mod, livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO)
	// Other changes leave the share alone.
	grant(t, r, modID, "share_screen", "mute_others")
	if hasSource(mod, livekit.TrackSource_SCREEN_SHARE) == false || len(f.mutedTracks) != 0 {
		t.Fatal("unrelated change stopped the share")
	}
	grant(t, r, modID, "mute_others")
	var room models.Room
	database.DB.First(&room)
	if room.ScreenShareIdentity != "" || hasSource(mod, livekit.TrackSource_SCREEN_SHARE) || hasSource(mod, livekit.TrackSource_SCREEN_SHARE_AUDIO) {
		t.Fatal("revoked share_screen kept the share")
	}
	assertMuted(t, f, "screen", "screen-audio")
}

func TestStopSharePermissions(t *testing.T) {
	cases := []struct {
		name   string
		holder uint
		actor  uint
		want   int
		code   apierror.Code
	}{
		{"mute_others stops a participant", 22, modID, 200, ""},
		{"mute_others stops another permission holder", mod2ID, modID, 200, ""},
		{"nobody stops the owner", 11, modID, 403, apierror.TargetProtected},
		{"no permission", modID, 22, 403, apierror.PermissionRequired},
		{"owner stops own share", 11, 11, 200, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := permissionsTestRouter(t)
			grant(t, r, 22, "share_screen")
			if w := shareRequest(r, "start", tokenFor(tc.holder), identityFor(tc.holder)); w.Code != 200 {
				t.Fatal("start:", w.Code, w.Body.String())
			}
			w := shareRequest(r, "stop", tokenFor(tc.actor), identityFor(tc.holder))
			if tc.code != "" {
				assertCode(t, w, tc.want, tc.code)
			} else if w.Code != tc.want {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestTransferOwnership(t *testing.T) {
	r, f := permissionsTestRouter(t)
	path := "/rooms/123-456-789/transfer-ownership"
	for _, actor := range []uint{22, granterID} {
		assertAPIError(t, moderationRequest(r, path, tokenFor(actor), `{"identity":"`+identityFor(22)+`"}`), 403, apierror.OwnerRequired, "Bu işlem yalnızca oda sahibine açık")
	}
	assertAPIError(t, moderationRequest(r, path, "owner-token", `{"identity":"`+identityFor(11)+`"}`), 400, apierror.InvalidRequest, "Sahipliği kendinize devredemezsiniz")
	f.participants[0].Identity = "user-22-00000000-0000-0000-0000-000000000000" // guest reconnected elsewhere
	assertAPIError(t, moderationRequest(r, path, "owner-token", `{"identity":"`+identityFor(22)+`"}`), 404, apierror.ParticipantNotFound, "Katılımcı artık odada değil")
	f.participants[0].Identity = identityFor(22)
	// The new owner's microphone lock is lifted: nobody may lock the owner.
	if w := micRequest(r, "owner-token", 22, `{"locked":true}`); w.Code != 200 {
		t.Fatal(w.Code)
	}

	w := moderationRequest(r, path, "owner-token", `{"identity":"`+identityFor(22)+`"}`)
	if w.Code != 200 || w.Body.String() != `{"caller_role":"participant","identity":"`+identityFor(22)+`","role":"owner"}` {
		t.Fatal(w.Code, w.Body.String())
	}
	all := strings.Join(models.AllPermissions, ",")
	if storedAccess(t, 22).Role != models.RoleOwner || storedAccess(t, 11).Role != models.RoleParticipant || mustRow(t, 11).Permissions != all {
		t.Fatal("ownership not transferred")
	}
	guest, former := f.participants[0], f.participants[1]
	if guest.Attributes[roleAttribute] != "owner" || guest.Attributes[permissionsAttribute] != all ||
		former.Attributes[roleAttribute] != "participant" || former.Attributes[permissionsAttribute] != all {
		t.Fatalf("attributes not published: %v %v", guest.Attributes, former.Attributes)
	}
	if locked, _ := microphoneLocked("123-456-789", 22); locked || guest.Attributes[microphoneLockAttribute] != "false" {
		t.Fatal("new owner's microphone lock survived")
	}
	// The former owner keeps every permission but is no longer the owner.
	assertAPIError(t, moderationRequest(r, path, "owner-token", `{"identity":"`+identityFor(modID)+`"}`), 403, apierror.OwnerRequired, "Bu işlem yalnızca oda sahibine açık")
	assertAPIError(t, permissionsRequest(r, "owner-token", 22), 403, apierror.TargetProtected, "Bu katılımcı üzerinde işlem yapma yetkiniz yok")
	if w := permissionsRequest(r, "owner-token", modID); w.Code != 200 {
		t.Fatal("former owner could not use grant_permissions:", w.Code, w.Body.String())
	}
}
