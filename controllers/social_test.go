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
	"reisub-backend/middleware"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/livekit/protocol/livekit"
)

// socialTestRouter serves the social API, behind the same auth as in
// production, to users alice (1), bob (2), carol (3) and a guest (9), whose
// tokens are their names. LiveKit is faked.
func socialTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	db := useTestDB(t)
	for _, u := range []models.User{
		{ID: 1, Nick: "alice", Password: "x", Token: "alice"},
		{ID: 2, Nick: "bob", Password: "x", Token: "bob"},
		{ID: 3, Nick: "carol", Password: "x", Token: "carol"},
		{ID: 9, Nick: "guest-1", Token: "guest", IsGuest: true},
	} {
		if err := db.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
	}
	fake := &fakeModeration{}
	oldFactory := newLiveKitRoomService
	newLiveKitRoomService = func(string) liveKitRoomService { return fake }
	t.Cleanup(func() { newLiveKitRoomService = oldFactory })
	t.Setenv("LIVEKIT_API_KEY", "test-key")
	t.Setenv("LIVEKIT_API_SECRET", "test-secret-000000000000000000000000")
	t.Setenv("LIVEKIT_INTERNAL_URL", "http://livekit:7880")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("", middleware.RequireAuth(), middleware.RequireAccount())
	g.GET("/me", GetProfile)
	g.PATCH("/me", UpdateProfile)
	g.GET("/social", GetSocialOverview)
	g.POST("/friends/requests", SendFriendRequest)
	g.POST("/friends/requests/:request_id/accept", AcceptFriendRequest)
	g.POST("/friends/requests/:request_id/decline", DeclineFriendRequest)
	g.DELETE("/friends/:user_id", RemoveFriend)
	g.POST("/teams", CreateTeam)
	g.GET("/teams/:team_id", GetTeam)
	g.DELETE("/teams/:team_id", DeleteTeam)
	g.POST("/teams/:team_id/members", AddTeamMember)
	g.DELETE("/teams/:team_id/members/:user_id", RemoveTeamMember)
	g.PUT("/teams/:team_id/members/:user_id/role", SetTeamMemberRole)
	g.POST("/teams/:team_id/meeting", StartTeamMeeting)
	g.GET("/chats/:kind/:id/messages", ListConversationMessages)
	g.POST("/chats/:kind/:id/messages", SendConversationMessage)
	g.POST("/chats/friend/:id/call", CallFriend)
	return r
}

