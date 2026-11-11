package controllers

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/livekit/protocol/livekit"
)

func leaveRequest(r *gin.Engine, userID uint) *httptest.ResponseRecorder {
	return moderationRequest(r, "/rooms/123-456-789/leave", tokenFor(userID), "")
}

func joinAs(t *testing.T, r *gin.Engine, userID uint) roomResponse {
	t.Helper()
	w := moderationRequest(r, "/rooms/join", tokenFor(userID), `{"room_id":"123-456-789","name":"x"}`)
	var response roomResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 {
		t.Fatal("join:", w.Code, w.Body.String())
	}
	return response
}

// disconnect removes every session of the given users from the fake room.
func disconnect(f *fakeModeration, userIDs ...uint) {
	var kept []*livekit.ParticipantInfo
	for _, p := range f.participants {
		id, _ := participantUserID(p.Identity)
		drop := false
		for _, gone := range userIDs {
			drop = drop || id == gone
		}
		if !drop {
			kept = append(kept, p)
		}
	}
	f.participants = kept
}

func session(f *fakeModeration, userID uint) *livekit.ParticipantInfo {
	return sessionsOf(f.participants, userID)[0]
}

func mustRow(t *testing.T, userID uint) models.RoomRole {
	t.Helper()
	row, err := roleRow(database.DB, "123-456-789", userID)
	if err != nil || row == nil {
		t.Fatalf("no role row for %d (%v)", userID, err)
	}
	return *row
}

func TestSuccessionPrefersMostPermissionsAndIsIdempotent(t *testing.T) {
	r, f := permissionsTestRouter(t)
	if w := leaveRequest(r, 11); w.Code != 200 || w.Body.String() != `{"status":"left"}` {
		t.Fatal(w.Code, w.Body.String())
	}
	// modID and mod2ID have four permissions each; modID got them earlier.
	successor := mustRow(t, modID)
	if successor.Role != models.RoleOwner || !successor.Acting || successor.PreviousPermissions != formerModerator || successor.GrantedBy != 0 {
		t.Fatalf("senior participant not made acting owner: %+v", successor)
	}
	attrs := session(f, modID).Attributes
	if attrs[roleAttribute] != "owner" || attrs[permissionsAttribute] != strings.Join(models.AllPermissions, ",") {
		t.Fatalf("successor's attributes not published: %v", attrs)
	}
	if row := mustRow(t, 11); !row.Suspended || storedAccess(t, 11).Role != models.RoleParticipant {
		t.Fatalf("away owner not suspended: %+v", row)
	}
	if mustRow(t, mod2ID).Permissions != formerModerator {
		t.Fatal("other participant changed")
	}

	// /leave and the LiveKit event may both arrive; neither repeats succession.
	updates := f.updates
	disconnect(f, 11)
	leaveRequest(r, 11)
	room := models.Room{}
	database.DB.First(&room)
	if err := runSuccession(t.Context(), f, &room, f.participants, func(p *livekit.ParticipantInfo) bool { return p.Identity == identityFor(11) }); err != nil {
		t.Fatal(err)
	}
	if again := mustRow(t, modID); again.GrantedAt != successor.GrantedAt || f.updates != updates {
		t.Fatal("succession was not idempotent")
	}
}

func TestSuccessionOrder(t *testing.T) {
	t.Run("most permissions beat an earlier grant", func(t *testing.T) {
		r, _ := permissionsTestRouter(t)
		grant(t, r, granterID, models.AllPermissions...)
		leaveRequest(r, 11)
		if storedAccess(t, granterID).Role != models.RoleOwner {
			t.Fatal("participant with the most permissions did not succeed")
		}
	})
	t.Run("equal permissions and grant time: joined earliest", func(t *testing.T) {
		r, f := permissionsTestRouter(t)
		same := mustRow(t, modID).GrantedAt
		database.DB.Model(&models.RoomRole{}).Where("user_id = ?", mod2ID).Update("granted_at", same)
		session(f, modID).JoinedAt = 200
		session(f, mod2ID).JoinedAt = 100
		leaveRequest(r, 11)
		if storedAccess(t, mod2ID).Role != models.RoleOwner || storedAccess(t, modID).Role != models.RoleParticipant {
			t.Fatal("earliest joiner did not win the tie")
		}
	})
}

func TestReturningOwnerReclaimsAndActingOwnerGetsPermissionsBack(t *testing.T) {
	r, f := permissionsTestRouter(t)
	leaveRequest(r, 11)
	disconnect(f, 11)

	if joined := joinAs(t, r, 11); joined.Role != models.RoleOwner || !joined.IsHost || len(joined.Permissions) != len(models.AllPermissions) {
		t.Fatalf("returning owner got %+v", joined)
	}
	if row := mustRow(t, modID); row.Role != models.RoleParticipant || row.Acting || row.Permissions != formerModerator {
		t.Fatalf("acting owner did not step back: %+v", row)
	}
	if attrs := session(f, modID).Attributes; attrs[roleAttribute] != "participant" || attrs[permissionsAttribute] != formerModerator {
		t.Fatalf("restored permissions not published: %v", attrs)
	}
}

