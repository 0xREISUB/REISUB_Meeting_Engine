package controllers

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reisub-backend/apierror"
	"reisub-backend/middleware"
)

func pngOf(width, height int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width; x++ {
		for y := 0; y < height; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	var out bytes.Buffer
	_ = png.Encode(&out, img)
	return out.Bytes()
}

func uploadAvatar(r http.Handler, token string, data []byte) *httptest.ResponseRecorder {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, _ := form.CreateFormFile("avatar", "me.png")
	_, _ = part.Write(data)
	_ = form.Close()
	req := httptest.NewRequest(http.MethodPut, "/me/avatar", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestProfilePictures(t *testing.T) {
	r := socialTestRouter(t)
	dir := t.TempDir()
	t.Setenv("AVATAR_DIR", dir)
	g := r.Group("", middleware.RequireAuth(), middleware.RequireAccount())
	g.PUT("/me/avatar", UploadAvatar)
	g.DELETE("/me/avatar", DeleteAvatar)
	r.GET("/avatars/:name", ServeAvatar)

	w := uploadAvatar(r, "alice", []byte("not a picture"))
	assertAPIError(t, w, http.StatusBadRequest, apierror.InvalidImage, "Resim JPEG ya da PNG olmalı")

	// A wide picture becomes a square of avatarSize.
	w = uploadAvatar(r, "alice", pngOf(600, 300))
	var profile profileResponse
	_ = json.Unmarshal(w.Body.Bytes(), &profile)
	if w.Code != http.StatusOK || !strings.HasPrefix(profile.AvatarURL, "/avatars/1-") {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	w = jsonRequest(r, http.MethodGet, profile.AvatarURL, "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("serve: %d %v", w.Code, w.Header())
	}
	config, err := jpeg.DecodeConfig(w.Body)
	if err != nil || config.Width != avatarSize || config.Height != avatarSize {
		t.Fatalf("stored picture: %+v %v", config, err)
	}

	// Friends see it.
	befriend(t, r, "alice", "bob")
	if got := overviewOf(t, r, "bob").Friends[0].AvatarURL; got != profile.AvatarURL {
		t.Fatalf("friend's avatar_url = %q", got)
	}

	// A new picture replaces the old one, whose file goes.
	first := profile.AvatarURL
	w = uploadAvatar(r, "alice", pngOf(100, 100))
	_ = json.Unmarshal(w.Body.Bytes(), &profile)
	if profile.AvatarURL == first {
		t.Fatal("same name for a new picture")
	}
	if _, err := os.Stat(filepath.Join(dir, strings.TrimPrefix(first, "/avatars/"))); !os.IsNotExist(err) {
		t.Fatalf("old picture kept: %v", err)
	}
	// Small pictures stay their size.
	w = jsonRequest(r, http.MethodGet, profile.AvatarURL, "", "")
	if config, _ = jpeg.DecodeConfig(w.Body); config.Width != 100 {
		t.Fatalf("small picture scaled to %d", config.Width)
	}

	w = jsonRequest(r, http.MethodDelete, "/me/avatar", "alice", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"avatar_url":""`) {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("files left: %v", entries)
	}
	for _, name := range []string{"../reisub.db", "1-zz.jpg"} {
		if w = jsonRequest(r, http.MethodGet, "/avatars/"+name, "", ""); w.Code != http.StatusNotFound {
			t.Fatalf("%s served: %d", name, w.Code)
		}
	}
	// Guests have no profile picture.
	w = uploadAvatar(r, "guest", pngOf(10, 10))
	assertAPIError(t, w, http.StatusForbidden, apierror.AccountRequired, "Bu özellik için giriş yapmalısınız")
}
