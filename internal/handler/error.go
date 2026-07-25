package handler

import (
	"net/http"

	apperrors "paperMaster/internal/errors"
	"paperMaster/internal/logger"

	"github.com/gin-gonic/gin"
)

// abortServerError applies the handler logging policy before delegating JSON
// serialization to middleware.ErrorHandler. Ordinary client mistakes already
// appear in the request access log, so logging them again as Error would make
// production failures difficult to find.
func abortServerError(c *gin.Context, event string, serverErr *apperrors.ServerError, cause error) {
	if serverErr == nil {
		return
	}
	switch serverErr.HTTPCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
		logger.Warn(c.Request.Context(), event, "code", serverErr.Code, "message", serverErr.Message)
	default:
		if serverErr.HTTPCode >= http.StatusInternalServerError {
			if cause != nil {
				logger.Error(c.Request.Context(), event, "code", serverErr.Code, "message", serverErr.Message, "error", cause)
			} else {
				logger.Error(c.Request.Context(), event, "code", serverErr.Code, "message", serverErr.Message)
			}
		}
	}
	_ = c.Error(serverErr)
	c.Abort()
}
