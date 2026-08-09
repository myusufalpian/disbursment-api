package audit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/repository"

	"github.com/google/uuid"
)

type mockAuditOutboxStore struct {
	fetchPendingFunc     func(ctx context.Context, limit int) ([]domain.AuditEvent, error)
	markDeliveredFunc    func(ctx context.Context, eventID uuid.UUID, deliveredAt time.Time) error
	recordFailureFunc    func(ctx context.Context, eventID uuid.UUID, errMessage string, nextAvailableAt time.Time) error
	reconcilePendingFunc func(ctx context.Context, minAge time.Duration) (int, int, error)
	cleanupDeliveredFunc func(ctx context.Context, olderThan time.Duration) (int64, error)
}

func (m *mockAuditOutboxStore) Insert(ctx context.Context, tx repository.Transaction, event domain.AuditEvent) error {
	return nil
}

func (m *mockAuditOutboxStore) FetchPending(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
	if m.fetchPendingFunc != nil {
		return m.fetchPendingFunc(ctx, limit)
	}
	return nil, nil
}

func (m *mockAuditOutboxStore) MarkDelivered(ctx context.Context, eventID uuid.UUID, deliveredAt time.Time) error {
	if m.markDeliveredFunc != nil {
		return m.markDeliveredFunc(ctx, eventID, deliveredAt)
	}
	return nil
}

func (m *mockAuditOutboxStore) RecordFailure(ctx context.Context, eventID uuid.UUID, errMessage string, nextAvailableAt time.Time) error {
	if m.recordFailureFunc != nil {
		return m.recordFailureFunc(ctx, eventID, errMessage, nextAvailableAt)
	}
	return nil
}

func (m *mockAuditOutboxStore) ReconcilePending(ctx context.Context, minAge time.Duration) (int, int, error) {
	if m.reconcilePendingFunc != nil {
		return m.reconcilePendingFunc(ctx, minAge)
	}
	return 0, 0, nil
}

func (m *mockAuditOutboxStore) CleanupDelivered(ctx context.Context, olderThan time.Duration) (int64, error) {
	if m.cleanupDeliveredFunc != nil {
		return m.cleanupDeliveredFunc(ctx, olderThan)
	}
	return 0, nil
}

type mockAuditProjectionStore struct {
	insertProjectionFunc func(ctx context.Context, tx repository.Transaction, event domain.AuditEvent) error
}

func (m *mockAuditProjectionStore) InsertProjection(ctx context.Context, tx repository.Transaction, event domain.AuditEvent) error {
	if m.insertProjectionFunc != nil {
		return m.insertProjectionFunc(ctx, tx, event)
	}
	return nil
}

func (m *mockAuditProjectionStore) FindLogBySourceEventID(ctx context.Context, sourceEventID uuid.UUID) (*domain.AuditEvent, error) {
	return nil, nil
}

type mockMetricsReporter struct {
	successCount  uint64
	failureCount  uint64
	backlogDepth  int64
	warningCount  int64
	criticalCount int64
}

func (m *mockMetricsReporter) RecordDeliverySuccess() {
	atomic.AddUint64(&m.successCount, 1)
}

func (m *mockMetricsReporter) RecordDeliveryFailure() {
	atomic.AddUint64(&m.failureCount, 1)
}

func (m *mockMetricsReporter) SetBacklogDepth(depth int) {
	atomic.StoreInt64(&m.backlogDepth, int64(depth))
}

func (m *mockMetricsReporter) SetReconciliationCounts(warning, critical int) {
	atomic.StoreInt64(&m.warningCount, int64(warning))
	atomic.StoreInt64(&m.criticalCount, int64(critical))
}

