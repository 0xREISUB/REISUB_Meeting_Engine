package controllers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/gin-gonic/gin"
)

// meetingServer serves the meeting API with the waiting room on, and /ws,
// over a real listener.
func meetingServer(t *testing.T) (*httptest.Server, *gin.Engine) {
	t.Helper()
	r, _ := waitingRoomRouter(t)
	r.GET("/ws", RealtimeSocket)
	server := httptest.NewServer(r)
	t.Cleanup(func() {
		server.Close()
		realtimeHandlers.Wait()
	})
	return server, r
}

// watch opens a connection watching the room as token and waits until it
// is ready.
func watch(t *testing.T, server *httptest.Server, token, roomID string) *websocket.Conn {
	t.Helper()
	conn := dial(t, server)
	if err := wsjson.Write(context.Background(), conn, map[string]string{"type": "auth", "token": token, "room": roomID}); err != nil {
		t.Fatal(err)
	}
	if event := readEvent(t, conn); event["type"] != "ready" {
		t.Fatalf("first event %v, want ready", event)
	}
	return conn
}

func expectRoomEvent(t *testing.T, conn *websocket.Conn, what string) {
	t.Helper()
	expectEventIn(t, conn, "123-456-789", what)
}

func expectEventIn(t *testing.T, conn *websocket.Conn, roomID, what string) {
	t.Helper()
	event := readEvent(t, conn)
	if event["type"] != "room" || event["room"] != roomID || event["what"] != what {
		t.Fatalf("got %v, want room %s", event, what)
	}
}

func TestMeetingChangesReachThoseWatchingAtOnce(t *testing.T) {
	server, r := meetingServer(t)
	// Guests watch meetings too, though they have no social connection.
	database.DB.Model(&models.User{}).Where("id = 22").Update("is_guest", true)
	owner := watch(t, server, "owner-token", "123-456-789")

	// Someone asks to be let in: the owner sees them at once. Only now,
	// waiting, may they watch.
	requestID := wait(t, r, 22, "Mehmet")
	expectRoomEvent(t, owner, roomState)
	waiter := watch(t, server, "guest-token", "123-456-789")
	// Asking again while waiting changes nothing: the next event is the
	// owner's message.
	wait(t, r, 22, "Mehmet")
	if w := jsonRequest(r, http.MethodPost, "/rooms/123-456-789/messages", "owner-token", `{"content":"bir dakika"}`); w.Code != http.StatusCreated {
		t.Fatal("send:", w.Code, w.Body.String())
	}
	expectRoomEvent(t, owner, roomChat)
	expectRoomEvent(t, waiter, roomChat)

	// Let in: the one waiting hears it at once.
	if w := moderationRequest(r, waitingPath(requestID)+"/admit", "owner-token", `{}`); w.Code != 200 {
		t.Fatal("admit:", w.Code, w.Body.String())
	}
	expectRoomEvent(t, waiter, roomState)
	expectRoomEvent(t, owner, roomState)

	// A chat message.
	if w := jsonRequest(r, http.MethodPost, "/rooms/123-456-789/messages", "guest-token", `{"content":"selam"}`); w.Code != http.StatusCreated {
		t.Fatal("send:", w.Code, w.Body.String())
	}
	expectRoomEvent(t, owner, roomChat)
	expectRoomEvent(t, waiter, roomChat)

	// The waiting room going off.
	if w := settingsRequest(r, "owner-token", false); w.Code != 200 {
		t.Fatal("settings:", w.Code, w.Body.String())
	}
	expectRoomEvent(t, owner, roomState)

	// Another meeting's watchers hear nothing of it.
	database.DB.Create(&models.Room{RoomID: "999-999-999", Name: "other", OwnerID: 11, Status: models.RoomStatusActive})
	database.DB.Create(&models.RoomMember{RoomID: "999-999-999", UserID: 11})
	other := watch(t, server, "owner-token", "999-999-999")
	jsonRequest(r, http.MethodPost, "/rooms/123-456-789/messages", "guest-token", `{"content":"tekrar"}`)
	expectRoomEvent(t, owner, roomChat)
	jsonRequest(r, http.MethodPost, "/rooms/999-999-999/messages", "owner-token", `{"content":"öteki"}`)
	expectEventIn(t, other, "999-999-999", roomChat)
}

