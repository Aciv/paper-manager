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

// Logger records request metadata only. Request and response bodies are
// deliberately excluded: they can contain credentials, DOI metadata, or
// document text, and buffering them can exhaust memory for large requests.
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		path := c.Request.URL.Path
		c.Next()

		ctx := c.Request.Context()
		requestID, _ := ctx.Value(types.RequestIDContextKey).(string)
		logger.GetLogger(ctx).WithGroup("http_request").Info("request completed",
			slog.String("request_id", secutils.SanitizeForLog(requestID)),
			slog.String("method", c.Request.Method),
			slog.String("path", secutils.SanitizeForLog(path)),
			slog.Int("status_code", c.Writer.Status()),
			slog.Int("size", c.Writer.Size()),
			slog.String("latency", time.Since(started).String()),
			slog.String("client_ip", secutils.SanitizeForLog(c.ClientIP())),
		)
	}
}
