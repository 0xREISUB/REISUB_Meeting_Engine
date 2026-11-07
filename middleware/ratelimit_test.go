package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRateLimitPerClientIP(t *testing.T) {
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = time.Now })
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/guest", RateLimit(3, 10*time.Minute), func(c *gin.Context) { c.Status(http.StatusCreated) })
	send := func(ip string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/guest", nil)
		req.RemoteAddr = ip + ":1234"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	for i := 0; i < 3; i++ {
		if w := send("203.0.113.1"); w.Code != http.StatusCreated {
			t.Fatalf("request %d: %d", i, w.Code)
		}
	}
	w := send("203.0.113.1")
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), `"code":"rate_limited"`) || !strings.Contains(w.Body.String(), `"error":"`) || w.Header().Get("Retry-After") != "601" {
		t.Fatalf("over limit: %d %s Retry-After=%s", w.Code, w.Body.String(), w.Header().Get("Retry-After"))
	}
	if w := send("203.0.113.2"); w.Code != http.StatusCreated {
		t.Fatal("another address was limited:", w.Code)
	}
	clock = clock.Add(10 * time.Minute)
	if w := send("203.0.113.1"); w.Code != http.StatusCreated {
		t.Fatal("limit did not reset after the window:", w.Code)
	}
}
