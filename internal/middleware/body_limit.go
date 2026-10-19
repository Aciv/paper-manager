package middleware

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	apperrors "paperMaster/internal/errors"

	"github.com/gin-gonic/gin"
)

// JSONBodyLimit rejects non-multipart JSON bodies larger than limit. It
// buffers at most limit bytes so chunked requests without Content-Length are
// covered as well, and it returns 413 instead of letting each handler surface
// a generic JSON bind error. Multipart uploads keep their handler-specific
// per-file and per-batch limits.
func JSONBodyLimit(limit int64) gin.HandlerFunc {
	if limit <= 0 {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		request := c.Request
		if request == nil || request.Body == nil || !isJSONContentType(request.Header.Get("Content-Type")) {
			c.Next()
			return
		}
		if request.ContentLength > limit {
			_ = request.Body.Close()
			c.Error(apperrors.NewPayloadTooLargeError("JSON request body exceeds the configured limit"))
			c.Abort()
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(c.Writer, request.Body, limit))
		if err != nil {
			var maxBytesError *http.MaxBytesError
			if errors.As(err, &maxBytesError) {
				c.Error(apperrors.NewPayloadTooLargeError("JSON request body exceeds the configured limit"))
			} else {
				c.Error(apperrors.NewBadRequestError("read JSON request body"))
			}
			c.Abort()
			return
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		c.Next()
	}
}

func isJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}
