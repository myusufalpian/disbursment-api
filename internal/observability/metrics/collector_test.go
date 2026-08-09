package metrics

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMetricsCollector_FullCoverage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("RecordHTTPRequest and Snapshot", func(t *testing.T) {
		collector := NewMetricsCollector()
		collector.RecordHTTPRequest("POST", "/disbursements", 201, 45)
		collector.RecordHTTPRequest("POST", "/disbursements", 201, 55)
		collector.RecordHTTPRequest("GET", "/disbursements", 404, 10)

		snapshot := collector.Snapshot()
		if snapshot.HTTPRequestsCount != 3 {
			t.Fatalf("expected 3 requests count, got %d", snapshot.HTTPRequestsCount)
		}
		expectedAverage := 110.0 / 3.0
		if math.Abs(snapshot.HTTPDurationMsAverage-expectedAverage) > 1e-9 {
			t.Fatalf("expected average duration %f, got %f", expectedAverage, snapshot.HTTPDurationMsAverage)
		}
		if snapshot.HTTPRequestsTotal["POST /disbursements 2xx"] != 2 {
			t.Fatalf("expected 2 POST requests 2xx, got %d", snapshot.HTTPRequestsTotal["POST /disbursements 2xx"])
		}
		if snapshot.HTTPRequestsTotal["GET /disbursements 4xx"] != 1 {
			t.Fatalf("expected 1 GET request 4xx, got %d", snapshot.HTTPRequestsTotal["GET /disbursements 4xx"])
		}
	})

	t.Run("RecordHTTPRequest normalizes UUID in path", func(t *testing.T) {
		collector := NewMetricsCollector()
		collector.RecordHTTPRequest("GET", "/disbursements/123e4567-e89b-12d3-a456-426614174000/status", 200, 15)
		collector.RecordHTTPRequest("GET", "/disbursements/98765432-e89b-12d3-a456-426614174999/status", 200, 25)

		snapshot := collector.Snapshot()
		if snapshot.HTTPRequestsTotal["GET /disbursements/:id/status 2xx"] != 2 {
			t.Fatalf("expected normalized key count 2, got map: %v", snapshot.HTTPRequestsTotal)
		}
	})

	t.Run("RecordIdempotencyClaim and Finalization", func(t *testing.T) {
		collector := NewMetricsCollector()
		collector.RecordIdempotencyClaim("acquired")
		collector.RecordIdempotencyClaim("replayed")
		collector.RecordFinalizationOutcome("approved")
		collector.RecordFinalizationOutcome("conflict")

		snapshot := collector.Snapshot()
		if len(snapshot.IdempotencyClaimsTotal) != 2 {
			t.Fatalf("expected exactly 2 idempotency claim labels, got %v", snapshot.IdempotencyClaimsTotal)
		}
		if snapshot.IdempotencyClaimsTotal["acquired"] != 1 {
			t.Fatalf("expected 1 acquired claim")
		}
		if snapshot.IdempotencyClaimsTotal["replayed"] != 1 {
			t.Fatalf("expected 1 replayed claim")
		}
		if snapshot.IdempotencyClaimsTotal["missing"] != 0 {
			t.Fatalf("expected absent idempotency claim label to remain zero")
		}
		if len(snapshot.FinalizationsTotal) != 2 {
			t.Fatalf("expected exactly 2 finalization labels, got %v", snapshot.FinalizationsTotal)
		}
		if snapshot.FinalizationsTotal["approved"] != 1 {
			t.Fatalf("expected 1 approved finalization")
		}
		if snapshot.FinalizationsTotal["conflict"] != 1 {
			t.Fatalf("expected 1 conflict finalization")
		}
		if snapshot.FinalizationsTotal["missing"] != 0 {
			t.Fatalf("expected absent finalization label to remain zero")
		}
	})

	t.Run("RecordAuthFailure, Delivery, Backlog, Reconciliation, DBStats", func(t *testing.T) {
		collector := NewMetricsCollector()
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
		collector := NewMetricsCollector()
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
		collector := NewMetricsCollector()
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

func TestMetricsCollectorHTTPHandlerContract(t *testing.T) {
	const expectedNoStore = "no-store, no-cache, must-revalidate"
	const expectedJSON = "application/json; charset=utf-8"

	t.Run("unconfigured token returns exact error response", func(t *testing.T) {
		router := gin.New()
		router.GET("/metrics", NewMetricsCollector().HTTPHandler(""))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))

		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 Service Unavailable, got %d", response.Code)
		}
		if response.Header().Get("Cache-Control") != expectedNoStore {
			t.Fatalf("expected no-store cache policy, got %q", response.Header().Get("Cache-Control"))
		}
		if response.Header().Get("Content-Type") != expectedJSON {
			t.Fatalf("expected exact JSON content type, got %q", response.Header().Get("Content-Type"))
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode unconfigured response: %v", err)
		}
		if len(body) != 1 || string(body["error"]) != `"metrics authentication is not configured"` {
			t.Fatalf("unexpected unconfigured response body: %s", response.Body.String())
		}
	})

	t.Run("missing token returns exact error response", func(t *testing.T) {
		testMetricsUnauthorizedResponse(t, expectedNoStore, expectedJSON, "")
	})

	t.Run("wrong token returns exact error response", func(t *testing.T) {
		testMetricsUnauthorizedResponse(t, expectedNoStore, expectedJSON, "wrong-token")
	})

	t.Run("valid token returns exact snapshot response", func(t *testing.T) {
		collector := NewMetricsCollector()
		collector.RecordHTTPRequest("GET", "/api/v1/disbursements", http.StatusOK, 10)
		router := gin.New()
		router.GET("/metrics", collector.HTTPHandler("sec-token-123"))
		request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		request.Header.Set("X-Metrics-Token", "sec-token-123")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", response.Code)
		}
		if response.Header().Get("Cache-Control") != expectedNoStore {
			t.Fatalf("expected no-store cache policy, got %q", response.Header().Get("Cache-Control"))
		}
		if response.Header().Get("Content-Type") != expectedJSON {
			t.Fatalf("expected exact JSON content type, got %q", response.Header().Get("Content-Type"))
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode valid response: %v", err)
		}
		assertMetricsJSONKeys(t, body,
			"http_requests_total",
			"http_duration_ms_average",
			"http_requests_count",
			"idempotency_claims_total",
			"finalizations_total",
			"auth_failures_total",
			"outbox_backlog_depth",
			"outbox_deliveries_total",
			"outbox_delivery_failures_total",
			"outbox_reconcile_warning_count",
			"outbox_reconcile_critical_count",
			"db_connections_open",
			"db_connections_in_use",
			"db_connections_idle",
		)
		var snapshot MetricsSnapshot
		if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
			t.Fatalf("decode metrics snapshot: %v", err)
		}
		if snapshot.HTTPRequestsCount != 1 || snapshot.HTTPRequestsTotal["GET /api/v1/disbursements 2xx"] != 1 {
			t.Fatalf("unexpected metrics snapshot values: %+v", snapshot)
		}
	})
}

