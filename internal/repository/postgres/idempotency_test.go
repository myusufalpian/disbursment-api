package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
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

	t.Run("Acquire existing claim fallback scenarios", func(t *testing.T) {
		req := domain.IdempotencyClaimRequest{
			Scope:       domain.IdempotencyScope{UserID: userID, Endpoint: endpoint, Key: key},
			Fingerprint: fingerprint,
			ClaimID:     claimID,
			LeaseUntil:  leaseUntil,
			ExpiresAt:   expiresAt,
			Now:         now,
		}

		// 1. Existing COMPLETED claim with matching fingerprint -> Replayed
		mock.ExpectQuery("^INSERT INTO idempotency_keys").
			WillReturnError(sql.ErrNoRows)

		mock.ExpectBegin()
		disbursementID := uuid.New()
		existingRows := sqlmock.NewRows([]string{
			"request_hash", "state", "claim_id", "lease_until", "expires_at", "disbursement_id", "response_status", "response_body",
		}).AddRow(
			fingerprint[:], "COMPLETED", claimID, leaseUntil, expiresAt, disbursementID, 201, []byte(`{"status":"PENDING"}`),
		)

		mock.ExpectQuery("^SELECT request_hash, state, claim_id").
			WithArgs(userID, endpoint, key).
			WillReturnRows(existingRows)
		mock.ExpectCommit()

		res, err := store.Acquire(context.Background(), req)
		if err != nil {
			t.Fatalf("Acquire existing failed: %v", err)
		}
		if res.Outcome != domain.ClaimReplayed {
			t.Errorf("expected ClaimReplayed, got %s", res.Outcome)
		}
		if res.Replay == nil || res.Replay.StatusCode != 201 {
			t.Errorf("expected 201 status code in replayed response, got %+v", res.Replay)
		}

		// 2. Existing IN_PROGRESS claim with expired lease -> Reclaimed
		mock.ExpectQuery("^INSERT INTO idempotency_keys").
			WillReturnError(sql.ErrNoRows)

		mock.ExpectBegin()
		pastLease := now.Add(-10 * time.Second)
		expiredLeaseRows := sqlmock.NewRows([]string{
			"request_hash", "state", "claim_id", "lease_until", "expires_at", "disbursement_id", "response_status", "response_body",
		}).AddRow(
			fingerprint[:], "IN_PROGRESS", uuid.New(), pastLease, expiresAt, nil, nil, nil,
		)

		mock.ExpectQuery("^SELECT request_hash, state, claim_id").
			WithArgs(userID, endpoint, key).
			WillReturnRows(expiredLeaseRows)

		mock.ExpectExec("^UPDATE idempotency_keys SET request_hash = \\$1").
			WithArgs(fingerprint[:], claimID, leaseUntil, expiresAt, userID, endpoint, key, now).
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()

		res2, err := store.Acquire(context.Background(), req)
		if err != nil {
			t.Logf("subtest 2 err = %v", err)
			t.Fatalf("Acquire expired lease failed: %v", err)
		}
		if res2.Outcome != domain.ClaimAcquired {
			t.Errorf("expected ClaimAcquired after lease reclaim, got %s", res2.Outcome)
		}

		// 3. Existing claim with fingerprint mismatch -> Reused
		mock.ExpectQuery("^INSERT INTO idempotency_keys").
			WillReturnError(sql.ErrNoRows)

		mock.ExpectBegin()
		diffFingerprint := sha256.Sum256([]byte("different payload"))
		mismatchRows := sqlmock.NewRows([]string{
			"request_hash", "state", "claim_id", "lease_until", "expires_at", "disbursement_id", "response_status", "response_body",
		}).AddRow(
			diffFingerprint[:], "IN_PROGRESS", uuid.New(), leaseUntil, expiresAt, nil, nil, nil,
		)

		mock.ExpectQuery("^SELECT request_hash, state, claim_id").
			WithArgs(userID, endpoint, key).
			WillReturnRows(mismatchRows)
		mock.ExpectCommit()

		res3, err := store.Acquire(context.Background(), req)
		if err != nil {
			t.Fatalf("Acquire mismatch failed: %v", err)
		}
		if res3.Outcome != domain.ClaimReused {
			t.Errorf("expected ClaimReused, got %s", res3.Outcome)
		}
	})
}
