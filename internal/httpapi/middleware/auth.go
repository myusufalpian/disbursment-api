package middleware

import (
	"context"
	"crypto/subtle"
	"fmt"
	"strings"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi/response"
	"disbursment-api/internal/observability/metrics"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type userContextKey struct{}

func Authenticate(jwtSecret string, expectedIssuer string, expectedAudience string, collector *metrics.MetricsCollector) gin.HandlerFunc {
	secretBytes := []byte(jwtSecret)

	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			if collector != nil {
				collector.RecordAuthFailure("unauthorized")
			}
			response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), domain.Unauthorized())
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
			if collector != nil {
				collector.RecordAuthFailure("unauthorized")
			}
			response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), domain.Unauthorized())
			c.Abort()
			return
		}

		tokenString := strings.TrimSpace(parts[1])
		token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
			if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
				return nil, fmt.Errorf("unexpected signing algorithm: %v", t.Header["alg"])
			}
			return secretBytes, nil
		})

		if err != nil || token == nil || !token.Valid {
			if collector != nil {
				collector.RecordAuthFailure("unauthorized")
			}
			response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), domain.Unauthorized())
			c.Abort()
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			if collector != nil {
				collector.RecordAuthFailure("unauthorized")
			}
			response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), domain.Unauthorized())
			c.Abort()
			return
		}

		if expectedIssuer != "" {
			iss, ok := claims["iss"].(string)
			if !ok || subtle.ConstantTimeCompare([]byte(iss), []byte(expectedIssuer)) != 1 {
				if collector != nil {
					collector.RecordAuthFailure("unauthorized")
				}
				response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), domain.Unauthorized())
				c.Abort()
				return
			}
		}

		if expectedAudience != "" {
			aud, ok := claims["aud"].(string)
			if !ok || subtle.ConstantTimeCompare([]byte(aud), []byte(expectedAudience)) != 1 {
				if collector != nil {
					collector.RecordAuthFailure("unauthorized")
				}
				response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), domain.Unauthorized())
				c.Abort()
				return
			}
		}

		subStr, _ := claims["sub"].(string)
		userID, err := uuid.Parse(subStr)
		if err != nil {
			if collector != nil {
				collector.RecordAuthFailure("unauthorized")
			}
			response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), domain.Unauthorized())
			c.Abort()
			return
		}

		username, _ := claims["username"].(string)
		roleStr, _ := claims["role"].(string)
		role := domain.UserRole(roleStr)

		if !role.IsValid() {
			if collector != nil {
				collector.RecordAuthFailure("unauthorized")
			}
			response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), domain.Unauthorized())
			c.Abort()
			return
		}

		userIdentity := domain.UserIdentity{
			ID:       userID,
			Username: username,
			Role:     role,
		}

		c.Set("userID", userID.String())
		ctx := context.WithValue(c.Request.Context(), userContextKey{}, userIdentity)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func UserIdentityFromContext(ctx context.Context) (domain.UserIdentity, bool) {
	if ctx == nil {
		return domain.UserIdentity{}, false
	}
	identity, ok := ctx.Value(userContextKey{}).(domain.UserIdentity)
	return identity, ok
}

func ContextWithUserIdentity(ctx context.Context, identity domain.UserIdentity) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, userContextKey{}, identity)
}
