package middleware

import (
	"net/http"
	"sync"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi/response"
	"disbursment-api/internal/observability/metrics"

	"github.com/gin-gonic/gin"
)

type ipRateLimiter struct {
	mu           sync.Mutex
	requests     map[string][]time.Time
	limit        int
	window       time.Duration
	cleanupAfter uint64
}

const maxTrackedClients = 10_000

func newIPRateLimiter(limit int, window time.Duration) *ipRateLimiter {
	if limit <= 0 {
		panic("rate limit must be greater than zero")
	}
	if window <= 0 || 2*window <= 0 {
		panic("rate limit window must be positive")
	}
	limiter := &ipRateLimiter{
		requests: make(map[string][]time.Time),
		limit:    limit,
		window:   window,
	}

	return limiter
}

func RateLimit(limit int, window time.Duration, collector *metrics.MetricsCollector) gin.HandlerFunc {
	limiter := newIPRateLimiter(limit, window)

	return func(c *gin.Context) {
		clientIP := c.ClientIP()
		if clientIP == "" {
			clientIP = "unknown"
		}

		limiter.mu.Lock()
		now := time.Now()
		limiter.cleanupAfter++
		if limiter.cleanupAfter >= 256 {
			limiter.cleanupExpired(now)
			limiter.cleanupAfter = 0
		}

		times := limiter.requests[clientIP]
		var valid []time.Time
		for _, t := range times {
			if now.Sub(t) <= window {
				valid = append(valid, t)
			}
		}

		if len(valid) >= limiter.limit {
			limiter.requests[clientIP] = valid
			limiter.mu.Unlock()

			if collector != nil {
				collector.RecordAuthFailure("rate_limited")
			}

			c.Header("Retry-After", "60")
			response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), &domain.Error{
				Code:    "TOO_MANY_REQUESTS",
				Message: "Terlalu banyak percobaan, silakan coba beberapa saat lagi",
				Status:  http.StatusTooManyRequests,
			})
			c.Abort()
			return
		}

		if len(times) == 0 && len(limiter.requests) >= maxTrackedClients {
			limiter.mu.Unlock()
			c.Header("Retry-After", "60")
			response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), &domain.Error{
				Code:    "TOO_MANY_REQUESTS",
				Message: "Terlalu banyak percobaan, silakan coba beberapa saat lagi",
				Status:  http.StatusTooManyRequests,
			})
			c.Abort()
			return
		}

		valid = append(valid, now)
		limiter.requests[clientIP] = valid
		limiter.mu.Unlock()

		c.Next()
	}
}

func (l *ipRateLimiter) cleanupExpired(now time.Time) {
	for ip, times := range l.requests {
		var valid []time.Time
		for _, timestamp := range times {
			if now.Sub(timestamp) <= l.window {
				valid = append(valid, timestamp)
			}
		}
		if len(valid) == 0 {
			delete(l.requests, ip)
		} else {
			l.requests[ip] = valid
		}
	}
}
