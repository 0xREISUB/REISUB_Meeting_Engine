package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
)

type pendingTimer struct {
	after time.Duration
	fire  func()
}

// captureTimers records scheduled room ends instead of starting timers.
func captureTimers(t *testing.T) *[]pendingTimer {
	t.Helper()
	timers := &[]pendingTimer{}
	old := afterFunc
	afterFunc = func(d time.Duration, f func()) *time.Timer {
		*timers = append(*timers, pendingTimer{d, f})
		return nil
	}
	t.Cleanup(func() { afterFunc = old })
	return timers
}

type timedBody struct {
	RoomID      string  `json:"room_id"`
	Status      string  `json:"status"`
	SecondsLeft *int64  `json:"seconds_left"`
	EndsAt      *string `json:"ends_at"`
}

func readTimed(t *testing.T, w *httptest.ResponseRecorder) timedBody {
	t.Helper()
	var body timedBody
	if w.Code/100 != 2 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return body
}

// assertTimeLeft checks seconds_left against wantSeconds (allowing for the
// test's own run time) and that ends_at is RFC 3339 UTC.
func assertTimeLeft(t *testing.T, what string, body timedBody, wantSeconds int64) {
	t.Helper()
	if body.SecondsLeft == nil || body.EndsAt == nil {
		t.Fatalf("%s: no time left reported", what)
	}
	if *body.SecondsLeft > wantSeconds || *body.SecondsLeft < wantSeconds-5 {
		t.Fatalf("%s: seconds_left=%d, want about %d", what, *body.SecondsLeft, wantSeconds)
	}
	endsAt, err := time.Parse(time.RFC3339, *body.EndsAt)
	if err != nil || !strings.HasSuffix(*body.EndsAt, "Z") || time.Until(endsAt) > time.Duration(wantSeconds+1)*time.Second {
		t.Fatalf("%s: ends_at=%q (%v)", what, *body.EndsAt, err)
	}
}

func createRoom(t *testing.T, r *gin.Engine, token string) timedBody {
	t.Helper()
	w := jsonRequest(r, http.MethodPost, "/rooms", token, `{"name":"x"}`)
	if w.Code != http.StatusCreated {
		t.Fatal("create:", w.Code, w.Body.String())
	}
	return readTimed(t, w)
}

