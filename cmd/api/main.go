package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"disbursment-api/internal/app"
	"disbursment-api/internal/config"
)

func main() {
	configuration, err := config.Load()
	if err != nil {
		slog.Error("configuration error", slog.String("error", err.Error()))
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	applicationContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	application, err := app.New(applicationContext, configuration, logger)
	if err != nil {
		logger.Error("application startup failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
	if err := application.Run(applicationContext); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("application stopped unexpectedly", slog.String("error", err.Error()))
		os.Exit(1)
	}
}
