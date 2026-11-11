package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"reisub-backend/apierror"
	"reisub-backend/controllers"
	"reisub-backend/database"
	"reisub-backend/policy"
	"reisub-backend/routes"

	"github.com/gin-gonic/gin"
)

func main() {
	database.ConnectDB()
	controllers.StartGuestCleanup()
	if _, err := policy.GuestsAllowed(); err != nil {
		log.Printf("warning: %v", err)
	}

	r := gin.New()
	r.Use(gin.Logger(), apierror.Recovery())
	if err := r.SetTrustedProxies(trustedProxies(os.Getenv("TRUSTED_PROXIES"))); err != nil {
		log.Fatal("Güvenilir proxy ayarı yapılandırılamadı: ", err)
	}
	r.Use(hstsMiddleware(os.Getenv("HSTS_MAX_AGE")), corsMiddleware())

	routes.SetupRoutes(r)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	if err := r.Run(":" + port); err != nil {
		log.Fatal(err)
	}
}

// trustedProxies parses TRUSTED_PROXIES, a comma-separated list of IPs or
// CIDRs (e.g. the reverse proxy's Docker network, 172.16.0.0/12). Only
// requests arriving from these addresses may set the client IP through
// X-Forwarded-For/X-Real-IP; empty means none, so the TCP peer is the client.
func trustedProxies(value string) []string {
	var proxies []string
	for _, proxy := range strings.Split(value, ",") {
		if proxy = strings.TrimSpace(proxy); proxy != "" {
			proxies = append(proxies, proxy)
		}
	}
	return proxies
}

// hstsMiddleware sends Strict-Transport-Security on HTTPS responses when
// HSTS_MAX_AGE (seconds) is positive. TLS ends at the reverse proxy, which
// reports the original scheme in X-Forwarded-Proto. Browsers ignore the
// header over plain HTTP, so a forged X-Forwarded-Proto on HTTP is harmless.
// No includeSubDomains or preload: raising the commitment stays a deliberate
// change of HSTS_MAX_AGE.
func hstsMiddleware(maxAge string) gin.HandlerFunc {
	seconds, err := strconv.Atoi(strings.TrimSpace(maxAge))
	if err != nil || seconds <= 0 {
		if strings.TrimSpace(maxAge) != "" {
			log.Printf("warning: invalid HSTS_MAX_AGE=%q, HSTS disabled", maxAge)
		}
		return func(c *gin.Context) { c.Next() }
	}
	value := "max-age=" + strconv.Itoa(seconds)
	return func(c *gin.Context) {
		if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
			c.Header("Strict-Transport-Security", value)
		}
		c.Next()
	}
}

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		isAllowedOrigin := isCorsOriginAllowed(origin)

		if isAllowedOrigin {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
			c.Header("Access-Control-Allow-Private-Network", "true")
		}

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

func isCorsOriginAllowed(origin string) bool {
	if origin == "" {
		return false
	}

	if os.Getenv("CORS_ALLOW_ALL") == "true" {
		return true
	}

	if strings.HasPrefix(origin, "http://localhost:") ||
		strings.HasPrefix(origin, "http://127.0.0.1:") {
		return true
	}

	for _, allowedOrigin := range strings.Split(os.Getenv("CORS_ALLOWED_ORIGINS"), ",") {
		if strings.TrimSpace(allowedOrigin) == origin {
			return true
		}
	}

	return false
}
