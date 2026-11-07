package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestUserDumpEndpointIsNotRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	SetupRoutes(router)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/users", nil)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":"not_found"`) {
		t.Fatalf("GET /users = %d %s, want a JSON 404", response.Code, response.Body.String())
	}
}

func TestPingContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	SetupRoutes(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ping", nil))
	if response.Code != http.StatusOK || response.Body.String() != `{"message":"pong","status":"active"}` {
		t.Fatalf("GET /ping = %d %s", response.Code, response.Body.String())
	}
}
