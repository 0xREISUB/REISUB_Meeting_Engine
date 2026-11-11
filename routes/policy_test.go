package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func policyRouter(t *testing.T) *gin.Engine {
	t.Helper()
	t.Setenv("WEB_DIR", "")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	SetupRoutes(r)
	return r
}

func TestPolicyIsPublicAndFollowsGuestsAllowed(t *testing.T) {
	for _, tc := range []struct {
		value, limit, cooldown, body string
	}{
		// The defaults: guests allowed, no time limit, no wait.
		{"", "", "", `{"guests_allowed":true,"meeting_cooldown_minutes":0,"meeting_time_limit_minutes":0}`},
		{"true", "45", "15", `{"guests_allowed":true,"meeting_cooldown_minutes":15,"meeting_time_limit_minutes":45}`},
		{"false", "0", "0", `{"guests_allowed":false,"meeting_cooldown_minutes":0,"meeting_time_limit_minutes":0}`},
		{"", "soon", "-5", `{"guests_allowed":true,"meeting_cooldown_minutes":0,"meeting_time_limit_minutes":0}`},
	} {
		t.Setenv("GUESTS_ALLOWED", tc.value)
		t.Setenv("MEETING_TIME_LIMIT_MINUTES", tc.limit)
		t.Setenv("MEETING_COOLDOWN_MINUTES", tc.cooldown)
		w := httptest.NewRecorder()
		// No Authorization header: the policy is public.
		policyRouter(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/policy", nil))
		if w.Code != 200 || w.Body.String() != tc.body {
			t.Fatalf("GUESTS_ALLOWED=%q: %d %s", tc.value, w.Code, w.Body.String())
		}
	}
}

func TestGuestSessionsRefusedWhenGuestsAreNotAllowed(t *testing.T) {
	t.Setenv("GUESTS_ALLOWED", "false")
	w := httptest.NewRecorder()
	policyRouter(t).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/guest", nil))
	if w.Code != 403 || w.Body.String() != `{"error":"Bu sunucu hesap gerektiriyor; lütfen giriş yapın","code":"guests_not_allowed"}` {
		t.Fatal(w.Code, w.Body.String())
	}
}
