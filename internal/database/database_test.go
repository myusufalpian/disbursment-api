package database

import (
	"context"
	"testing"
	"time"

	"disbursment-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestOpen(t *testing.T) {
	t.Run("Open invalid URL driver failure", func(t *testing.T) {
		cfg := config.DatabaseConfig{
			URL:                   "invalid-dsn",
			MaxOpenConnections:    5,
			MaxIdleConnections:    2,
			ConnectionMaxLifetime: 1 * time.Minute,
		}

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		db, err := Open(ctx, cfg)
		if err == nil {
			if db != nil {
				_ = db.Close()
			}
			t.Fatalf("expected error for pinging invalid database DSN, got nil")
		}
	})

	t.Run("Open with sqlmock ping failure", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
		if err != nil {
			t.Fatalf("failed to create sqlmock: %v", err)
		}
		defer db.Close()

		mock.ExpectPing().WillReturnError(sqlmock.ErrCancelled)

		cfg := config.DatabaseConfig{
			URL:                   "postgres://user:pass@localhost:5432/db?sslmode=disable",
			MaxOpenConnections:    5,
			MaxIdleConnections:    2,
			ConnectionMaxLifetime: 1 * time.Minute,
		}

		ctx := context.Background()
		result, err := Open(ctx, cfg)
		if err == nil {
			if result != nil {
				_ = result.Close()
			}
			t.Fatalf("expected ping error, got nil")
		}
	})
}
