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
	// Every file is checked again before use: Flutter's file names stay the
	// same from build to build (the icon font, cut down to the icons a
	// build uses, among them), so a cached one could belong to an older
	// build and lack what the new code needs. An unchanged file costs a 304
	// without its body.
	c.Header("Cache-Control", "no-cache")
	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
}
