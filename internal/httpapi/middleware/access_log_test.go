package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"disbursment-api/internal/observability/metrics"

	"github.com/gin-gonic/gin"
)

func TestAccessLogMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	collector := metrics.NewMetricsCollector()

	router := gin.New()
	router.Use(RequestID())
	router.Use(AccessLog(logger, collector))

	router.GET("/test-endpoint", func(c *gin.Context) {
		c.Set("userID", "user-123")
		c.String(http.StatusOK, "OK")
	})

	req := httptest.NewRequest(http.MethodGet, "/test-endpoint", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", rec.Code)
	}

	logOutput := logBuf.String()
	if !bytes.Contains(logBuf.Bytes(), []byte(`"path":"/test-endpoint"`)) {
		t.Fatalf("expected log output to contain path, got: %s", logOutput)
	}
	if !bytes.Contains(logBuf.Bytes(), []byte(`"user_id":"user-123"`)) {
		t.Fatalf("expected log output to contain user_id, got: %s", logOutput)
	}

	snapshot := collector.Snapshot()
	if snapshot.HTTPRequestsTotal["GET /test-endpoint 2xx"] != 1 {
		t.Fatalf("expected 1 recorded HTTP request in metrics, got %v", snapshot.HTTPRequestsTotal)
	}
}