func overviewOf(t *testing.T, r *gin.Engine, token string) socialOverview {
	t.Helper()
	w := jsonRequest(r, http.MethodGet, "/social", token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("overview: %d %s", w.Code, w.Body.String())
	}
	var overview socialOverview
	if err := json.Unmarshal(w.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	return overview
}

func befriend(t *testing.T, r *gin.Engine, from, to string) {
	t.Helper()
	if w := jsonRequest(r, http.MethodPost, "/friends/requests", from, `{"nick":"`+to+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("request: %d %s", w.Code, w.Body.String())
	}
	requests := overviewOf(t, r, to).Requests
	if len(requests) == 0 {
		t.Fatal("no request arrived")
	}
	path := fmt.Sprintf("/friends/requests/%d/accept", requests[len(requests)-1].ID)
	if w := jsonRequest(r, http.MethodPost, path, to, ""); w.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
}

func TestGuestsHaveNoSocialFeatures(t *testing.T) {
	r := socialTestRouter(t)
	for _, path := range []string{"/me", "/social"} {
		w := jsonRequest(r, http.MethodGet, path, "guest", "")
		assertAPIError(t, w, http.StatusForbidden, apierror.AccountRequired, "Bu özellik için giriş yapmalısınız")
	}
}

func TestProfileChangesAndLimits(t *testing.T) {
	r := socialTestRouter(t)
	w := jsonRequest(r, http.MethodGet, "/me", "alice", "")
	if w.Code != http.StatusOK || !jsonHas(w, `"display_name":"alice"`, `"presence":"online"`) {
		t.Fatalf("profile defaults to the nick: %s", w.Body.String())
	}
	w = jsonRequest(r, http.MethodPatch, "/me", "alice", `{"display_name":"  Alice A  ","status_message":"coding","presence":"busy"}`)
	if w.Code != http.StatusOK || !jsonHas(w, `"display_name":"Alice A"`, `"status_message":"coding"`, `"presence":"busy"`) {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	// Only what is sent changes; an empty name goes back to the nick.
	w = jsonRequest(r, http.MethodPatch, "/me", "alice", `{"display_name":""}`)
	if !jsonHas(w, `"display_name":"alice"`, `"status_message":"coding"`, `"presence":"busy"`) {
		t.Fatalf("partial update: %s", w.Body.String())
	}
	w = jsonRequest(r, http.MethodPatch, "/me", "alice", `{"presence":"away"}`)
	assertAPIError(t, w, http.StatusBadRequest, apierror.InvalidProfile, "Geçersiz durum")
	long := fmt.Sprintf(`{"status_message":"%081d"}`, 0)
	w = jsonRequest(r, http.MethodPatch, "/me", "alice", long)
	assertAPIError(t, w, http.StatusBadRequest, apierror.InvalidProfile, "Durum mesajı en fazla 80 karakter olabilir")
}

func TestFriendRequestsBothWays(t *testing.T) {
	r := socialTestRouter(t)
	w := jsonRequest(r, http.MethodPost, "/friends/requests", "alice", `{"nick":"alice"}`)
	assertAPIError(t, w, http.StatusBadRequest, apierror.CannotAddSelf, "Kendinizi arkadaş olarak ekleyemezsiniz")
	w = jsonRequest(r, http.MethodPost, "/friends/requests", "alice", `{"nick":"nobody"}`)
	assertAPIError(t, w, http.StatusNotFound, apierror.UserNotFound, "Kullanıcı bulunamadı")
	w = jsonRequest(r, http.MethodPost, "/friends/requests", "alice", `{"nick":"guest-1"}`)
	assertAPIError(t, w, http.StatusNotFound, apierror.UserNotFound, "Kullanıcı bulunamadı")

	if w = jsonRequest(r, http.MethodPost, "/friends/requests", "alice", `{"nick":"bob"}`); w.Code != http.StatusCreated {
		t.Fatalf("request: %d %s", w.Code, w.Body.String())
	}
	w = jsonRequest(r, http.MethodPost, "/friends/requests", "alice", `{"nick":"bob"}`)
	assertAPIError(t, w, http.StatusConflict, apierror.RequestPending, "İstek zaten gönderildi")
	if out := overviewOf(t, r, "alice").Outgoing; len(out) != 1 || out[0].User.Nick != "bob" {
		t.Fatalf("alice's outgoing: %+v", out)
	}
	in := overviewOf(t, r, "bob").Requests
	if len(in) != 1 || in[0].User.Nick != "alice" {
		t.Fatalf("bob's requests: %+v", in)
	}
	// Only bob may answer it.
	w = jsonRequest(r, http.MethodPost, fmt.Sprintf("/friends/requests/%d/accept", in[0].ID), "alice", "")
	assertAPIError(t, w, http.StatusNotFound, apierror.FriendRequestNotFound, "Arkadaşlık isteği bulunamadı")
	// Bob asking back says yes.
	if w = jsonRequest(r, http.MethodPost, "/friends/requests", "bob", `{"nick":"alice"}`); w.Code != http.StatusOK {
		t.Fatalf("asking back: %d %s", w.Code, w.Body.String())
	}
	if friends := overviewOf(t, r, "alice").Friends; len(friends) != 1 || friends[0].Nick != "bob" {
		t.Fatalf("alice's friends: %+v", friends)
	}
	w = jsonRequest(r, http.MethodPost, "/friends/requests", "alice", `{"nick":"bob"}`)
	assertAPIError(t, w, http.StatusConflict, apierror.AlreadyFriends, "Zaten arkadaşsınız")

	// Carol's request is turned down, then bob is unfriended.
	jsonRequest(r, http.MethodPost, "/friends/requests", "carol", `{"nick":"alice"}`)
	in = overviewOf(t, r, "alice").Requests
	if w = jsonRequest(r, http.MethodPost, fmt.Sprintf("/friends/requests/%d/decline", in[0].ID), "alice", ""); w.Code != http.StatusNoContent {
		t.Fatalf("decline: %d", w.Code)
	}
	if w = jsonRequest(r, http.MethodDelete, "/friends/2", "alice", ""); w.Code != http.StatusNoContent {
		t.Fatalf("unfriend: %d", w.Code)
	}
	overview := overviewOf(t, r, "alice")
	if len(overview.Friends)+len(overview.Requests)+len(overview.Outgoing) != 0 {
		t.Fatalf("left over: %+v", overview)
	}
}

func TestFriendsSeeWhetherSomeoneIsAround(t *testing.T) {
	r := socialTestRouter(t)
	befriend(t, r, "alice", "bob")
	// Bob just made requests, so alice sees him online.
	if p := overviewOf(t, r, "alice").Friends[0].Presence; p != presenceOnline {
		t.Fatalf("presence = %s, want online", p)
	}
	jsonRequest(r, http.MethodPatch, "/me", "bob", `{"presence":"busy"}`)
	if p := overviewOf(t, r, "alice").Friends[0].Presence; p != presenceBusy {
		t.Fatalf("presence = %s, want busy", p)
	}
	// Quiet for a while: offline, with when he was last around.
	old := time.Now().Add(-10 * time.Minute)
	database.DB.Model(&models.User{}).Where("id = 2").UpdateColumn("active_at", old)
	friend := overviewOf(t, r, "alice").Friends[0]
	if friend.Presence != presenceOffline || friend.LastSeenSeconds == nil || *friend.LastSeenSeconds < 590 {
		t.Fatalf("quiet friend: %+v", friend)
	}
	// In a meeting shows even without requests.
	database.DB.Create(&models.Room{RoomID: "111-222-333", Name: "m", Status: models.RoomStatusActive})
	markInMeeting("111-222-333", "user-2-00000000-0000-4000-8000-000000000000")
	if p := overviewOf(t, r, "alice").Friends[0].Presence; p != presenceInMeeting {
		t.Fatalf("presence = %s, want in_meeting", p)
	}
	// Hidden users are offline, with no last seen.
	jsonRequest(r, http.MethodPatch, "/me", "bob", `{"presence":"invisible"}`)
	friend = overviewOf(t, r, "alice").Friends[0]
	if friend.Presence != presenceOffline || friend.LastSeenSeconds != nil {
		t.Fatalf("hidden friend: %+v", friend)
	}
}

func TestFriendChatIsForFriends(t *testing.T) {
	r := socialTestRouter(t)
	w := jsonRequest(r, http.MethodPost, "/chats/friend/2/messages", "alice", `{"content":"hi"}`)
	assertAPIError(t, w, http.StatusForbidden, apierror.NotFriends, "Arkadaş değilsiniz")
	befriend(t, r, "alice", "bob")
	w = jsonRequest(r, http.MethodPost, "/chats/friend/2/messages", "alice", `{"content":"  hi bob  "}`)
	if w.Code != http.StatusCreated || !jsonHas(w, `"content":"hi bob"`, `"is_mine":true`, `"sender":"alice"`) {
		t.Fatalf("send: %d %s", w.Code, w.Body.String())
	}
	jsonRequest(r, http.MethodPost, "/chats/friend/1/messages", "bob", `{"content":"hey"}`)
	var messages []socialMessageResponse
	w = jsonRequest(r, http.MethodGet, "/chats/friend/1/messages", "bob", "")
	_ = json.Unmarshal(w.Body.Bytes(), &messages)
	if len(messages) != 2 || messages[0].Content != "hi bob" || messages[0].IsMine || !messages[1].IsMine {
		t.Fatalf("bob's view: %+v", messages)
	}
	// Only what is newer than the last one seen.
	w = jsonRequest(r, http.MethodGet, fmt.Sprintf("/chats/friend/1/messages?after=%d", messages[0].ID), "bob", "")
	_ = json.Unmarshal(w.Body.Bytes(), &messages)
	if len(messages) != 1 || messages[0].Content != "hey" {
		t.Fatalf("after: %+v", messages)
	}
	// Carol cannot read it.
	w = jsonRequest(r, http.MethodGet, "/chats/friend/1/messages", "carol", "")
	assertAPIError(t, w, http.StatusForbidden, apierror.NotFriends, "Arkadaş değilsiniz")
}

func TestCallingAFriendLeavesAnInvitation(t *testing.T) {
	r := socialTestRouter(t)
	befriend(t, r, "alice", "bob")
	w := jsonRequest(r, http.MethodPost, "/chats/friend/2/call", "alice", "")
	var call struct {
		RoomID string `json:"room_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &call)
	if w.Code != http.StatusCreated || !roomIDPattern.MatchString(call.RoomID) {
		t.Fatalf("call: %d %s", w.Code, w.Body.String())
	}
	var messages []socialMessageResponse
	w = jsonRequest(r, http.MethodGet, "/chats/friend/1/messages", "bob", "")
	_ = json.Unmarshal(w.Body.Bytes(), &messages)
	if len(messages) != 1 || messages[0].RoomID != call.RoomID || messages[0].Sender != "alice" {
		t.Fatalf("invitation: %+v", messages)
	}
}

func TestTeamsMembersAndRoles(t *testing.T) {
	r := socialTestRouter(t)
	befriend(t, r, "alice", "bob")
	befriend(t, r, "bob", "carol")
	w := jsonRequest(r, http.MethodPost, "/teams", "alice", `{"name":"  "}`)
	assertAPIError(t, w, http.StatusBadRequest, apierror.InvalidTeam, "Takım adı 1-40 karakter olmalı")
	w = jsonRequest(r, http.MethodPost, "/teams", "alice", `{"name":"Core"}`)
	var team teamSummary
	_ = json.Unmarshal(w.Body.Bytes(), &team)
	if w.Code != http.StatusCreated || team.Role != models.TeamRoleOwner || team.MemberCount != 1 || team.Color == "" {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	base := fmt.Sprintf("/teams/%d", team.ID)

	// Only friends of the adder join; others do not even see the team.
	w = jsonRequest(r, http.MethodPost, base+"/members", "alice", `{"user_id":3}`)
	assertAPIError(t, w, http.StatusBadRequest, apierror.NotFriends, "Takıma yalnızca arkadaşlarınızı ekleyebilirsiniz")
	w = jsonRequest(r, http.MethodGet, base, "carol", "")
	assertAPIError(t, w, http.StatusNotFound, apierror.TeamNotFound, "Takım bulunamadı")
	if w = jsonRequest(r, http.MethodPost, base+"/members", "alice", `{"user_id":2}`); w.Code != http.StatusNoContent {
		t.Fatalf("add bob: %d %s", w.Code, w.Body.String())
	}
	// A plain member may not add, an admin may: bob adds his friend carol.
	w = jsonRequest(r, http.MethodPost, base+"/members", "bob", `{"user_id":3}`)
	assertAPIError(t, w, http.StatusForbidden, apierror.TeamRoleRequired, "Bu işlem için takım yöneticisi olmalısınız")
	jsonRequest(r, http.MethodPut, base+"/members/2/role", "alice", `{"role":"admin"}`)
	if w = jsonRequest(r, http.MethodPost, base+"/members", "bob", `{"user_id":3}`); w.Code != http.StatusNoContent {
		t.Fatalf("admin adds: %d %s", w.Code, w.Body.String())
	}
	var detail teamDetail
	w = jsonRequest(r, http.MethodGet, base, "carol", "")
	_ = json.Unmarshal(w.Body.Bytes(), &detail)
	if detail.MemberCount != 3 || len(detail.Members) != 3 || detail.Role != models.TeamRoleMember {
		t.Fatalf("detail: %+v", detail)
	}
	// The team chat is the members'.
	if w = jsonRequest(r, http.MethodPost, fmt.Sprintf("/chats/team/%d/messages", team.ID), "carol", `{"content":"hello team"}`); w.Code != http.StatusCreated {
		t.Fatalf("team chat: %d %s", w.Code, w.Body.String())
	}
	// An admin may not remove another admin or the owner.
	w = jsonRequest(r, http.MethodDelete, base+"/members/1", "bob", "")
	assertAPIError(t, w, http.StatusForbidden, apierror.TeamRoleRequired, "Bu işlem için yetkiniz yok")

	// The owner leaves: the admin takes over.
	if w = jsonRequest(r, http.MethodDelete, base+"/members/1", "alice", ""); w.Code != http.StatusNoContent {
		t.Fatalf("leave: %d %s", w.Code, w.Body.String())
	}
	if teams := overviewOf(t, r, "bob").Teams; len(teams) != 1 || teams[0].Role != models.TeamRoleOwner {
		t.Fatalf("bob's teams: %+v", teams)
	}
	if teams := overviewOf(t, r, "alice").Teams; len(teams) != 0 {
		t.Fatalf("alice still in: %+v", teams)
	}
	// The last ones out end the team.
	jsonRequest(r, http.MethodDelete, base+"/members/3", "carol", "")
	jsonRequest(r, http.MethodDelete, base+"/members/2", "bob", "")
	var count int64
	database.DB.Model(&models.Team{}).Count(&count)
	if count != 0 {
		t.Fatalf("%d teams left", count)
	}
}

func TestTeamMeetingStartsOnceAndShowsLive(t *testing.T) {
	r := socialTestRouter(t)
	befriend(t, r, "alice", "bob")
	w := jsonRequest(r, http.MethodPost, "/teams", "alice", `{"name":"Core"}`)
	var team teamSummary
	_ = json.Unmarshal(w.Body.Bytes(), &team)
	base := fmt.Sprintf("/teams/%d", team.ID)
	jsonRequest(r, http.MethodPost, base+"/members", "alice", `{"user_id":2}`)

	start := func(token string) (string, bool) {
		w := jsonRequest(r, http.MethodPost, base+"/meeting", token, "")
		var body struct {
			RoomID  string `json:"room_id"`
			Started bool   `json:"started"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != http.StatusOK || body.RoomID == "" {
			t.Fatalf("start: %d %s", w.Code, w.Body.String())
		}
		return body.RoomID, body.Started
	}
	room, started := start("alice")
	if !started {
		t.Fatal("first start did not start")
	}
	// Before alice is even in, bob gets the same room, with no second
	// invitation.
	again, _ := start("bob")
	if again != room {
		t.Fatalf("second start opened %s, not %s", again, room)
	}
	var messages []socialMessageResponse
	w = jsonRequest(r, http.MethodGet, fmt.Sprintf("/chats/team/%d/messages", team.ID), "bob", "")
	_ = json.Unmarshal(w.Body.Bytes(), &messages)
	if len(messages) != 1 || messages[0].RoomID != room {
		t.Fatalf("invitations: %+v", messages)
	}
	if overviewOf(t, r, "bob").Teams[0].MeetingLive {
		t.Fatal("live before anyone joined")
	}
	markInMeeting(room, "user-1-00000000-0000-4000-8000-000000000000")
	teams := overviewOf(t, r, "bob").Teams
	if !teams[0].MeetingLive || teams[0].RoomID != room {
		t.Fatalf("not live: %+v", teams)
	}
	// Someone there: joining it starts nothing.
	if _, started = start("bob"); started {
		t.Fatal("joining a live meeting started it again")
	}
}

// jsonHas reports whether the response body contains every fragment.
func jsonHas(w *httptest.ResponseRecorder, fragments ...string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(w.Body.String(), fragment) {
			return false
		}
	}
	return true
}

func TestMeetingPresenceFollowsTheWebhook(t *testing.T) {
	useTestDB(t)
	database.DB.Create(&models.Room{RoomID: "111-222-333", Name: "m", Status: models.RoomStatusActive})
	alice := "user-1-00000000-0000-4000-8000-000000000000"
	bob := "user-2-00000000-0000-4000-8000-000000000000"
	markInMeeting("111-222-333", alice)
	markInMeeting("111-222-333", alice)
	markInMeeting("111-222-333", bob)
	markInMeeting("111-222-333", "not-a-user")
	// Bob leaves; LiveKit may still list him for a moment.
	connected := []*livekit.ParticipantInfo{{Identity: alice}, {Identity: bob}}
	recordMeetingPresence("111-222-333", connected, func(p *livekit.ParticipantInfo) bool { return p.Identity == bob })
	in, err := usersInMeeting([]uint{1, 2})
	if err != nil || !in[1] || in[2] {
		t.Fatalf("in meeting: %v %v", in, err)
	}
	// A closed room keeps nobody in a meeting.
	database.DB.Model(&models.Room{}).Where("room_id = ?", "111-222-333").Update("status", models.RoomStatusClosed)
	if in, _ = usersInMeeting([]uint{1}); in[1] {
		t.Fatal("still in a closed room")
	}
}
