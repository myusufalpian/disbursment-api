package middleware

import (
	"fmt"
	"log/slog"
	"runtime/debug"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi/response"

	"github.com/gin-gonic/gin"
)

func Recovery(logger *slog.Logger) gin.HandlerFunc {
	return func(context *gin.Context) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}

			requestID := RequestIDFromContext(context.Request.Context())
			logger.Error("panic recovered",
				slog.String("request_id", requestID),
				slog.String("panic_type", fmt.Sprintf("%T", recovered)),
				slog.String("stack", string(debug.Stack())),
			)
			if !context.Writer.Written() {
				response.WriteError(context.Writer, requestID, domain.Internal())
			}
			context.Abort()
		}()
		context.Next()
	}
}
