package router

import (
	"net/http"
	"testing"

	"paperMaster/internal/handler"

	"github.com/gin-gonic/gin"
)

// TestReferenceRootRoutesRegisterBesideDownloadRoute guards the route tree:
// /api/references/:referenceID is the root of several existing static
// sub-paths, and Gin panics at registration time if they conflict.
func TestReferenceRootRoutesRegisterBesideDownloadRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/api/references/:referenceID/download-pdf", func(c *gin.Context) {})
	ServerPaperMetadata(engine, nil, handler.NewPaperCitationHandler(nil), nil)

	routes := make(map[string]bool)
	for _, route := range engine.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, expected := range []string{
		http.MethodPatch + " /api/references/:referenceID",
		http.MethodDelete + " /api/references/:referenceID",
		http.MethodPost + " /api/references/:referenceID/resolve",
		http.MethodPost + " /api/references/:referenceID/rematch",
		http.MethodPost + " /api/references/:referenceID/download-pdf",
	} {
		if !routes[expected] {
			t.Fatalf("missing route %q; registered=%v", expected, routes)
		}
	}
}
