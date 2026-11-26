package push

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendAsksFCMAtHighPriorityWithDataOnly(t *testing.T) {
	var got map[string]map[string]any
	answer := http.StatusOK
	body := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(answer)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	f := &FCM{client: server.Client(), endpoint: server.URL}

	if err := f.Send(context.Background(), Message{Token: "phone", Data: map[string]string{"type": "message"}}); err != nil {
		t.Fatal(err)
	}
	m := got["message"]
	android := m["android"].(map[string]any)
	if m["token"] != "phone" || m["data"].(map[string]any)["type"] != "message" || m["notification"] != nil || android["priority"] != "high" || android["ttl"] != "3600s" {
		t.Fatalf("sent %+v", got)
	}

	// A dead token, either way FCM says it.
	answer, body = http.StatusNotFound, `{}`
	if err := f.Send(context.Background(), Message{Token: "gone"}); !errors.Is(err, ErrUnregistered) {
		t.Fatalf("404: %v", err)
	}
	answer, body = http.StatusBadRequest, `{"error":{"status":"INVALID_ARGUMENT","details":[{"errorCode":"UNREGISTERED"}]}}`
	if err := f.Send(context.Background(), Message{Token: "gone"}); !errors.Is(err, ErrUnregistered) {
		t.Fatalf("UNREGISTERED: %v", err)
	}
	// Anything else is a passing failure; the token stays.
	answer, body = http.StatusServiceUnavailable, `{}`
	if err := f.Send(context.Background(), Message{Token: "phone"}); err == nil || errors.Is(err, ErrUnregistered) {
		t.Fatalf("503: %v", err)
	}
}
