package controllers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"reisub-backend/database"
	"reisub-backend/middleware"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
)

// callServer is the social API with calls, push and /ws over a real
// listener.
func callServer(t *testing.T) (*httptest.Server, *gin.Engine, *fakePush) {
	t.Helper()
	r := socialTestRouter(t)
	g := r.Group("", middleware.RequireAuth(), middleware.RequireAccount())
	g.PUT("/me/push-device", PutPushDevice)
	r.POST("/calls/:call_id/answer", AnswerCall)
	r.POST("/calls/:call_id/decline", DeclineCall)
	r.GET("/ws", RealtimeSocket)
	fake := usePush(t)
	server := httptest.NewServer(r)
	t.Cleanup(func() {
		server.Close()
		realtimeHandlers.Wait()
		callsMu.Lock()
		for id, timer := range callTimers {
			timer.Stop()
			delete(callTimers, id)
		}
		callsMu.Unlock()
	})
	return server, r, fake
}

func callOf(t *testing.T, event map[string]any) callEvent {
	t.Helper()
	var call callEvent
	raw, _ := json.Marshal(event["call"])
	if err := json.Unmarshal(raw, &call); err != nil {
		t.Fatal(err)
	}
	return call
}

func ring(t *testing.T, r *gin.Engine, caller string, friendID uint) {
	t.Helper()
	if w := jsonRequest(r, http.MethodPost, fmt.Sprintf("/chats/friend/%d/call", friendID), caller, ""); w.Code != http.StatusCreated {
		t.Fatal("call:", w.Code, w.Body.String())
	}
}

// pushesOfType waits for the sends and answers those of one type.
func (f *fakePush) ofType(kind string) []map[string]string {
	pushSends.Wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	var found []map[string]string
	for _, m := range f.sent {
		if m.Data["type"] == kind {
			found = append(found, m.Data)
		}
	}
	return found
}

func TestACallRingsAndStopsWhenAnswered(t *testing.T) {
	server, r, fake := callServer(t)
	befriend(t, r, "alice", "bob")
	registerPhone(t, r, "bob", "bob-phone")
	bobComputer := connect(t, server, "bob")

	ring(t, r, "alice", 2)
	call := callOf(t, nextOfType(t, bobComputer, "call"))
	if call.Caller != "alice" || call.Kind != "friend" || call.TargetID != 1 || call.Secret == "" || call.RoomID == "" {
		t.Fatalf("call %+v", call)
	}
	// The phone rings too, with the same secret.
	pushed := fake.ofType("call")
	if len(pushed) != 1 || pushed[0]["secret"] != call.Secret || pushed[0]["call_id"] != fmt.Sprint(call.ID) {
		t.Fatalf("pushed %+v", pushed)
	}

	// Answered on the computer: the phone stops, and the call is over.
	w := jsonRequest(r, http.MethodPost, fmt.Sprintf("/calls/%d/answer", call.ID), "bob", "")
	if w.Code != http.StatusOK || !jsonHas(w, call.RoomID, models.CallAnswered) {
		t.Fatal("answer:", w.Code, w.Body.String())
	}
	if end := callOf(t, nextOfType(t, bobComputer, "call_end")); end.ID != call.ID || end.Missed {
		t.Fatalf("end %+v", end)
	}
	if ends := fake.ofType("call_end"); len(ends) != 1 || ends[0]["missed"] != "" {
		t.Fatalf("phone told %+v", ends)
	}
	var stored models.Call
	database.DB.First(&stored, call.ID)
	if stored.EndedAt == nil {
		t.Fatal("answered call still rings")
	}
}

