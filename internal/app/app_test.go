package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"disbursment-api/internal/config"
	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi"
	"disbursment-api/internal/observability/metrics"
	"disbursment-api/internal/repository"
	"disbursment-api/internal/repository/postgres"
	"disbursment-api/internal/service/audit"
	"disbursment-api/internal/service/idempotency"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type lifecycleRelayStore struct{}

func (lifecycleRelayStore) Insert(context.Context, repository.Transaction, domain.AuditEvent) error {
	return nil
}

func (lifecycleRelayStore) FetchPending(context.Context, int) ([]domain.AuditEvent, error) {
	return nil, nil
}

func (lifecycleRelayStore) MarkDelivered(context.Context, uuid.UUID, time.Time) error {
	return nil
}

func (lifecycleRelayStore) RecordFailure(context.Context, uuid.UUID, string, time.Time) error {
	return nil
}

func (lifecycleRelayStore) ReconcilePending(context.Context, time.Duration, time.Duration) (int, int, error) {
	return 0, 0, nil
}

func (lifecycleRelayStore) CleanupDelivered(context.Context, time.Duration) (int64, error) {
	return 0, nil
}

func (lifecycleRelayStore) InsertProjection(context.Context, repository.Transaction, domain.AuditEvent) error {
	return nil
}

func (lifecycleRelayStore) FindLogBySourceEventID(context.Context, uuid.UUID) (*domain.AuditEvent, error) {
	return nil, nil
}

func TestApplicationLifecycleRunAndShutdown(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()
	sqlxDB := sqlx.NewDb(db, "sqlmock")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on random port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	relayStore := lifecycleRelayStore{}
	collector := metrics.NewMetricsCollector()

	relayService, err := audit.NewRelayService(&relayStore, &relayStore, collector, logger, audit.RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})
	if err != nil {
		t.Fatalf("failed to create relay service: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	app := &Application{
		database:        sqlxDB,
		server:          server,
		relayService:    relayService,
		metrics:         collector,
		shutdownTimeout: 100 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())

	runErrChan := make(chan error, 1)
	go func() {
		runErrChan <- app.Run(ctx)
	}()

	// Poll for the server readiness condition instead of sleeping for a fixed duration.
	readinessDeadline := time.NewTimer(2 * time.Second)
	defer readinessDeadline.Stop()
	readinessTicker := time.NewTicker(5 * time.Millisecond)
	defer readinessTicker.Stop()

	var resp *http.Response
	for {
		resp, err = http.Get("http://" + addr + "/health")
		if err == nil && resp.StatusCode == http.StatusOK {
			break
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		select {
		case <-readinessTicker.C:
		case <-readinessDeadline.C:
			t.Fatalf("HTTP server did not become ready: %v", err)
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status OK, got %d", resp.StatusCode)
	}

	// Trigger cancellation to verify graceful shutdown of server and relay worker
	cancel()

	select {
	case err := <-runErrChan:
		if err != nil {
			t.Fatalf("Application.Run returned unexpected error on shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Application.Run timed out during shutdown")
	}
}

func TestNewErrorBranches(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	t.Run("database open failure", func(t *testing.T) {
		cfg := config.Config{
			Database: config.DatabaseConfig{
				URL: "postgres://invalid:invalid@127.0.0.1:1/invalid_db?sslmode=disable",
			},
		}
		_, err := New(ctx, cfg, logger)
		if err == nil {
			t.Fatalf("expected database open failure, got nil")
		}
	})

	t.Run("idempotency coordinator init failure", func(t *testing.T) {
		db, _, _ := sqlmock.New()
		defer db.Close()
		sqlxDB := sqlx.NewDb(db, "sqlmock")

		cfg := config.Config{
			Idempotency: config.IdempotencyConfig{
				LeaseTTL:  0,
				ReplayTTL: 10 * time.Minute,
			},
		}

		// Mock app initialization step with sqlxDB directly
		metricsCollector := metrics.NewMetricsCollector()
		idempotencyStore := postgres.NewIdempotencyStore(sqlxDB)
		_, err := idempotency.NewDefaultCoordinator(idempotencyStore, cfg.Idempotency.LeaseTTL, cfg.Idempotency.ReplayTTL, metricsCollector)
		if err == nil {
			t.Fatalf("expected error for zero LeaseTTL")
		}
	})

	t.Run("router init failure on invalid trusted proxy", func(t *testing.T) {
		cfg := config.Config{
			HTTP: config.HTTPConfig{
				MaxRequestBodyBytes: 1024,
				TrustedProxies:      []string{"invalid-ip-address"},
			},
		}

		metricsCollector := metrics.NewMetricsCollector()
		_, err := httpapi.NewRouter(cfg.HTTP.MaxRequestBodyBytes, logger, domain.NewStaticKeyProvider("v1", "secret", nil), "iss", "aud", nil, nil, metricsCollector, "token", cfg.HTTP.TrustedProxies, nil)
		if err == nil {
			t.Fatalf("expected router error for invalid trusted proxy")
		}
	})
}
