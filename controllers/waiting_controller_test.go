package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
)

func settingsRequest(r *gin.Engine, token string, on bool) *httptest.ResponseRecorder {
	body := `{"waiting_room":false}`
	if on {
		body = `{"waiting_room":true}`
	}
	return jsonRequest(r, http.MethodPut, "/rooms/123-456-789/settings", token, body)
}

func joinRequest(r *gin.Engine, userID uint, name string) *httptest.ResponseRecorder {
	return moderationRequest(r, "/rooms/join", tokenFor(userID), `{"room_id":"123-456-789","name":"`+name+`"}`)
}

func waitingPath(requestID string) string { return "/rooms/123-456-789/waiting/" + requestID }

type stateBody struct {
	ScreenShareIdentity string         `json:"screen_share_identity"`
	WaitingRoom         bool           `json:"waiting_room"`
	Waiting             []waitingEntry `json:"waiting"`
}

func readState(t *testing.T, w *httptest.ResponseRecorder) stateBody {
	t.Helper()
	var state stateBody
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil || state.Waiting == nil {
		t.Fatal("state:", w.Code, w.Body.String())
	}
	return state
}

// waitingRoomRouter is the permissions router with the waiting room on.
// The owner and modID (admit_members) enter directly; the guest and
// granterID (no admit_members, never admitted) wait.
func waitingRoomRouter(t *testing.T) (*gin.Engine, *fakeModeration) {
	t.Helper()
	r, f := permissionsTestRouter(t)
	if w := settingsRequest(r, "owner-token", true); w.Code != 200 || !readState(t, w).WaitingRoom {
		t.Fatal("turn on:", w.Code, w.Body.String())
	}
	return r, f
}

// wait asks to join as userID and returns the request ID.
func wait(t *testing.T, r *gin.Engine, userID uint, name string) string {
	t.Helper()
	w := joinRequest(r, userID, name)
	var body struct {
		Status    string `json:"status"`
		RequestID string `json:"request_id"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Status != "waiting" || body.RequestID == "" {
		t.Fatal("expected to wait:", w.Code, w.Body.String())
	}
	return body.RequestID
}

func pollStatus(t *testing.T, r *gin.Engine, userID uint, requestID string) string {
	t.Helper()
	w := jsonRequest(r, http.MethodGet, waitingPath(requestID), tokenFor(userID), "")
	var body struct {
		Status string `json:"status"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatal("poll:", w.Code, w.Body.String())
	}
	return body.Status
}

