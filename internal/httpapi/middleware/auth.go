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
	return AuthenticateWithKeyProvider(domain.NewStaticKeyProvider("v1", jwtSecret, nil), expectedIssuer, expectedAudience, collector)
}

func AuthenticateWithKeyProvider(keyProvider domain.KeyProvider, expectedIssuer string, expectedAudience string, collector *metrics.MetricsCollector) gin.HandlerFunc {
	return func(c *gin.Context) {
		if keyProvider == nil {
			if collector != nil {
				collector.RecordAuthFailure("unauthorized")
			}
			response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), domain.Unauthorized())
			c.Abort()
			return
		}
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
		parseTokenWithSecret := func(sec []byte) (*jwt.Token, error) {
			return jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
				if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
					return nil, fmt.Errorf("unexpected signing algorithm: %v", t.Header["alg"])
				}
				return sec, nil
			})
		}

		var token *jwt.Token
		var parseErr error

		var kid string
		hasKid := false
		if rawToken, _, err := new(jwt.Parser).ParseUnverified(tokenString, jwt.MapClaims{}); err == nil {
			if rawKid, exists := rawToken.Header["kid"]; exists {
				parsedKid, ok := rawKid.(string)
				if !ok {
					if collector != nil {
						collector.RecordAuthFailure("unauthorized")
					}
					response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), domain.Unauthorized())
					c.Abort()
					return
				}
				kid = parsedKid
				hasKid = true
			}
		}

		if hasKid {
			secret, ok := keyProvider.GetKey(kid)
			if !ok {
				if collector != nil {
					collector.RecordAuthFailure("unauthorized")
				}
				response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), domain.Unauthorized())
				c.Abort()
				return
			}
			token, parseErr = parseTokenWithSecret(secret)
		} else {
			// Legacy token without kid header claim: try active key first, then fallback to legacy keys
			activeKid, activeSecret := keyProvider.ActiveKey()
			if len(activeSecret) > 0 {
				token, parseErr = parseTokenWithSecret(activeSecret)
			}
			if parseErr != nil || token == nil || !token.Valid {
				for k, secret := range keyProvider.AllKeys() {
					if k == activeKid {
						continue
					}
					if t, err := parseTokenWithSecret(secret); err == nil && t != nil && t.Valid {
						token = t
						parseErr = nil
						break
					}
				}
			}
		}

		if parseErr != nil || token == nil || !token.Valid {
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

		username, usernameOK := claims["username"].(string)
		if !usernameOK || strings.TrimSpace(username) == "" {
			if collector != nil {
				collector.RecordAuthFailure("unauthorized")
			}
			response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), domain.Unauthorized())
			c.Abort()
			return
		}

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
