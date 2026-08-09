package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"disbursment-api/internal/domain"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestMiddleware_RBAC_BodyLimit_And_Auth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "test-jwt-secret-key-32-chars-ok"

	t.Run("BodyLimit middleware limits body reader", func(t *testing.T) {
		router := gin.New()
		router.Use(BodyLimit(10))
		router.POST("/test", func(c *gin.Context) {
			_, err := io.ReadAll(c.Request.Body)
			if err != nil {
				c.String(http.StatusRequestEntityTooLarge, err.Error())
				return
			}
			c.String(http.StatusOK, "OK")
		})

		largePayload := strings.Repeat("a", 100)
		req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(largePayload))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected HTTP 413, got %d", rec.Code)
		}
	})

	t.Run("RequireRole middleware grants allowed roles and forbids unauthorized roles", func(t *testing.T) {
		router := gin.New()
		router.Use(RequestID())
		router.Use(func(c *gin.Context) {
			identity := domain.UserIdentity{
				ID:       uuid.New(),
				Username: "testop",
				Role:     domain.RoleOperator,
			}
			ctx := ContextWithUserIdentity(c.Request.Context(), identity)
			c.Request = c.Request.WithContext(ctx)
			c.Next()
		})
		router.GET("/admin-only", RequireRole(domain.RoleAdmin), func(c *gin.Context) {
			c.String(http.StatusOK, "OK")
		})
		router.GET("/operator-allowed", RequireRole(domain.RoleOperator, domain.RoleAdmin), func(c *gin.Context) {
			c.String(http.StatusOK, "OK")
		})

		// Operator attempting admin route -> 403
		req1 := httptest.NewRequest(http.MethodGet, "/admin-only", nil)
		rec1 := httptest.NewRecorder()
		router.ServeHTTP(rec1, req1)
		if rec1.Code != http.StatusForbidden {
			t.Fatalf("expected HTTP 403, got %d", rec1.Code)
		}

		// Operator attempting operator route -> 200
		req2 := httptest.NewRequest(http.MethodGet, "/operator-allowed", nil)
		rec2 := httptest.NewRecorder()
		router.ServeHTTP(rec2, req2)
		if rec2.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d", rec2.Code)
		}
	})

	t.Run("Authenticate middleware validates Bearer token", func(t *testing.T) {
		router := gin.New()
		router.Use(Authenticate(secret, "", "", nil))
		router.GET("/protected", func(c *gin.Context) {
			c.String(http.StatusOK, "OK")
		})

		// Missing header -> 401
		req1 := httptest.NewRequest(http.MethodGet, "/protected", nil)
		rec1 := httptest.NewRecorder()
		router.ServeHTTP(rec1, req1)
		if rec1.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for missing auth header, got %d", rec1.Code)
		}

		// Valid token -> 200
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"sub":  "11111111-1111-1111-1111-111111111111",
			"role": string(domain.RoleAdmin),
			"exp":  time.Now().Add(time.Hour).Unix(),
		})
		signed, _ := token.SignedString([]byte(secret))

		req2 := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req2.Header.Set("Authorization", "Bearer "+signed)
		rec2 := httptest.NewRecorder()
		router.ServeHTTP(rec2, req2)
		if rec2.Code != http.StatusOK {
			t.Fatalf("expected 200 for valid token, got %d", rec2.Code)
		}
	})
}