func TestNoTimeLimitByDefault(t *testing.T) {
	r, _ := moderationTestRouter(t)
	timers := captureTimers(t)
	created := jsonRequest(r, http.MethodPost, "/rooms", "owner-token", `{"name":"x"}`)
	if created.Code != 201 || strings.Contains(created.Body.String(), "seconds_left") || strings.Contains(created.Body.String(), "ends_at") {
		t.Fatal(created.Code, created.Body.String())
	}
	room := readTimed(t, created).RoomID
	if w := jsonRequest(r, http.MethodGet, "/rooms/"+room+"/state", "owner-token", ""); strings.Contains(w.Body.String(), "seconds_left") {
		t.Fatal("state reports a limit:", w.Body.String())
	}
	if len(*timers) != 0 {
		t.Fatal("a room without a limit was scheduled to end")
	}
	// Ending a room without a limit never holds anyone back.
	moderationRequest(r, "/rooms/"+room+"/close", "owner-token", `{}`)
	if w := jsonRequest(r, http.MethodPost, "/rooms", "owner-token", `{"name":"x"}`); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestLimitedRoomReportsTimeLeftEverywhere(t *testing.T) {
	r, _ := moderationTestRouter(t)
	timers := captureTimers(t)
	t.Setenv("MEETING_TIME_LIMIT_MINUTES", "30")
	created := createRoom(t, r, "owner-token")
	assertTimeLeft(t, "create", created, 1800)
	if len(*timers) != 1 || (*timers)[0].after > 30*time.Minute || (*timers)[0].after < 29*time.Minute {
		t.Fatalf("end not scheduled for the limit: %+v", *timers)
	}
	base := "/rooms/" + created.RoomID
	assertTimeLeft(t, "join", readTimed(t, jsonRequest(r, http.MethodPost, "/rooms/join", "guest-token", `{"room_id":"`+created.RoomID+`","name":"x"}`)), 1800)
	assertTimeLeft(t, "state", readTimed(t, jsonRequest(r, http.MethodGet, base+"/state", "guest-token", "")), 1800)

	// The waiting room's "admitted" answer carries it too.
	addMember(t, &fakeModeration{}, 77, "", time.Time{}, false)
	if w := jsonRequest(r, http.MethodPut, base+"/settings", "owner-token", `{"waiting_room":true}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var waiting struct {
		RequestID string `json:"request_id"`
	}
	json.Unmarshal(jsonRequest(r, http.MethodPost, "/rooms/join", tokenFor(77), `{"room_id":"`+created.RoomID+`","name":"x"}`).Body.Bytes(), &waiting)
	moderationRequest(r, base+"/waiting/"+waiting.RequestID+"/admit", "owner-token", `{}`)
	admitted := readTimed(t, jsonRequest(r, http.MethodGet, base+"/waiting/"+waiting.RequestID, tokenFor(77), ""))
	if admitted.Status != "admitted" {
		t.Fatal("not admitted:", admitted.Status)
	}
	assertTimeLeft(t, "admitted", admitted, 1800)

	// A room created before the limit was set keeps having none.
	if w := jsonRequest(r, http.MethodPost, "/rooms/join", "guest-token", `{"room_id":"123-456-789","name":"x"}`); strings.Contains(w.Body.String(), "seconds_left") {
		t.Fatal("old room got a limit:", w.Body.String())
	}
}

// expire moves a room's end into the past.
func expire(t *testing.T, roomID string) time.Time {
	t.Helper()
	endsAt := time.Now().Add(-time.Second)
	if err := database.DB.Model(&models.Room{}).Where("room_id = ?", roomID).Update("ends_at", endsAt).Error; err != nil {
		t.Fatal(err)
	}
	return endsAt
}

func TestRoomEndsAtItsLimitAndHoldsBackCreatorAndOwner(t *testing.T) {
	r, f := moderationTestRouter(t)
	timers := captureTimers(t)
	t.Setenv("MEETING_TIME_LIMIT_MINUTES", "30")
	t.Setenv("MEETING_COOLDOWN_MINUTES", "10")
	addMember(t, f, modID, "", time.Time{}, false)
	room := createRoom(t, r, "owner-token").RoomID
	// The guest joins and becomes the owner before the end.
	joined := jsonRequest(r, http.MethodPost, "/rooms/join", "guest-token", `{"room_id":"`+room+`","name":"x"}`)
	guestSession := connectWith(t, f, joined.Body.Bytes())
	if w := moderationRequest(r, "/rooms/"+room+"/transfer-ownership", "owner-token", `{"identity":"`+guestSession.Identity+`"}`); w.Code != 200 {
		t.Fatal("transfer:", w.Code, w.Body.String())
	}
	expire(t, room)
	// Joining after the end, before the timer runs, is refused.
	assertAPIError(t, jsonRequest(r, http.MethodPost, "/rooms/join", tokenFor(modID), `{"room_id":"`+room+`","name":"x"}`), 404, apierror.RoomNotFound, "Oda bulunamadı")
	(*timers)[0].fire()

	var ended models.Room
	database.DB.Where("room_id = ?", room).First(&ended)
	if ended.Status != models.RoomStatusClosed || !ended.EndedAtLimit || f.deletes != 1 {
		t.Fatalf("room not ended at its limit: %+v deletes=%d", ended, f.deletes)
	}
	for _, token := range []string{"owner-token", "guest-token"} {
		w := jsonRequest(r, http.MethodPost, "/rooms", token, `{"name":"x"}`)
		var body struct {
			Code       string `json:"code"`
			Error      string `json:"error"`
			RetryAfter int64  `json:"retry_after_seconds"`
		}
		json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != http.StatusTooManyRequests || body.Code != string(apierror.MeetingCooldown) || body.Error == "" ||
			body.RetryAfter < 595 || body.RetryAfter > 600 || w.Header().Get("Retry-After") != strconv.FormatInt(body.RetryAfter, 10) {
			t.Fatalf("%s: %d %s Retry-After=%s", token, w.Code, w.Body.String(), w.Header().Get("Retry-After"))
		}
	}
	// Joining other meetings stays allowed, and others may create rooms.
	if w := jsonRequest(r, http.MethodPost, "/rooms/join", "owner-token", `{"room_id":"123-456-789","name":"x"}`); w.Code != 200 {
		t.Fatal("held-back user could not join:", w.Code, w.Body.String())
	}
	if w := jsonRequest(r, http.MethodPost, "/rooms", tokenFor(modID), `{"name":"x"}`); w.Code != 201 {
		t.Fatal("bystander held back:", w.Code, w.Body.String())
	}
	// The wait ends.
	database.DB.Model(&models.MeetingCooldown{}).Where("1 = 1").Update("until", time.Now().Add(-time.Second))
	if w := jsonRequest(r, http.MethodPost, "/rooms", "owner-token", `{"name":"x"}`); w.Code != 201 {
		t.Fatal("still held back after the wait:", w.Code, w.Body.String())
	}
}

func TestRoomsEndedOtherwiseStartNoWait(t *testing.T) {
	r, f := moderationTestRouter(t)
	timers := captureTimers(t)
	t.Setenv("MEETING_TIME_LIMIT_MINUTES", "30")
	t.Setenv("MEETING_COOLDOWN_MINUTES", "10")
	room := createRoom(t, r, "owner-token").RoomID
	moderationRequest(r, "/rooms/"+room+"/close", "owner-token", `{}`)
	expire(t, room)
	(*timers)[0].fire() // the timer still fires later
	var cooldowns int64
	database.DB.Model(&models.MeetingCooldown{}).Count(&cooldowns)
	if cooldowns != 0 || f.deletes != 1 {
		t.Fatalf("a closed room started a wait (%d) or was deleted again (%d)", cooldowns, f.deletes)
	}
	if w := jsonRequest(r, http.MethodPost, "/rooms", "owner-token", `{"name":"x"}`); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestFailedEndIsRetried(t *testing.T) {
	r, f := moderationTestRouter(t)
	timers := captureTimers(t)
	t.Setenv("MEETING_TIME_LIMIT_MINUTES", "30")
	room := createRoom(t, r, "owner-token").RoomID
	expire(t, room)
	f.deleteError = errors.New("network unavailable")
	(*timers)[0].fire()
	if len(*timers) != 2 || (*timers)[1].after != endRetry {
		t.Fatalf("no retry scheduled: %+v", *timers)
	}
	f.deleteError = nil
	(*timers)[1].fire()
	var ended models.Room
	database.DB.Where("room_id = ?", room).First(&ended)
	if ended.Status != models.RoomStatusClosed || !ended.EndedAtLimit {
		t.Fatalf("retry did not end the room: %+v", ended)
	}
}

func TestStartupEndsOverdueRoomsAndSchedulesTheRest(t *testing.T) {
	_, f := moderationTestRouter(t)
	timers := captureTimers(t)
	t.Setenv("MEETING_COOLDOWN_MINUTES", "10")
	f.participants = nil
	past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	for _, room := range []models.Room{
		{RoomID: "111-111-111", Name: "overdue", OwnerID: 11, Status: models.RoomStatusActive, EndsAt: &past},
		{RoomID: "222-222-222", Name: "later", OwnerID: 11, Status: models.RoomStatusActive, EndsAt: &future},
		{RoomID: "333-333-333", Name: "closed", OwnerID: 11, Status: models.RoomStatusClosed, EndsAt: &past},
	} {
		if err := database.DB.Create(&room).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := ScheduleRoomEnds(); err != nil {
		t.Fatal(err)
	}
	if len(*timers) != 2 || (*timers)[0].after > 0 || (*timers)[1].after < 59*time.Minute {
		t.Fatalf("unexpected schedule: %+v", *timers)
	}
	for _, timer := range *timers {
		if timer.after <= 0 {
			timer.fire() // time.AfterFunc runs these at once
		}
	}
	status := func(roomID string) string {
		var room models.Room
		database.DB.Where("room_id = ?", roomID).First(&room)
		return room.Status
	}
	if status("111-111-111") != models.RoomStatusClosed || status("222-222-222") != models.RoomStatusActive {
		t.Fatal("startup did not end the overdue room or ended the other")
	}
	// The wait counts from the end time, not from the restart.
	left, _ := cooldownLeft(11)
	if left > 0 {
		t.Fatalf("wait extended by the restart: %v left", left)
	}
}
