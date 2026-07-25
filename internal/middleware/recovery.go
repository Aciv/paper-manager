package middleware

import (
	"fmt"
	"log/slog"
	"runtime/debug"

	"paperMaster/internal/logger"

	"github.com/gin-gonic/gin"
)

// Recovery is a middleware that recovers from panics
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				ctx := c.Request.Context()

				// Print stacktrace
				stacktrace := debug.Stack()

				// RequestID middleware stores the request ID in ctx through
				// logger.WithRequestID. Do not read c.Get("RequestID"): that key
				// is not set and a failed type assertion would panic while recovering.
				logger.ErrorWithArgs(ctx, "panic recovered",
					slog.Any("error", fmt.Errorf("panic: %v", err)),
					slog.String("stacktrace", string(stacktrace)),
				)

				// Recovery must remain safe when used without ErrorHandler (for
				// example in a focused test router). Never expose the panic value.
				c.AbortWithStatusJSON(500, gin.H{
					"success": false,
					"error":   gin.H{"code": 1007, "message": "internal server error"},
				})
			}
		}()

		c.Next()
	}
}
