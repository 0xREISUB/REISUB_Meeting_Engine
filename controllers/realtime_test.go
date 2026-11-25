package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/gin-gonic/gin"
)

// realtimeServer serves the social API and /ws over a real listener.
func realtimeServer(t *testing.T) (*httptest.Server, *gin.Engine) {
	t.Helper()
	r := socialTestRouter(t)
	r.GET("/ws", RealtimeSocket)
	server := httptest.NewServer(r)
	// After the connections close, and before the test database goes.
	t.Cleanup(func() {
		server.Close()
		realtimeHandlers.Wait()
	})
	return server, r
}

// connect opens a realtime connection as token and waits until it is
// ready.
func connect(t *testing.T, server *httptest.Server, token string) *websocket.Conn {
	t.Helper()
	conn := dial(t, server)
	if err := wsjson.Write(context.Background(), conn, map[string]string{"type": "auth", "token": token}); err != nil {
		t.Fatal(err)
	}
	if event := readEvent(t, conn); event["type"] != "ready" {
		t.Fatalf("first event %v, want ready", event)
	}
	return conn
}

func dial(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func readEvent(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var event map[string]any
	if err := wsjson.Read(ctx, conn, &event); err != nil {
		t.Fatal(err)
	}
	return event
}

// nextOfType skips other events until one of type comes.
func nextOfType(t *testing.T, conn *websocket.Conn, kind string) map[string]any {
	t.Helper()
	for {
		if event := readEvent(t, conn); event["type"] == kind {
			return event
		}
	}
}

func closedWith(t *testing.T, conn *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := conn.Read(ctx)
	var closeErr websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("not closed: %v", err)
	}
	return closeErr.Code
}

func TestRealtimeTurnsAwayWhoeverIsNotAnAccount(t *testing.T) {
	server, _ := realtimeServer(t)
	conn := dial(t, server)
	_ = wsjson.Write(context.Background(), conn, map[string]string{"type": "hello"})
	if code := closedWith(t, conn); code != closeAuthRequired {
		t.Fatalf("no auth: closed with %d", code)
	}
	conn = dial(t, server)
	_ = wsjson.Write(context.Background(), conn, map[string]string{"type": "auth", "token": "nobody"})
	if code := closedWith(t, conn); code != closeInvalidToken {
		t.Fatalf("bad token: closed with %d", code)
	}
	conn = dial(t, server)
	_ = wsjson.Write(context.Background(), conn, map[string]string{"type": "auth", "token": "guest"})
	if code := closedWith(t, conn); code != closeAccountRequired {
		t.Fatalf("guest: closed with %d", code)
	}
}

func TestRealtimeDeliversMessagesAtOnce(t *testing.T) {
	server, r := realtimeServer(t)
	befriend(t, r, "alice", "bob")
	bob := connect(t, server, "bob")
	aliceOther := connect(t, server, "alice")

	w := jsonRequest(r, http.MethodPost, "/chats/friend/2/messages", "alice", `{"content":"anında"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("send: %d %s", w.Code, w.Body.String())
	}
	event := nextOfType(t, bob, "message")
	var message notificationMessage
	raw, _ := json.Marshal(event["message"])
	_ = json.Unmarshal(raw, &message)
	if message.Content != "anında" || message.IsMine || message.Kind != "friend" || message.TargetID != 1 || message.Title != "alice" {
		t.Fatalf("bob got %+v", message)
	}
	// Alice's other device gets it too, as hers.
	raw, _ = json.Marshal(nextOfType(t, aliceOther, "message")["message"])
	_ = json.Unmarshal(raw, &message)
	if !message.IsMine || message.TargetID != 2 || message.Title != "bob" {
		t.Fatalf("alice's other device got %+v", message)
	}
}

func TestRealtimeSignalsSocialChangesAndPresence(t *testing.T) {
	server, r := realtimeServer(t)
	bob := connect(t, server, "bob")
	// A request reaches bob as a change to read again.
	jsonRequest(r, http.MethodPost, "/friends/requests", "alice", `{"nick":"bob"}`)
	nextOfType(t, bob, "social")
	requests := overviewOf(t, r, "bob").Requests
	if len(requests) != 1 {
		t.Fatalf("requests: %+v", requests)
	}

	befriend(t, r, "carol", "bob")
	// Bob, long quiet but connected, is online for carol.
	old := time.Now().Add(-time.Hour)
	database.DB.Model(&models.User{}).Where("id = 2").UpdateColumn("active_at", old)
	if p := overviewOf(t, r, "carol").Friends[0].Presence; p != presenceOnline {
		t.Fatalf("connected bob is %s", p)
	}
	// Carol, connected, hears when he goes; he is offline, last seen now.
	carol := connect(t, server, "carol")
	bob.Close(websocket.StatusNormalClosure, "")
	nextOfType(t, carol, "social")
	friend := overviewOf(t, r, "carol").Friends[0]
	if friend.Presence != presenceOffline || friend.LastSeenSeconds == nil || *friend.LastSeenSeconds > 5 {
		t.Fatalf("bob after leaving: %+v", friend)
	}
}
