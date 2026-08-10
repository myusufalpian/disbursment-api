package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

type HealthChecker interface {
	PingContext(ctx context.Context) error
}

type HealthResponse struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

var (
	healthUPResponse           = HealthResponse{Status: "UP"}
	healthDOWNResponse         = HealthResponse{Status: "DOWN", Reason: "database ping failed"}
	healthUnconfiguredResponse = HealthResponse{Status: "DOWN", Reason: "health checker not configured"}
)

type HealthHandler struct {
	db     HealthChecker
	logger *slog.Logger
}

func NewHealthHandler(db HealthChecker, logger *slog.Logger) *HealthHandler {
	return &HealthHandler{db: db, logger: logger}
}

func (h *HealthHandler) Healthz(c *gin.Context) {
	c.JSON(http.StatusOK, healthUPResponse)
}

func (h *HealthHandler) Readyz(c *gin.Context) {
	if h.db == nil {
		if h.logger != nil {
			h.logger.Warn("readiness probe failed: health checker not configured")
		}
		c.JSON(http.StatusServiceUnavailable, healthUnconfiguredResponse)
		return
	}

	if err := h.db.PingContext(c.Request.Context()); err != nil {
		if h.logger != nil {
			h.logger.Warn("readiness probe database ping failed", slog.String("error", err.Error()))
		}
		c.JSON(http.StatusServiceUnavailable, healthDOWNResponse)
		return
	}

	c.JSON(http.StatusOK, healthUPResponse)
}
