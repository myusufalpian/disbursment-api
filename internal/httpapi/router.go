package httpapi

import (
	"log/slog"
	"net/http"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi/middleware"
	"disbursment-api/internal/httpapi/response"

	"github.com/gin-gonic/gin"
)

func NewRouter(
	maxRequestBodyBytes int64,
	logger *slog.Logger,
	jwtSecret string,
	authHandler *AuthHandler,
	disbursementHandler *DisbursementHandler,
) *gin.Engine {
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

	if disbursementHandler != nil && jwtSecret != "" {
		registerDisbursementRoutes(router, jwtSecret, disbursementHandler)
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

func registerDisbursementRoutes(router *gin.Engine, jwtSecret string, handler *DisbursementHandler) {
	authMiddleware := middleware.Authenticate(jwtSecret)

	registerGroup := func(group *gin.RouterGroup) {
		group.Use(authMiddleware)
		group.POST("", middleware.RequireRole(domain.RoleOperator, domain.RoleAdmin, domain.RoleSuperadmin), handler.Create)
		group.GET("", middleware.RequireRole(domain.RoleOperator, domain.RoleAdmin, domain.RoleSuperadmin), handler.List)
		group.GET("/:id", middleware.RequireRole(domain.RoleOperator, domain.RoleAdmin, domain.RoleSuperadmin), handler.GetByID)
		group.PATCH("/:id/status", middleware.RequireRole(domain.RoleAdmin, domain.RoleSuperadmin), handler.UpdateStatus)
		group.DELETE("/:id", middleware.RequireRole(domain.RoleSuperadmin), handler.Delete)
	}

	disbursements := router.Group("/disbursements")
	registerGroup(disbursements)

	v1Disbursements := router.Group("/api/v1/disbursements")
	registerGroup(v1Disbursements)
}
