package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi/middleware"
	"disbursment-api/internal/httpapi/response"
	"disbursment-api/internal/observability/metrics"

	"github.com/gin-gonic/gin"
)

func NewRouter(
	maxRequestBodyBytes int64,
	logger *slog.Logger,
	jwtSecret string,
	jwtIssuer string,
	jwtAudience string,
	authHandler *AuthHandler,
	disbursementHandler *DisbursementHandler,
	metricsCollector *metrics.MetricsCollector,
	metricsToken string,
	trustedProxies []string,
) (*gin.Engine, error) {
	router := gin.New()
	if len(trustedProxies) > 0 {
		if err := router.SetTrustedProxies(trustedProxies); err != nil {
			return nil, fmt.Errorf("configure trusted proxies: %w", err)
		}
	} else {
		if err := router.SetTrustedProxies(nil); err != nil {
			return nil, fmt.Errorf("disable trusted proxies: %w", err)
		}
	}
	router.Use(
		middleware.RequestID(),
		middleware.Recovery(logger),
		middleware.AccessLog(logger, metricsCollector),
		middleware.BodyLimit(maxRequestBodyBytes),
	)

	if metricsCollector != nil {
		router.GET("/metrics", metricsCollector.HTTPHandler(metricsToken))
	}

	if authHandler != nil {
		registerAuthRoutes(router, authHandler, metricsCollector)
	}

	if disbursementHandler != nil && jwtSecret != "" {
		registerDisbursementRoutes(router, jwtSecret, jwtIssuer, jwtAudience, disbursementHandler, metricsCollector)
	}

	router.NoRoute(func(context *gin.Context) {
		response.WriteError(context.Writer, middleware.RequestIDFromContext(context.Request.Context()), &domain.Error{
			Code:    domain.CodeDisbursementNotFound,
			Message: "Resource tidak ditemukan",
			Status:  http.StatusNotFound,
		})
	})
	return router, nil
}

func registerAuthRoutes(router *gin.Engine, authHandler *AuthHandler, collector *metrics.MetricsCollector) {
	rateLimiter := middleware.RateLimit(10, 1*time.Minute, collector)

	router.POST("/auth/login", rateLimiter, authHandler.Login)
	router.POST("/auth/refresh", rateLimiter, authHandler.Refresh)
	router.POST("/auth/logout", authHandler.Logout)

	v1 := router.Group("/api/v1")
	{
		v1.POST("/auth/login", rateLimiter, authHandler.Login)
		v1.POST("/auth/refresh", rateLimiter, authHandler.Refresh)
		v1.POST("/auth/logout", authHandler.Logout)
	}
}

func registerDisbursementRoutes(router *gin.Engine, jwtSecret string, jwtIssuer string, jwtAudience string, handler *DisbursementHandler, metricsCollector *metrics.MetricsCollector) {
	authMiddleware := middleware.Authenticate(jwtSecret, jwtIssuer, jwtAudience, metricsCollector)

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
