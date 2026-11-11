package controllers

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/webhook"
)

const removerID = 66 // remove_members only

func removeRequest(r *gin.Engine, token string, target uint) *httptest.ResponseRecorder {
	return moderationRequest(r, "/rooms/123-456-789/participants/"+identityFor(target)+"/remove", token, `{}`)
}

func assertBanned(t *testing.T, r *gin.Engine, userID uint) {
	t.Helper()
	const message = "Bu toplantıdan çıkarıldınız ve tekrar katılamazsınız"
	assertAPIError(t, moderationRequest(r, "/rooms/join", tokenFor(userID), `{"room_id":"123-456-789","name":"x"}`), 403, apierror.RemovedFromRoom, message)
	assertAPIError(t, moderationRequest(r, "/rooms/123-456-789/messages", tokenFor(userID), `{"content":"hi"}`), 403, apierror.RemovedFromRoom, message)
	assertAPIError(t, jsonRequest(r, http.MethodGet, "/rooms/123-456-789/messages", tokenFor(userID), ""), 403, apierror.RemovedFromRoom, message)
}

func TestRemoveMemberChecksPermissionAndTarget(t *testing.T) {
	r, f := permissionsTestRouter(t)
	addMember(t, f, removerID, models.PermissionRemoveMembers, time.Now(), true)
	// Former moderators did not get remove_members.
	for _, actor := range []uint{22, modID, granterID} {
		assertAPIError(t, removeRequest(r, tokenFor(actor), mod2ID), 403, apierror.PermissionRequired, "Bu işlem için yetkiniz yok")
	}
	for _, tc := range []struct{ actor, target uint }{{removerID, 11}, {removerID, removerID}, {11, 11}} {
		assertAPIError(t, removeRequest(r, tokenFor(tc.actor), tc.target), 403, apierror.TargetProtected, "Bu katılımcı üzerinde işlem yapma yetkiniz yok")
	}
	assertAPIError(t, moderationRequest(r, "/rooms/123-456-789/participants/bogus/remove", "owner-token", `{}`), 400, apierror.InvalidIdentity, "Katılımcı kimliği geçersiz")
	assertAPIError(t, removeRequest(r, "owner-token", 999), 404, apierror.ParticipantNotFound, "Kullanıcı bulunamadı")
	if len(f.removed) != 0 {
		t.Fatal("a refused request removed someone:", f.removed)
	}
	var bans int64
	database.DB.Model(&models.RoomBan{}).Count(&bans)
	if bans != 0 {
		t.Fatal("a refused request recorded a ban")
	}
	// Anyone with remove_members may remove a participant, permission holders included.
	if w := removeRequest(r, tokenFor(removerID), mod2ID); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestRemoveMemberBansDisconnectsAndCleansUp(t *testing.T) {
	r, f := permissionsTestRouter(t)
	if w := shareRequest(r, "start", tokenFor(modID), identityFor(modID)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	publishScreen(session(f, modID), livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO)
	// A second device of the same user is removed as well.
	second := &livekit.ParticipantInfo{Identity: "user-33-00000000-0000-0000-0000-000000000000", Permission: &livekit.ParticipantPermission{CanPublish: true}}
	f.participants = append(f.participants, second)

	w := removeRequest(r, "owner-token", modID)
	if w.Code != 200 || w.Body.String() != `{"identity":"`+identityFor(modID)+`","removed":true}` {
		t.Fatal(w.Code, w.Body.String())
	}
	if !slices.Equal(f.removed, []string{identityFor(modID), second.Identity}) || len(sessionsOf(f.participants, modID)) != 0 {
		t.Fatalf("sessions not removed: %v", f.removed)
	}
	var ban models.RoomBan
	if err := database.DB.Where("room_id = ? AND user_id = ?", "123-456-789", modID).First(&ban).Error; err != nil || ban.RemovedBy != 11 || ban.RemovedAt.IsZero() {
		t.Fatalf("ban not recorded: %+v %v", ban, err)
	}
	if row, _ := roleRow(database.DB, "123-456-789", modID); row != nil {
		t.Fatalf("permissions survived removal: %+v", row)
	}
	var room models.Room
	database.DB.First(&room)
	if room.ScreenShareIdentity != "" {
		t.Fatal("removed member still holds the screen share")
	}
	assertMuted(t, f, "screen", "screen-audio")
	assertBanned(t, r, modID)
	// Removing again is harmless.
	if w := removeRequest(r, "owner-token", modID); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestRemovingAnOfflineMemberOnlyBansThem(t *testing.T) {
	r, f := permissionsTestRouter(t)
	addMember(t, f, 88, models.PermissionEndMeeting, time.Now(), false)
	if w := removeRequest(r, "owner-token", 88); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(f.removed) != 0 {
		t.Fatal("LiveKit called for a user who is not connected:", f.removed)
	}
	assertBanned(t, r, 88)
	// Even an empty room does not make a removed user its acting owner.
	disconnect(f, 22, modID, mod2ID, granterID)
	leaveRequest(r, 11)
	f.participants = nil
	assertBanned(t, r, 88)
	if storedAccess(t, 88).Role != models.RoleParticipant {
		t.Fatal("removed user became owner")
	}
}

func TestRemovedMemberReconnectingWithAnOldTokenIsRemovedAgain(t *testing.T) {
	r, f := permissionsTestRouter(t)
	if w := removeRequest(r, "owner-token", 22); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	// The client still holds a LiveKit token or refresh token from before the
	// ban and reconnects to the media server directly.
	f.removed = nil
	joined := func(identity string) *livekit.WebhookEvent {
		return &livekit.WebhookEvent{Event: webhook.EventParticipantJoined, Room: &livekit.Room{Name: "123-456-789"}, Participant: &livekit.ParticipantInfo{Identity: identity}}
	}
	if w := serve(r, signedWebhook(t, joined(testParticipantIdentity), "test-key", testSecret, false)); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	if !slices.Equal(f.removed, []string{testParticipantIdentity}) {
		t.Fatalf("banned reconnect not removed: %v", f.removed)
	}
	// Others joining are left alone.
	serve(r, signedWebhook(t, joined(identityFor(modID)), "test-key", testSecret, false))
	if len(f.removed) != 1 {
		t.Fatal("a member who was not removed was disconnected")
	}
	assertBanned(t, r, 22)
}

func TestBansEndWithTheRoom(t *testing.T) {
	r, _ := permissionsTestRouter(t)
	removeRequest(r, "owner-token", 22)
	if w := moderationRequest(r, "/rooms/123-456-789/close", "owner-token", `{}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var bans int64
	database.DB.Model(&models.RoomBan{}).Count(&bans)
	if bans != 0 {
		t.Fatal("bans survived the room")
	}
}
