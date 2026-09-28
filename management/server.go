package management

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const maxRequestBytes = 32 * 1024

//go:embed dashboard.html login.html
var pages embed.FS

type Deployment interface {
	Status(context.Context) ([]ServiceStatus, error)
	Action(context.Context, string, string) error
	Logs(context.Context, string) (string, error)
	Recreate(context.Context, ...string) error
}

type ServerOptions struct {
	ConfigPath        string
	AdminHashPath     string
	BootstrapPassword string
	CookieSecure      bool
	Deployment        Deployment
}

type Server struct {
	configPath    string
	adminHashPath string
	admin         *adminAuth
	deployment    Deployment
	templates     *template.Template
}

func NewHandler(options ServerOptions) (http.Handler, error) {
	if options.ConfigPath == "" || options.AdminHashPath == "" || options.Deployment == nil {
		return nil, errors.New("Yönetim sunucusu ayarları eksik")
	}
	if err := os.MkdirAll(filepathDir(options.AdminHashPath), 0700); err != nil {
		return nil, err
	}
	admin, err := newAdminAuth(options.AdminHashPath, options.BootstrapPassword, options.CookieSecure)
	if err != nil {
		return nil, err
	}
	if err := removeBootstrapPassword(options.ConfigPath); err != nil {
		return nil, err
	}
	parsedTemplates, err := template.ParseFS(pages, "dashboard.html", "login.html")
	if err != nil {
		return nil, err
	}
	server := &Server{
		configPath:    options.ConfigPath,
		adminHashPath: options.AdminHashPath,
		admin:         admin,
		deployment:    options.Deployment,
		templates:     parsedTemplates,
	}

	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery(), securityHeaders())
	router.GET("/_health", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	router.GET("/", server.index)
	router.POST("/login", server.login)
	router.POST("/logout", server.requireAdmin(), server.requireCSRF(), server.logout)

	api := router.Group("/api", server.requireAdmin())
	api.GET("/settings", server.getSettings)
	api.POST("/settings", server.requireCSRF(), server.updateSettings)
	api.GET("/status", server.getStatus)
	api.GET("/logs", server.getLogs)
	api.POST("/services/:service/:action", server.requireCSRF(), server.serviceAction)
	api.POST("/password", server.requireCSRF(), server.changePassword)

	return router, nil
}

func (server *Server) index(c *gin.Context) {
	if _, ok := server.currentSession(c); !ok {
		c.Header("Cache-Control", "no-store")
		c.Header("Content-Type", "text/html; charset=utf-8")
		if err := server.templates.ExecuteTemplate(c.Writer, "login.html", map[string]bool{"Invalid": false}); err != nil {
			c.Status(http.StatusInternalServerError)
		}
		return
	}
	session, _ := server.currentSession(c)
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := server.templates.ExecuteTemplate(c.Writer, "dashboard.html", map[string]string{"CSRF": session.csrf}); err != nil {
		c.Status(http.StatusInternalServerError)
	}
}

func (server *Server) login(c *gin.Context) {
	client := clientAddress(c.Request.RemoteAddr)
	if !server.admin.allowLogin(client) {
		c.String(http.StatusTooManyRequests, "Çok fazla giriş denemesi. Bir dakika sonra tekrar deneyin.")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBytes)
	if err := c.Request.ParseForm(); err != nil || !server.admin.verifyPassword(c.PostForm("password")) {
		c.Header("Cache-Control", "no-store")
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Writer.WriteHeader(http.StatusUnauthorized)
		_ = server.templates.ExecuteTemplate(c.Writer, "login.html", map[string]bool{"Invalid": true})
		return
	}
	sessionID, _, err := server.admin.newSession()
	if err != nil {
		c.String(http.StatusInternalServerError, "Oturum oluşturulamadı")
		return
	}
	server.admin.clearLoginAttempts(client)
	http.SetCookie(c.Writer, &http.Cookie{
		Name: "reisub_manager_session", Value: sessionID, Path: "/",
		HttpOnly: true, Secure: server.admin.cookieSecure, SameSite: http.SameSiteStrictMode,
		Expires: time.Now().Add(8 * time.Hour), MaxAge: 8 * 60 * 60,
	})
	c.Redirect(http.StatusSeeOther, "/")
}

