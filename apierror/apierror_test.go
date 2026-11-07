package apierror

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRespondKeepsErrorAndAddsCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Recovery())
	r.GET("/fail", func(c *gin.Context) { Respond(c, http.StatusConflict, ScreenShareBusy, "meşgul") })
	r.GET("/panic", func(*gin.Context) { panic("boom") })
	r.NoRoute(NoRoute)

	for _, tc := range []struct {
		path   string
		status int
		code   Code
	}{
		{"/fail", http.StatusConflict, ScreenShareBusy},
		{"/panic", http.StatusInternalServerError, Internal},
		{"/missing", http.StatusNotFound, NotFound},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: non-JSON body %q", tc.path, w.Body.String())
		}
		if w.Code != tc.status || body["code"] != string(tc.code) {
			t.Fatalf("%s: %d %v, want %d code %s", tc.path, w.Code, body, tc.status, tc.code)
		}
		if msg, ok := body["error"].(string); !ok || msg == "" {
			t.Fatalf("%s: missing error message: %v", tc.path, body)
		}
	}
}

// Every error response must go through this package so it carries a code.
func TestNoHandlerWritesUncodedErrors(t *testing.T) {
	raw := regexp.MustCompile(`gin\.H\{\s*"error"`)
	files, err := filepath.Glob("../*/*.go")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, "../main.go")
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if raw.Match(content) {
			t.Errorf("%s writes an error without a code; use apierror.Respond", file)
		}
	}
}
