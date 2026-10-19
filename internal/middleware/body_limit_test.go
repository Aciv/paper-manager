package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

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
