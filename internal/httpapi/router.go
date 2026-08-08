package httpapi

import (
	"log/slog"
	"net/http"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi/middleware"
	"disbursment-api/internal/httpapi/response"
)

import "github.com/gin-gonic/gin"

func NewRouter(maxRequestBodyBytes int64, logger *slog.Logger) *gin.Engine {
	router := gin.New()
	router.Use(
		middleware.RequestID(),
		middleware.Recovery(logger),
		middleware.AccessLog(logger),
		middleware.BodyLimit(maxRequestBodyBytes),
	)
	router.NoRoute(func(context *gin.Context) {
		response.WriteError(context.Writer, middleware.RequestIDFromContext(context.Request.Context()), &domain.Error{
			Code:    domain.CodeDisbursementNotFound,
			Message: "Resource tidak ditemukan",
			Status:  http.StatusNotFound,
		})
	})
	return router
}
