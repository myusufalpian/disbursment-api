package middleware

import (
	"log/slog"
	"time"

	"disbursment-api/internal/observability/metrics"

	"github.com/gin-gonic/gin"
)

const unmatchedRouteLabel = "unmatched"

func AccessLog(logger *slog.Logger, collector *metrics.MetricsCollector) gin.HandlerFunc {
	return func(context *gin.Context) {
		startedAt := time.Now()
		context.Next()

		latencyMs := time.Since(startedAt).Milliseconds()
		status := context.Writer.Status()
		method := context.Request.Method
		path := context.Request.URL.Path
		requestID := RequestIDFromContext(context.Request.Context())
		clientIP := context.ClientIP()

		isProbe := path == "/healthz" || path == "/readyz"
		if logger != nil && !isProbe {
			attrs := []slog.Attr{
				slog.String("request_id", requestID),
				slog.String("method", method),
				slog.String("path", path),
				slog.Int("status_code", status),
				slog.Int64("latency_ms", latencyMs),
				slog.String("client_ip", clientIP),
				slog.Int("bytes_out", context.Writer.Size()),
			}
			if identity, ok := UserIdentityFromContext(context.Request.Context()); ok {
				attrs = append(attrs, slog.String("user_id", identity.ID.String()))
			} else if userID, ok := context.Get("userID"); ok {
				attrs = append(attrs, slog.Any("user_id", userID))
			}
			logger.LogAttrs(context.Request.Context(), slog.LevelInfo, "request completed", attrs...)
		}

		if collector != nil && !isProbe {
			metricPath := context.FullPath()
			if metricPath == "" {
				metricPath = unmatchedRouteLabel
			}
			collector.RecordHTTPRequest(method, metricPath, status, latencyMs)
		}
	}
}
