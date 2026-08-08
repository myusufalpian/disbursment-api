package postgres

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/repository"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

const insertIdempotencyClaim = `
INSERT INTO idempotency_keys (
    user_id, endpoint, key, request_hash, state, claim_id, lease_until, expires_at
) VALUES (
    $1, $2, $3, $4, 'IN_PROGRESS', $5, $6, $7
)
ON CONFLICT (user_id, endpoint, key) DO NOTHING
RETURNING claim_id`

const selectIdempotencyClaimForUpdate = `
SELECT request_hash, state, claim_id, lease_until, expires_at,
       disbursement_id, response_status, response_body
FROM idempotency_keys
WHERE user_id = $1 AND endpoint = $2 AND key = $3
FOR UPDATE`

const reclaimIdempotencyClaim = `
UPDATE idempotency_keys
SET request_hash = $1,
    state = 'IN_PROGRESS',
    claim_id = $2,
    lease_until = $3,
    expires_at = $4,
    disbursement_id = NULL,
    response_status = NULL,
    response_body = NULL,
    updated_at = now()
WHERE user_id = $5
  AND endpoint = $6
  AND key = $7
  AND (
      expires_at <= $8
      OR (
          state = 'IN_PROGRESS'
          AND request_hash = $1
          AND lease_until <= $8
      )
  )`

const verifyIdempotencyOwner = `
SELECT state, claim_id
FROM idempotency_keys
WHERE user_id = $1 AND endpoint = $2 AND key = $3
FOR UPDATE`

const completeIdempotencyClaim = `
UPDATE idempotency_keys
SET state = 'COMPLETED',
    disbursement_id = $1,
    response_status = $2,
    response_body = $3,
    updated_at = $4
WHERE user_id = $5
  AND endpoint = $6
  AND key = $7
  AND claim_id = $8
  AND state = 'IN_PROGRESS'`

const releaseIdempotencyClaim = `
DELETE FROM idempotency_keys
WHERE user_id = $1
  AND endpoint = $2
  AND key = $3
  AND claim_id = $4
  AND state = 'IN_PROGRESS'`

type IdempotencyStore struct {
	database *sqlx.DB
}

type storedIdempotencyClaim struct {
	requestHash    []byte
	state          domain.IdempotencyState
	claimID        uuid.UUID
	leaseUntil     time.Time
	expiresAt      time.Time
	disbursementID sql.NullString
	responseStatus sql.NullInt16
	responseBody   []byte
}

func NewIdempotencyStore(database *sqlx.DB) *IdempotencyStore {
	return &IdempotencyStore{database: database}
}

func (store *IdempotencyStore) Acquire(ctx context.Context, request domain.IdempotencyClaimRequest) (domain.IdempotencyClaimResult, error) {
	if !request.Valid() {
		return domain.IdempotencyClaimResult{}, repository.NewError(repository.ErrorConstraint, fmt.Errorf("invalid idempotency claim request"))
	}

	var claimID uuid.UUID
	err := store.database.QueryRowxContext(
		ctx,
		insertIdempotencyClaim,
		request.Scope.UserID,
		request.Scope.Endpoint,
		request.Scope.Key,
		request.Fingerprint[:],
		request.ClaimID,
		request.LeaseUntil,
		request.ExpiresAt,
	).Scan(&claimID)
	if err == nil {
		return domain.IdempotencyClaimResult{Outcome: domain.ClaimAcquired, ClaimID: claimID}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.IdempotencyClaimResult{}, repository.Classify(err)
	}
	return store.acquireExisting(ctx, request)
}

func (store *IdempotencyStore) VerifyOwnership(ctx context.Context, transaction repository.Transaction, scope domain.IdempotencyScope, claimID uuid.UUID) error {
	if !scope.Valid() || claimID == uuid.Nil {
		return repository.NewError(repository.ErrorConstraint, fmt.Errorf("invalid idempotency ownership check"))
	}
	tx, err := unwrapTransaction(transaction)
	if err != nil {
		return err
	}

	var state domain.IdempotencyState
	var storedClaimID uuid.UUID
	err = tx.QueryRowxContext(ctx, verifyIdempotencyOwner, scope.UserID, scope.Endpoint, scope.Key).Scan(&state, &storedClaimID)
	if err != nil {
		return repository.Classify(err)
	}
	if state != domain.IdempotencyInProgress || storedClaimID != claimID {
		return repository.NewError(repository.ErrorOwnershipLost, fmt.Errorf("idempotency ownership no longer held"))
	}
	return nil
}

