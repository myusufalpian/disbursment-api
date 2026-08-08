package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"disbursment-api/internal/domain"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestRecoveryReturnsSafeErrorAndKeepsRouterAvailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	router := gin.New()
	router.Use(RequestID(), Recovery(logger))
	router.GET("/panic", func(context *gin.Context) {
		panic(errors.New("panic-sensitive-token"))
	})
	router.GET("/healthy", func(context *gin.Context) {
		context.Status(http.StatusNoContent)
	})

	panicRecorder := httptest.NewRecorder()
	panicRequest := httptest.NewRequest(http.MethodGet, "/panic", nil)
	router.ServeHTTP(panicRecorder, panicRequest)

	if panicRecorder.Code != http.StatusInternalServerError {
		t.Fatalf("panic response status = %d, want %d", panicRecorder.Code, http.StatusInternalServerError)
	}
	requestID := panicRecorder.Header().Get(RequestIDHeader)
	parsed, err := uuid.Parse(requestID)
	if err != nil || parsed.Version() != 4 {
		t.Fatalf("panic response request ID = %q, want UUID v4; parse error = %v", requestID, err)
	}
	var response struct {
		Success bool `json:"success"`
		Error   struct {
			Code domain.ErrorCode `json:"code"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(panicRecorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode panic response: %v", err)
	}
	if response.Success {
		t.Error("panic response success = true, want false")
	}
	if response.Error.Code != domain.CodeInternalError {
		t.Errorf("panic error code = %q, want %q", response.Error.Code, domain.CodeInternalError)
	}
	if response.RequestID != requestID {
		t.Errorf("panic response request_id = %q, want %q", response.RequestID, requestID)
	}

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &entry); err != nil {
		t.Fatalf("decode recovery log: %v", err)
	}
	if entry["panic_type"] == "" || entry["stack"] == "" {
		t.Errorf("recovery log = %#v, want panic_type and stack", entry)
	}
	if strings.Contains(logs.String(), "panic-sensitive-token") {
		t.Error("recovery log contains raw panic payload")
	}

	healthyRecorder := httptest.NewRecorder()
	healthyRequest := httptest.NewRequest(http.MethodGet, "/healthy", nil)
	router.ServeHTTP(healthyRecorder, healthyRequest)
	if healthyRecorder.Code != http.StatusNoContent {
		t.Errorf("healthy response status = %d, want %d", healthyRecorder.Code, http.StatusNoContent)
	}
}
