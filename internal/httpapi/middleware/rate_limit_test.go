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

	router := gin.New()
	router.Use(RateLimit(3, 100*time.Millisecond, nil))
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

	// Wait for window to expire
	time.Sleep(120 * time.Millisecond)

	// Subsequent request after window expiration should succeed again
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("POST", "/auth/login", nil)
	req2.RemoteAddr = "192.168.1.1:1234"
	router.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK after window expiration, got %d", w2.Code)
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
		router := gin.New()
		router.Use(RateLimit(1000, 10*time.Millisecond, nil))
		router.POST("/login", func(c *gin.Context) { c.Status(http.StatusOK) })

		// Make 257 requests to trigger cleanupAfter >= 256
		for i := 0; i < 257; i++ {
			w := httptest.NewRecorder()
			req, _ := http.NewRequest("POST", "/login", nil)
			req.RemoteAddr = "10.0.0.1:1234"
			router.ServeHTTP(w, req)
		}
	})

	t.Run("maxTrackedClients reached", func(t *testing.T) {
		limiter := newIPRateLimiter(10, time.Minute)
		// Populate requests map to maxTrackedClients
		for i := 0; i < maxTrackedClients; i++ {
			limiter.requests[string(rune(i))] = []time.Time{time.Now()}
		}

		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set("limiter", limiter)
			RateLimit(10, time.Minute, nil)(c)
		})
	})
}