func TestDecliningFromThePhoneTellsTheCaller(t *testing.T) {
	server, r, fake := callServer(t)
	befriend(t, r, "alice", "bob")
	registerPhone(t, r, "bob", "bob-phone")
	alice := connect(t, server, "alice")
	ring(t, r, "alice", 2)
	pushed := fake.ofType("call")
	if len(pushed) != 1 {
		t.Fatalf("pushed %+v", pushed)
	}
	path := "/calls/" + pushed[0]["call_id"] + "/decline"

	// From the notification, without the app: only the secret works.
	if w := jsonRequest(r, http.MethodPost, path, "", `{"secret":"wrong"}`); w.Code != http.StatusNotFound {
		t.Fatal("wrong secret:", w.Code)
	}
	if w := jsonRequest(r, http.MethodPost, path, "", `{}`); w.Code != http.StatusUnauthorized {
		t.Fatal("no secret, no session:", w.Code)
	}
	if w := jsonRequest(r, http.MethodPost, path, "", `{"secret":"`+pushed[0]["secret"]+`"}`); w.Code != http.StatusOK {
		t.Fatal("decline:", w.Code, w.Body.String())
	}
	if declined := callOf(t, nextOfType(t, alice, "call_declined")); declined.By != "bob" {
		t.Fatalf("declined %+v", declined)
	}
	// Carol, who was not called, cannot answer it.
	befriend(t, r, "carol", "alice")
	if w := jsonRequest(r, http.MethodPost, path, "carol", ""); w.Code != http.StatusNotFound {
		t.Fatal("carol:", w.Code)
	}
}

func TestAnUnansweredCallBecomesMissed(t *testing.T) {
	old := callRingTime
	callRingTime = 200 * time.Millisecond
	t.Cleanup(func() { callRingTime = old })
	server, r, fake := callServer(t)
	befriend(t, r, "alice", "bob")
	registerPhone(t, r, "bob", "bob-phone")
	alice := connect(t, server, "alice")
	bob := connect(t, server, "bob")
	ring(t, r, "alice", 2)
	nextOfType(t, bob, "call")
	if end := callOf(t, nextOfType(t, bob, "call_end")); !end.Missed || end.Caller != "alice" {
		t.Fatalf("end %+v", end)
	}
	nextOfType(t, alice, "call_unanswered")
	if ends := fake.ofType("call_end"); len(ends) != 1 || ends[0]["missed"] != "1" || ends[0]["title"] != "alice" {
		t.Fatalf("phone told %+v", ends)
	}
	var invitee models.CallInvitee
	database.DB.First(&invitee)
	if invitee.State != models.CallMissed {
		t.Fatal(invitee.State)
	}
}

func TestATeamMeetingRingsEveryoneUntilTheyAnswer(t *testing.T) {
	server, r, _ := callServer(t)
	befriend(t, r, "alice", "bob")
	befriend(t, r, "alice", "carol")
	w := jsonRequest(r, http.MethodPost, "/teams", "alice", `{"name":"Core"}`)
	var team teamSummary
	_ = json.Unmarshal(w.Body.Bytes(), &team)
	base := fmt.Sprintf("/teams/%d", team.ID)
	jsonRequest(r, http.MethodPost, base+"/members", "alice", `{"user_id":2}`)
	jsonRequest(r, http.MethodPost, base+"/members", "alice", `{"user_id":3}`)
	bob, carol := connect(t, server, "bob"), connect(t, server, "carol")

	if w := jsonRequest(r, http.MethodPost, base+"/meeting", "alice", ""); w.Code != http.StatusOK {
		t.Fatal("start:", w.Code, w.Body.String())
	}
	call := callOf(t, nextOfType(t, bob, "call"))
	if call.Kind != "team" || call.Title != "Core" || call.TargetID != team.ID || call.Caller != "alice" {
		t.Fatalf("call %+v", call)
	}
	nextOfType(t, carol, "call")

	// Bob answers; carol's phone goes on ringing.
	jsonRequest(r, http.MethodPost, fmt.Sprintf("/calls/%d/answer", call.ID), "bob", "")
	nextOfType(t, bob, "call_end")
	var ringing int64
	database.DB.Model(&models.CallInvitee{}).Where("state = ?", models.CallRinging).Count(&ringing)
	if ringing != 1 {
		t.Fatalf("%d still ringing", ringing)
	}

	// Alice leaves before carol answers: carol's stops, missed.
	endCallsBy(call.RoomID, 1)
	if end := callOf(t, nextOfType(t, carol, "call_end")); !end.Missed || end.Title != "Core" {
		t.Fatalf("carol %+v", end)
	}
}
