package router

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
