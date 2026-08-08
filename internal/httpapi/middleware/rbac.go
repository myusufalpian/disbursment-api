package middleware

import (
	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi/response"

	"github.com/gin-gonic/gin"
)

func RequireRole(allowedRoles ...domain.UserRole) gin.HandlerFunc {
	allowed := make(map[domain.UserRole]bool, len(allowedRoles))
	for _, role := range allowedRoles {
		allowed[role] = true
	}

	return func(c *gin.Context) {
		identity, ok := UserIdentityFromContext(c.Request.Context())
		if !ok {
			response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), domain.Unauthorized())
			c.Abort()
			return
		}

		if !allowed[identity.Role] {
			response.WriteError(c.Writer, RequestIDFromContext(c.Request.Context()), domain.Forbidden())
			c.Abort()
			return
		}

		c.Next()
	}
}
