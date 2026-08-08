package postgres

import (
	"context"
	"database/sql"
	"time"

	"disbursment-api/internal/repository"

	"github.com/jmoiron/sqlx"
)

type RefreshSessionStore struct {
	db *sqlx.DB
}

func NewRefreshSessionStore(db *sqlx.DB) *RefreshSessionStore {
	return &RefreshSessionStore{db: db}
}

type queryGetExecutor interface {
	GetContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error
}

func getGetExecutor(tx repository.Transaction, db *sqlx.DB) queryGetExecutor {
	if tx != nil {
		if t, ok := tx.(*transaction); ok && t.tx != nil {
			return t.tx
		}
	}
	return db
}

func (store *RefreshSessionStore) Create(ctx context.Context, transaction repository.Transaction, session repository.RefreshSession) error {
	tx, err := unwrapTransaction(transaction)
	if err != nil {
		return err
	}
	const query = `
		INSERT INTO refresh_sessions (id, user_id, token_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, now())
	`
	_, err = tx.ExecContext(ctx, query, session.ID, session.UserID, session.TokenHash, session.ExpiresAt)
	if err != nil {
		return repository.Classify(err)
	}
	return nil
}

func (store *RefreshSessionStore) FindByTokenHash(ctx context.Context, transaction repository.Transaction, tokenHash string) (repository.RefreshSession, error) {
	const query = `
		SELECT id, user_id, token_hash, expires_at, revoked_at, replaced_by_id
		  FROM refresh_sessions
		 WHERE token_hash = $1
	`
	executor := getGetExecutor(transaction, store.db)
	var result repository.RefreshSession
	err := executor.GetContext(ctx, &result, query, tokenHash)
	if err != nil {
		return repository.RefreshSession{}, repository.Classify(err)
	}
	return result, nil
}

func (store *RefreshSessionStore) Rotate(ctx context.Context, transaction repository.Transaction, oldTokenHash string, newSession repository.RefreshSession, now time.Time) error {
	tx, err := unwrapTransaction(transaction)
	if err != nil {
		return err
	}

	const insertQuery = `
		INSERT INTO refresh_sessions (id, user_id, token_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`
	_, err = tx.ExecContext(ctx, insertQuery, newSession.ID, newSession.UserID, newSession.TokenHash, newSession.ExpiresAt, now)
	if err != nil {
		return repository.Classify(err)
	}

	const updateQuery = `
		UPDATE refresh_sessions
		   SET revoked_at = $1,
		       replaced_by_id = $2
		 WHERE token_hash = $3
		   AND revoked_at IS NULL
		   AND expires_at > $1
	`
	result, err := tx.ExecContext(ctx, updateQuery, now, newSession.ID, oldTokenHash)
	if err != nil {
		return repository.Classify(err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return repository.Classify(err)
	}
	if rows == 0 {
		return repository.NewError(repository.ErrorNotFound, sql.ErrNoRows)
	}
	return nil
}

func (store *RefreshSessionStore) RevokeByTokenHash(ctx context.Context, transaction repository.Transaction, tokenHash string, now time.Time) error {
	tx, err := unwrapTransaction(transaction)
	if err != nil {
		return err
	}
	const query = `
		UPDATE refresh_sessions
		   SET revoked_at = $1
		 WHERE token_hash = $2
		   AND revoked_at IS NULL
	`
	_, err = tx.ExecContext(ctx, query, now, tokenHash)
	if err != nil {
		return repository.Classify(err)
	}
	return nil
}
