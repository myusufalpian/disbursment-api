package audit

import (
	"context"
	"fmt"

	"log/slog"
	"sync"
	"time"

	"disbursment-api/internal/repository"

	"github.com/google/uuid"
)

type MetricsReporter interface {
	RecordDeliverySuccess()
	RecordDeliveryFailure()
	SetBacklogDepth(depth int)
	SetReconciliationCounts(warning int, critical int)
}

type ReconciliationReport struct {
	WarningCount  int
	CriticalCount int
}

type RelayService struct {
	outboxStore     repository.AuditOutboxStore
	projectionStore repository.AuditProjectionStore
	metrics         MetricsReporter
	logger          *slog.Logger
	mu              sync.Mutex
	running         bool
	stopChan        chan struct{}
}

func NewRelayService(
	outboxStore repository.AuditOutboxStore,
	projectionStore repository.AuditProjectionStore,
	metrics MetricsReporter,
	logger *slog.Logger,
) (*RelayService, error) {
	if outboxStore == nil {
		return nil, fmt.Errorf("audit outbox store required")
	}
	if projectionStore == nil {
		return nil, fmt.Errorf("audit projection store required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &RelayService{
		outboxStore:     outboxStore,
		projectionStore: projectionStore,
		metrics:         metrics,
		logger:          logger,
	}, nil
}

func (s *RelayService) ProcessBatch(ctx context.Context, batchSize int) (int, error) {
	if batchSize <= 0 {
		batchSize = 50
	}
	events, err := s.outboxStore.FetchPending(ctx, batchSize)
	if err != nil {
		s.logger.Error("failed to fetch pending outbox events", slog.String("error", err.Error()))
		return 0, fmt.Errorf("fetch pending outbox events: %w", err)
	}

	if s.metrics != nil {
		s.metrics.SetBacklogDepth(len(events))
	}

	if len(events) == 0 {
		return 0, nil
	}

	processedCount := 0
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return processedCount, err
		}

		err := s.projectionStore.InsertProjection(ctx, nil, event)
		if err != nil {
			s.logger.Warn("audit log projection failed, scheduling backoff retry",
				slog.String("event_id", event.EventID.String()),
				slog.String("error", err.Error()),
			)
			if s.metrics != nil {
				s.metrics.RecordDeliveryFailure()
			}
			nextAvailableAt := time.Now().UTC().Add(5 * time.Second)
			if recErr := s.outboxStore.RecordFailure(ctx, event.EventID, err.Error(), nextAvailableAt); recErr != nil {
				s.logger.Error("failed to record outbox delivery failure",
					slog.String("event_id", event.EventID.String()),
					slog.String("error", recErr.Error()),
				)
			}
			continue
		}

		if err := s.outboxStore.MarkDelivered(ctx, event.EventID, time.Now().UTC()); err != nil {
			s.logger.Error("failed to mark outbox event delivered",
				slog.String("event_id", event.EventID.String()),
				slog.String("error", err.Error()),
			)
			if s.metrics != nil {
				s.metrics.RecordDeliveryFailure()
			}
			continue
		}

		if s.metrics != nil {
			s.metrics.RecordDeliverySuccess()
		}
		processedCount++
	}

	return processedCount, nil
}

func (s *RelayService) Reconcile(ctx context.Context) (ReconciliationReport, error) {
	warningCount, criticalCount, err := s.outboxStore.ReconcilePending(ctx, 5*time.Minute)
	if err != nil {
		s.logger.Error("failed to run outbox reconciliation", slog.String("error", err.Error()))
		return ReconciliationReport{}, fmt.Errorf("outbox reconciliation failed: %w", err)
	}

	if warningCount > 0 || criticalCount > 0 {
		s.logger.Warn("outbox pending events exceed threshold",
			slog.Int("warning_count", warningCount),
			slog.Int("critical_count", criticalCount),
		)
	}

	if s.metrics != nil {
		s.metrics.SetReconciliationCounts(warningCount, criticalCount)
	}

	return ReconciliationReport{
		WarningCount:  warningCount,
		CriticalCount: criticalCount,
	}, nil
}

func (s *RelayService) Cleanup(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		retentionDays = 30
	}
	duration := time.Duration(retentionDays) * 24 * time.Hour
	cleaned, err := s.outboxStore.CleanupDelivered(ctx, duration)
	if err != nil {
		s.logger.Error("failed to cleanup delivered outbox events", slog.String("error", err.Error()))
		return 0, fmt.Errorf("outbox cleanup failed: %w", err)
	}
	s.logger.Info("cleaned up delivered outbox events", slog.Int64("count", cleaned))
	return cleaned, nil
}

func (s *RelayService) StartWorker(ctx context.Context, interval time.Duration, batchSize int) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("relay worker already running")
	}
	s.running = true
	s.stopChan = make(chan struct{})
	stopChan := s.stopChan
	s.mu.Unlock()

	if interval <= 0 {
		interval = 5 * time.Second
	}

	go func() {
		processTicker := time.NewTicker(interval)
		defer processTicker.Stop()

		reconcileInterval := 15 * time.Minute
		if interval < 1*time.Second {
			reconcileInterval = 50 * time.Millisecond
		}
		reconcileTicker := time.NewTicker(reconcileInterval)
		defer reconcileTicker.Stop()

		cleanupInterval := 24 * time.Hour
		if interval < 1*time.Second {
			cleanupInterval = 100 * time.Millisecond
		}
		cleanupTicker := time.NewTicker(cleanupInterval)
		defer cleanupTicker.Stop()

		// Run immediate initial execution
		_, _ = s.ProcessBatch(ctx, batchSize)
		_, _ = s.Reconcile(ctx)
		_, _ = s.Cleanup(ctx, 30)

		for {
			select {
			case <-ctx.Done():
				s.stopWorker()
				return
			case <-stopChan:
				return
			case <-processTicker.C:
				_, _ = s.ProcessBatch(ctx, batchSize)
			case <-reconcileTicker.C:
				_, _ = s.Reconcile(ctx)
			case <-cleanupTicker.C:
				_, _ = s.Cleanup(ctx, 30)
			}
		}
	}()

	return nil
}

func (s *RelayService) StopWorker() {
	s.stopWorker()
}

func (s *RelayService) stopWorker() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	s.running = false
	close(s.stopChan)
}

type EventIdentityHelper struct{}

func (h EventIdentityHelper) GenerateID() uuid.UUID {
	return uuid.New()
}
