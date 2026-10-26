package middleware

import (
	"bytes"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"paperMaster/internal/config"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Consolidated from body_limit_test.go.
// ---------------------------------------------------------------------------

// TestJSONBodyLimitRejectsOversizedBodies verifies that JSON requests exceeding
// the configured body limit are rejected with 413 for both declared and
// chunked lengths, while smaller bodies pass through.
func TestJSONBodyLimitRejectsOversizedBodies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(ErrorHandler())
	engine.Use(JSONBodyLimit(64))
	engine.POST("/json", func(c *gin.Context) {
		var payload map[string]any
		if err := c.ShouldBindJSON(&payload); err != nil {
			c.Error(err)
			c.Abort()
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	for _, test := range []struct {
		name          string
		contentLength int64
		body          string
		wantStatus    int
	}{
		{name: "content length over limit", contentLength: 100, body: `{"value":"` + strings.Repeat("x", 100) + `"}`, wantStatus: http.StatusRequestEntityTooLarge},
		{name: "chunked body over limit", contentLength: -1, body: `{"value":"` + strings.Repeat("x", 100) + `"}`, wantStatus: http.StatusRequestEntityTooLarge},
		{name: "body within limit", contentLength: -1, body: `{"value":"ok"}`, wantStatus: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/json", bytes.NewBufferString(test.body))
			request.Header.Set("Content-Type", "application/json")
			request.ContentLength = test.contentLength
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

// TestJSONBodyLimitLeavesMultipartAlone confirms that the JSON body limit does
// not apply to multipart uploads, allowing a larger file body to reach the handler.
func TestJSONBodyLimitLeavesMultipartAlone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(ErrorHandler())
	engine.Use(JSONBodyLimit(8))
	engine.POST("/upload", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	request := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewBufferString(strings.Repeat("x", 1024)))
	request.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("multipart status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from write_limit_test.go.
// ---------------------------------------------------------------------------

func newWriteLimitEngine(t *testing.T, cfg config.Config, handler gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(ErrorHandler(), WriteLimit(cfg))
	engine.POST("/api/document/upload", handler)
	engine.GET("/api/document/search", handler)
	return engine
}

// TestWriteLimitReturns429AfterBurst verifies that write-rate limiting returns
// 429 with a Retry-After header once the burst is exhausted while read requests
// remain unaffected.
func TestWriteLimitReturns429AfterBurst(t *testing.T) {
	cfg := config.Config{HTTPWriteRatePerMinute: 1, HTTPWriteRateBurst: 1,
		HTTPUploadConcurrency: 4, HTTPBatchConcurrency: 2}
	engine := newWriteLimitEngine(t, cfg, func(c *gin.Context) { c.Status(http.StatusOK) })

	first := httptest.NewRecorder()
	engine.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/api/document/upload", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d", first.Code)
	}
	second := httptest.NewRecorder()
	engine.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/api/document/upload", nil))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429; body=%s", second.Code, second.Body.String())
	}
	if second.Header().Get("Retry-After") == "" {
		t.Fatal("429 response is missing Retry-After")
	}

	read := httptest.NewRecorder()
	engine.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/api/document/search", nil))
	if read.Code != http.StatusOK {
		t.Fatalf("read status = %d, want unaffected", read.Code)
	}
}

// TestWriteLimitCapsConcurrentUploads verifies that overlapping uploads beyond
// the configured concurrency limit are rejected with 429 while the first request
// completes normally.
func TestWriteLimitCapsConcurrentUploads(t *testing.T) {
	cfg := config.Config{HTTPWriteRatePerMinute: 10000, HTTPWriteRateBurst: 100,
		HTTPUploadConcurrency: 1, HTTPBatchConcurrency: 1}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	engine := newWriteLimitEngine(t, cfg, func(c *gin.Context) {
		once.Do(func() { started <- struct{}{} })
		<-release
		c.Status(http.StatusOK)
	})

	firstDone := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/document/upload", nil))
		firstDone <- response.Code
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first upload did not start")
	}
	second := httptest.NewRecorder()
	engine.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/api/document/upload", nil))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("concurrent status = %d, want 429", second.Code)
	}
	close(release)
	if code := <-firstDone; code != http.StatusOK {
		t.Fatalf("first status = %d", code)
	}
}
