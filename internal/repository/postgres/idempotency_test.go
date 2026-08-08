package postgres

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"disbursment-api/internal/domain"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

func TestIdempotencyStore_AcquireAndComplete(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	sqlxDB := sqlx.NewDb(db, "sqlmock")
	store := NewIdempotencyStore(sqlxDB)

	userID := uuid.New()
	endpoint := "POST /disbursements"
	key := uuid.New()
	claimID := uuid.New()
	now := time.Now().UTC()
	leaseUntil := now.Add(30 * time.Second)
	expiresAt := now.Add(24 * time.Hour)
	fingerprint := sha256.Sum256([]byte("payload"))

	t.Run("Acquire new claim success", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"claim_id"}).AddRow(claimID)
		mock.ExpectQuery("^INSERT INTO idempotency_keys").
			WithArgs(userID, endpoint, key, fingerprint[:], claimID, leaseUntil, expiresAt).
			WillReturnRows(rows)

		req := domain.IdempotencyClaimRequest{
			Scope:       domain.IdempotencyScope{UserID: userID, Endpoint: endpoint, Key: key},
			Fingerprint: fingerprint,
			ClaimID:     claimID,
			LeaseUntil:  leaseUntil,
			ExpiresAt:   expiresAt,
			Now:         now,
		}

		res, err := store.Acquire(context.Background(), req)
		if err != nil {
			t.Fatalf("Acquire failed: %v", err)
		}
		if res.Outcome != domain.ClaimAcquired {
			t.Errorf("expected ClaimAcquired, got %s", res.Outcome)
		}
	})

	t.Run("VerifyOwnership success", func(t *testing.T) {
		mock.ExpectBegin()
		rows := sqlmock.NewRows([]string{"state", "claim_id"}).AddRow("IN_PROGRESS", claimID)
		mock.ExpectQuery("^SELECT state, claim_id FROM idempotency_keys").
			WithArgs(userID, endpoint, key).
			WillReturnRows(rows)

		tx, _ := sqlxDB.BeginTxx(context.Background(), nil)
		scope := domain.IdempotencyScope{UserID: userID, Endpoint: endpoint, Key: key}

		err := store.VerifyOwnership(context.Background(), newTestTx(tx), scope, claimID)
		if err != nil {
			t.Fatalf("VerifyOwnership failed: %v", err)
		}
	})

	t.Run("Complete claim success", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectExec("^UPDATE idempotency_keys SET state = 'COMPLETED'").
			WithArgs(sqlmock.AnyArg(), 201, sqlmock.AnyArg(), sqlmock.AnyArg(), userID, endpoint, key, claimID).
			WillReturnResult(sqlmock.NewResult(1, 1))

		tx, _ := sqlxDB.BeginTxx(context.Background(), nil)
		completion := domain.IdempotencyCompletion{
			Scope:       domain.IdempotencyScope{UserID: userID, Endpoint: endpoint, Key: key},
			ClaimID:     claimID,
			Response:    domain.ReplayResponse{StatusCode: 201, Body: []byte(`{"status":"PENDING"}`), DisbursementID: uuid.New()},
			CompletedAt: now,
		}

		err := store.Complete(context.Background(), newTestTx(tx), completion)
		if err != nil {
			t.Fatalf("Complete failed: %v", err)
		}
	})

	t.Run("Release claim success", func(t *testing.T) {
		mock.ExpectExec("^DELETE FROM idempotency_keys").
			WithArgs(userID, endpoint, key, claimID).
			WillReturnResult(sqlmock.NewResult(1, 1))

		scope := domain.IdempotencyScope{UserID: userID, Endpoint: endpoint, Key: key}
		err := store.Release(context.Background(), scope, claimID)
		if err != nil {
			t.Fatalf("Release failed: %v", err)
		}
	})
}
