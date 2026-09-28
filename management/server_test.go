package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testDeployment struct{}

func (testDeployment) Status(context.Context) ([]ServiceStatus, error) {
	return []ServiceStatus{{Name: "backend", State: "running", Health: "healthy"}, {Name: "livekit", State: "running"}}, nil
}
func (testDeployment) Action(context.Context, string, string) error { return nil }
func (testDeployment) Logs(context.Context, string) (string, error) { return "test logs", nil }
func (testDeployment) Recreate(context.Context, ...string) error    { return nil }

func TestManagementLoginMasksSecretsAndRequiresCSRF(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "stack.env")
	config := strings.Join([]string{
		"LIVEKIT_API_KEY=devkey",
		"LIVEKIT_API_SECRET=devsecret",
		"LIVEKIT_PUBLIC_URL=wss://rtc.example.test",
		"PORT=8080",
		"CORS_ALLOWED_ORIGINS=https://meeting.example.test",
		"MANAGER_ADMIN_PASSWORD=initial-admin-password-123",
	}, "\n") + "\n"
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(ServerOptions{
		ConfigPath:        configPath,
		AdminHashPath:     filepath.Join(directory, "data", "admin.hash"),
		BootstrapPassword: "initial-admin-password-123",
		Deployment:        testDeployment{},
	})
	if err != nil {
		t.Fatal(err)
	}

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://manager.example.test/", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Yönetim paneli") {
		t.Fatalf("login page response = %d, %s", page.Code, page.Body.String())
	}

	loginForm := url.Values{"password": {"initial-admin-password-123"}}
	loginRequest := httptest.NewRequest(http.MethodPost, "http://manager.example.test/login", strings.NewReader(loginForm.Encode()))
	loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusSeeOther {
		t.Fatalf("login response = %d, %s", loginResponse.Code, loginResponse.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, cookie := range loginResponse.Result().Cookies() {
		if cookie.Name == "reisub_manager_session" {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil {
		t.Fatal("login did not set a session cookie")
	}

	settingsRequest := httptest.NewRequest(http.MethodGet, "http://manager.example.test/api/settings", nil)
	settingsRequest.AddCookie(sessionCookie)
	settingsResponse := httptest.NewRecorder()
	handler.ServeHTTP(settingsResponse, settingsRequest)
	if settingsResponse.Code != http.StatusOK {
		t.Fatalf("settings response = %d, %s", settingsResponse.Code, settingsResponse.Body.String())
	}
	if strings.Contains(settingsResponse.Body.String(), "devsecret") || strings.Contains(settingsResponse.Body.String(), "devkey") {
		t.Fatalf("settings API exposed a credential: %s", settingsResponse.Body.String())
	}

	actionRequest := httptest.NewRequest(http.MethodPost, "http://manager.example.test/api/services/backend/stop", nil)
	actionRequest.AddCookie(sessionCookie)
	actionResponse := httptest.NewRecorder()
	handler.ServeHTTP(actionResponse, actionRequest)
	if actionResponse.Code != http.StatusForbidden {
		t.Fatalf("state change without Origin/CSRF returned %d", actionResponse.Code)
	}

	bootstrap, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bootstrap), "MANAGER_ADMIN_PASSWORD") {
		t.Fatal("bootstrap password was not removed from runtime config")
	}
}
