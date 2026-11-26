package controllers

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"reisub-backend/database"
	"reisub-backend/middleware"
	"reisub-backend/models"
	"reisub-backend/push"

	"github.com/gin-gonic/gin"
)

// fakePush records what would go to FCM; tokens in dead answer
// ErrUnregistered.
type fakePush struct {
	mu   sync.Mutex
	sent []push.Message
	dead map[string]bool
}

func (f *fakePush) Send(_ context.Context, m push.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	if f.dead[m.Token] {
		return push.ErrUnregistered
	}
	return nil
}

func (f *fakePush) tokens() []string {
	pushSends.Wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	var tokens []string
	for _, m := range f.sent {
		tokens = append(tokens, m.Token)
	}
	f.sent = nil
	return tokens
}

func usePush(t *testing.T) *fakePush {
	t.Helper()
	fake := &fakePush{dead: map[string]bool{}}
	pushSender = fake
	startPushWorkers()
	t.Cleanup(func() {
		pushSends.Wait()
		pushSender = nil
	})
	return fake
}

// pushRouter is the social router with the push device endpoints.
func pushRouter(t *testing.T) (*fakePush, *gin.Engine) {
	t.Helper()
	r := socialTestRouter(t)
	g := r.Group("", middleware.RequireAuth(), middleware.RequireAccount())
	g.PUT("/me/push-device", PutPushDevice)
	g.DELETE("/me/push-device", DeletePushDevice)
	return usePush(t), r
}

func registerPhone(t *testing.T, r *gin.Engine, token, device string) {
	t.Helper()
	if w := jsonRequest(r, http.MethodPut, "/me/push-device", token, `{"token":"`+device+`","platform":"android"}`); w.Code != http.StatusNoContent {
		t.Fatal("register phone:", w.Code, w.Body.String())
	}
}

func TestANewMessageReachesTheOtherSidesPhones(t *testing.T) {
	fake, r := pushRouter(t)
	befriend(t, r, "alice", "bob")
	registerPhone(t, r, "bob", "bob-phone")
	registerPhone(t, r, "alice", "alice-phone")
	if w := jsonRequest(r, http.MethodPost, "/chats/friend/2/messages", "alice", `{"content":"gizli metin"}`); w.Code != http.StatusCreated {
		t.Fatal("send:", w.Code, w.Body.String())
	}
	pushSends.Wait()
	fake.mu.Lock()
	sent := fake.sent
	fake.sent = nil
	fake.mu.Unlock()
	// Bob's phone, not the sender's.
	if len(sent) != 1 || sent[0].Token != "bob-phone" {
		t.Fatalf("sent %+v", sent)
	}
	data := sent[0].Data
	if data["type"] != "message" || data["kind"] != "friend" || data["target_id"] != "1" || data["title"] != "alice" || data["sender"] != "alice" || data["message_id"] == "" || data["invite"] != "" {
		t.Fatalf("data %+v", data)
	}
	// What was written never goes to Google.
	for _, value := range data {
		if strings.Contains(value, "gizli") {
			t.Fatal("the message text was pushed")
		}
	}
	// A call says so.
	if w := jsonRequest(r, http.MethodPost, "/chats/friend/2/call", "alice", ""); w.Code >= 300 {
		t.Fatal("call:", w.Code, w.Body.String())
	}
	pushSends.Wait()
	fake.mu.Lock()
	if len(fake.sent) != 1 || fake.sent[0].Data["invite"] != "1" {
		t.Fatalf("call pushed %+v", fake.sent)
	}
	fake.sent = nil
	fake.mu.Unlock()
}

func TestOnlySignedInPhonesGetPushes(t *testing.T) {
	fake, r := pushRouter(t)
	befriend(t, r, "alice", "bob")
	registerPhone(t, r, "bob", "bob-phone")
	send := func() {
		t.Helper()
		if w := jsonRequest(r, http.MethodPost, "/chats/friend/2/messages", "alice", `{"content":"selam"}`); w.Code != http.StatusCreated {
			t.Fatal("send:", w.Code, w.Body.String())
		}
	}

	// Stopped on the device: nothing more.
	if w := jsonRequest(r, http.MethodDelete, "/me/push-device", "bob", ""); w.Code != http.StatusNoContent {
		t.Fatal("delete:", w.Code)
	}
	send()
	if got := fake.tokens(); len(got) != 0 {
		t.Fatalf("stopped phone got %v", got)
	}

	// A session that ended takes its phone with it.
	registerPhone(t, r, "bob", "bob-phone")
	database.DB.Model(&models.Session{}).Where("user_id = 2").UpdateColumn("expires_at", time.Now().Add(-time.Minute))
	send()
	if got := fake.tokens(); len(got) != 0 {
		t.Fatalf("signed-out phone got %v", got)
	}
	database.DB.Model(&models.Session{}).Where("user_id = 2").UpdateColumn("expires_at", nil)

	// A token FCM no longer knows is forgotten.
	fake.mu.Lock()
	fake.dead["bob-phone"] = true
	fake.mu.Unlock()
	send()
	if got := fake.tokens(); len(got) != 1 {
		t.Fatalf("sent %v", got)
	}
	var left int64
	database.DB.Model(&models.PushDevice{}).Count(&left)
	if left != 0 {
		t.Fatal("a dead token was kept")
	}
}

func TestAPhoneTokenBelongsToOneSession(t *testing.T) {
	fake, r := pushRouter(t)
	befriend(t, r, "alice", "bob")
	befriend(t, r, "carol", "bob")
	// The phone was alice's, then carol signs in on it.
	registerPhone(t, r, "alice", "shared-phone")
	registerPhone(t, r, "carol", "shared-phone")
	jsonRequest(r, http.MethodPost, "/chats/friend/1/messages", "bob", `{"content":"alice'e"}`)
	if got := fake.tokens(); len(got) != 0 {
		t.Fatalf("alice's message went to the phone carol now uses: %v", got)
	}
	jsonRequest(r, http.MethodPost, "/chats/friend/3/messages", "bob", `{"content":"carol'a"}`)
	if got := fake.tokens(); len(got) != 1 || got[0] != "shared-phone" {
		t.Fatalf("carol's phone got %v", got)
	}
	// Only Android has push.
	if w := jsonRequest(r, http.MethodPut, "/me/push-device", "bob", `{"token":"x","platform":"ios"}`); w.Code != http.StatusBadRequest {
		t.Fatal("ios:", w.Code)
	}
}
