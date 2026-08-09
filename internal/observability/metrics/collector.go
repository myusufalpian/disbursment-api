package metrics

import (
	"crypto/subtle"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

type MetricsCollector struct {
	mu                      sync.RWMutex
	httpRequestsTotal       map[string]*uint64
	httpDurationMsTotal     uint64
	httpDurationMsCount     uint64
	idempotencyClaimsTotal  map[string]*uint64
	finalizationsTotal      map[string]*uint64
	authFailuresTotal       map[string]*uint64
	outboxBacklogDepth      int64
	outboxDeliveriesTotal   uint64
	outboxDeliveryFailures  uint64
	outboxReconcileWarning  int64
	outboxReconcileCritical int64
	dbConnectionsOpen       int64
	dbConnectionsInUse      int64
	dbConnectionsIdle       int64
}

var (
	defaultCollector *MetricsCollector
	once             sync.Once
)

func Default() *MetricsCollector {
	once.Do(func() {
		defaultCollector = NewMetricsCollector()
	})
	return defaultCollector
}

func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		httpRequestsTotal:      make(map[string]*uint64),
		idempotencyClaimsTotal: make(map[string]*uint64),
		finalizationsTotal:     make(map[string]*uint64),
		authFailuresTotal:      make(map[string]*uint64),
	}
}

func (c *MetricsCollector) RecordHTTPRequest(method, path string, status int, durationMs int64) {
	key := method + " " + sanitizePath(path) + " " + httpStatusString(status)
	c.incMapCounter(c.httpRequestsTotal, key)
	if durationMs > 0 {
		atomic.AddUint64(&c.httpDurationMsTotal, uint64(durationMs))
	}
	atomic.AddUint64(&c.httpDurationMsCount, 1)
}

func (c *MetricsCollector) RecordIdempotencyClaim(result string) {
	c.incMapCounter(c.idempotencyClaimsTotal, result)
}

func (c *MetricsCollector) RecordFinalizationOutcome(outcome string) {
	c.incMapCounter(c.finalizationsTotal, outcome)
}

func (c *MetricsCollector) RecordAuthFailure(reason string) {
	c.incMapCounter(c.authFailuresTotal, reason)
}

func (c *MetricsCollector) RecordDeliverySuccess() {
	atomic.AddUint64(&c.outboxDeliveriesTotal, 1)
}

func (c *MetricsCollector) RecordDeliveryFailure() {
	atomic.AddUint64(&c.outboxDeliveryFailures, 1)
}

func (c *MetricsCollector) SetBacklogDepth(depth int) {
	atomic.StoreInt64(&c.outboxBacklogDepth, int64(depth))
}

func (c *MetricsCollector) SetReconciliationCounts(warning, critical int) {
	atomic.StoreInt64(&c.outboxReconcileWarning, int64(warning))
	atomic.StoreInt64(&c.outboxReconcileCritical, int64(critical))
}

func (c *MetricsCollector) UpdateDBStats(open, inUse, idle int) {
	atomic.StoreInt64(&c.dbConnectionsOpen, int64(open))
	atomic.StoreInt64(&c.dbConnectionsInUse, int64(inUse))
	atomic.StoreInt64(&c.dbConnectionsIdle, int64(idle))
}

func (c *MetricsCollector) incMapCounter(targetMap map[string]*uint64, key string) {
	c.mu.RLock()
	ptr, exists := targetMap[key]
	c.mu.RUnlock()

	if !exists {
		c.mu.Lock()
		ptr, exists = targetMap[key]
		if !exists {
			var val uint64
			ptr = &val
			targetMap[key] = ptr
		}
		c.mu.Unlock()
	}
	atomic.AddUint64(ptr, 1)
}

