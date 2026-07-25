package middleware

import (
	"net/http"
	"paperMaster/internal/errors"

	"github.com/gin-gonic/gin"
)

// ErrorHandler 是一个处理应用错误的中间件
func ErrorHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 处理请求
		c.Next()

		// 检查是否有错误
		if len(c.Errors) > 0 {
			// 获取最后一个错误
			err := c.Errors.Last().Err

			// 检查是否为应用错误
			if serverErr, ok := errors.IsServerError(err); ok {
				// 返回应用错误
				c.JSON(serverErr.HTTPCode, gin.H{
					"success": false,
					"error": gin.H{
						"code":    serverErr.Code,
						"message": serverErr.Message,
						"details": serverErr.Details,
					},
				})
				return
			}

			// 处理其他类型的错误
			c.JSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"error": gin.H{
					"code":    250, //errors.ErrInternalServer,
					"message": "Internal server error",
				},
			})
		}
	}
}
