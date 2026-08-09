package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMetricsCollector_FullCoverage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	collector := NewMetricsCollector()

	t.Run("RecordHTTPRequest and Snapshot", func(t *testing.T) {
		collector.RecordHTTPRequest("POST", "/disbursements", 201, 45)
		collector.RecordHTTPRequest("POST", "/disbursements", 201, 55)
		collector.RecordHTTPRequest("GET", "/disbursements", 404, 10)

		snapshot := collector.Snapshot()
		if snapshot.HTTPRequestsCount != 3 {
			t.Fatalf("expected 3 requests count, got %d", snapshot.HTTPRequestsCount)
		}
		if snapshot.HTTPDurationMsAverage != 36.666666666666664 && (snapshot.HTTPDurationMsAverage < 36.0 || snapshot.HTTPDurationMsAverage > 37.0) {
			t.Fatalf("unexpected average duration: %f", snapshot.HTTPDurationMsAverage)
		}
		if snapshot.HTTPRequestsTotal["POST /disbursements 2xx"] != 2 {
			t.Fatalf("expected 2 POST requests 2xx, got %d", snapshot.HTTPRequestsTotal["POST /disbursements 2xx"])
		}
	})

	t.Run("RecordHTTPRequest normalizes UUID in path", func(t *testing.T) {
		collector.RecordHTTPRequest("GET", "/disbursements/123e4567-e89b-12d3-a456-426614174000/status", 200, 15)
		collector.RecordHTTPRequest("GET", "/disbursements/98765432-e89b-12d3-a456-426614174999/status", 200, 25)

		snapshot := collector.Snapshot()
		if snapshot.HTTPRequestsTotal["GET /disbursements/:id/status 2xx"] != 2 {
			t.Fatalf("expected normalized key count 2, got map: %v", snapshot.HTTPRequestsTotal)
		}
	})

	t.Run("RecordIdempotencyClaim and Finalization", func(t *testing.T) {
		collector.RecordIdempotencyClaim("acquired")
		collector.RecordIdempotencyClaim("replayed")
		collector.RecordFinalizationOutcome("approved")
		collector.RecordFinalizationOutcome("conflict")

		snapshot := collector.Snapshot()
		if snapshot.IdempotencyClaimsTotal["acquired"] != 1 {
			t.Fatalf("expected 1 acquired claim")
		}
		if snapshot.FinalizationsTotal["approved"] != 1 {
			t.Fatalf("expected 1 approved finalization")
		}
	})

	t.Run("RecordAuthFailure, Delivery, Backlog, Reconciliation, DBStats", func(t *testing.T) {
		collector.RecordAuthFailure("invalid_credentials")
		collector.RecordDeliverySuccess()
		collector.RecordDeliveryFailure()
		collector.SetBacklogDepth(15)
		collector.SetReconciliationCounts(2, 1)
		collector.UpdateDBStats(10, 3, 7)

		snapshot := collector.Snapshot()
		if snapshot.AuthFailuresTotal["invalid_credentials"] != 1 {
			t.Fatalf("expected 1 auth failure")
		}
		if snapshot.OutboxDeliveriesTotal != 1 || snapshot.OutboxDeliveryFailures != 1 {
			t.Fatalf("expected 1 delivery success & failure")
		}
		if snapshot.OutboxBacklogDepth != 15 {
			t.Fatalf("expected 15 backlog depth")
		}
		if snapshot.OutboxReconcileWarning != 2 || snapshot.OutboxReconcileCritical != 1 {
			t.Fatalf("expected warning 2 critical 1")
		}
		if snapshot.DBConnectionsOpen != 10 || snapshot.DBConnectionsInUse != 3 || snapshot.DBConnectionsIdle != 7 {
			t.Fatalf("expected DB open 10 inUse 3 idle 7")
		}
	})

	t.Run("HTTPHandler endpoint", func(t *testing.T) {
		router := gin.New()
		router.GET("/metrics", collector.HTTPHandler(""))

		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected HTTP 503 for missing metrics authentication, got %d", rec.Code)
		}

	})

	t.Run("HTTPHandler with token validation", func(t *testing.T) {
		router := gin.New()
		router.GET("/metrics", collector.HTTPHandler("sec-token-123"))

		// 1. Request without token returns 401
		req1 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec1 := httptest.NewRecorder()
		router.ServeHTTP(rec1, req1)
		if rec1.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 Unauthorized without token, got %d", rec1.Code)
		}

		// 2. Request with valid header token returns 200
		req2 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req2.Header.Set("X-Metrics-Token", "sec-token-123")
		rec2 := httptest.NewRecorder()
		router.ServeHTTP(rec2, req2)
		if rec2.Code != http.StatusOK {
			t.Fatalf("expected 200 OK with valid header token, got %d", rec2.Code)
		}
	})

	t.Run("Default collector instance", func(t *testing.T) {
		def := Default()
		if def == nil {
			t.Fatalf("expected non-nil default collector")
		}
	})
}
