package middleware

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const RequestIDHeader = "X-Request-ID"

type requestIDContextKey struct{}

func RequestID() gin.HandlerFunc {
	return func(request *gin.Context) {
		requestID := request.GetHeader(RequestIDHeader)
		if !isUUIDv4(requestID) {
			requestID = uuid.NewString()
		}

		request.Request = request.Request.WithContext(context.WithValue(request.Request.Context(), requestIDContextKey{}, requestID))
		request.Header(RequestIDHeader, requestID)
		request.Next()
	}
}

func RequestIDFromContext(context context.Context) string {
	requestID, _ := context.Value(requestIDContextKey{}).(string)
	return requestID
}

func isUUIDv4(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.Version() == 4
}
