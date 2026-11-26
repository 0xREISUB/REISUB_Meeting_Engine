// Package push sends notifications to phones through Firebase Cloud
// Messaging (FCM).
package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"golang.org/x/oauth2/google"
)

// ErrUnregistered means the device token is no longer valid: the app was
// removed or its data cleared. It should be forgotten.
var ErrUnregistered = errors.New("push: device token no longer registered")

// Message is one notification to one device. Data is all it carries; the
// app builds what it shows from it.
type Message struct {
	Token string
	Data  map[string]string
}

// Sender delivers messages.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// fcmScope is what the service account may do: send messages.
const fcmScope = "https://www.googleapis.com/auth/firebase.messaging"

// FCM sends through the FCM HTTP v1 API as a service account.
type FCM struct {
	client   *http.Client
	endpoint string
}

// NewFCMFromFile reads a service account key (JSON, from the Firebase
// console) and returns a sender for its project.
func NewFCMFromFile(ctx context.Context, path string) (*FCM, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("push: read credentials: %w", err)
	}
	var key struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(data, &key); err != nil || key.ProjectID == "" {
		return nil, errors.New("push: credentials name no project_id")
	}
	creds, err := google.CredentialsFromJSONWithType(ctx, data, google.ServiceAccount, fcmScope)
	if err != nil {
		return nil, fmt.Errorf("push: credentials: %w", err)
	}
	client := oauth2Client(ctx, creds)
	return &FCM{
		client:   client,
		endpoint: "https://fcm.googleapis.com/v1/projects/" + key.ProjectID + "/messages:send",
	}, nil
}

// ttl is how long FCM keeps a message for a phone that is off: a message
// notice older than this is not worth showing.
const ttl = time.Hour

// Send delivers m at high priority, so it wakes a sleeping phone.
func (f *FCM) Send(ctx context.Context, m Message) error {
	body, err := json.Marshal(map[string]any{"message": map[string]any{
		"token": m.Token,
		"data":  m.Data,
		"android": map[string]any{
			"priority": "high",
			"ttl":      fmt.Sprintf("%ds", int(ttl.Seconds())),
		},
	}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("push: send: %w", err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if unregistered(resp.StatusCode, answer) {
		return ErrUnregistered
	}
	return fmt.Errorf("push: FCM answered %d", resp.StatusCode)
}

// unregistered tells a dead token from other errors: 404, or UNREGISTERED
// in the error details.
func unregistered(status int, body []byte) bool {
	if status == http.StatusNotFound {
		return true
	}
	var answer struct {
		Error struct {
			Status  string `json:"status"`
			Details []struct {
				ErrorCode string `json:"errorCode"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &answer) != nil {
		return false
	}
	for _, d := range answer.Error.Details {
		if d.ErrorCode == "UNREGISTERED" {
			return true
		}
	}
	return false
}
