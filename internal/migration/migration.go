package migration

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

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
	// A repeatable migration command should treat an already-current schema as success.
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

const AllowLocalSeedEnv = "ALLOW_LOCAL_SEED"

func AssertLocalSeedTarget(databaseURL string, authorized bool) error {
	if !authorized {
		return fmt.Errorf("seeding fixed-credential accounts requires %s=1", AllowLocalSeedEnv)
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return fmt.Errorf("parse database URL: %w", err)
	}
	if !isLoopbackSeedHost(parsed.Hostname()) {
		return fmt.Errorf("seed target must be a loopback database host, got %q", parsed.Hostname())
	}
	return nil
}

func isLoopbackSeedHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
