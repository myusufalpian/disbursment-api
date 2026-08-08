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
	ID           uuid.UUID `db:"id"`
	Username     string    `db:"username"`
	PasswordHash string    `db:"password_hash"`
	Role         string    `db:"role"`
}

type RefreshSession struct {
	ID           uuid.UUID  `db:"id"`
	UserID       uuid.UUID  `db:"user_id"`
	TokenHash    string     `db:"token_hash"`
	ExpiresAt    time.Time  `db:"expires_at"`
	RevokedAt    *time.Time `db:"revoked_at"`
	ReplacedByID *uuid.UUID `db:"replaced_by_id"`
}

type UserStore interface {
	FindByID(context.Context, uuid.UUID) (User, error)
	FindByUsername(context.Context, string) (User, error)
}

type RefreshSessionStore interface {
	Create(context.Context, Transaction, RefreshSession) error
	FindByTokenHash(context.Context, Transaction, string) (RefreshSession, error)
	Rotate(context.Context, Transaction, string, RefreshSession, time.Time) error
	RevokeByTokenHash(context.Context, Transaction, string, time.Time) error
}

type DisbursementFilter struct {
	Page      int
	Limit     int
	Status    domain.DisbursementStatus
	Search    string
	SortBy    string
	SortOrder string
	DateRange *domain.DateRange
}

type DisbursementStore interface {
	FindByID(context.Context, uuid.UUID) (domain.Disbursement, error)
	Insert(context.Context, Transaction, domain.Disbursement) error
	List(context.Context, DisbursementFilter) ([]domain.Disbursement, int, error)
	UpdateStatus(context.Context, Transaction, uuid.UUID, domain.Decision) (domain.Disbursement, error)
	SoftDelete(context.Context, Transaction, uuid.UUID) (domain.Disbursement, bool, error)
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