func TestNewRelayServiceConstructor(t *testing.T) {
	outbox := &mockAuditOutboxStore{}
	projection := &mockAuditProjectionStore{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	_, err := NewRelayService(nil, projection, nil, logger)
	if err == nil {
		t.Fatalf("expected error for nil outbox store")
	}

	_, err = NewRelayService(outbox, nil, nil, logger)
	if err == nil {
		t.Fatalf("expected error for nil projection store")
	}

	service, err := NewRelayService(outbox, projection, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if service == nil {
		t.Fatalf("expected non-nil service")
	}
}

func TestRelayService_ProcessBatch(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("ProcessBatch empty pending events", func(t *testing.T) {
		outbox := &mockAuditOutboxStore{
			fetchPendingFunc: func(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
				return nil, nil
			},
		}
		metrics := &mockMetricsReporter{}
		service, _ := NewRelayService(outbox, &mockAuditProjectionStore{}, metrics, logger)

		count, err := service.ProcessBatch(context.Background(), 10)
		if err != nil {
			t.Fatalf("ProcessBatch failed: %v", err)
		}
		if count != 0 {
			t.Fatalf("expected 0, got %d", count)
		}
		if metrics.backlogDepth != 0 {
			t.Fatalf("expected 0 backlog depth, got %d", metrics.backlogDepth)
		}
	})

	t.Run("ProcessBatch fetch error propagates", func(t *testing.T) {
		outbox := &mockAuditOutboxStore{
			fetchPendingFunc: func(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
				return nil, errors.New("db query failed")
			},
		}
		service, _ := NewRelayService(outbox, &mockAuditProjectionStore{}, nil, logger)
		_, err := service.ProcessBatch(context.Background(), 10)
		if err == nil {
			t.Fatalf("expected error for fetch failure")
		}
	})

	t.Run("ProcessBatch successfully projects and marks delivered", func(t *testing.T) {
		eventID := uuid.New()
		event := domain.AuditEvent{
			EventID:    eventID,
			EntityType: "disbursement",
			EntityID:   uuid.New(),
			Action:     "disbursement.created",
			ActorID:    uuid.New(),
			RequestID:  uuid.New(),
			OccurredAt: time.Now().UTC(),
		}

		deliveredCount := 0
		outbox := &mockAuditOutboxStore{
			fetchPendingFunc: func(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
				return []domain.AuditEvent{event}, nil
			},
			markDeliveredFunc: func(ctx context.Context, id uuid.UUID, deliveredAt time.Time) error {
				if id == eventID {
					deliveredCount++
				}
				return nil
			},
		}

		projectedCount := 0
		projection := &mockAuditProjectionStore{
			insertProjectionFunc: func(ctx context.Context, tx repository.Transaction, e domain.AuditEvent) error {
				if e.EventID == eventID {
					projectedCount++
				}
				return nil
			},
		}

		metrics := &mockMetricsReporter{}
		service, _ := NewRelayService(outbox, projection, metrics, logger)

		count, err := service.ProcessBatch(context.Background(), 10)
		if err != nil {
			t.Fatalf("ProcessBatch failed: %v", err)
		}
		if count != 1 {
			t.Fatalf("expected 1, got %d", count)
		}
		if projectedCount != 1 || deliveredCount != 1 {
			t.Fatalf("expected projected 1 delivered 1, got proj %d del %d", projectedCount, deliveredCount)
		}
		if metrics.successCount != 1 {
			t.Fatalf("expected success count 1, got %d", metrics.successCount)
		}
	})

	t.Run("ProcessBatch projection failure records failure", func(t *testing.T) {
		eventID := uuid.New()
		event := domain.AuditEvent{
			EventID:    eventID,
			EntityType: "disbursement",
			EntityID:   uuid.New(),
			Action:     "disbursement.created",
			ActorID:    uuid.New(),
			RequestID:  uuid.New(),
			OccurredAt: time.Now().UTC(),
		}

		failureRecorded := false
		outbox := &mockAuditOutboxStore{
			fetchPendingFunc: func(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
				return []domain.AuditEvent{event}, nil
			},
			recordFailureFunc: func(ctx context.Context, id uuid.UUID, msg string, nextAt time.Time) error {
				if id == eventID {
					failureRecorded = true
				}
				return nil
			},
		}

		projection := &mockAuditProjectionStore{
			insertProjectionFunc: func(ctx context.Context, tx repository.Transaction, e domain.AuditEvent) error {
				return errors.New("unique constraint fail")
			},
		}

		metrics := &mockMetricsReporter{}
		service, _ := NewRelayService(outbox, projection, metrics, logger)

		count, err := service.ProcessBatch(context.Background(), 10)
		if err != nil {
			t.Fatalf("ProcessBatch unexpected error: %v", err)
		}
		if count != 0 {
			t.Fatalf("expected 0 processed, got %d", count)
		}
		if !failureRecorded {
			t.Fatalf("expected failure to be recorded")
		}
		if metrics.failureCount != 1 {
			t.Fatalf("expected failure count 1, got %d", metrics.failureCount)
		}
	})
}

func TestRelayService_ReconcileAndCleanup(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("Reconcile success", func(t *testing.T) {
		outbox := &mockAuditOutboxStore{
			reconcilePendingFunc: func(ctx context.Context, minAge time.Duration) (int, int, error) {
				return 3, 1, nil
			},
		}
		metrics := &mockMetricsReporter{}
		service, _ := NewRelayService(outbox, &mockAuditProjectionStore{}, metrics, logger)

		report, err := service.Reconcile(context.Background())
		if err != nil {
			t.Fatalf("Reconcile failed: %v", err)
		}
		if report.WarningCount != 3 || report.CriticalCount != 1 {
			t.Fatalf("expected warn 3 crit 1, got warn %d crit %d", report.WarningCount, report.CriticalCount)
		}
		if metrics.warningCount != 3 || metrics.criticalCount != 1 {
			t.Fatalf("expected metrics warn 3 crit 1, got %d and %d", metrics.warningCount, metrics.criticalCount)
		}
	})

	t.Run("Reconcile error propagates", func(t *testing.T) {
		outbox := &mockAuditOutboxStore{
			reconcilePendingFunc: func(ctx context.Context, minAge time.Duration) (int, int, error) {
				return 0, 0, errors.New("reconcile db error")
			},
		}
		service, _ := NewRelayService(outbox, &mockAuditProjectionStore{}, nil, logger)
		_, err := service.Reconcile(context.Background())
		if err == nil {
			t.Fatalf("expected error for reconcile failure")
		}
	})

	t.Run("Cleanup success", func(t *testing.T) {
		outbox := &mockAuditOutboxStore{
			cleanupDeliveredFunc: func(ctx context.Context, olderThan time.Duration) (int64, error) {
				return 42, nil
			},
		}
		service, _ := NewRelayService(outbox, &mockAuditProjectionStore{}, nil, logger)
		cleaned, err := service.Cleanup(context.Background(), 30)
		if err != nil {
			t.Fatalf("Cleanup failed: %v", err)
		}
		if cleaned != 42 {
			t.Fatalf("expected 42 cleaned, got %d", cleaned)
		}
	})
}

func TestRelayService_StartAndStopWorker(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	outbox := &mockAuditOutboxStore{}
	projection := &mockAuditProjectionStore{}

	service, _ := NewRelayService(outbox, projection, nil, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := service.StartWorker(ctx, 10*time.Millisecond, 5)
	if err != nil {
		t.Fatalf("StartWorker failed: %v", err)
	}

	err = service.StartWorker(ctx, 10*time.Millisecond, 5)
	if err == nil {
		t.Fatalf("expected error starting worker twice")
	}

	time.Sleep(30 * time.Millisecond)
	service.StopWorker()
}

func TestRelayService_EdgeCases(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("ProcessBatch context cancelled early", func(t *testing.T) {
		event := domain.AuditEvent{
			EventID:    uuid.New(),
			EntityType: "disbursement",
			EntityID:   uuid.New(),
			Action:     "created",
			ActorID:    uuid.New(),
			RequestID:  uuid.New(),
			OccurredAt: time.Now().UTC(),
		}
		outbox := &mockAuditOutboxStore{
			fetchPendingFunc: func(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
				return []domain.AuditEvent{event, event}, nil
			},
		}
		service, _ := NewRelayService(outbox, &mockAuditProjectionStore{}, nil, logger)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel context immediately

		_, err := service.ProcessBatch(ctx, 0)
		if err == nil {
			t.Fatalf("expected error on cancelled context")
		}
	})

	t.Run("ProcessBatch mark delivered error", func(t *testing.T) {
		event := domain.AuditEvent{
			EventID:    uuid.New(),
			EntityType: "disbursement",
			EntityID:   uuid.New(),
			Action:     "created",
			ActorID:    uuid.New(),
			RequestID:  uuid.New(),
			OccurredAt: time.Now().UTC(),
		}
		outbox := &mockAuditOutboxStore{
			fetchPendingFunc: func(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
				return []domain.AuditEvent{event}, nil
			},
			markDeliveredFunc: func(ctx context.Context, id uuid.UUID, deliveredAt time.Time) error {
				return errors.New("db lock timeout")
			},
		}
		metrics := &mockMetricsReporter{}
		service, _ := NewRelayService(outbox, &mockAuditProjectionStore{}, metrics, logger)

		count, err := service.ProcessBatch(context.Background(), 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if count != 0 {
			t.Fatalf("expected 0 processed due to mark delivered failure")
		}
		if metrics.failureCount != 1 {
			t.Fatalf("expected 1 failure count in metrics")
		}
	})

	t.Run("ProcessBatch record failure error is logged safely", func(t *testing.T) {
		event := domain.AuditEvent{
			EventID:    uuid.New(),
			EntityType: "disbursement",
			EntityID:   uuid.New(),
			Action:     "created",
			ActorID:    uuid.New(),
			RequestID:  uuid.New(),
			OccurredAt: time.Now().UTC(),
		}
		outbox := &mockAuditOutboxStore{
			fetchPendingFunc: func(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
				return []domain.AuditEvent{event}, nil
			},
			recordFailureFunc: func(ctx context.Context, id uuid.UUID, msg string, nextAt time.Time) error {
				return errors.New("failed to write failure to DB")
			},
		}
		projection := &mockAuditProjectionStore{
			insertProjectionFunc: func(ctx context.Context, tx repository.Transaction, e domain.AuditEvent) error {
				return errors.New("projection failed")
			},
		}
		service, _ := NewRelayService(outbox, projection, nil, logger)

		count, err := service.ProcessBatch(context.Background(), 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if count != 0 {
			t.Fatalf("expected 0 processed")
		}
	})

	t.Run("Concurrent relay workers process safely", func(t *testing.T) {
		outbox := &mockAuditOutboxStore{}
		service, _ := NewRelayService(outbox, &mockAuditProjectionStore{}, nil, logger)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var wg sync.WaitGroup
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = service.ProcessBatch(ctx, 10)
				_, _ = service.Reconcile(ctx)
			}()
		}
		wg.Wait()
	})

	t.Run("Rapid Stop and Start worker is race safe", func(t *testing.T) {
		outbox := &mockAuditOutboxStore{}
		service, _ := NewRelayService(outbox, &mockAuditProjectionStore{}, nil, logger)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		for i := 0; i < 20; i++ {
			err := service.StartWorker(ctx, 10*time.Millisecond, 10)
			if err != nil {
				t.Fatalf("iteration %d: start worker failed: %v", i, err)
			}
			time.Sleep(2 * time.Millisecond)
			service.StopWorker()
		}
	})
}
