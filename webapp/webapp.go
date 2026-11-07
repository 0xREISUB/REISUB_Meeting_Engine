// Package webapp serves the Flutter web build (flutter build web) from a
// directory, next to the API on the same origin.
//
// It only answers requests that no API route matched, so it can never
// shadow the API. The app uses hash routing, so there is no SPA fallback:
// unknown paths get the API's JSON 404.
package webapp

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"reisub-backend/apierror"

	"github.com/gin-gonic/gin"
)

// cachedFor is the Cache-Control for assets/ and canvaskit/. Their names are
// not content-hashed, so keep it short; Last-Modified revalidation follows.
const cachedFor = "public, max-age=3600"

// Handler serves files from Dir. A zero Dir disables the web app.
type Handler struct {
	Dir string
}

// NoRoute serves a file of the web build for GET/HEAD, otherwise the JSON 404.
func (h Handler) NoRoute(c *gin.Context) {
	if h.Dir == "" || (c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead) {
		apierror.NoRoute(c)
		return
	}
	h.serve(c, c.Request.URL.Path)
}

// Invite serves the invite landing page for GET /j/:room_id. The page reads
// the room code from the path itself and shows an error for invalid codes.
func (h Handler) Invite(c *gin.Context) {
	if h.Dir == "" {
		apierror.NoRoute(c)
		return
	}
	h.serve(c, "/invite/index.html")
}

func (h Handler) serve(c *gin.Context, urlPath string) {
	clean := path.Clean("/" + urlPath)
	// Never expose dotfiles such as .last_build_id.
	for _, segment := range strings.Split(clean, "/") {
		if strings.HasPrefix(segment, ".") {
			apierror.NoRoute(c)
			return
		}
	}
	name := filepath.Join(h.Dir, filepath.FromSlash(clean))
	info, err := os.Stat(name)
	if err == nil && info.IsDir() {
		name = filepath.Join(name, "index.html")
		info, err = os.Stat(name)
	}
	if err != nil || info.IsDir() {
		apierror.NoRoute(c)
		return
	}
	file, err := os.Open(name)
	if err != nil {
		apierror.NoRoute(c)
		return
	}
	defer file.Close()
	c.Header("Cache-Control", cacheControl(clean))
	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
}

// cacheControl makes everything revalidate except the large assets/ and
// canvaskit/ trees. index.html, flutter_bootstrap.js, main.dart.js,
// flutter_service_worker.js, version.json and manifest.json must never be
// served stale after a deploy.
func cacheControl(clean string) string {
	if strings.HasPrefix(clean, "/assets/") || strings.HasPrefix(clean, "/canvaskit/") {
		return cachedFor
	}
	return "no-cache"
}
