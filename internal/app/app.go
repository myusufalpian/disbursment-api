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
	"disbursment-api/internal/observability/metrics"
	"disbursment-api/internal/repository/postgres"
	"disbursment-api/internal/service/audit"
	"disbursment-api/internal/service/auth"
	"disbursment-api/internal/service/disbursement"
	"disbursment-api/internal/service/idempotency"

	"github.com/jmoiron/sqlx"
)

type Application struct {
	database        *sqlx.DB
	server          *http.Server
	relayService    *audit.RelayService
	metrics         *metrics.MetricsCollector
	shutdownTimeout time.Duration
}

func New(ctx context.Context, config config.Config, logger *slog.Logger) (*Application, error) {
	db, err := database.Open(ctx, config.Database)
	if err != nil {
		return nil, err
	}

	metricsCollector := metrics.NewMetricsCollector()

	userStore := postgres.NewUserStore(db)
	sessionStore := postgres.NewRefreshSessionStore(db)
	transactor := postgres.NewTransactor(db)
	disbursementStore := postgres.NewDisbursementStore(db)
	auditOutboxStore := postgres.NewAuditOutboxStore(db)
	auditProjectionStore := postgres.NewAuditProjectionStore(db)
	idempotencyStore := postgres.NewIdempotencyStore(db)

	relayService, err := audit.NewRelayService(auditOutboxStore, auditProjectionStore, metricsCollector, logger)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("audit relay service init failed: %w", err)
	}

	idempotencyCoordinator, err := idempotency.NewDefaultCoordinator(
		idempotencyStore,
		config.Idempotency.LeaseTTL,
		config.Idempotency.ReplayTTL,
		metricsCollector,
	)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("idempotency coordinator init failed: %w", err)
	}

	authService := auth.NewServiceWithIssuerAudience(
		userStore,
		sessionStore,
		transactor,
		config.Security.JWTSecret,
		config.Security.JWTIssuer,
		config.Security.JWTAudience,
		config.Security.AccessTokenTTL,
		config.Security.RefreshTokenTTL,
		metricsCollector,
	)

	disbursementService, err := disbursement.NewService(
		disbursementStore,
		auditOutboxStore,
		transactor,
		idempotencyCoordinator,
		metricsCollector,
	)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("disbursement service init failed: %w", err)
	}

	validatorEngine, err := validation.New()
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("validator init failed: %w", err)
	}

	authHandler := httpapi.NewAuthHandler(authService, validatorEngine)
	disbursementHandler := httpapi.NewDisbursementHandler(disbursementService, validatorEngine)

	router, err := httpapi.NewRouter(
		config.HTTP.MaxRequestBodyBytes,
		logger,
		config.Security.JWTSecret,
		config.Security.JWTIssuer,
		config.Security.JWTAudience,
		authHandler,
		disbursementHandler,
		metricsCollector,
		config.HTTP.MetricsToken,
		config.HTTP.TrustedProxies,
	)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("router init failed: %w", err)
	}

	return &Application{
		database:     db,
		relayService: relayService,
		metrics:      metricsCollector,
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

	if application.relayService != nil {
		if err := application.relayService.StartWorker(ctx, 5*time.Second, 50); err != nil {
			return fmt.Errorf("start audit relay worker: %w", err)
		}
		defer application.relayService.StopWorker()
	}

	// Periodically update DB pool metrics
	if application.database != nil && application.metrics != nil {
		go func() {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					stats := application.database.Stats()
					application.metrics.UpdateDBStats(stats.OpenConnections, stats.InUse, stats.Idle)
				}
			}
		}()
	}

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
