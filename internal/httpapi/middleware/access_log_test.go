package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/observability/metrics"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestAccessLogMiddlewareEmitsOneCompleteSuccessRecord(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		requestID = "bca91f16-dc07-4c8d-a16d-b8bc2103df98"
		clientIP  = "203.0.113.10"
		path      = "/success"
		body      = "OK"
	)
	userID := "41e4fa42-8f3a-4a70-9f1f-9d8f7a3a4e21"

	var accessLogs bytes.Buffer
	var recoveryLogs bytes.Buffer
	collector := metrics.NewMetricsCollector()
	router := newAccessLogTestRouter(t,
		slog.New(slog.NewJSONHandler(&accessLogs, nil)),
		slog.New(slog.NewJSONHandler(&recoveryLogs, nil)),
		collector,
	)
	router.GET(path, func(c *gin.Context) {
		identity := domain.UserIdentity{ID: uuid.MustParse(userID), Role: domain.RoleOperator}
		c.Request = c.Request.WithContext(ContextWithUserIdentity(c.Request.Context(), identity))
		c.String(http.StatusOK, body)
	})

	recorder := serveAccessLogRequest(router, http.MethodGet, path, requestID, clientIP)
	if recorder.Code != http.StatusOK {
		t.Fatalf("response status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if recorder.Body.String() != body {
		t.Fatalf("response body = %q, want %q", recorder.Body.String(), body)
	}

	assertAccessRecord(t, accessLogs.Bytes(), accessLogRecordExpectation{
		requestID: requestID,
		method:    http.MethodGet,
		path:      path,
		status:    http.StatusOK,
		clientIP:  clientIP,
		bytesOut:  len(body),
		userID:    userID,
	})
	if recoveryLogs.Len() != 0 {
		t.Fatalf("recovery logger emitted %q for successful request", recoveryLogs.String())
	}
	if got := collector.Snapshot().HTTPRequestsTotal["GET /success 2xx"]; got != 1 {
		t.Fatalf("metrics request count = %d, want 1", got)
	}
}

func TestAccessLogMiddlewareEmitsOneCompleteErrorRecord(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		requestID = "d6f5a1c0-7e2b-4c8f-9a1d-3b6e5f708192"
		clientIP  = "198.51.100.24"
		path      = "/error"
		body      = "failure"
	)

	var accessLogs bytes.Buffer
	var recoveryLogs bytes.Buffer
	router := newAccessLogTestRouter(t,
		slog.New(slog.NewJSONHandler(&accessLogs, nil)),
		slog.New(slog.NewJSONHandler(&recoveryLogs, nil)),
		metrics.NewMetricsCollector(),
	)
	router.POST(path, func(c *gin.Context) {
		c.String(http.StatusInternalServerError, body)
	})

	recorder := serveAccessLogRequest(router, http.MethodPost, path, requestID, clientIP)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("response status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if recorder.Body.String() != body {
		t.Fatalf("response body = %q, want %q", recorder.Body.String(), body)
	}

	assertAccessRecord(t, accessLogs.Bytes(), accessLogRecordExpectation{
		requestID: requestID,
		method:    http.MethodPost,
		path:      path,
		status:    http.StatusInternalServerError,
		clientIP:  clientIP,
		bytesOut:  len(body),
	})
}