type MetricsSnapshot struct {
	HTTPRequestsTotal       map[string]uint64 `json:"http_requests_total"`
	HTTPDurationMsAverage   float64           `json:"http_duration_ms_average"`
	HTTPRequestsCount       uint64            `json:"http_requests_count"`
	IdempotencyClaimsTotal  map[string]uint64 `json:"idempotency_claims_total"`
	FinalizationsTotal      map[string]uint64 `json:"finalizations_total"`
	AuthFailuresTotal       map[string]uint64 `json:"auth_failures_total"`
	OutboxBacklogDepth      int64             `json:"outbox_backlog_depth"`
	OutboxDeliveriesTotal   uint64            `json:"outbox_deliveries_total"`
	OutboxDeliveryFailures  uint64            `json:"outbox_delivery_failures_total"`
	OutboxReconcileWarning  int64             `json:"outbox_reconcile_warning_count"`
	OutboxReconcileCritical int64             `json:"outbox_reconcile_critical_count"`
	DBConnectionsOpen       int64             `json:"db_connections_open"`
	DBConnectionsInUse      int64             `json:"db_connections_in_use"`
	DBConnectionsIdle       int64             `json:"db_connections_idle"`
}

func (c *MetricsCollector) Snapshot() MetricsSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()

	snapshot := MetricsSnapshot{
		HTTPRequestsTotal:       copyMap(c.httpRequestsTotal),
		HTTPRequestsCount:       atomic.LoadUint64(&c.httpDurationMsCount),
		IdempotencyClaimsTotal:  copyMap(c.idempotencyClaimsTotal),
		FinalizationsTotal:      copyMap(c.finalizationsTotal),
		AuthFailuresTotal:       copyMap(c.authFailuresTotal),
		OutboxBacklogDepth:      atomic.LoadInt64(&c.outboxBacklogDepth),
		OutboxDeliveriesTotal:   atomic.LoadUint64(&c.outboxDeliveriesTotal),
		OutboxDeliveryFailures:  atomic.LoadUint64(&c.outboxDeliveryFailures),
		OutboxReconcileWarning:  atomic.LoadInt64(&c.outboxReconcileWarning),
		OutboxReconcileCritical: atomic.LoadInt64(&c.outboxReconcileCritical),
		DBConnectionsOpen:       atomic.LoadInt64(&c.dbConnectionsOpen),
		DBConnectionsInUse:      atomic.LoadInt64(&c.dbConnectionsInUse),
		DBConnectionsIdle:       atomic.LoadInt64(&c.dbConnectionsIdle),
	}

	count := snapshot.HTTPRequestsCount
	totalMs := atomic.LoadUint64(&c.httpDurationMsTotal)
	if count > 0 {
		snapshot.HTTPDurationMsAverage = float64(totalMs) / float64(count)
	}

	return snapshot
}

func (c *MetricsCollector) HTTPHandler(metricsToken string) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if metricsToken == "" {
			ctx.Header("Cache-Control", "no-store, no-cache, must-revalidate")
			ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": "metrics authentication is not configured"})
			ctx.Abort()
			return
		}
		reqToken := ctx.GetHeader("X-Metrics-Token")
		if reqToken == "" || subtle.ConstantTimeCompare([]byte(reqToken), []byte(metricsToken)) != 1 {
			ctx.Header("Cache-Control", "no-store, no-cache, must-revalidate")
			ctx.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized metrics access"})
			ctx.Abort()
			return
		}

		snapshot := c.Snapshot()
		ctx.Header("Cache-Control", "no-store, no-cache, must-revalidate")
		ctx.Header("Content-Type", "application/json; charset=utf-8")
		ctx.JSON(http.StatusOK, snapshot)
	}
}

func copyMap(src map[string]*uint64) map[string]uint64 {
	dst := make(map[string]uint64, len(src))
	for k, v := range src {
		if v != nil {
			dst[k] = atomic.LoadUint64(v)
		}
	}
	return dst
}

var uuidRegex = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

func sanitizePath(path string) string {
	if path == "" {
		return "/"
	}
	return uuidRegex.ReplaceAllString(path, ":id")
}

func httpStatusString(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500:
		return "5xx"
	default:
		return "other"
	}
}
