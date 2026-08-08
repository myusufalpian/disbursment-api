package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
)

func AccessLog(logger *slog.Logger) gin.HandlerFunc {
	return func(context *gin.Context) {
		startedAt := time.Now()
		context.Next()

		logger.Info("request completed",
			slog.String("request_id", RequestIDFromContext(context.Request.Context())),
			slog.String("method", context.Request.Method),
			slog.String("path", context.Request.URL.Path),
			slog.Int("status_code", context.Writer.Status()),
			slog.Int64("latency_ms", time.Since(startedAt).Milliseconds()),
		)
	}
}
