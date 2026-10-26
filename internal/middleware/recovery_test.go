package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"paperMaster/internal/middleware"

	"github.com/gin-gonic/gin"
)

// TestRecoveryHandlesPanicWithoutRequestIDContextValue verifies that a panicking
// handler yields a 500 response without leaking the panic value or requiring a
// request ID in the context.
func TestRecoveryHandlesPanicWithoutRequestIDContextValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID(), middleware.Recovery())
	router.GET("/panic", func(_ *gin.Context) {
		panic("test panic")
	})

	request := httptest.NewRequest(http.MethodGet, "/panic", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "test panic") {
		t.Fatalf("panic value leaked to client: %s", response.Body.String())
	}
}