func testMetricsUnauthorizedResponse(t *testing.T, expectedNoStore string, expectedJSON string, token string) {
	t.Helper()
	collector := NewMetricsCollector()
	router := gin.New()
	router.GET("/metrics", collector.HTTPHandler("sec-token-123"))
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if token != "" {
		request.Header.Set("X-Metrics-Token", token)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", response.Code)
	}
	if response.Header().Get("Cache-Control") != expectedNoStore {
		t.Fatalf("expected no-store cache policy, got %q", response.Header().Get("Cache-Control"))
	}
	if response.Header().Get("Content-Type") != expectedJSON {
		t.Fatalf("expected exact JSON content type, got %q", response.Header().Get("Content-Type"))
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode unauthorized response: %v", err)
	}
	if len(body) != 1 || string(body["error"]) != `"unauthorized metrics access"` {
		t.Fatalf("unexpected unauthorized response body: %s", response.Body.String())
	}
}

func assertMetricsJSONKeys(t *testing.T, body map[string]json.RawMessage, expected ...string) {
	t.Helper()
	if len(body) != len(expected) {
		t.Fatalf("expected JSON keys %v, got %v", expected, body)
	}
	for _, key := range expected {
		if _, ok := body[key]; !ok {
			t.Fatalf("expected JSON key %q in %v", key, body)
		}
	}
}
