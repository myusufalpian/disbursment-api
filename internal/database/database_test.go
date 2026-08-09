package database

import (
	"context"
	"strings"
	"testing"
	"time"

	"disbursment-api/internal/config"
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
		if !strings.Contains(err.Error(), "ping database:") {
			t.Fatalf("expected error message to contain 'ping database:', got: %v", err)
		}
		if strings.Contains(err.Error(), "<nil>") {
			t.Fatalf("error message contains unexpected nil format: %v", err)
		}
	})
}
