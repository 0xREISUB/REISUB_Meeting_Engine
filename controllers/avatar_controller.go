package controllers

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // PNG profile pictures
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"reisub-backend/apierror"
	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/gin-gonic/gin"
	"golang.org/x/image/draw"
)

const (
	// maxAvatarBytes is the largest upload taken; clients send far less.
	maxAvatarBytes = 8 << 20
	// maxAvatarPixels guards against images that are small files but huge
	// when decoded.
	maxAvatarPixels = 40_000_000
	// avatarSize is the side of the square picture kept.
	avatarSize = 256
)

var avatarNamePattern = regexp.MustCompile(`^[0-9]+-[0-9a-f]{16}\.jpg$`)

// avatarDir is where profile pictures are kept: AVATAR_DIR, else an
// "avatars" directory beside the database, so they live on its volume.
func avatarDir() string {
	if dir := os.Getenv("AVATAR_DIR"); dir != "" {
		return dir
	}
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "reisub.db"
	}
	return filepath.Join(filepath.Dir(dbPath), "avatars")
}

// avatarURL is where clients load a picture from, relative to the server;
// empty without one.
func avatarURL(file string) string {
	if file == "" {
		return ""
	}
	return "/avatars/" + file
}

var errImageTooLarge = errors.New("image too large")

// squareAvatar decodes a JPEG or PNG, crops its middle square and scales
// it to avatarSize, re-encoding it as a JPEG without any metadata.
func squareAvatar(data []byte) ([]byte, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width*config.Height > maxAvatarPixels {
		return nil, errImageTooLarge
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	side := min(b.Dx(), b.Dy())
	crop := image.Rect(0, 0, side, side).Add(image.Pt(b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2))
	size := min(side, avatarSize)
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	// Transparent parts become the app's dark background.
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.RGBA{R: 0x11, G: 0x0F, B: 0x16, A: 0xFF}}, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, crop, draw.Over, nil)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 88}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// UploadAvatar sets the profile picture from the multipart field "avatar".
func UploadAvatar(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAvatarBytes+64<<10)
	header, err := c.FormFile("avatar")
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			apierror.Respond(c, http.StatusRequestEntityTooLarge, apierror.ImageTooLarge, "Resim en fazla 8 MB olabilir")
			return
		}
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidImage, "Resim gerekli")
		return
	}
	if header.Size > maxAvatarBytes {
		apierror.Respond(c, http.StatusRequestEntityTooLarge, apierror.ImageTooLarge, "Resim en fazla 8 MB olabilir")
		return
	}
	file, err := header.Open()
	if err != nil {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidImage, "Resim okunamadı")
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, maxAvatarBytes+1))
	file.Close()
	if err != nil {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidImage, "Resim okunamadı")
		return
	}
	picture, err := squareAvatar(data)
	if errors.Is(err, errImageTooLarge) {
		apierror.Respond(c, http.StatusRequestEntityTooLarge, apierror.ImageTooLarge, "Resim çok büyük")
		return
	}
	if err != nil {
		apierror.Respond(c, http.StatusBadRequest, apierror.InvalidImage, "Resim JPEG ya da PNG olmalı")
		return
	}

	user := c.MustGet("user").(models.User)
	random := make([]byte, 8)
	_, _ = rand.Read(random)
	name := fmt.Sprintf("%d-%s.jpg", user.ID, hex.EncodeToString(random))
	dir := avatarDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Resim kaydedilemedi")
		return
	}
	// Written whole, then moved into place, so no half picture is served.
	tmp := filepath.Join(dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, picture, 0o640); err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Resim kaydedilemedi")
		return
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmp)
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Resim kaydedilemedi")
		return
	}
	if err := database.DB.Model(&models.User{}).Where("id = ?", user.ID).Update("avatar_file", name).Error; err != nil {
		_ = os.Remove(filepath.Join(dir, name))
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Resim kaydedilemedi")
		return
	}
	removeAvatarFile(user.AvatarFile)
	user.AvatarFile = name
	socialChanged(append(contactsOf(user.ID), user.ID)...)
	c.JSON(http.StatusOK, makeProfile(user))
}

// DeleteAvatar takes the profile picture away.
func DeleteAvatar(c *gin.Context) {
	user := c.MustGet("user").(models.User)
	if err := database.DB.Model(&models.User{}).Where("id = ?", user.ID).Update("avatar_file", "").Error; err != nil {
		apierror.Respond(c, http.StatusInternalServerError, apierror.Internal, "Resim silinemedi")
		return
	}
	removeAvatarFile(user.AvatarFile)
	user.AvatarFile = ""
	socialChanged(append(contactsOf(user.ID), user.ID)...)
	c.JSON(http.StatusOK, makeProfile(user))
}

func removeAvatarFile(name string) {
	if !avatarNamePattern.MatchString(name) {
		return
	}
	if err := os.Remove(filepath.Join(avatarDir(), name)); err != nil && !os.IsNotExist(err) {
		log.Printf("warning: could not remove avatar %s: %v", name, err)
	}
}

// ServeAvatar answers a profile picture. Its name changes with every
// upload, so it may be cached for good.
func ServeAvatar(c *gin.Context) {
	name := c.Param("name")
	if !avatarNamePattern.MatchString(name) {
		apierror.Respond(c, http.StatusNotFound, apierror.NotFound, "Resim bulunamadı")
		return
	}
	path := filepath.Join(avatarDir(), name)
	if _, err := os.Stat(path); err != nil {
		apierror.Respond(c, http.StatusNotFound, apierror.NotFound, "Resim bulunamadı")
		return
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("X-Content-Type-Options", "nosniff")
	c.File(path)
}