// refusedWith asks to watch roomID as token and answers the close code.
func refusedWith(t *testing.T, server *httptest.Server, token, roomID string) websocket.StatusCode {
	t.Helper()
	conn := dial(t, server)
	_ = wsjson.Write(context.Background(), conn, map[string]string{"type": "auth", "token": token, "room": roomID})
	return closedWith(t, conn)
}

func TestWatchingNeedsAMeetingOneIsIn(t *testing.T) {
	server, r := meetingServer(t)
	// A code alone is not enough: an unknown code and a meeting one is not
	// in look the same, so watching tells nothing about which codes exist.
	if code := refusedWith(t, server, "owner-token", "000-000-000"); code != closeRoomGone {
		t.Fatalf("unknown room: closed with %d", code)
	}
	if code := refusedWith(t, server, "guest-token", "123-456-789"); code != closeRoomGone {
		t.Fatalf("not in the room: closed with %d", code)
	}
	// Waiting to be let in is enough.
	requestID := wait(t, r, 22, "Mehmet")
	watch(t, server, "guest-token", "123-456-789").CloseNow()
	// Turned away, they may no longer start watching.
	if w := moderationRequest(r, waitingPath(requestID)+"/reject", "owner-token", `{}`); w.Code != 200 {
		t.Fatal("reject:", w.Code, w.Body.String())
	}
	if code := refusedWith(t, server, "guest-token", "123-456-789"); code != closeRoomGone {
		t.Fatalf("rejected: closed with %d", code)
	}
	// Removed from the meeting: not even its owner's grants help.
	database.DB.Create(&models.RoomBan{RoomID: "123-456-789", UserID: modID})
	if code := refusedWith(t, server, tokenFor(modID), "123-456-789"); code != closeRoomGone {
		t.Fatalf("removed user: closed with %d", code)
	}
	waitForNoWatchers(t, "123-456-789")
}

func waitForNoWatchers(t *testing.T, roomID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for watchers.count(roomID) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("a refused or closed connection is still watching")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRefusedCodesCountWithWrongRoomCodes(t *testing.T) {
	server, _ := meetingServer(t)
	// The same window as wrong codes under /rooms.
	for i := 0; i < 29; i++ {
		if code := refusedWith(t, server, "guest-token", "000-000-000"); code != closeRoomGone {
			t.Fatalf("guess %d: closed with %d", i, code)
		}
	}
	RoomMisses.Add("127.0.0.1") // a 404 from /rooms
	// The window is used up: even the right code is no longer tried, so
	// guessing cannot go on here.
	if code := refusedWith(t, server, "owner-token", "123-456-789"); code != closeRateLimited {
		t.Fatalf("over the limit: closed with %d", code)
	}
	if over, _ := RoomMisses.Exhausted("127.0.0.1"); !over {
		t.Fatal("refused codes were not counted")
	}
}

func TestAScreenShareEndsWithItsHolderLeaving(t *testing.T) {
	r, _ := permissionsTestRouter(t)
	database.DB.Model(&models.Room{}).Where("room_id = ?", "123-456-789").Updates(map[string]any{
		"screen_share_identity": identityFor(11), "screen_share_lease_until": time.Now().Add(time.Hour).Unix(),
	})
	if w := serve(r, signedWebhook(t, ownerLeftEvent(), "test-key", testSecret, false)); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	var room models.Room
	database.DB.Where("room_id = ?", "123-456-789").First(&room)
	if room.ScreenShareIdentity != "" {
		t.Fatalf("share still held by %q", room.ScreenShareIdentity)
	}
}
