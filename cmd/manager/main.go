package main

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"reisub-backend/management"
)

func main() {
	configPath := envOr("MANAGER_CONFIG_PATH", "/manager-config/stack.env")
	adminHashPath := envOr("MANAGER_ADMIN_HASH_PATH", "/manager-data/admin.hash")
	bootstrapPassword, err := readBootstrapPassword(os.Getenv("MANAGER_ADMIN_PASSWORD_FILE"))
	if err != nil {
		log.Fatal("İlk admin parolası okunamadı: ", err)
	}
	compose := management.NewDockerCompose(
		envOr("MANAGER_COMPOSE_FILE", "/deployment/docker-compose.yml"),
		envOr("MANAGER_PROJECT_DIR", "/deployment"),
		envOr("MANAGER_ENV_FILE", "/manager-config/stack.env"),
		func() []string {
			settings, _, err := management.ReadSettings(configPath)
			if err != nil {
				return nil
			}
			return []string{settings.LiveKitAPIKey, settings.LiveKitAPISecret}
		},
	)
	handler, err := management.NewHandler(management.ServerOptions{
		ConfigPath:        configPath,
		AdminHashPath:     adminHashPath,
		BootstrapPassword: bootstrapPassword,
		CookieSecure:      envOr("MANAGER_COOKIE_SECURE", "true") != "false",
		Deployment:        compose,
	})
	if err != nil {
		log.Fatal("Yönetim servisi başlatılamadı: ", err)
	}
	if passwordPath := os.Getenv("MANAGER_ADMIN_PASSWORD_FILE"); passwordPath != "" {
		if err := os.Remove(passwordPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Fatal("İlk admin parola dosyası kaldırılamadı: ", err)
		}
	}
	server := &http.Server{
		Addr:              envOr("MANAGER_ADDR", ":8090"),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("Yönetim servisi %s üzerinde başlatılıyor", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal("Yönetim servisi durdu: ", err)
	}
}

func readBootstrapPassword(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	content, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return string(bytes.TrimSpace(content)), nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
