package database

import (
	"context"
	"fmt"

	"disbursment-api/internal/config"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

func Open(ctx context.Context, config config.DatabaseConfig) (*sqlx.DB, error) {
	database, err := sqlx.Open("postgres", config.URL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	database.SetMaxOpenConns(config.MaxOpenConnections)
	database.SetMaxIdleConns(config.MaxIdleConnections)
	database.SetConnMaxLifetime(config.ConnectionMaxLifetime)

	if err := database.PingContext(ctx); err != nil {
		if closeErr := database.Close(); closeErr != nil {
			return nil, fmt.Errorf("ping database: %w (close error: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return database, nil
}
