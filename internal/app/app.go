package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"disbursment-api/internal/config"
	"disbursment-api/internal/database"
	"disbursment-api/internal/httpapi"
	"disbursment-api/internal/httpapi/validation"
	"disbursment-api/internal/repository/postgres"
	"disbursment-api/internal/service/auth"
)

type Application struct {
	database        databaseCloser
	server          *http.Server
	shutdownTimeout time.Duration
}

type databaseCloser interface {
	Close() error
}

func New(ctx context.Context, config config.Config, logger *slog.Logger) (*Application, error) {
	db, err := database.Open(ctx, config.Database)
	if err != nil {
		return nil, err
	}

	userStore := postgres.NewUserStore(db)
	sessionStore := postgres.NewRefreshSessionStore(db)
	transactor := postgres.NewTransactor(db)

	authService := auth.NewService(
		userStore,
		sessionStore,
		transactor,
		config.Security.JWTSecret,
		config.Security.AccessTokenTTL,
		config.Security.RefreshTokenTTL,
	)

	validatorEngine, err := validation.New()
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("validator init failed: %w", err)
	}

	authHandler := httpapi.NewAuthHandler(authService, validatorEngine)
	router := httpapi.NewRouter(config.HTTP.MaxRequestBodyBytes, logger, authHandler)

	return &Application{
		database: db,
		server: &http.Server{
			Addr:         config.HTTP.Address,
			Handler:      router,
			ReadTimeout:  config.HTTP.ReadTimeout,
			WriteTimeout: config.HTTP.WriteTimeout,
			IdleTimeout:  config.HTTP.IdleTimeout,
		},
		shutdownTimeout: config.HTTP.ShutdownTimeout,
	}, nil
}

func (application *Application) Run(ctx context.Context) error {
	defer application.database.Close()

	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- application.server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), application.shutdownTimeout)
		defer cancel()
		if err := application.server.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("shutdown server: %w", err)
		}
		return nil
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("run server: %w", err)
	}
}
