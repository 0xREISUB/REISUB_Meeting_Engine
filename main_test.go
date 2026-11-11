package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestClientIPTrustsOnlyConfiguredProxies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := r.SetTrustedProxies(trustedProxies(" 172.16.0.0/12 , ")); err != nil {
		t.Fatal(err)
	}
	r.GET("/ip", func(c *gin.Context) { c.String(http.StatusOK, c.ClientIP()) })
	ip := func(remote, forwarded string) string {
		req := httptest.NewRequest(http.MethodGet, "/ip", nil)
		req.RemoteAddr = remote + ":5555"
		req.Header.Set("X-Forwarded-For", forwarded)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Body.String()
	}
	// Through the reverse proxy: the address it appended is the client.
	if got := ip("172.18.0.1", "198.51.100.9, 203.0.113.7"); got != "203.0.113.7" {
		t.Fatalf("via proxy: %s", got)
	}
	// Directly on the published port: a forged header is ignored.
	if got := ip("203.0.113.50", "198.51.100.9"); got != "203.0.113.50" {
		t.Fatalf("direct: %s", got)
	}
	if trustedProxies("") != nil {
		t.Fatal("empty TRUSTED_PROXIES must trust nobody")
	}
}

func TestHSTSOnlyOnHTTPSAndOnlyWhenConfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)
	header := func(maxAge, proto string) string {
		r := gin.New()
		r.Use(hstsMiddleware(maxAge))
		r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if proto != "" {
			req.Header.Set("X-Forwarded-Proto", proto)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Header().Get("Strict-Transport-Security")
	}
	if got := header("300", "https"); got != "max-age=300" {
		t.Fatalf("HTTPS: %q", got)
	}
	if got := header("300", "http"); got != "" {
		t.Fatalf("HTTP got HSTS: %q", got)
	}
	for _, off := range []string{"", "0", "-5", "abc"} {
		if got := header(off, "https"); got != "" {
			t.Fatalf("HSTS_MAX_AGE=%q sent %q", off, got)
		}
	}
}

func TestPolicyIsAllowedThroughCORSLikePing(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://meeting.example")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(corsMiddleware())
	r.GET("/policy", func(c *gin.Context) { c.Status(http.StatusOK) })
	for _, method := range []string{http.MethodGet, http.MethodOptions} {
		req := httptest.NewRequest(method, "/policy", nil)
		req.Header.Set("Origin", "https://meeting.example")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Header().Get("Access-Control-Allow-Origin") != "https://meeting.example" {
			t.Fatalf("%s /policy: no CORS header", method)
		}
	}
}
