package postgres

import (
	"context"
	"errors"
	"testing"

	"disbursment-api/internal/repository"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
)

func TestTransactor_WithinTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	sqlxDB := sqlx.NewDb(db, "sqlmock")
	transactor := NewTransactor(sqlxDB)

	t.Run("WithinTransaction commit success", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectCommit()

		err := transactor.WithinTransaction(context.Background(), func(ctx context.Context, tx repository.Transaction) error {
			if tx.Context() == nil {
				t.Fatalf("expected non-nil context")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("WithinTransaction failed: %v", err)
		}
	})

	t.Run("WithinTransaction rollback on operation error", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectRollback()

		err := transactor.WithinTransaction(context.Background(), func(ctx context.Context, tx repository.Transaction) error {
			return errors.New("operation failed")
		})
		if err == nil {
			t.Fatalf("expected error from operation")
		}
	})
}
