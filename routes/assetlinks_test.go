package routes

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

func assetLinksRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	SetupRoutes(r)
	return r
}

type statement struct {
	Relation []string `json:"relation"`
	Target   struct {
		Namespace    string   `json:"namespace"`
		PackageName  string   `json:"package_name"`
		Fingerprints []string `json:"sha256_cert_fingerprints"`
	} `json:"target"`
}

func TestAssetLinksDefaults(t *testing.T) {
	t.Setenv("ANDROID_PACKAGE_NAME", "")
	t.Setenv("ANDROID_CERT_FINGERPRINTS", "")
	t.Setenv("WEB_DIR", "")
	r := assetLinksRouter(t)
	// No Authorization header: the endpoint is public.
	w := get(r, http.MethodGet, "/.well-known/assetlinks.json")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Location") != "" {
		t.Fatalf("GET = %d, Content-Type %q, Location %q", w.Code, w.Header().Get("Content-Type"), w.Header().Get("Location"))
	}
	want := `[{"relation":["delegate_permission/common.handle_all_urls"],"target":{"namespace":"android_app","package_name":"xyz.reisub.meeting","sha256_cert_fingerprints":["62:CE:B3:E9:79:E2:8D:86:FD:73:42:2C:2C:AA:65:30:AD:31:72:31:DA:1F:08:91:B0:AE:6A:36:9A:15:A7:6C"]}}]`
	if w.Body.String() != want {
		t.Fatalf("body:\n%s\nwant:\n%s", w.Body.String(), want)
	}
	if w := get(r, http.MethodHead, "/.well-known/assetlinks.json"); w.Code != http.StatusOK {
		t.Fatalf("HEAD = %d", w.Code)
	}
}

func TestAssetLinksUsesConfiguredValues(t *testing.T) {
	t.Setenv("ANDROID_PACKAGE_NAME", " com.example.meeting ")
	t.Setenv("ANDROID_CERT_FINGERPRINTS", "aa:bb , CC:DD,")
	r := assetLinksRouter(t)
	w := get(r, http.MethodGet, "/.well-known/assetlinks.json")
	var statements []statement
	if err := json.Unmarshal(w.Body.Bytes(), &statements); err != nil || len(statements) != 1 {
		t.Fatalf("invalid JSON %q: %v", w.Body.String(), err)
	}
	got := statements[0]
	if got.Target.Namespace != "android_app" || got.Target.PackageName != "com.example.meeting" ||
		len(got.Target.Fingerprints) != 2 || got.Target.Fingerprints[0] != "AA:BB" || got.Target.Fingerprints[1] != "CC:DD" ||
		len(got.Relation) != 1 || got.Relation[0] != "delegate_permission/common.handle_all_urls" {
		t.Fatalf("unexpected statement: %+v", got)
	}
}

// With a web build configured, the explicit route still wins over the
// static handler, which refuses dotfile paths.
func TestAssetLinksServedAlongsideWebBuild(t *testing.T) {
	r := webRouter(t)
	if w := get(r, http.MethodGet, "/.well-known/assetlinks.json"); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("GET with WEB_DIR = %d %s", w.Code, w.Body.String())
	}
	if w := get(r, http.MethodGet, "/j/123-456-789"); w.Code != http.StatusOK {
		t.Fatalf("invite link broke: %d", w.Code)
	}
}
