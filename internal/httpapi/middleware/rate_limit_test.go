package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRateLimitMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	limiter := newIPRateLimiter(3, 100*time.Millisecond)
	currentTime := time.Date(2026, time.August, 9, 16, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return currentTime }

	router := gin.New()
	router.Use(rateLimitHandler(limiter, nil))
	router.POST("/auth/login", func(c *gin.Context) {
		c.String(http.StatusOK, "OK")
	})

	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/auth/login", nil)
		req.RemoteAddr = "192.168.1.1:1234"
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("request %d expected 200 OK, got %d", i+1, w.Code)
		}
	}

	// 4th request within window should return 429 Too Many Requests
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/auth/login", nil)
	req.RemoteAddr = "192.168.1.1:1234"
	router.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests, got %d", w.Code)
	}
}

func TestRateLimitRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		limit  int
		window time.Duration
	}{
		{name: "zero limit", limit: 0, window: time.Minute},
		{name: "negative limit", limit: -1, window: time.Minute},
		{name: "zero window", limit: 10, window: 0},
		{name: "negative window", limit: 10, window: -time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("expected invalid rate-limit configuration to panic")
				}
			}()
			newIPRateLimiter(test.limit, test.window)
		})
	}
}

func TestRateLimitCleanupAndMaxClients(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("cleanupExpired invocation", func(t *testing.T) {
		limiter := newIPRateLimiter(1000, 10*time.Millisecond)
		now := time.Date(2026, time.August, 9, 7, 0, 0, 0, time.UTC)
		limiter.requests["expired"] = []time.Time{now.Add(-time.Second)}
		limiter.requests["active"] = []time.Time{now}

		limiter.cleanupExpired(now)

		if _, ok := limiter.requests["expired"]; ok {
			t.Fatal("expected expired client state to be removed")
		}
		if len(limiter.requests["active"]) != 1 {
			t.Fatalf("active client timestamps = %d, want 1", len(limiter.requests["active"]))
		}
	})

	t.Run("maxTrackedClients reached", func(t *testing.T) {
		limiter := newIPRateLimiter(10, time.Minute)
		now := time.Date(2026, time.August, 9, 7, 0, 0, 0, time.UTC)
		limiter.now = func() time.Time { return now }
		for i := 0; i < maxTrackedClients; i++ {
			limiter.requests[string(rune(i))] = []time.Time{now}
		}

		router := gin.New()
		router.Use(rateLimitHandler(limiter, nil))
		router.GET("/limited", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest(http.MethodGet, "/limited", nil)
		req.RemoteAddr = "192.168.1.2:1234"
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("expected 429 Too Many Requests, got %d", w.Code)
		}
		if got := w.Header().Get("Retry-After"); got != "60" {
			t.Fatalf("Retry-After = %q, want %q", got, "60")
		}
	})
}
