package controllers

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

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

func TestSuccessionPromotesLongestModeratorAndIsIdempotent(t *testing.T) {
	r, f := rolesTestRouter(t)
	disconnect(f, owner2ID) // the co-owner left earlier, while 11 was still here
	if w := leaveRequest(r, 11); w.Code != 200 || w.Body.String() != `{"status":"left"}` {
		t.Fatal(w.Code, w.Body.String())
	}
	successor := mustRow(t, modID)
	if successor.Role != models.RoleOwner || !successor.Acting || successor.PreviousRole != models.RoleModerator || successor.GrantedBy != 0 {
		t.Fatalf("longest moderator not made acting owner: %+v", successor)
	}
	if session(f, modID).Attributes[roleAttribute] != "owner" {
		t.Fatal("successor's attribute not published")
	}
	for _, away := range []uint{11, owner2ID} {
		if row := mustRow(t, away); !row.Suspended || storedRole(t, away) != models.RoleParticipant {
			t.Fatalf("away owner %d not suspended: %+v", away, row)
		}
	}
	if storedRole(t, mod2ID) != models.RoleModerator {
		t.Fatal("other moderator changed")
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
	if again := mustRow(t, modID); again.GrantedAt != successor.GrantedAt || storedRole(t, mod2ID) != models.RoleModerator || f.updates != updates {
		t.Fatal("succession was not idempotent")
	}
}

func TestReturningOwnerReclaimsAndActingOwnerReverts(t *testing.T) {
	r, f := rolesTestRouter(t)
	disconnect(f, owner2ID)
	leaveRequest(r, 11)
	disconnect(f, 11)

	if joined := joinAs(t, r, 11); joined.Role != models.RoleOwner || !joined.IsHost {
		t.Fatalf("returning owner got %q", joined.Role)
	}
	if row := mustRow(t, modID); row.Role != models.RoleModerator || row.Acting {
		t.Fatalf("acting owner did not revert: %+v", row)
	}
	if session(f, modID).Attributes[roleAttribute] != "moderator" {
		t.Fatal("reverted role not published")
	}
	// The other suspended owner keeps the right to reclaim.
	if !mustRow(t, owner2ID).Suspended {
		t.Fatal("other away owner lost suspension")
	}
	if joined := joinAs(t, r, owner2ID); joined.Role != models.RoleOwner {
		t.Fatal("second away owner could not reclaim")
	}
}

func TestSuccessionPicksRandomParticipantWhenNoModeratorIsConnected(t *testing.T) {
	r, f := rolesTestRouter(t)
	addMember(t, f, 66, "", time.Time{}, true)
	disconnect(f, modID, mod2ID, owner2ID)
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
	if row.Role != models.RoleOwner || !row.Acting || row.PreviousRole != "" {
		t.Fatalf("random participant not made acting owner: %+v", row)
	}
	locked, _ := microphoneLocked("123-456-789", 66)
	if locked || !hasSource(session(f, 66), livekit.TrackSource_MICROPHONE) {
		t.Fatal("successor's microphone lock was not lifted")
	}
	// When the owner returns, the participant goes back to having no role.
	disconnect(f, 11)
	joinAs(t, r, 11)
	if storedRole(t, 66) != models.RoleParticipant || session(f, 66).Attributes[roleAttribute] != "" {
		t.Fatal("random successor did not revert to participant")
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
	r, f := rolesTestRouter(t)
	disconnect(f, 22, modID, mod2ID, owner2ID)
	if w := leaveRequest(r, 11); w.Code != 200 {
		t.Fatal(w.Code)
	}
	disconnect(f, 11)
	if storedRole(t, 11) != models.RoleParticipant {
		t.Fatal("departed owner kept powers")
	}
	if first := joinAs(t, r, 22); first.Role != models.RoleOwner || !first.IsHost {
		t.Fatalf("first joiner got %q", first.Role)
	}
	if second := joinAs(t, r, mod2ID); second.Role != models.RoleModerator {
		t.Fatalf("second joiner got %q", second.Role)
	}
	// The original owner still reclaims; the first joiner reverts.
	if joinAs(t, r, 11).Role != models.RoleOwner || storedRole(t, 22) != models.RoleParticipant {
		t.Fatal("original owner could not reclaim from the first joiner")
	}
}

func TestActingOwnerWhoLeavesRevertsAndSuccessionContinues(t *testing.T) {
	r, f := rolesTestRouter(t)
	disconnect(f, owner2ID)
	leaveRequest(r, 11)
	disconnect(f, 11)
	// The acting owner (modID) leaves too: they revert, mod2 takes over.
	leaveRequest(r, modID)
	disconnect(f, modID)
	if row := mustRow(t, modID); row.Role != models.RoleModerator || row.Acting || row.Suspended {
		t.Fatalf("departed acting owner not reverted: %+v", row)
	}
	if row := mustRow(t, mod2ID); row.Role != models.RoleOwner || !row.Acting {
		t.Fatalf("next moderator not acting owner: %+v", row)
	}
	if !mustRow(t, 11).Suspended {
		t.Fatal("original owner lost the right to reclaim")
	}
	joinAs(t, r, 11)
	if storedRole(t, mod2ID) != models.RoleModerator {
		t.Fatal("acting owner did not revert when the owner returned")
	}
}

func TestLeaveWhileAnotherOwnerIsConnectedChangesNothing(t *testing.T) {
	r, f := rolesTestRouter(t)
	updates := f.updates
	if w := leaveRequest(r, 11); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if storedRole(t, 11) != models.RoleOwner || mustRow(t, 11).Suspended || storedRole(t, modID) != models.RoleModerator || f.updates != updates {
		t.Fatal("leaving with a connected co-owner changed roles")
	}
}

func TestExplicitRoleChangeEndsReclaimRight(t *testing.T) {
	r, f := rolesTestRouter(t)
	disconnect(f, owner2ID)
	leaveRequest(r, 11)
	disconnect(f, 11)
	// The acting owner demotes the away owner; that is final.
	if w := roleRequest(r, tokenFor(modID), 11, "participant"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if joinAs(t, r, 11).Role != models.RoleParticipant || storedRole(t, modID) != models.RoleOwner {
		t.Fatal("explicitly demoted owner reclaimed ownership")
	}
}
