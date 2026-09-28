package routes

import (
	"net/http"
	"net/http/httptest"
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
	if response.Code != http.StatusNotFound {
		t.Fatalf("GET /users status = %d, want %d", response.Code, http.StatusNotFound)
	}
}
