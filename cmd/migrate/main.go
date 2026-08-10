package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"path/filepath"

	"disbursment-api/internal/config"
	"disbursment-api/internal/database"
	"disbursment-api/internal/migration"
)

func main() {
	action := flag.String("action", "up", "migration action: up or down")
	steps := flag.Int("steps", 0, "number of migration steps; zero means all")
	directory := flag.String("directory", "migrations", "migration directory")
	seed := flag.Bool("seed", false, "apply idempotent local/test seed after successful up migration")
	flag.Parse()

	databaseURL, err := config.DatabaseURL()
	if err != nil {
		slog.Error("configuration error", slog.String("error", err.Error()))
		os.Exit(1)
	}
	if *seed {
		if *action != "up" {
			slog.Error("seed requires up migration action")
			os.Exit(1)
		}
		if err := migration.AssertLocalSeedTarget(databaseURL, os.Getenv(migration.AllowLocalSeedEnv) == "1"); err != nil {
			slog.Error("seed refused", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}
	if err := migration.Run(databaseURL, *directory, *action, *steps); err != nil {
		slog.Error("migration failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
	if !*seed {
		return
	}

	configuration, err := config.Load()
	if err != nil {
		slog.Error("configuration error", slog.String("error", err.Error()))
		os.Exit(1)
	}
	database, err := database.Open(context.Background(), configuration.Database)
	if err != nil {
		slog.Error("database connection failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer database.Close()
	if err := migration.ApplySeed(context.Background(), database, filepath.Join(*directory, "000001_local_users.seed.sql")); err != nil {
		slog.Error("seed failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}
