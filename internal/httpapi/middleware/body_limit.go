package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func BodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(context *gin.Context) {
		context.Request.Body = http.MaxBytesReader(context.Writer, context.Request.Body, maxBytes)
		context.Next()
	}
}
