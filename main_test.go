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
