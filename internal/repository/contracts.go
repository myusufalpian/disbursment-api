package repository

import (
	"context"
	"time"

	"disbursment-api/internal/domain"

	"github.com/google/uuid"
)

type Transaction interface {
	Context() context.Context
}

type Transactor interface {
	WithinTransaction(context.Context, func(context.Context, Transaction) error) error
}

type User struct {
	ID           uuid.UUID
	Username     string
	PasswordHash string
	Role         string
}

type RefreshSession struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	TokenHash string
	ExpiresAt time.Time
	RevokedAt *time.Time
}

type UserStore interface {
	FindByID(context.Context, uuid.UUID) (User, error)
}

type RefreshSessionStore interface {
	FindByTokenHash(context.Context, string) (RefreshSession, error)
	Revoke(context.Context, Transaction, uuid.UUID, time.Time) error
}

type DisbursementStore interface {
	FindByID(context.Context, uuid.UUID) (domain.Disbursement, error)
	Insert(context.Context, Transaction, domain.Disbursement) error
}

type IdempotencyStore interface {
	Acquire(context.Context, domain.IdempotencyClaimRequest) (domain.IdempotencyClaimResult, error)
	VerifyOwnership(context.Context, Transaction, domain.IdempotencyScope, uuid.UUID) error
	Complete(context.Context, Transaction, domain.IdempotencyCompletion) error
	Release(context.Context, domain.IdempotencyScope, uuid.UUID) error
}

type AuditOutboxStore interface {
	Insert(context.Context, Transaction, domain.AuditEvent) error
}

type AuditProjectionStore interface {
	InsertProjection(context.Context, Transaction, domain.AuditEvent) error
}
