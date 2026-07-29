package middleware

import (
	"context"
	"log/slog"
	"time"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	secutils "paperMaster/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// RequestID adds a trusted correlation ID to the request context. Incoming
// values are sanitized before entering logs, while the header preserves the
// original value for client correlation.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			requestID = uuid.NewString()
		}
		c.Header("X-Request-ID", requestID)
		ctx := logger.WithRequestID(c.Request.Context(), secutils.SanitizeForLog(requestID))
		ctx = context.WithValue(ctx, types.RequestIDContextKey, requestID)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// silentPaths lists request paths that are deliberately excluded from access
// logging. These paths are called at high frequency and never fail in practice;
// logging every invocation drowns out real application signals.
var silentPaths = map[string]struct{}{
	"/health": {},
}

// Logger records request metadata only. Request and response bodies are
// deliberately excluded: they can contain credentials, DOI metadata, or
// document text, and buffering them can exhaust memory for large requests.
//
// Successful and redirect responses (2xx/3xx) are logged at Debug level so
// that LOG_LEVEL=info surfaces only client and server errors by default.
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, silent := silentPaths[c.Request.URL.Path]; silent {
			c.Next()
			return
		}
		started := time.Now()
		path := c.Request.URL.Path
		c.Next()

		ctx := c.Request.Context()
		requestID, _ := ctx.Value(types.RequestIDContextKey).(string)
		attrs := []any{slog.Group("http_request",
			slog.String("request_id", secutils.SanitizeForLog(requestID)),
			slog.String("method", c.Request.Method),
			slog.String("path", secutils.SanitizeForLog(path)),
			slog.Int("status_code", c.Writer.Status()),
			slog.Int("size", c.Writer.Size()),
			slog.String("latency", time.Since(started).String()),
			slog.String("client_ip", secutils.SanitizeForLog(c.ClientIP())),
		)}
		if c.Writer.Status() >= 400 {
			logger.Info(ctx, "request completed", attrs...)
		} else {
			logger.Debug(ctx, "request completed", attrs...)
		}
	}
}
