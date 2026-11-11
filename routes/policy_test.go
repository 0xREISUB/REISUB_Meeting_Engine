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
		value, body string
	}{
		{"", `{"guests_allowed":true}`}, // the default
		{"true", `{"guests_allowed":true}`},
		{"false", `{"guests_allowed":false}`},
	} {
		t.Setenv("GUESTS_ALLOWED", tc.value)
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
