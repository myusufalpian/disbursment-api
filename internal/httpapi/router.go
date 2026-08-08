package httpapi

import (
	"log/slog"
	"net/http"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi/middleware"
	"disbursment-api/internal/httpapi/response"

	"github.com/gin-gonic/gin"
)

func NewRouter(maxRequestBodyBytes int64, logger *slog.Logger, authHandler *AuthHandler) *gin.Engine {
	router := gin.New()
	router.Use(
		middleware.RequestID(),
		middleware.Recovery(logger),
		middleware.AccessLog(logger),
		middleware.BodyLimit(maxRequestBodyBytes),
	)

	if authHandler != nil {
		registerAuthRoutes(router, authHandler)
	}

	router.NoRoute(func(context *gin.Context) {
		response.WriteError(context.Writer, middleware.RequestIDFromContext(context.Request.Context()), &domain.Error{
			Code:    domain.CodeDisbursementNotFound,
			Message: "Resource tidak ditemukan",
			Status:  http.StatusNotFound,
		})
	})
	return router
}

func registerAuthRoutes(router *gin.Engine, authHandler *AuthHandler) {
	router.POST("/auth/login", authHandler.Login)
	router.POST("/auth/refresh", authHandler.Refresh)
	router.POST("/auth/logout", authHandler.Logout)

	v1 := router.Group("/api/v1")
	{
		v1.POST("/auth/login", authHandler.Login)
		v1.POST("/auth/refresh", authHandler.Refresh)
		v1.POST("/auth/logout", authHandler.Logout)
	}
}
