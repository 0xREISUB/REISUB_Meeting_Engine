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

func TestLimitNotFoundCountsOnlyNotFound(t *testing.T) {
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = time.Now })
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(LimitNotFound(3, 10*time.Minute))
	r.GET("/rooms/:room_id", func(c *gin.Context) {
		if c.Param("room_id") == "known" {
			c.Status(http.StatusOK)
			return
		}
		c.Status(http.StatusNotFound)
	})
	send := func(ip, room string) int {
		req := httptest.NewRequest(http.MethodGet, "/rooms/"+room, nil)
		req.RemoteAddr = ip + ":1234"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	// Using a known room never counts.
	for i := 0; i < 10; i++ {
		if code := send("203.0.113.1", "known"); code != http.StatusOK {
			t.Fatalf("known room %d: %d", i, code)
		}
	}
	for i := 0; i < 3; i++ {
		if code := send("203.0.113.1", "guess"); code != http.StatusNotFound {
			t.Fatalf("guess %d: %d", i, code)
		}
	}
	// The limit is reached: every request from this address waits, a known
	// room included, so a correct guess cannot be told apart.
	for _, room := range []string{"guess", "known"} {
		if code := send("203.0.113.1", room); code != http.StatusTooManyRequests {
			t.Fatalf("%s after the limit: %d", room, code)
		}
	}
	if code := send("203.0.113.2", "guess"); code != http.StatusNotFound {
		t.Fatal("another address was limited:", code)
	}
	clock = clock.Add(10 * time.Minute)
	if code := send("203.0.113.1", "guess"); code != http.StatusNotFound {
		t.Fatal("limit did not reset after the window:", code)
	}
}