func TestSuccessionPicksRandomParticipantWhenNobodyHasPermissions(t *testing.T) {
	r, f := permissionsTestRouter(t)
	addMember(t, f, 66, "", time.Time{}, true)
	disconnect(f, modID, mod2ID, granterID)
	// Lock 66's microphone: becoming owner must lift it.
	if w := micRequest(r, "owner-token", 66, `{"locked":true}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	var calls []int
	old := randomIndex
	randomIndex = func(n int) (int, error) { calls = append(calls, n); return n - 1, nil }
	t.Cleanup(func() { randomIndex = old })

	leaveRequest(r, 11)
	// Candidates are the connected users 22 and 66, in ID order.
	if len(calls) != 1 || calls[0] != 2 {
		t.Fatalf("random pick over %v, want one pick over 2 candidates", calls)
	}
	row := mustRow(t, 66)
	if row.Role != models.RoleOwner || !row.Acting || row.PreviousPermissions != "" {
		t.Fatalf("random participant not made acting owner: %+v", row)
	}
	locked, _ := microphoneLocked("123-456-789", 66)
	if locked || !hasSource(session(f, 66), livekit.TrackSource_MICROPHONE) {
		t.Fatal("successor's microphone lock was not lifted")
	}
	// When the owner returns, the participant goes back to having nothing.
	disconnect(f, 11)
	joinAs(t, r, 11)
	if row, _ := roleRow(database.DB, "123-456-789", 66); row != nil || session(f, 66).Attributes[roleAttribute] != "participant" {
		t.Fatal("random successor did not step back")
	}
}

func TestRandomIndexStaysInRange(t *testing.T) {
	seen := map[int]bool{}
	for i := 0; i < 200; i++ {
		v, err := randomIndex(3)
		if err != nil || v < 0 || v > 2 {
			t.Fatal(v, err)
		}
		seen[v] = true
	}
	if len(seen) != 3 {
		t.Fatalf("not all indexes drawn: %v", seen)
	}
}

func TestSuccessionWithNobodyConnectedWaitsForNextJoiner(t *testing.T) {
	r, f := permissionsTestRouter(t)
	disconnect(f, 22, modID, mod2ID, granterID)
	if w := leaveRequest(r, 11); w.Code != 200 {
		t.Fatal(w.Code)
	}
	disconnect(f, 11)
	if storedAccess(t, 11).Role != models.RoleParticipant {
		t.Fatal("departed owner kept powers")
	}
	if first := joinAs(t, r, mod2ID); first.Role != models.RoleOwner || !first.IsHost {
		t.Fatalf("first joiner got %q", first.Role)
	}
	if second := joinAs(t, r, 22); second.Role != models.RoleParticipant || len(second.Permissions) != 0 {
		t.Fatalf("second joiner got %+v", second)
	}
	// The original owner still reclaims; the first joiner gets their permissions back.
	if joinAs(t, r, 11).Role != models.RoleOwner || mustRow(t, mod2ID).Permissions != formerModerator || storedAccess(t, mod2ID).Role != models.RoleParticipant {
		t.Fatal("original owner could not reclaim from the first joiner")
	}
}

func TestActingOwnerWhoLeavesStepsBackAndSuccessionContinues(t *testing.T) {
	r, f := permissionsTestRouter(t)
	leaveRequest(r, 11)
	disconnect(f, 11)
	// The acting owner (modID) leaves too: they step back, mod2 takes over.
	leaveRequest(r, modID)
	disconnect(f, modID)
	if row := mustRow(t, modID); row.Role != models.RoleParticipant || row.Acting || row.Permissions != formerModerator {
		t.Fatalf("departed acting owner did not step back: %+v", row)
	}
	if row := mustRow(t, mod2ID); row.Role != models.RoleOwner || !row.Acting {
		t.Fatalf("next participant not acting owner: %+v", row)
	}
	if !mustRow(t, 11).Suspended {
		t.Fatal("original owner lost the right to reclaim")
	}
	joinAs(t, r, 11)
	if storedAccess(t, mod2ID).Role != models.RoleParticipant {
		t.Fatal("acting owner did not step back when the owner returned")
	}
}

func TestParticipantLeavingChangesNothing(t *testing.T) {
	r, f := permissionsTestRouter(t)
	updates := f.updates
	if w := leaveRequest(r, modID); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if storedAccess(t, 11).Role != models.RoleOwner || mustRow(t, 11).Suspended || mustRow(t, modID).Permissions != formerModerator || f.updates != updates {
		t.Fatal("a participant leaving changed roles")
	}
}

func TestTransferByActingOwnerEndsReclaimRight(t *testing.T) {
	r, f := permissionsTestRouter(t)
	leaveRequest(r, 11)
	disconnect(f, 11)
	// The away owner is protected while the acting owner is in charge.
	assertCode(t, permissionsRequest(r, tokenFor(modID), 11), 403, apierror.TargetProtected)
	if w := moderationRequest(r, "/rooms/123-456-789/transfer-ownership", tokenFor(modID), `{"identity":"`+identityFor(22)+`"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	all := strings.Join(models.AllPermissions, ",")
	if storedAccess(t, 22).Role != models.RoleOwner || mustRow(t, 22).Acting || mustRow(t, modID).Permissions != all {
		t.Fatal("acting owner's transfer not applied")
	}
	if joined := joinAs(t, r, 11); joined.Role != models.RoleParticipant || strings.Join(joined.Permissions, ",") != all || storedAccess(t, 22).Role != models.RoleOwner {
		t.Fatalf("owner reclaimed after a transfer: %+v", joined)
	}
}