func TestRecoveryLogCorrelatesPanicWithRequestIDInProductionOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		requestID = "6f4e3d2c-1b0a-49f8-87e6-5d4c3b2a1908"
		clientIP  = "192.0.2.44"
		path      = "/panic"
	)

	var accessLogs bytes.Buffer
	var recoveryLogs bytes.Buffer
	accessLogger := slog.New(slog.NewJSONHandler(&accessLogs, nil))
	recoveryLogger := slog.New(slog.NewJSONHandler(&recoveryLogs, nil))
	router := gin.New()
	if err := router.SetTrustedProxies(nil); err != nil {
		t.Fatalf("disable trusted proxies: %v", err)
	}
	// Match the production order: AccessLog wraps Recovery so it records the recovered response.
	router.Use(RequestID(), AccessLog(accessLogger, metrics.NewMetricsCollector()), Recovery(recoveryLogger))
	router.GET(path, func(c *gin.Context) {
		panic("panic-sensitive-token")
	})

	recorder := serveAccessLogRequest(router, http.MethodGet, path, requestID, clientIP)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("response status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	panicBody := append([]byte(nil), recorder.Body.Bytes()...)
	var response struct {
		Success bool `json:"success"`
		Error   struct {
			Code    domain.ErrorCode `json:"code"`
			Message string           `json:"message"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}
	if err := json.NewDecoder(bytes.NewReader(panicBody)).Decode(&response); err != nil {
		t.Fatalf("decode panic response: %v", err)
	}
	if response.Success {
		t.Error("panic response success = true, want false")
	}
	if response.Error.Code != domain.CodeInternalError {
		t.Errorf("panic error code = %q, want %q", response.Error.Code, domain.CodeInternalError)
	}
	if response.Error.Message != "Terjadi kesalahan internal" {
		t.Errorf("panic error message = %q, want %q", response.Error.Message, "Terjadi kesalahan internal")
	}
	if response.RequestID != requestID {
		t.Errorf("panic response request_id = %q, want %q", response.RequestID, requestID)
	}

	decoder := json.NewDecoder(bytes.NewReader(recoveryLogs.Bytes()))
	var records []map[string]any
	for {
		var record map[string]any
		err := decoder.Decode(&record)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode recovery log: %v; output: %s", err, recoveryLogs.String())
		}
		records = append(records, record)
	}
	if len(records) != 1 {
		t.Fatalf("recovery log record count = %d, want exactly 1; output: %s", len(records), recoveryLogs.String())
	}
	record := records[0]
	timeValue, ok := record["time"].(string)
	if !ok || timeValue == "" {
		t.Fatalf("recovery log time = %v (%T), want RFC3339 timestamp", record["time"], record["time"])
	}
	if _, err := time.Parse(time.RFC3339Nano, timeValue); err != nil {
		t.Fatalf("recovery log time = %q, want RFC3339 timestamp: %v", timeValue, err)
	}
	assertStringField(t, record, "level", "ERROR")
	assertStringField(t, record, "msg", "panic recovered")
	assertStringField(t, record, "request_id", requestID)
	assertStringField(t, record, "panic_type", "string")
	if stack, ok := record["stack"].(string); !ok || stack == "" {
		t.Fatalf("recovery log stack = %v (%T), want non-empty string", record["stack"], record["stack"])
	}
	assertAccessRecord(t, accessLogs.Bytes(), accessLogRecordExpectation{
		requestID: requestID,
		method:    http.MethodGet,
		path:      path,
		status:    http.StatusInternalServerError,
		clientIP:  clientIP,
		bytesOut:  len(panicBody),
	})
}

type accessLogRecordExpectation struct {
	requestID string
	method    string
	path      string
	status    int
	clientIP  string
	bytesOut  int
	userID    string
}

func newAccessLogTestRouter(t *testing.T, accessLogger *slog.Logger, recoveryLogger *slog.Logger, collector *metrics.MetricsCollector) *gin.Engine {
	t.Helper()
	router := gin.New()
	if err := router.SetTrustedProxies(nil); err != nil {
		t.Fatalf("disable trusted proxies: %v", err)
	}
	router.Use(RequestID(), AccessLog(accessLogger, collector), Recovery(recoveryLogger))
	return router
}

func serveAccessLogRequest(router http.Handler, method string, path string, requestID string, clientIP string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set(RequestIDHeader, requestID)
	request.RemoteAddr = clientIP + ":4567"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func assertAccessRecord(t *testing.T, output []byte, expectation accessLogRecordExpectation) {
	t.Helper()

	decoder := json.NewDecoder(bytes.NewReader(output))
	var records []map[string]any
	for {
		var record map[string]any
		err := decoder.Decode(&record)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode access log JSON: %v; output: %s", err, output)
		}
		records = append(records, record)
	}
	if len(records) != 1 {
		t.Fatalf("access log record count = %d, want exactly 1; output: %s", len(records), output)
	}
	record := records[0]
	assertJSONSlogEnvelope(t, record)

	assertStringField(t, record, "request_id", expectation.requestID)
	assertStringField(t, record, "method", expectation.method)
	assertStringField(t, record, "path", expectation.path)
	assertNumberField(t, record, "status_code", expectation.status)
	latencyMs := assertNumberField(t, record, "latency_ms", -1)
	if latencyMs < 0 {
		t.Fatalf("log latency_ms = %v, want non-negative value", latencyMs)
	}
	assertStringField(t, record, "client_ip", expectation.clientIP)
	assertNumberField(t, record, "bytes_out", expectation.bytesOut)
	if expectation.userID != "" {
		assertStringField(t, record, "user_id", expectation.userID)
	} else if _, ok := record["user_id"]; ok {
		t.Fatalf("log user_id = %v, want field omitted when identity is absent", record["user_id"])
	}
}

func assertJSONSlogEnvelope(t *testing.T, record map[string]any) {
	t.Helper()

	timeValue, ok := record["time"].(string)
	if !ok || timeValue == "" {
		t.Fatalf("log time = %v (%T), want RFC3339 timestamp", record["time"], record["time"])
	}
	if _, err := time.Parse(time.RFC3339Nano, timeValue); err != nil {
		t.Fatalf("log time = %q, want RFC3339 timestamp: %v", timeValue, err)
	}
	assertStringField(t, record, "level", "INFO")
	assertStringField(t, record, "msg", "request completed")
}

func assertStringField(t *testing.T, record map[string]any, field string, want string) {
	t.Helper()
	got, ok := record[field].(string)
	if !ok || got != want {
		t.Fatalf("log %s = %v (%T), want %q", field, record[field], record[field], want)
	}
}

func assertNumberField(t *testing.T, record map[string]any, field string, want int) float64 {
	t.Helper()
	got, ok := record[field].(float64)
	if !ok || (want >= 0 && got != float64(want)) {
		t.Fatalf("log %s = %v (%T), want JSON number %d", field, record[field], record[field], want)
	}
	return got
}
