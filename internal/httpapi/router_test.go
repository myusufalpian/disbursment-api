package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/observability/metrics"
)

func TestNewRouterWithHealth_ConfiguresRoutesAndHealth(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	collector := metrics.NewMetricsCollector()
	keyProvider := domain.NewStaticKeyProvider("v1", "jwt-secret-key-12345", nil)

	router, err := NewRouter(
		1024,
		logger,
		keyProvider,
		"test-issuer",
		"test-audience",
		nil,
		nil,
		collector,
		"metrics-token-123",
		[]string{"127.0.0.1"},
		&mockHealthChecker{},
	)
	if err != nil {
		t.Fatalf("expected no error creating router with health, got %v", err)
	}

	// Test GET /healthz
	wHealth := httptest.NewRecorder()
	reqHealth, _ := http.NewRequest(http.MethodGet, "/healthz", nil)
	router.ServeHTTP(wHealth, reqHealth)
	if wHealth.Code != http.StatusOK {
		t.Fatalf("expected /healthz status 200, got %d", wHealth.Code)
	}

	// Test GET /readyz
	wReady := httptest.NewRecorder()
	reqReady, _ := http.NewRequest(http.MethodGet, "/readyz", nil)
	router.ServeHTTP(wReady, reqReady)
	if wReady.Code != http.StatusOK {
		t.Fatalf("expected /readyz status 200, got %d", wReady.Code)
	}

	// Test NoRoute (404)
	wNotFound := httptest.NewRecorder()
	reqNotFound, _ := http.NewRequest(http.MethodGet, "/unknown-route-123", nil)
	router.ServeHTTP(wNotFound, reqNotFound)
	if wNotFound.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown route, got %d", wNotFound.Code)
	}
}
