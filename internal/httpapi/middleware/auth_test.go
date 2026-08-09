package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"disbursment-api/internal/domain"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const middlewareRequestID = "990e8400-e29b-41d4-a716-446655440000"

func fixedMiddlewareTime() time.Time {
	return time.Date(2099, time.January, 2, 3, 4, 5, 0, time.UTC)
}

func signedMiddlewareToken(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("failed to sign test token: %v", err)
	}
	return signed
}

func middlewareRequest(t *testing.T) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "/protected", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	request.Header.Set(RequestIDHeader, middlewareRequestID)
	return request
}

type middlewareErrorEnvelope struct {
	Success bool `json:"success"`
	Error   struct {
		Code    domain.ErrorCode `json:"code"`
		Message string           `json:"message"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}

func assertExactMiddlewareJSONKeys(t *testing.T, raw map[string]json.RawMessage, want ...string) {
	t.Helper()
	if len(raw) != len(want) {
		t.Fatalf("expected JSON keys %v, got %v", want, raw)
	}
	for _, key := range want {
		if _, ok := raw[key]; !ok {
			t.Fatalf("expected JSON key %q in %v", key, raw)
		}
	}
}

func assertMiddlewareError(t *testing.T, response *httptest.ResponseRecorder, status int, code domain.ErrorCode, message string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("expected status %d, got %d: %s", status, response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("expected exact JSON content type, got %q", got)
	}
	var envelope middlewareErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to parse error response: %v", err)
	}
	if envelope.Success || envelope.Error.Code != code || envelope.Error.Message != message || envelope.RequestID != middlewareRequestID {
		t.Fatalf("unexpected error envelope: %+v", envelope)
	}

	var rawEnvelope map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &rawEnvelope); err != nil {
		t.Fatalf("failed to parse raw error response: %v", err)
	}
	assertExactMiddlewareJSONKeys(t, rawEnvelope, "success", "error", "request_id")
	var rawError map[string]json.RawMessage
	if err := json.Unmarshal(rawEnvelope["error"], &rawError); err != nil {
		t.Fatalf("failed to parse raw error body: %v", err)
	}
	assertExactMiddlewareJSONKeys(t, rawError, "code", "message")
}

func TestAuthenticateMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "test-secret-key"
	issuer := "test-issuer"
	audience := "test-audience"
	userID := uuid.MustParse("aa0e8400-e29b-41d4-a716-446655440000")

	setupRouter := func() *gin.Engine {
		router := gin.New()
		router.Use(RequestID())
		router.Use(Authenticate(secret, issuer, audience, nil))
		router.GET("/protected", func(c *gin.Context) {
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
		return router
	}

	t.Run("missing authorization header returns exact UNAUTHORIZED envelope", func(t *testing.T) {
		response := httptest.NewRecorder()
		setupRouter().ServeHTTP(response, middlewareRequest(t))
		assertMiddlewareError(t, response, http.StatusUnauthorized, domain.CodeUnauthorized, "Autentikasi diperlukan")
	})

	t.Run("expired token returns exact UNAUTHORIZED envelope", func(t *testing.T) {
		claims := jwt.MapClaims{
			"sub":      userID.String(),
			"username": "operator1",
			"role":     "OPERATOR",
			"iss":      issuer,
			"aud":      audience,
			"iat":      time.Date(2000, time.January, 2, 3, 4, 5, 0, time.UTC).Unix(),
			"exp":      time.Date(2000, time.January, 2, 4, 4, 5, 0, time.UTC).Unix(),
		}
		request := middlewareRequest(t)
		request.Header.Set("Authorization", "Bearer "+signedMiddlewareToken(t, secret, claims))
		response := httptest.NewRecorder()
		setupRouter().ServeHTTP(response, request)
		assertMiddlewareError(t, response, http.StatusUnauthorized, domain.CodeUnauthorized, "Autentikasi diperlukan")
	})

	t.Run("audience mismatch returns exact UNAUTHORIZED envelope", func(t *testing.T) {
		claims := jwt.MapClaims{
			"sub":      userID.String(),
			"username": "operator1",
			"role":     "OPERATOR",
			"iss":      issuer,
			"aud":      "wrong-audience",
			"iat":      fixedMiddlewareTime().Unix(),
			"exp":      fixedMiddlewareTime().Add(time.Hour).Unix(),
		}
		request := middlewareRequest(t)
		request.Header.Set("Authorization", "Bearer "+signedMiddlewareToken(t, secret, claims))
		response := httptest.NewRecorder()
		setupRouter().ServeHTTP(response, request)
		assertMiddlewareError(t, response, http.StatusUnauthorized, domain.CodeUnauthorized, "Autentikasi diperlukan")
	})

	t.Run("missing audience claim returns exact UNAUTHORIZED envelope", func(t *testing.T) {
		claims := jwt.MapClaims{
			"sub":      userID.String(),
			"username": "operator1",
			"role":     "OPERATOR",
			"iss":      issuer,
			"iat":      fixedMiddlewareTime().Unix(),
			"exp":      fixedMiddlewareTime().Add(time.Hour).Unix(),
		}
		request := middlewareRequest(t)
		request.Header.Set("Authorization", "Bearer "+signedMiddlewareToken(t, secret, claims))
		response := httptest.NewRecorder()
		setupRouter().ServeHTTP(response, request)
		assertMiddlewareError(t, response, http.StatusUnauthorized, domain.CodeUnauthorized, "Autentikasi diperlukan")
	})

	t.Run("valid token injects the exact UserIdentity into context", func(t *testing.T) {
		claims := jwt.MapClaims{
			"sub":      userID.String(),
			"username": "operator1",
			"role":     "OPERATOR",
			"iss":      issuer,
			"aud":      audience,
			"iat":      fixedMiddlewareTime().Unix(),
			"exp":      fixedMiddlewareTime().Add(time.Hour).Unix(),
		}
		request := middlewareRequest(t)
		request.Header.Set("Authorization", "Bearer "+signedMiddlewareToken(t, secret, claims))
		response := httptest.NewRecorder()
		setupRouter().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", response.Code, response.Body.String())
		}
		var identity struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &identity); err != nil {
			t.Fatalf("failed to parse identity response: %v", err)
		}
		if identity.ID != userID.String() || identity.Username != "operator1" || identity.Role != "OPERATOR" {
			t.Fatalf("unexpected identity response: %+v", identity)
		}
	})
}

func signedMiddlewareTokenWithMethod(t *testing.T, method jwt.SigningMethod, key interface{}, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(method, claims)
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("failed to sign test token with %s: %v", method.Alg(), err)
	}
	return signed
}

func TestAuthenticateMiddlewareRejectsInvalidBearerCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "test-secret-key"
	issuer := "test-issuer"
	audience := "test-audience"
	userID := uuid.MustParse("aa0e8400-e29b-41d4-a716-446655440000")

	validClaims := func() jwt.MapClaims {
		return jwt.MapClaims{
			"sub":      userID.String(),
			"username": "operator1",
			"role":     "OPERATOR",
			"iss":      issuer,
			"aud":      audience,
			"iat":      fixedMiddlewareTime().Unix(),
			"exp":      fixedMiddlewareTime().Add(time.Hour).Unix(),
		}
	}

	type rejectionCase struct {
		name          string
		authorization func(t *testing.T) string
	}
	signed := func(method jwt.SigningMethod, key interface{}, claims jwt.MapClaims) func(*testing.T) string {
		return func(t *testing.T) string {
			return "Bearer " + signedMiddlewareTokenWithMethod(t, method, key, claims)
		}
	}

	tests := []rejectionCase{
		{
			name:          "malformed bearer scheme",
			authorization: func(t *testing.T) string { return "Basic token" },
		},
		{
			name:          "missing bearer token",
			authorization: func(t *testing.T) string { return "Bearer" },
		},
		{
			name:          "blank bearer token",
			authorization: func(t *testing.T) string { return "Bearer   " },
		},
		{
			name:          "extra bearer segments",
			authorization: func(t *testing.T) string { return "Bearer token extra" },
		},
		{
			name:          "wrong signing secret",
			authorization: signed(jwt.SigningMethodHS256, []byte("wrong-secret"), validClaims()),
		},
		{
			name:          "HS384 signing algorithm",
			authorization: signed(jwt.SigningMethodHS384, []byte(secret), validClaims()),
		},
		{
			name:          "none signing algorithm",
			authorization: signed(jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, validClaims()),
		},
		{
			name: "invalid subject",
			authorization: signed(jwt.SigningMethodHS256, []byte(secret), func() jwt.MapClaims {
				claims := validClaims()
				claims["sub"] = "not-a-uuid"
				return claims
			}()),
		},
		{
			name: "missing subject",
			authorization: signed(jwt.SigningMethodHS256, []byte(secret), func() jwt.MapClaims {
				claims := validClaims()
				delete(claims, "sub")
				return claims
			}()),
		},
		{
			name: "invalid role",
			authorization: signed(jwt.SigningMethodHS256, []byte(secret), func() jwt.MapClaims {
				claims := validClaims()
				claims["role"] = "UNKNOWN"
				return claims
			}()),
		},
		{
			name: "missing username",
			authorization: signed(jwt.SigningMethodHS256, []byte(secret), func() jwt.MapClaims {
				claims := validClaims()
				delete(claims, "username")
				return claims
			}()),
		},
		{
			name: "blank username",
			authorization: signed(jwt.SigningMethodHS256, []byte(secret), func() jwt.MapClaims {
				claims := validClaims()
				claims["username"] = "   "
				return claims
			}()),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			protectedReached := false
			router := gin.New()
			router.Use(RequestID(), Authenticate(secret, issuer, audience, nil))
			router.GET("/protected", func(c *gin.Context) {
				protectedReached = true
				c.String(http.StatusOK, "protected")
			})

			request := middlewareRequest(t)
			request.Header.Set("Authorization", test.authorization(t))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if protectedReached {
				t.Fatal("protected handler was reached for rejected credentials")
			}
			assertMiddlewareError(t, response, http.StatusUnauthorized, domain.CodeUnauthorized, "Autentikasi diperlukan")
		})
	}
}

func TestRequireRoleMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	setupRouter := func() *gin.Engine {
		router := gin.New()
		router.Use(RequestID())
		router.Use(func(c *gin.Context) {
			role := c.GetHeader("X-Test-Role")
			if role != "" {
				identity := domain.UserIdentity{
					ID:       uuid.MustParse("bb0e8400-e29b-41d4-a716-446655440000"),
					Username: "testuser",
					Role:     domain.UserRole(role),
				}
				ctx := c.Request.Context()
				c.Request = c.Request.WithContext(context.WithValue(ctx, userContextKey{}, identity))
			}
			c.Next()
		})
		router.GET("/admin-only", RequireRole(domain.RoleAdmin, domain.RoleSuperadmin), func(c *gin.Context) {
			c.String(http.StatusOK, "welcome admin")
		})
		return router
	}

	t.Run("operator role accessing admin route returns exact FORBIDDEN envelope", func(t *testing.T) {
		request := middlewareRequest(t)
		request.URL.Path = "/admin-only"
		request.Header.Set("X-Test-Role", "OPERATOR")
		response := httptest.NewRecorder()
		setupRouter().ServeHTTP(response, request)
		assertMiddlewareError(t, response, http.StatusForbidden, domain.CodeForbidden, "Akses ditolak")
	})

	t.Run("admin role accessing admin route succeeds 200 OK", func(t *testing.T) {
		request := middlewareRequest(t)
		request.URL.Path = "/admin-only"
		request.Header.Set("X-Test-Role", "ADMIN")
		response := httptest.NewRecorder()
		setupRouter().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", response.Code)
		}
	})

	t.Run("issuer mismatch returns exact UNAUTHORIZED envelope", func(t *testing.T) {
		secret := "secret"
		claims := jwt.MapClaims{
			"sub":      uuid.MustParse("cc0e8400-e29b-41d4-a716-446655440000").String(),
			"username": "user1",
			"role":     "ADMIN",
			"iss":      "wrong-issuer",
			"aud":      "expected-audience",
			"iat":      fixedMiddlewareTime().Unix(),
			"exp":      fixedMiddlewareTime().Add(time.Hour).Unix(),
		}
		request := middlewareRequest(t)
		request.Header.Set("Authorization", "Bearer "+signedMiddlewareToken(t, secret, claims))
		router := gin.New()
		router.Use(RequestID(), Authenticate(secret, "expected-issuer", "expected-audience", nil))
		router.GET("/protected", func(c *gin.Context) { c.String(http.StatusOK, "OK") })
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assertMiddlewareError(t, response, http.StatusUnauthorized, domain.CodeUnauthorized, "Autentikasi diperlukan")
	})

	t.Run("missing issuer claim returns exact UNAUTHORIZED envelope", func(t *testing.T) {
		secret := "secret"
		claims := jwt.MapClaims{
			"sub":      uuid.MustParse("dd0e8400-e29b-41d4-a716-446655440000").String(),
			"username": "user1",
			"role":     "ADMIN",
			"aud":      "expected-audience",
			"iat":      fixedMiddlewareTime().Unix(),
			"exp":      fixedMiddlewareTime().Add(time.Hour).Unix(),
		}
		request := middlewareRequest(t)
		request.Header.Set("Authorization", "Bearer "+signedMiddlewareToken(t, secret, claims))
		router := gin.New()
		router.Use(RequestID(), Authenticate(secret, "expected-issuer", "expected-audience", nil))
		router.GET("/protected", func(c *gin.Context) { c.String(http.StatusOK, "OK") })
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assertMiddlewareError(t, response, http.StatusUnauthorized, domain.CodeUnauthorized, "Autentikasi diperlukan")
	})
}
