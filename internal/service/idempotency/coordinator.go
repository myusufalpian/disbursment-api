package idempotency

import (
	"context"
	"fmt"
	"strings"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/repository"

	"github.com/google/uuid"
)

type Clock interface {
	Now() time.Time
}

type UUIDGenerator interface {
	New() (uuid.UUID, error)
}

type Coordinator struct {
	store     repository.IdempotencyStore
	clock     Clock
	generator UUIDGenerator
	leaseTTL  time.Duration
	replayTTL time.Duration
}

type systemClock struct{}

type randomUUIDGenerator struct{}

func NewCoordinator(store repository.IdempotencyStore, clock Clock, generator UUIDGenerator, leaseTTL time.Duration, replayTTL time.Duration) (*Coordinator, error) {
	if store == nil || clock == nil || generator == nil || leaseTTL <= 0 || replayTTL <= leaseTTL {
		return nil, fmt.Errorf("invalid idempotency coordinator configuration")
	}
	return &Coordinator{store: store, clock: clock, generator: generator, leaseTTL: leaseTTL, replayTTL: replayTTL}, nil
}

func NewDefaultCoordinator(store repository.IdempotencyStore, leaseTTL time.Duration, replayTTL time.Duration) (*Coordinator, error) {
	return NewCoordinator(store, systemClock{}, randomUUIDGenerator{}, leaseTTL, replayTTL)
}

func (coordinator *Coordinator) Claim(ctx context.Context, userID uuid.UUID, method string, endpoint string, keyValue string, payload any) (domain.IdempotencyClaimResult, error) {
	key, err := domain.ParseIdempotencyKey(keyValue)
	if err != nil {
		return domain.IdempotencyClaimResult{}, err
	}
	fingerprint, err := Fingerprint(method, endpoint, payload)
	if err != nil {
		return domain.IdempotencyClaimResult{}, err
	}
	claimID, err := coordinator.generator.New()
	if err != nil {
		return domain.IdempotencyClaimResult{}, fmt.Errorf("generate idempotency claim ID: %w", err)
	}
	now := coordinator.clock.Now().UTC()
	scope := domain.IdempotencyScope{UserID: userID, Endpoint: strings.TrimSpace(endpoint), Key: key}
	request := domain.IdempotencyClaimRequest{
		Scope:       scope,
		Fingerprint: fingerprint,
		ClaimID:     claimID,
		LeaseUntil:  now.Add(coordinator.leaseTTL),
		ExpiresAt:   now.Add(coordinator.replayTTL),
		Now:         now,
	}
	return coordinator.store.Acquire(ctx, request)
}

func (coordinator *Coordinator) VerifyOwnership(ctx context.Context, transaction repository.Transaction, scope domain.IdempotencyScope, claimID uuid.UUID) error {
	return coordinator.store.VerifyOwnership(ctx, transaction, scope, claimID)
}

func (coordinator *Coordinator) Complete(ctx context.Context, transaction repository.Transaction, completion domain.IdempotencyCompletion) error {
	canonicalBody, err := canonicalJSON(completion.Response.Body)
	if err != nil {
		return err
	}
	completion.Response.Body = canonicalBody
	return coordinator.store.Complete(ctx, transaction, completion)
}

func (coordinator *Coordinator) Release(ctx context.Context, scope domain.IdempotencyScope, claimID uuid.UUID) error {
	return coordinator.store.Release(ctx, scope, claimID)
}

func (systemClock) Now() time.Time {
	return time.Now()
}

func (randomUUIDGenerator) New() (uuid.UUID, error) {
	return uuid.NewRandom()
}
