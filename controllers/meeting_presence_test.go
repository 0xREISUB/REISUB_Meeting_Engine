package controllers

import (
	"strings"
	"testing"
	"time"

	"reisub-backend/database"
	"reisub-backend/models"
)

// listen connects a fake device for userID and answers what it receives.
func listen(userID uint) *realtimeClient {
	client := &realtimeClient{userID: userID, send: make(chan []byte, 16), gone: make(chan struct{})}
	hub.add(client)
	return client
}

func heardSocial(t *testing.T, client *realtimeClient) bool {
	t.Helper()
	for {
		select {
		case data := <-client.send:
			if strings.Contains(string(data), `"social"`) {
				return true
			}
		case <-time.After(200 * time.Millisecond):
			return false
		}
	}
}

func TestLeavingAMeetingIsHeardAtOnce(t *testing.T) {
	r := socialTestRouter(t)
	befriend(t, r, "alice", "bob")
	database.DB.Create(&models.Room{RoomID: "111-222-333", Name: "m", Status: models.RoomStatusActive})
	alice := "user-1-00000000-0000-4000-8000-000000000000"
	markInMeeting("111-222-333", alice)
	bob := listen(2)

	leftMeeting("111-222-333", alice)
	if in, _ := usersInMeeting([]uint{1}); in[1] {
		t.Fatal("still in the meeting")
	}
	if !heardSocial(t, bob) {
		t.Fatal("bob did not hear")
	}
	// Leaving again changes nothing and says nothing.
	leftMeeting("111-222-333", alice)
	if heardSocial(t, bob) {
		t.Fatal("told twice")
	}
}

func TestAnEndedRoomEmptiesEvenWhenClosed(t *testing.T) {
	r := socialTestRouter(t)
	befriend(t, r, "alice", "bob")
	database.DB.Create(&models.Room{RoomID: "111-222-333", Name: "m", Status: models.RoomStatusActive})
	markInMeeting("111-222-333", "user-1-00000000-0000-4000-8000-000000000000")
	// Closed first, as ending a meeting does, then LiveKit reports it.
	database.DB.Model(&models.Room{}).Where("room_id = ?", "111-222-333").Update("status", models.RoomStatusClosed)
	bob := listen(2)

	clearMeetingPresence("111-222-333")
	var rows int64
	database.DB.Model(&models.MeetingPresence{}).Where("room_id = ?", "111-222-333").Count(&rows)
	if rows != 0 {
		t.Fatalf("%d rows left", rows)
	}
	if !heardSocial(t, bob) {
		t.Fatal("bob did not hear")
	}
}

func TestLeavingOnPurposeDoesNotWaitForLiveKit(t *testing.T) {
	r, _ := moderationTestRouter(t)
	markInMeeting("123-456-789", testParticipantIdentity)
	if w := moderationRequest(r, "/rooms/123-456-789/leave", "guest-token", ""); w.Code != 200 {
		t.Fatalf("leave: %d %s", w.Code, w.Body.String())
	}
	if in, _ := usersInMeeting([]uint{22}); in[22] {
		t.Fatal("still in the meeting after leaving")
	}
}
