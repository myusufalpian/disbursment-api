package migration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jmoiron/sqlx"
)

func Run(databaseURL, directory, action string, steps int) error {
	absoluteDirectory, err := filepath.Abs(directory)
	if err != nil {
		return fmt.Errorf("resolve migration directory: %w", err)
	}
	migrator, err := migrate.New("file://"+absoluteDirectory, databaseURL)
	if err != nil {
		return fmt.Errorf("initialize migrations: %w", err)
	}
	defer migrator.Close()

	switch action {
	case "up":
		if steps > 0 {
			err = migrator.Steps(steps)
		} else {
			err = migrator.Up()
		}
	case "down":
		if steps > 0 {
			err = migrator.Steps(-steps)
		} else {
			err = migrator.Down()
		}
	default:
		return fmt.Errorf("unsupported migration action %q", action)
	}
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

func ApplySeed(ctx context.Context, database *sqlx.DB, path string) error {
	statement, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read seed: %w", err)
	}
	if _, err := database.ExecContext(ctx, string(statement)); err != nil {
		return fmt.Errorf("apply seed: %w", err)
	}
	return nil
}
