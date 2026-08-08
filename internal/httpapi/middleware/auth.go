package middleware

import (
	"context"
	"fmt"
	"strings"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi/response"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type userContextKey struct{}

func Authenticate(jwtSecret string) gin.HandlerFunc {
	secretBytes := []byte(jwtSecret)

	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), domain.Unauthorized())
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
			response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), domain.Unauthorized())
			c.Abort()
			return
		}

		tokenString := strings.TrimSpace(parts[1])
		token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return secretBytes, nil
		})

		if err != nil || token == nil || !token.Valid {
			response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), domain.Unauthorized())
			c.Abort()
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), domain.Unauthorized())
			c.Abort()
			return
		}

		subStr, _ := claims["sub"].(string)
		userID, err := uuid.Parse(subStr)
		if err != nil {
			response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), domain.Unauthorized())
			c.Abort()
			return
		}

		username, _ := claims["username"].(string)
		roleStr, _ := claims["role"].(string)
		role := domain.UserRole(roleStr)

		if !role.IsValid() {
			response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), domain.Unauthorized())
			c.Abort()
			return
		}

		userIdentity := domain.UserIdentity{
			ID:       userID,
			Username: username,
			Role:     role,
		}

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
