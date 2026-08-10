package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type mockHealthChecker struct {
	pingErr error
}

func (m *mockHealthChecker) PingContext(ctx context.Context) error {
	return m.pingErr
}

func TestHealthz(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHealthHandler(nil, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/healthz", nil)

	handler.Healthz(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if w.Body.String() != `{"status":"UP"}` {
		t.Fatalf("expected status UP body, got %s", w.Body.String())
	}
}

func TestReadyz_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHealthHandler(&mockHealthChecker{pingErr: nil}, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/readyz", nil)

	handler.Readyz(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if w.Body.String() != `{"status":"UP"}` {
		t.Fatalf("expected status UP body, got %s", w.Body.String())
	}
}

func TestReadyz_Failure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHealthHandler(&mockHealthChecker{pingErr: errors.New("connection reset")}, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/readyz", nil)

	handler.Readyz(c)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", w.Code)
	}
}

func TestReadyz_NilDB_Returns503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHealthHandler(nil, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/readyz", nil)

	handler.Readyz(c)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503 when db is nil, got %d", w.Code)
	}
}