func TestWaitingRoomSettingNeedsAdmitMembers(t *testing.T) {
	r, _ := permissionsTestRouter(t)
	if state := readState(t, stateRequest(r, "owner-token")); state.WaitingRoom || len(state.Waiting) != 0 {
		t.Fatalf("waiting room on by default: %+v", state)
	}
	for _, actor := range []uint{22, granterID} {
		assertAPIError(t, settingsRequest(r, tokenFor(actor), true), 403, apierror.PermissionRequired, "Bu işlem için yetkiniz yok")
	}
	assertAPIError(t, jsonRequest(r, http.MethodPut, "/rooms/123-456-789/settings", "owner-token", `{}`), 400, apierror.InvalidRequest, "Bekleme odası ayarı gerekli")
	w := settingsRequest(r, tokenFor(modID), true)
	if w.Code != 200 || w.Body.String() != `{"screen_share_identity":"","waiting":[],"waiting_room":true}` {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestWaitingRoomLetsSomeInDirectly(t *testing.T) {
	r, f := permissionsTestRouter(t)
	// 77 joins while the waiting room is off, so they count as admitted.
	addMember(t, f, 77, "", time.Time{}, false)
	if w := joinRequest(r, 77, "early"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	removeRequest(r, "owner-token", 22)
	if w := settingsRequest(r, "owner-token", true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, userID := range []uint{11, modID, 77} {
		if w := joinRequest(r, userID, "x"); w.Code != 200 || !json.Valid(w.Body.Bytes()) {
			t.Fatalf("user %d had to wait: %d %s", userID, w.Code, w.Body.String())
		}
	}
	// Removed users are refused before the waiting room.
	assertAPIError(t, joinRequest(r, 22, "x"), 403, apierror.RemovedFromRoom, "Bu toplantıdan çıkarıldınız ve tekrar katılamazsınız")
	// The owner enters directly even while away (suspended).
	disconnect(f, 22, modID, mod2ID, granterID)
	leaveRequest(r, 11)
	disconnect(f, 11)
	if !mustRow(t, 11).Suspended {
		t.Fatal("owner not away")
	}
	if joined := joinAs(t, r, 11); joined.Role != models.RoleOwner {
		t.Fatalf("away owner had to wait: %+v", joined)
	}
	// Others wait, and joining again while waiting keeps the request.
	first := wait(t, r, granterID, "Ayşe")
	if again := wait(t, r, granterID, "Ayşe K."); again != first {
		t.Fatalf("second join got a new request %s, want %s", again, first)
	}
	if state := readState(t, stateRequest(r, "owner-token")); len(state.Waiting) != 1 || state.Waiting[0].Name != "Ayşe K." {
		t.Fatalf("waiting list: %+v", state.Waiting)
	}
}

func TestWaitingRequestPermissionsAndAdmission(t *testing.T) {
	r, _ := waitingRoomRouter(t)
	requestID := wait(t, r, 22, "Mehmet")
	if got := pollStatus(t, r, 22, requestID); got != "waiting" {
		t.Fatal(got)
	}
	// Only the requester may read or cancel the request.
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		assertAPIError(t, jsonRequest(r, method, waitingPath(requestID), tokenFor(modID), ""), 404, apierror.WaitingRequestNotFound, "Katılma isteği bulunamadı")
	}
	// Answering needs admit_members.
	for _, action := range []string{"/admit", "/reject"} {
		for _, actor := range []uint{22, granterID} {
			assertAPIError(t, moderationRequest(r, waitingPath(requestID)+action, tokenFor(actor), `{}`), 403, apierror.PermissionRequired, "Bu işlem için yetkiniz yok")
		}
	}
	assertAPIError(t, moderationRequest(r, waitingPath("unknown")+"/admit", "owner-token", `{}`), 404, apierror.WaitingRequestNotFound, "Katılma isteği bulunamadı")

	w := moderationRequest(r, waitingPath(requestID)+"/admit", tokenFor(modID), `{}`)
	if state := readState(t, w); !state.WaitingRoom || len(state.Waiting) != 0 {
		t.Fatalf("admitted request still listed: %+v", state)
	}
	poll := jsonRequest(r, http.MethodGet, waitingPath(requestID), "guest-token", "")
	var admitted admittedResponse
	if poll.Code != 200 || json.Unmarshal(poll.Body.Bytes(), &admitted) != nil || admitted.Status != "admitted" ||
		admitted.Token == "" || admitted.Participant != "Mehmet" || admitted.Role != models.RoleParticipant || admitted.Permissions == nil || admitted.RoomID != "123-456-789" {
		t.Fatal("admitted poll:", poll.Code, poll.Body.String())
	}
	// Admitted users are remembered: rejoining never waits again.
	if w := joinRequest(r, 22, "Mehmet"); w.Code != 200 {
		t.Fatal("admitted user had to wait again:", w.Code, w.Body.String())
	}
}

func TestRejectedUserMayAskAgain(t *testing.T) {
	r, _ := waitingRoomRouter(t)
	requestID := wait(t, r, 22, "Mehmet")
	if w := moderationRequest(r, waitingPath(requestID)+"/reject", "owner-token", `{}`); w.Code != 200 || len(readState(t, w).Waiting) != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	if got := pollStatus(t, r, 22, requestID); got != "rejected" {
		t.Fatal(got)
	}
	var bans int64
	database.DB.Model(&models.RoomBan{}).Count(&bans)
	again := wait(t, r, 22, "Mehmet")
	if bans != 0 || again == requestID || pollStatus(t, r, 22, again) != "waiting" {
		t.Fatal("rejection banned the user or reused the old request")
	}
}

func TestWaitingRequestsEnd(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		r, _ := waitingRoomRouter(t)
		requestID := wait(t, r, 22, "Mehmet")
		if w := jsonRequest(r, http.MethodDelete, waitingPath(requestID), "guest-token", ""); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		assertAPIError(t, jsonRequest(r, http.MethodGet, waitingPath(requestID), "guest-token", ""), 404, apierror.WaitingRequestNotFound, "Katılma isteği bulunamadı")
	})
	t.Run("expired without polls", func(t *testing.T) {
		r, _ := waitingRoomRouter(t)
		requestID := wait(t, r, 22, "Mehmet")
		database.DB.Model(&models.WaitingRequest{}).Where("request_id = ?", requestID).Update("last_polled_at", time.Now().Add(-waitingExpiry-time.Second))
		if state := readState(t, stateRequest(r, "owner-token")); len(state.Waiting) != 0 {
			t.Fatal("expired request listed")
		}
		assertAPIError(t, jsonRequest(r, http.MethodGet, waitingPath(requestID), "guest-token", ""), 404, apierror.WaitingRequestNotFound, "Katılma isteği bulunamadı")
	})
	t.Run("room ended", func(t *testing.T) {
		r, _ := waitingRoomRouter(t)
		requestID := wait(t, r, 22, "Mehmet")
		moderationRequest(r, "/rooms/123-456-789/close", "owner-token", `{}`)
		if w := jsonRequest(r, http.MethodGet, waitingPath(requestID), "guest-token", ""); w.Code != 404 {
			t.Fatal(w.Code, w.Body.String())
		}
		var left int64
		database.DB.Model(&models.WaitingRequest{}).Count(&left)
		if left != 0 {
			t.Fatal("requests survived the room")
		}
	})
}

func TestTurningTheWaitingRoomOffAdmitsEveryone(t *testing.T) {
	r, _ := waitingRoomRouter(t)
	guest := wait(t, r, 22, "Mehmet")
	granter := wait(t, r, granterID, "Ayşe")
	w := settingsRequest(r, "owner-token", false)
	if state := readState(t, w); state.WaitingRoom || len(state.Waiting) != 0 {
		t.Fatalf("after turning off: %+v", state)
	}
	if pollStatus(t, r, 22, guest) != "admitted" || pollStatus(t, r, granterID, granter) != "admitted" {
		t.Fatal("waiting users not admitted")
	}
	if !mustAdmitted(t, 22) || !mustAdmitted(t, granterID) {
		t.Fatal("admitted users not remembered")
	}
}

func mustAdmitted(t *testing.T, userID uint) bool {
	t.Helper()
	admitted, err := isAdmitted("123-456-789", userID)
	if err != nil {
		t.Fatal(err)
	}
	return admitted
}

func TestWaitingListOnlyForAdmitters(t *testing.T) {
	r, _ := waitingRoomRouter(t)
	requestID := wait(t, r, granterID, "Ayşe")
	for _, tc := range []struct {
		user uint
		sees bool
	}{{11, true}, {modID, true}, {mod2ID, true}, {22, false}, {granterID, false}} {
		state := readState(t, stateRequest(r, tokenFor(tc.user)))
		if !state.WaitingRoom {
			t.Fatalf("user %d: waiting_room not reported", tc.user)
		}
		if tc.sees != (len(state.Waiting) == 1) || tc.sees && (state.Waiting[0].RequestID != requestID || state.Waiting[0].Name != "Ayşe") {
			t.Fatalf("user %d sees %+v", tc.user, state.Waiting)
		}
	}
	// Someone still waiting cannot use the chat.
	assertAPIError(t, moderationRequest(r, "/rooms/123-456-789/messages", tokenFor(granterID), `{"content":"hi"}`), 403, apierror.NotAdmitted, "Toplantıya henüz alınmadınız")
}