func (server *Server) logout(c *gin.Context) {
	if cookie, err := c.Cookie("reisub_manager_session"); err == nil {
		server.admin.deleteSession(cookie)
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: "reisub_manager_session", Value: "", Path: "/", HttpOnly: true,
		Secure: server.admin.cookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
	c.Status(http.StatusNoContent)
}

func (server *Server) getSettings(c *gin.Context) {
	settings, _, err := ReadSettings(server.configPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ayarlar okunamadı"})
		return
	}
	c.JSON(http.StatusOK, settings.View())
}

func (server *Server) updateSettings(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var requested Settings
	if err := decoder.Decode(&requested); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ayar isteği geçersiz"})
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ayar isteğinde fazladan veri var"})
		return
	}

	current, oldValues, err := ReadSettings(server.configPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Mevcut ayarlar okunamadı"})
		return
	}
	if requested.LiveKitAPIKey == "" {
		requested.LiveKitAPIKey = current.LiveKitAPIKey
	}
	if requested.LiveKitAPISecret == "" {
		requested.LiveKitAPISecret = current.LiveKitAPISecret
	}
	if err := ValidateSettings(requested); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if requested == current {
		c.JSON(http.StatusOK, gin.H{"updated": false, "recreated": []string{}})
		return
	}

	oldContent, err := os.ReadFile(server.configPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Config yedeklenemedi"})
		return
	}
	statuses, err := server.deployment.Status(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Servis durumu alınamadı: " + err.Error()})
		return
	}
	affected := affectedServices(current, requested)
	running := runningServices(statuses, affected)
	if err := WriteSettings(server.configPath, oldValues, requested); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ayarlar kaydedilemedi"})
		return
	}
	if len(running) == 0 {
		c.JSON(http.StatusOK, gin.H{"updated": true, "recreated": []string{}})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 75*time.Second)
	defer cancel()
	updateErr := server.deployment.Recreate(ctx, running...)
	if updateErr == nil {
		updateErr = waitForReady(ctx, server.deployment, running)
	}
	if updateErr != nil {
		rollbackErr := atomicWrite(server.configPath, oldContent, 0600)
		if rollbackErr == nil {
			rollbackContext, rollbackCancel := context.WithTimeout(context.Background(), 45*time.Second)
			rollbackErr = server.deployment.Recreate(rollbackContext, running...)
			rollbackCancel()
		}
		message := "Ayar değişikliğinden sonra servis doğrulanamadı; önceki ayarlara dönüldü"
		if rollbackErr != nil {
			message += "; servis geri alma işlemi de başarısız oldu"
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": message})
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": true, "recreated": running})
}

func (server *Server) getStatus(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	statuses, err := server.deployment.Status(ctx)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"services": statuses})
}

func (server *Server) getLogs(c *gin.Context) {
	service := c.Query("service")
	if err := validateService(service); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Servis seçimi geçersiz"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	logs, err := server.deployment.Logs(ctx, service)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"service": service, "logs": logs})
}

func (server *Server) serviceAction(c *gin.Context) {
	service := c.Param("service")
	action := c.Param("action")
	if err := validateService(service); err != nil || (action != "start" && action != "stop" && action != "restart") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Servis işlemi geçersiz"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	if err := server.deployment.Action(ctx, service, action); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"service": service, "action": action})
}

func (server *Server) changePassword(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBytes)
	var request struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Parola isteği geçersiz"})
		return
	}
	if err := server.admin.changePassword(request.CurrentPassword, request.NewPassword, server.adminHashPath); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"changed": true})
}

func (server *Server) requireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		current, ok := server.currentSession(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Oturum gerekli"})
			return
		}
		c.Set("csrf", current.csrf)
		c.Next()
	}
}

func (server *Server) requireCSRF() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin, err := url.Parse(c.GetHeader("Origin"))
		if err != nil || origin.Host == "" || !strings.EqualFold(origin.Host, c.Request.Host) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "İstek kaynağı doğrulanamadı"})
			return
		}
		expected, _ := c.Get("csrf")
		if !equalToken(expected.(string), c.GetHeader("X-CSRF-Token")) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "CSRF doğrulaması başarısız"})
			return
		}
		c.Next()
	}
}

func (server *Server) currentSession(c *gin.Context) (session, bool) {
	cookie, err := c.Cookie("reisub_manager_session")
	if err != nil {
		return session{}, false
	}
	return server.admin.getSession(cookie)
}

func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; form-action 'self'")
		c.Next()
	}
}

func affectedServices(before, after Settings) []string {
	if before.LiveKitAPIKey != after.LiveKitAPIKey || before.LiveKitAPISecret != after.LiveKitAPISecret {
		return []string{"backend", "livekit"}
	}
	return []string{"backend"}
}

func runningServices(statuses []ServiceStatus, affected []string) []string {
	running := make([]string, 0, len(affected))
	for _, service := range affected {
		for _, status := range statuses {
			if status.Name == service && status.State == "running" {
				running = append(running, service)
				break
			}
		}
	}
	return running
}

func waitForReady(ctx context.Context, deployment Deployment, services []string) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		statuses, err := deployment.Status(ctx)
		if err == nil && allServicesReady(statuses, services) {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("Servisler zamanında sağlıklı duruma geçmedi")
		case <-ticker.C:
		}
	}
}

func allServicesReady(statuses []ServiceStatus, services []string) bool {
	for _, service := range services {
		found := false
		for _, status := range statuses {
			if status.Name == service {
				found = status.State == "running" && status.Health != "unhealthy" && status.Health != "starting"
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func clientAddress(remote string) string {
	address, _, err := net.SplitHostPort(remote)
	if err != nil {
		return remote
	}
	return address
}

func filepathDir(path string) string {
	index := strings.LastIndex(path, "/")
	if index < 0 {
		return "."
	}
	if index == 0 {
		return "/"
	}
	return path[:index]
}
