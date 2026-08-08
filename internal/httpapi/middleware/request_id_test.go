package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestRequestIDPreservesValidIDAndGeneratesV4ForInvalidInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	validRequestID := "bca91f16-dc07-4c8d-a16d-b8bc2103df98"
	tests := []struct {
		name       string
		incomingID string
		wantExact  bool
	}{
		{name: "valid UUID v4", incomingID: validRequestID, wantExact: true},
		{name: "missing header", incomingID: "", wantExact: false},
		{name: "malformed header", incomingID: "not-a-uuid", wantExact: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.Use(RequestID())
			router.GET("/", func(context *gin.Context) {
				context.String(http.StatusOK, RequestIDFromContext(context.Request.Context()))
			})
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			if test.incomingID != "" {
				request.Header.Set(RequestIDHeader, test.incomingID)
			}

			router.ServeHTTP(recorder, request)

			requestID := recorder.Header().Get(RequestIDHeader)
			if test.wantExact && requestID != test.incomingID {
				t.Errorf("response request ID = %q, want %q", requestID, test.incomingID)
			}
			parsed, err := uuid.Parse(requestID)
			if err != nil || parsed.Version() != 4 {
				t.Errorf("response request ID = %q, want UUID v4; parse error = %v", requestID, err)
			}
			if recorder.Body.String() != requestID {
				t.Errorf("request context ID = %q, want %q", recorder.Body.String(), requestID)
			}
		})
	}
}