func (store *IdempotencyStore) Complete(ctx context.Context, transaction repository.Transaction, completion domain.IdempotencyCompletion) error {
	if !completion.Valid() {
		return repository.NewError(repository.ErrorConstraint, fmt.Errorf("invalid idempotency completion"))
	}
	tx, err := unwrapTransaction(transaction)
	if err != nil {
		return err
	}

	result, err := tx.ExecContext(
		ctx,
		completeIdempotencyClaim,
		completion.Response.DisbursementID,
		completion.Response.StatusCode,
		completion.Response.Body,
		completion.CompletedAt.UTC(),
		completion.Scope.UserID,
		completion.Scope.Endpoint,
		completion.Scope.Key,
		completion.ClaimID,
	)
	if err != nil {
		return repository.Classify(err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return repository.Classify(err)
	}
	if rowsAffected != 1 {
		return repository.NewError(repository.ErrorOwnershipLost, fmt.Errorf("idempotency completion ownership lost"))
	}
	return nil
}

func (store *IdempotencyStore) acquireExisting(ctx context.Context, request domain.IdempotencyClaimRequest) (domain.IdempotencyClaimResult, error) {
	tx, err := store.database.BeginTxx(ctx, nil)
	if err != nil {
		return domain.IdempotencyClaimResult{}, repository.Classify(err)
	}
	defer tx.Rollback()

	stored, err := loadIdempotencyClaim(ctx, tx, request.Scope)
	if err != nil {
		return domain.IdempotencyClaimResult{}, err
	}
	result, reclaim := classifyIdempotencyClaim(stored, request)
	if !reclaim {
		if err := tx.Commit(); err != nil {
			return domain.IdempotencyClaimResult{}, repository.Classify(err)
		}
		return result, nil
	}

	update, err := tx.ExecContext(
		ctx,
		reclaimIdempotencyClaim,
		request.Fingerprint[:],
		request.ClaimID,
		request.LeaseUntil,
		request.ExpiresAt,
		request.Scope.UserID,
		request.Scope.Endpoint,
		request.Scope.Key,
		request.Now,
	)
	if err != nil {
		return domain.IdempotencyClaimResult{}, repository.Classify(err)
	}
	rowsAffected, err := update.RowsAffected()
	if err != nil {
		return domain.IdempotencyClaimResult{}, repository.Classify(err)
	}
	if rowsAffected != 1 {
		return domain.IdempotencyClaimResult{}, repository.NewError(repository.ErrorConflict, fmt.Errorf("idempotency claim changed before reclaim"))
	}
	if err := tx.Commit(); err != nil {
		return domain.IdempotencyClaimResult{}, repository.Classify(err)
	}
	return domain.IdempotencyClaimResult{Outcome: domain.ClaimAcquired, ClaimID: request.ClaimID}, nil
}

func loadIdempotencyClaim(ctx context.Context, transaction *sqlx.Tx, scope domain.IdempotencyScope) (storedIdempotencyClaim, error) {
	stored := storedIdempotencyClaim{}
	err := transaction.QueryRowxContext(ctx, selectIdempotencyClaimForUpdate, scope.UserID, scope.Endpoint, scope.Key).Scan(
		&stored.requestHash,
		&stored.state,
		&stored.claimID,
		&stored.leaseUntil,
		&stored.expiresAt,
		&stored.disbursementID,
		&stored.responseStatus,
		&stored.responseBody,
	)
	if err != nil {
		return storedIdempotencyClaim{}, repository.Classify(err)
	}
	stored.leaseUntil = stored.leaseUntil.UTC()
	stored.expiresAt = stored.expiresAt.UTC()
	return stored, nil
}

func classifyIdempotencyClaim(stored storedIdempotencyClaim, request domain.IdempotencyClaimRequest) (domain.IdempotencyClaimResult, bool) {
	if stored.expiresAt.After(request.Now) && subtle.ConstantTimeCompare(stored.requestHash, request.Fingerprint[:]) != 1 {
		return domain.IdempotencyClaimResult{Outcome: domain.ClaimReused}, false
	}
	if stored.state == domain.IdempotencyCompleted && stored.expiresAt.After(request.Now) {
		replay, err := stored.replayResponse()
		if err != nil {
			return domain.IdempotencyClaimResult{Outcome: domain.ClaimReused}, false
		}
		return domain.IdempotencyClaimResult{Outcome: domain.ClaimReplayed, Replay: &replay}, false
	}
	if stored.state == domain.IdempotencyInProgress && stored.expiresAt.After(request.Now) && stored.leaseUntil.After(request.Now) {
		remaining := stored.leaseUntil.Sub(request.Now)
		return domain.IdempotencyClaimResult{
			Outcome:    domain.ClaimInProgress,
			RetryAfter: time.Duration(math.Ceil(remaining.Seconds())) * time.Second,
		}, false
	}
	return domain.IdempotencyClaimResult{}, true
}

func (stored storedIdempotencyClaim) replayResponse() (domain.ReplayResponse, error) {
	if !stored.disbursementID.Valid || !stored.responseStatus.Valid || !json.Valid(stored.responseBody) {
		return domain.ReplayResponse{}, fmt.Errorf("invalid completed idempotency record")
	}
	disbursementID, err := uuid.Parse(stored.disbursementID.String)
	if err != nil {
		return domain.ReplayResponse{}, err
	}
	return domain.ReplayResponse{
		StatusCode:     int(stored.responseStatus.Int16),
		Body:           json.RawMessage(stored.responseBody),
		DisbursementID: disbursementID,
	}, nil
}

func (store *IdempotencyStore) Release(ctx context.Context, scope domain.IdempotencyScope, claimID uuid.UUID) error {
	if !scope.Valid() || claimID == uuid.Nil {
		return repository.NewError(repository.ErrorConstraint, fmt.Errorf("invalid idempotency release"))
	}
	result, err := store.database.ExecContext(ctx, releaseIdempotencyClaim, scope.UserID, scope.Endpoint, scope.Key, claimID)
	if err != nil {
		return repository.Classify(err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return repository.Classify(err)
	}
	if rowsAffected != 1 {
		return repository.NewError(repository.ErrorOwnershipLost, fmt.Errorf("idempotency release ownership lost"))
	}
	return nil
}
