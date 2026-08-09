package postgres

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"disbursment-api/internal/repository"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

func TestRefreshSessionStore(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("sqlmock expectations: %v", err)
		}
	})

	sqlxDB := sqlx.NewDb(db, "postgres")
	store := NewRefreshSessionStore(sqlxDB)

	userID := uuid.New()
	tokenHash := "hash123"
	newTokenHash := "hash456"
	now := time.Now().UTC()
	expiresAt := now.Add(24 * time.Hour)

	session := repository.RefreshSession{
		ID:        uuid.New(),
		UserID:    userID,
		TokenHash: tokenHash,
		ExpiresAt: expiresAt,
	}

	t.Run("Create refresh session success", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectExec("^INSERT INTO refresh_sessions").
			WithArgs(session.ID, session.UserID, session.TokenHash, session.ExpiresAt).
			WillReturnResult(sqlmock.NewResult(1, 1))

		tx := beginSQLMockTx(t, mock, sqlxDB)
		err := store.Create(context.Background(), newTestTx(tx), session)
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
	})

	t.Run("FindByTokenHash found", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"id", "user_id", "token_hash", "expires_at", "revoked_at", "replaced_by_id"}).
			AddRow(session.ID, session.UserID, session.TokenHash, session.ExpiresAt, nil, nil)

		mock.ExpectQuery("^SELECT (.+) FROM refresh_sessions WHERE token_hash = \\$1").
			WithArgs(tokenHash).
			WillReturnRows(rows)

		found, err := store.FindByTokenHash(context.Background(), nil, tokenHash)
		if err != nil {
			t.Fatalf("FindByTokenHash failed: %v", err)
		}
		if found.TokenHash != tokenHash {
			t.Errorf("expected %s, got %s", tokenHash, found.TokenHash)
		}
	})

	t.Run("FindByTokenHash not found", func(t *testing.T) {
		mock.ExpectQuery("^SELECT (.+) FROM refresh_sessions WHERE token_hash = \\$1").
			WithArgs("nonexistent").
			WillReturnError(sql.ErrNoRows)

		_, err := store.FindByTokenHash(context.Background(), nil, "nonexistent")
		if err == nil {
			t.Fatalf("expected error for nonexistent token hash")
		}
		if !repository.IsNotFound(err) {
			t.Errorf("expected IsNotFound error")
		}
	})

	t.Run("Rotate refresh token success", func(t *testing.T) {
		newSession := repository.RefreshSession{
			ID:        uuid.New(),
			UserID:    userID,
			TokenHash: newTokenHash,
			ExpiresAt: expiresAt,
		}

		mock.ExpectBegin()
		mock.ExpectExec("^INSERT INTO refresh_sessions").
			WithArgs(newSession.ID, session.UserID, newTokenHash, session.ExpiresAt, now).
			WillReturnResult(sqlmock.NewResult(1, 1))

		mock.ExpectExec("^UPDATE refresh_sessions SET revoked_at = \\$1").
			WithArgs(now, newSession.ID, tokenHash).
			WillReturnResult(sqlmock.NewResult(1, 1))

		tx := beginSQLMockTx(t, mock, sqlxDB)

		err := store.Rotate(context.Background(), newTestTx(tx), tokenHash, newSession, now)
		if err != nil {
			t.Fatalf("Rotate failed: %v", err)
		}
	})

	t.Run("RevokeByTokenHash success", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectExec("^UPDATE refresh_sessions SET revoked_at = \\$1 WHERE token_hash = \\$2").
			WithArgs(now, tokenHash).
			WillReturnResult(sqlmock.NewResult(1, 1))

		tx := beginSQLMockTx(t, mock, sqlxDB)
		err := store.RevokeByTokenHash(context.Background(), newTestTx(tx), tokenHash, now)
		if err != nil {
			t.Fatalf("RevokeByTokenHash failed: %v", err)
		}
	})
}
