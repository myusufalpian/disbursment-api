package domain

import (
	"github.com/google/uuid"
)

type UserRole string

const (
	RoleOperator   UserRole = "OPERATOR"
	RoleAdmin      UserRole = "ADMIN"
	RoleSuperadmin UserRole = "SUPERADMIN"
)

func (role UserRole) IsValid() bool {
	return role == RoleOperator || role == RoleAdmin || role == RoleSuperadmin
}

type User struct {
	ID           uuid.UUID
	Username     string
	PasswordHash string
	Role         UserRole
}

type UserIdentity struct {
	ID       uuid.UUID
	Username string
	Role     UserRole
}
