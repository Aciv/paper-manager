package router

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"os"
	"paperMaster/internal/handler"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Consolidated from router_test.go.
// ---------------------------------------------------------------------------

// TestCORSOrigins verifies that the CORS origin list is trimmed and that an
// empty value falls back to the local development origin.
func TestCORSOrigins(t *testing.T) {
	if got, want := corsOrigins(" https://one.example,https://two.example "), []string{"https://one.example", "https://two.example"}; !reflect.DeepEqual(got, want) {
		t.Errorf("corsOrigins() = %#v, want %#v", got, want)
	}
	if got, want := corsOrigins(" , "), []string{"http://localhost:5173"}; !reflect.DeepEqual(got, want) {
		t.Errorf("corsOrigins() fallback = %#v, want %#v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from desktop_router_test.go.
// ---------------------------------------------------------------------------

// TestDesktopFrontendRouting verifies SPA fallback, asset MIME and cache
// headers, API/MCP passthrough, traversal rejection, and that an empty
// WEB_DIST_DIR leaves API routing untouched.
func TestDesktopFrontendRouting(t *testing.T) {
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, "assets"), 0700)
	_ = os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>reader</html>"), 0600)
	_ = os.WriteFile(filepath.Join(root, "assets", "main-123.js"), []byte("export {};"), 0600)
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-API", "true")
		http.NotFound(w, r)
	})
	handler, err := NewStaticFrontend(api, root)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	for _, tc := range []struct {
		method, path string
		code         int
		api          bool
	}{
		{"GET", "/", 200, false},
		{"GET", "/document/abc", 200, false},
		{"HEAD", "/document/abc", 200, false},
		{"GET", "/assets/main-123.js", 200, false},
		{"GET", "/assets/missing.js", 404, false},
		{"GET", "/api/missing", 404, true},
		{"GET", "/api", 404, true},
		{"GET", "/mcp/unknown", 404, true},
		{"GET", "/health", 404, true},
		{"POST", "/document/abc", 404, true},
		{"GET", "/../runtime.json", 404, false},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Accept", "text/html")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		if result.Code != tc.code || (result.Header().Get("X-API") == "true") != tc.api {
			t.Errorf("%s %s: %d, api=%s", tc.method, tc.path, result.Code, result.Header().Get("X-API"))
		}
		if strings.HasSuffix(tc.path, ".js") && result.Code == 200 {
			if !strings.Contains(result.Header().Get("Content-Type"), "javascript") ||
				!strings.Contains(result.Header().Get("Cache-Control"), "immutable") {
				t.Fatal("asset MIME/cache headers missing")
			}
		}
	}
	disabled, err := NewStaticFrontend(api, "")
	if err != nil {
		t.Fatal(err)
	}
	result := httptest.NewRecorder()
	disabled.ServeHTTP(result, httptest.NewRequest("GET", "/", nil))
	if result.Header().Get("X-API") != "true" {
		t.Fatal("development routing changed without WEB_DIST_DIR")
	}
}

// ---------------------------------------------------------------------------
// Consolidated from reference_routes_test.go.
// ---------------------------------------------------------------------------

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
