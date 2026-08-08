package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"disbursment-api/internal/domain"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestAuthenticateMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "test-secret-key"

	userID := uuid.New()
	validClaims := jwt.MapClaims{
		"sub":      userID.String(),
		"username": "operator1",
		"role":     "OPERATOR",
		"exp":      time.Now().Add(1 * time.Hour).Unix(),
	}
	validToken := jwt.NewWithClaims(jwt.SigningMethodHS256, validClaims)
	validTokenString, _ := validToken.SignedString([]byte(secret))

	expiredClaims := jwt.MapClaims{
		"sub":      userID.String(),
		"username": "operator1",
		"role":     "OPERATOR",
		"exp":      time.Now().Add(-1 * time.Hour).Unix(),
	}
	expiredToken := jwt.NewWithClaims(jwt.SigningMethodHS256, expiredClaims)
	expiredTokenString, _ := expiredToken.SignedString([]byte(secret))

	setupRouter := func() *gin.Engine {
		r := gin.New()
		r.Use(RequestID())
		r.Use(Authenticate(secret))
		r.GET("/protected", func(c *gin.Context) {
			identity, ok := UserIdentityFromContext(c.Request.Context())
			if !ok {
				c.String(http.StatusInternalServerError, "no user context")
				return
			}
			c.JSON(http.StatusOK, gin.H{
				"id":       identity.ID.String(),
				"username": identity.Username,
				"role":     string(identity.Role),
			})
		})
		return r
	}

	t.Run("missing authorization header returns 401 UNAUTHORIZED", func(t *testing.T) {
		router := setupRouter()
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/protected", nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected status 401, got %d", w.Code)
		}
	})

	t.Run("expired token returns 401 UNAUTHORIZED", func(t *testing.T) {
		router := setupRouter()
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", "Bearer "+expiredTokenString)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected status 401, got %d", w.Code)
		}
	})

	t.Run("valid token injects UserIdentity into context", func(t *testing.T) {
		router := setupRouter()
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", "Bearer "+validTokenString)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}
	})
}

func TestRequireRoleMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	setupRouter := func() *gin.Engine {
		r := gin.New()
		r.Use(RequestID())
		r.Use(func(c *gin.Context) {
			// Simulate injected user identity from mock header
			role := c.GetHeader("X-Test-Role")
			if role != "" {
				identity := domain.UserIdentity{
					ID:       uuid.New(),
					Username: "testuser",
					Role:     domain.UserRole(role),
				}
				ctx := c.Request.Context()
				c.Request = c.Request.WithContext(context.WithValue(ctx, userContextKey{}, identity))
			}
			c.Next()
		})
		r.GET("/admin-only", RequireRole(domain.RoleAdmin, domain.RoleSuperadmin), func(c *gin.Context) {
			c.String(http.StatusOK, "welcome admin")
		})
		return r
	}

	t.Run("operator role accessing admin route returns 403 FORBIDDEN", func(t *testing.T) {
		router := setupRouter()
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/admin-only", nil)
		req.Header.Set("X-Test-Role", "OPERATOR")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected status 403, got %d", w.Code)
		}
	})

	t.Run("admin role accessing admin route succeeds 200 OK", func(t *testing.T) {
		router := setupRouter()
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/admin-only", nil)
		req.Header.Set("X-Test-Role", "ADMIN")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}
	})
}
