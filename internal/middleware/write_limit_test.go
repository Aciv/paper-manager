package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"paperMaster/internal/config"

	"github.com/gin-gonic/gin"
)

func newWriteLimitEngine(t *testing.T, cfg config.Config, handler gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(ErrorHandler(), WriteLimit(cfg))
	engine.POST("/api/document/upload", handler)
	engine.GET("/api/document/search", handler)
	return engine
}

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
