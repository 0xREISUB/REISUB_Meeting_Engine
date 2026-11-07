package routes

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// webRouter serves a fake Flutter build. It also contains files named like
// API routes to prove the API always wins.
func webRouter(t *testing.T) *gin.Engine {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"index.html":                "app-index",
		"flutter_bootstrap.js":      "bootstrap",
		"main.dart.js":              "main",
		"flutter_service_worker.js": "sw",
		"version.json":              "{}",
		"manifest.json":             "{}",
		"favicon.png":               "png",
		"assets/AssetManifest.bin":  "assets",
		"canvaskit/canvaskit.wasm":  "wasm",
		"invite/index.html":         "invite-page",
		".last_build_id":            "secret",
		"ping":                      "shadow",
		"rooms/join":                "shadow",
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("WEB_DIR", dir)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	SetupRoutes(r)
	return r
}

func get(r *gin.Engine, method, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

func TestWebBuildIsServedWithCacheHeaders(t *testing.T) {
	r := webRouter(t)
	for target, want := range map[string]struct{ body, cache string }{
		"/":                          {"app-index", "no-cache"},
		"/?room=123-456-789":         {"app-index", "no-cache"},
		"/index.html":                {"app-index", "no-cache"},
		"/flutter_bootstrap.js":      {"bootstrap", "no-cache"},
		"/main.dart.js":              {"main", "no-cache"},
		"/flutter_service_worker.js": {"sw", "no-cache"},
		"/version.json":              {"{}", "no-cache"},
		"/manifest.json":             {"{}", "no-cache"},
		"/favicon.png":               {"png", "no-cache"},
		"/assets/AssetManifest.bin":  {"assets", "public, max-age=3600"},
		"/canvaskit/canvaskit.wasm":  {"wasm", "public, max-age=3600"},
		"/invite/":                   {"invite-page", "no-cache"},
		"/invite/?room=123-456-789":  {"invite-page", "no-cache"},
	} {
		w := get(r, http.MethodGet, target)
		if w.Code != 200 || w.Body.String() != want.body || w.Header().Get("Cache-Control") != want.cache {
			t.Errorf("GET %s = %d %q cache %q; want %q cache %q", target, w.Code, w.Body.String(), w.Header().Get("Cache-Control"), want.body, want.cache)
		}
	}
}

func TestShortInviteLinkServesInvitePage(t *testing.T) {
	r := webRouter(t)
	// Invalid codes get the same page, which shows its own error.
	for _, target := range []string{"/j/123-456-789", "/j/not-a-code"} {
		w := get(r, http.MethodGet, target)
		if w.Code != 200 || w.Body.String() != "invite-page" || w.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("GET %s = %d %q", target, w.Code, w.Body.String())
		}
	}
	if w := get(r, http.MethodHead, "/j/123-456-789"); w.Code != 200 {
		t.Errorf("HEAD invite = %d", w.Code)
	}
}

func TestWebBuildNeverShadowsTheAPI(t *testing.T) {
	r := webRouter(t)
	if w := get(r, http.MethodGet, "/ping"); w.Body.String() != `{"message":"pong","status":"active"}` {
		t.Fatalf("GET /ping = %q", w.Body.String())
	}
	if w := get(r, http.MethodGet, "/rooms/123-456-789/state"); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"code":"auth_required"`) {
		t.Fatalf("API route shadowed: %d %s", w.Code, w.Body.String())
	}
	if w := get(r, http.MethodPost, "/rooms/join"); w.Code != http.StatusUnauthorized {
		t.Fatalf("POST /rooms/join = %d %s", w.Code, w.Body.String())
	}
	for _, target := range []string{"/missing.js", "/.last_build_id", "/assets/../.last_build_id", "/%2e%2e/etc/passwd", "/assets/"} {
		if w := get(r, http.MethodGet, target); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"not_found"`) {
			t.Errorf("GET %s = %d %q, want JSON 404", target, w.Code, w.Body.String())
		}
	}
	if w := get(r, http.MethodPost, "/index.html"); w.Code != http.StatusNotFound {
		t.Errorf("POST /index.html = %d", w.Code)
	}
}

func TestWebAppDisabledWithoutWebDir(t *testing.T) {
	t.Setenv("WEB_DIR", "")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	SetupRoutes(r)
	for _, target := range []string{"/", "/j/123-456-789"} {
		if w := get(r, http.MethodGet, target); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"not_found"`) {
			t.Errorf("GET %s without WEB_DIR = %d %s", target, w.Code, w.Body.String())
		}
	}
}
