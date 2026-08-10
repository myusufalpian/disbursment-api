package audit

import (
	"context"
	"encoding/json"
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

func (m *mockAuditOutboxStore) ReconcilePending(ctx context.Context, minAge time.Duration, criticalAge time.Duration) (int, int, error) {
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

	_, err := NewRelayService(nil, projection, nil, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})
	if err == nil {
		t.Fatalf("expected error for nil outbox store")
	}

	_, err = NewRelayService(outbox, nil, nil, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})
	if err == nil {
		t.Fatalf("expected error for nil projection store")
	}

	service, err := NewRelayService(outbox, projection, nil, nil, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})
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
		service, _ := NewRelayService(outbox, &mockAuditProjectionStore{}, metrics, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})

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
		service, _ := NewRelayService(outbox, &mockAuditProjectionStore{}, nil, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})
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
		service, _ := NewRelayService(outbox, projection, metrics, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})

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
		service, _ := NewRelayService(outbox, projection, metrics, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})

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
		service, _ := NewRelayService(outbox, &mockAuditProjectionStore{}, metrics, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})

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
		service, _ := NewRelayService(outbox, &mockAuditProjectionStore{}, nil, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})
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
		service, _ := NewRelayService(outbox, &mockAuditProjectionStore{}, nil, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})
		cleaned, err := service.Cleanup(context.Background())
		if err != nil {
			t.Fatalf("Cleanup failed: %v", err)
		}
		if cleaned != 42 {
			t.Fatalf("expected 42 cleaned, got %d", cleaned)
		}
	})
}

func waitForRelaySignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()

	select {
	case <-signal:
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", description)
	}
}

func stopRelayWorker(t *testing.T, service *RelayService) {
	t.Helper()
	stopped := make(chan struct{})
	go func() {
		service.StopWorker()
		close(stopped)
	}()
	waitForRelaySignal(t, stopped, "StopWorker return")
}

func TestRelayService_StartAndStopWorker(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	event := domain.AuditEvent{EventID: uuid.New(), EntityType: "disbursement", EntityID: uuid.New(), Action: "created", ActorID: uuid.New(), RequestID: uuid.New(), OccurredAt: time.Now().UTC()}
	fetchStarted := make(chan struct{}, 1)
	projected := make(chan uuid.UUID, 1)
	delivered := make(chan uuid.UUID, 1)
	initialCycleComplete := make(chan struct{}, 1)
	outbox := &mockAuditOutboxStore{
		fetchPendingFunc: func(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
			fetchStarted <- struct{}{}
			return []domain.AuditEvent{event}, nil
		},
		markDeliveredFunc: func(ctx context.Context, eventID uuid.UUID, deliveredAt time.Time) error {
			delivered <- eventID
			return nil
		},
		cleanupDeliveredFunc: func(ctx context.Context, olderThan time.Duration) (int64, error) {
			initialCycleComplete <- struct{}{}
			return 0, nil
		},
	}
	projection := &mockAuditProjectionStore{
		insertProjectionFunc: func(ctx context.Context, tx repository.Transaction, projectedEvent domain.AuditEvent) error {
			projected <- projectedEvent.EventID
			return nil
		},
	}

	service, err := NewRelayService(outbox, projection, nil, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})
	if err != nil {
		t.Fatalf("failed to create relay service: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := service.StartWorker(ctx, time.Hour, 5); err != nil {
		t.Fatalf("StartWorker failed: %v", err)
	}
	waitForRelaySignal(t, fetchStarted, "initial worker fetch")
	select {
	case eventID := <-projected:
		if eventID != event.EventID {
			t.Fatalf("expected projected event %s, got %s", event.EventID, eventID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial projection")
	}
	select {
	case eventID := <-delivered:
		if eventID != event.EventID {
			t.Fatalf("expected delivered event %s, got %s", event.EventID, eventID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial delivery")
	}
	waitForRelaySignal(t, initialCycleComplete, "initial worker cycle")

	if err := service.StartWorker(ctx, time.Hour, 5); err == nil {
		t.Fatalf("expected error starting worker twice")
	}
	stopRelayWorker(t, service)
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
		service, _ := NewRelayService(outbox, &mockAuditProjectionStore{}, nil, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

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
		service, _ := NewRelayService(outbox, &mockAuditProjectionStore{}, metrics, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})

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
		service, _ := NewRelayService(outbox, projection, nil, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})

		count, err := service.ProcessBatch(context.Background(), 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if count != 0 {
			t.Fatalf("expected 0 processed")
		}
	})

	t.Run("Concurrent relay workers process each pending event once", func(t *testing.T) {
		events := make([]domain.AuditEvent, 5)
		for i := range events {
			events[i] = domain.AuditEvent{EventID: uuid.New(), EntityType: "disbursement", EntityID: uuid.New(), Action: "created", ActorID: uuid.New(), RequestID: uuid.New(), OccurredAt: time.Now().UTC()}
		}

		var stateMu sync.Mutex
		claimed := make(map[uuid.UUID]bool)
		delivered := make(map[uuid.UUID]bool)
		projectionAttempts := make(map[uuid.UUID]int)
		markDeliveredCalls := make(map[uuid.UUID]int)
		projected := make(map[uuid.UUID]bool)
		outbox := &mockAuditOutboxStore{
			fetchPendingFunc: func(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
				stateMu.Lock()
				defer stateMu.Unlock()
				pending := make([]domain.AuditEvent, 0, limit)
				for _, event := range events {
					if claimed[event.EventID] || delivered[event.EventID] {
						continue
					}
					claimed[event.EventID] = true
					pending = append(pending, event)
					if len(pending) == limit {
						break
					}
				}
				return pending, nil
			},
			markDeliveredFunc: func(ctx context.Context, eventID uuid.UUID, deliveredAt time.Time) error {
				stateMu.Lock()
				defer stateMu.Unlock()
				markDeliveredCalls[eventID]++
				delivered[eventID] = true
				return nil
			},
		}
		projection := &mockAuditProjectionStore{
			insertProjectionFunc: func(ctx context.Context, tx repository.Transaction, event domain.AuditEvent) error {
				stateMu.Lock()
				defer stateMu.Unlock()
				projectionAttempts[event.EventID]++
				projected[event.EventID] = true
				return nil
			},
		}
		service, err := NewRelayService(outbox, projection, nil, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})
		if err != nil {
			t.Fatalf("failed to create relay service: %v", err)
		}

		ctx := context.Background()
		start := make(chan struct{})
		results := make(chan struct {
			count int
			err   error
		}, 5)
		var wg sync.WaitGroup
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				count, processErr := service.ProcessBatch(ctx, 10)
				results <- struct {
					count int
					err   error
				}{count: count, err: processErr}
			}()
		}
		close(start)
		wg.Wait()
		close(results)

		totalProcessed := 0
		for result := range results {
			if result.err != nil {
				t.Fatalf("concurrent ProcessBatch failed: %v", result.err)
			}
			totalProcessed += result.count
		}
		if totalProcessed != len(events) {
			t.Fatalf("expected %d processed events, got %d", len(events), totalProcessed)
		}
		stateMu.Lock()
		defer stateMu.Unlock()
		for _, event := range events {
			if !delivered[event.EventID] || !projected[event.EventID] {
				t.Fatalf("event %s was not fully delivered: projected=%t delivered=%t", event.EventID, projected[event.EventID], delivered[event.EventID])
			}
			if projectionAttempts[event.EventID] != 1 || markDeliveredCalls[event.EventID] != 1 {
				t.Fatalf("event %s was processed more than once: projection_attempts=%d mark_calls=%d", event.EventID, projectionAttempts[event.EventID], markDeliveredCalls[event.EventID])
			}
		}
	})

	t.Run("Worker restart is synchronized by initial cycle cleanup", func(t *testing.T) {
		events := make([]domain.AuditEvent, 20)
		for i := range events {
			events[i] = domain.AuditEvent{EventID: uuid.New(), EntityType: "disbursement", EntityID: uuid.New(), Action: "created", ActorID: uuid.New(), RequestID: uuid.New(), OccurredAt: time.Now().UTC()}
		}
		fetches := make(chan int, len(events))
		deliveries := make(chan uuid.UUID, len(events))
		cycleCleanups := make(chan struct{}, len(events))
		var fetchIndex atomic.Int32
		outbox := &mockAuditOutboxStore{
			fetchPendingFunc: func(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
				index := int(fetchIndex.Add(1)) - 1
				fetches <- index
				return []domain.AuditEvent{events[index]}, nil
			},
			markDeliveredFunc: func(ctx context.Context, eventID uuid.UUID, deliveredAt time.Time) error {
				deliveries <- eventID
				return nil
			},
			cleanupDeliveredFunc: func(ctx context.Context, olderThan time.Duration) (int64, error) {
				cycleCleanups <- struct{}{}
				return 0, nil
			},
		}
		service, err := NewRelayService(outbox, &mockAuditProjectionStore{}, nil, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})
		if err != nil {
			t.Fatalf("failed to create relay service: %v", err)
		}
		ctx := context.Background()

		for i, event := range events {
			if err := service.StartWorker(ctx, time.Hour, 1); err != nil {
				t.Fatalf("iteration %d: start worker failed: %v", i, err)
			}
			select {
			case fetchNumber := <-fetches:
				if fetchNumber != i {
					t.Fatalf("iteration %d: expected fetch %d, got %d", i, i, fetchNumber)
				}
			case <-time.After(time.Second):
				t.Fatalf("iteration %d: timed out waiting for initial fetch", i)
			}
			select {
			case eventID := <-deliveries:
				if eventID != event.EventID {
					t.Fatalf("iteration %d: expected delivery %s, got %s", i, event.EventID, eventID)
				}
			case <-time.After(time.Second):
				t.Fatalf("iteration %d: timed out waiting for delivery", i)
			}
			waitForRelaySignal(t, cycleCleanups, "initial worker cycle cleanup")
			stopRelayWorker(t, service)
		}
	})
}

const statefulRelayLeaseDuration = 5 * time.Minute

type statefulRelayFake struct {
	pending                  []domain.AuditEvent
	delivered                map[uuid.UUID]bool
	deliveredAt              map[uuid.UUID]time.Time
	projectionAttempts       map[uuid.UUID]int
	markDeliveredCalls       map[uuid.UUID]int
	lastDeliveryError        map[uuid.UUID]string
	nextAvailableAt          map[uuid.UUID]time.Time
	leaseUntil               map[uuid.UUID]time.Time
	now                      time.Time
	failFirstProjection      bool
	failFirstMark            bool
	projected                []domain.AuditEvent
	projectedBySourceEventID map[uuid.UUID]domain.AuditEvent
	failures                 []uuid.UUID
}

func newStatefulRelayFake(event domain.AuditEvent) *statefulRelayFake {
	return &statefulRelayFake{
		pending:                  []domain.AuditEvent{event},
		delivered:                make(map[uuid.UUID]bool),
		deliveredAt:              make(map[uuid.UUID]time.Time),
		projectionAttempts:       make(map[uuid.UUID]int),
		markDeliveredCalls:       make(map[uuid.UUID]int),
		lastDeliveryError:        make(map[uuid.UUID]string),
		nextAvailableAt:          make(map[uuid.UUID]time.Time),
		leaseUntil:               make(map[uuid.UUID]time.Time),
		projectedBySourceEventID: make(map[uuid.UUID]domain.AuditEvent),
		now:                      time.Date(2026, time.August, 9, 7, 0, 0, 0, time.UTC),
		failFirstProjection:      true,
	}
}

func (f *statefulRelayFake) Insert(ctx context.Context, tx repository.Transaction, event domain.AuditEvent) error {
	return nil
}

func (f *statefulRelayFake) FetchPending(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	events := make([]domain.AuditEvent, 0, len(f.pending))
	now := f.now
	for _, event := range f.pending {
		if f.delivered[event.EventID] {
			continue
		}
		if availableAt := f.nextAvailableAt[event.EventID]; !availableAt.IsZero() && now.Before(availableAt) {
			continue
		}
		if leaseUntil := f.leaseUntil[event.EventID]; !leaseUntil.IsZero() && now.Before(leaseUntil) {
			continue
		}
		events = append(events, event)
		f.leaseUntil[event.EventID] = now.Add(statefulRelayLeaseDuration)
		if len(events) == limit {
			break
		}
	}
	return events, nil
}

func (f *statefulRelayFake) MarkDelivered(ctx context.Context, eventID uuid.UUID, deliveredAt time.Time) error {
	f.markDeliveredCalls[eventID]++
	if f.failFirstMark && f.markDeliveredCalls[eventID] == 1 {
		f.lastDeliveryError[eventID] = "mark delivered unavailable"
		return errors.New(f.lastDeliveryError[eventID])
	}
	f.delivered[eventID] = true
	f.deliveredAt[eventID] = deliveredAt.UTC()
	f.lastDeliveryError[eventID] = ""
	return nil
}

func (f *statefulRelayFake) RecordFailure(ctx context.Context, eventID uuid.UUID, errMessage string, nextAvailableAt time.Time) error {
	f.failures = append(f.failures, eventID)
	f.lastDeliveryError[eventID] = errMessage
	f.nextAvailableAt[eventID] = nextAvailableAt.UTC()
	delete(f.leaseUntil, eventID)
	return nil
}

func (f *statefulRelayFake) makeAvailable(eventID uuid.UUID) {
	f.nextAvailableAt[eventID] = f.now.Add(-time.Second)
}

func (f *statefulRelayFake) advance(duration time.Duration) {
	f.now = f.now.Add(duration)
}

func (f *statefulRelayFake) ReconcilePending(ctx context.Context, minAge time.Duration, criticalAge time.Duration) (int, int, error) {
	return 0, 0, nil
}

func (f *statefulRelayFake) CleanupDelivered(ctx context.Context, olderThan time.Duration) (int64, error) {
	return 0, nil
}

func (f *statefulRelayFake) InsertProjection(ctx context.Context, tx repository.Transaction, event domain.AuditEvent) error {
	f.projectionAttempts[event.EventID]++
	if f.failFirstProjection && f.projectionAttempts[event.EventID] == 1 {
		f.lastDeliveryError[event.EventID] = "projection unavailable"
		return errors.New(f.lastDeliveryError[event.EventID])
	}
	if _, exists := f.projectedBySourceEventID[event.EventID]; exists {
		return nil
	}
	f.projectedBySourceEventID[event.EventID] = event
	f.projected = append(f.projected, event)
	return nil
}

func (f *statefulRelayFake) FindLogBySourceEventID(ctx context.Context, sourceEventID uuid.UUID) (*domain.AuditEvent, error) {
	event, ok := f.projectedBySourceEventID[sourceEventID]
	if !ok {
		return nil, nil
	}
	return &event, nil
}

func countProjectedEvents(events []domain.AuditEvent, sourceEventID uuid.UUID) int {
	count := 0
	for _, event := range events {
		if event.EventID == sourceEventID {
			count++
		}
	}
	return count
}

func TestRelayService_RestartRetriesFailedProjection(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	event := domain.AuditEvent{
		EventID:    uuid.New(),
		EntityType: "disbursement",
		EntityID:   uuid.New(),
		Action:     "disbursement.approved",
		ActorID:    uuid.New(),
		RequestID:  uuid.New(),
		BeforeData: json.RawMessage(`{"Status":"PENDING","Amount":100000}`),
		AfterData:  json.RawMessage(`{"Status":"APPROVED","Amount":100000}`),
		OccurredAt: time.Date(2026, time.August, 9, 7, 0, 0, 0, time.UTC),
	}
	fake := newStatefulRelayFake(event)
	firstService, err := NewRelayService(fake, fake, nil, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})
	if err != nil {
		t.Fatalf("failed to create first relay service: %v", err)
	}

	count, err := firstService.ProcessBatch(context.Background(), 1)
	if err != nil {
		t.Fatalf("first relay attempt failed unexpectedly: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected failed first attempt to process 0 events, got %d", count)
	}
	if len(fake.failures) != 1 || fake.failures[0] != event.EventID {
		t.Fatalf("expected event failure to be persisted, got %+v", fake.failures)
	}
	if fake.delivered[event.EventID] {
		t.Fatal("failed event must remain undelivered")
	}
	if fake.projectionAttempts[event.EventID] != 1 || fake.lastDeliveryError[event.EventID] != "projection unavailable" {
		t.Fatalf("expected one persisted projection failure, attempts=%d error=%q", fake.projectionAttempts[event.EventID], fake.lastDeliveryError[event.EventID])
	}
	if fake.nextAvailableAt[event.EventID].IsZero() {
		t.Fatal("expected projection failure to persist a retry availability time")
	}
	if pending, err := fake.FetchPending(context.Background(), 1); err != nil {
		t.Fatalf("fetch pending before backoff expiry failed: %v", err)
	} else if len(pending) != 0 {
		t.Fatalf("expected backoff to suppress immediate retry, got %d pending events", len(pending))
	}
	fake.makeAvailable(event.EventID)

	secondService, err := NewRelayService(fake, fake, nil, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})
	if err != nil {
		t.Fatalf("failed to create restarted relay service: %v", err)
	}
	count, err = secondService.ProcessBatch(context.Background(), 1)
	if err != nil {
		t.Fatalf("restart relay attempt failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected restarted relay to process 1 event, got %d", count)
	}
	if !fake.delivered[event.EventID] {
		t.Fatal("expected retried event to be marked delivered")
	}
	if fake.markDeliveredCalls[event.EventID] != 1 || fake.deliveredAt[event.EventID].IsZero() {
		t.Fatalf("expected one persisted delivery timestamp, calls=%d delivered_at=%s", fake.markDeliveredCalls[event.EventID], fake.deliveredAt[event.EventID])
	}
	if fake.deliveredAt[event.EventID].Location() != time.UTC {
		t.Fatalf("expected delivery timestamp in UTC, got %s", fake.deliveredAt[event.EventID].Location())
	}
	if projectedCount := countProjectedEvents(fake.projected, event.EventID); projectedCount > 1 {
		t.Fatalf("expected at most one projected event for source event %s, got %d", event.EventID, projectedCount)
	}
	if len(fake.projected) != 1 {
		t.Fatalf("expected one projected event, got %d", len(fake.projected))
	}
	projected := fake.projected[0]
	if projected.EventID != event.EventID || projected.EntityType != "disbursement" || projected.EntityID != event.EntityID || projected.Action != "disbursement.approved" || projected.ActorID != event.ActorID || projected.RequestID != event.RequestID || !projected.OccurredAt.Equal(event.OccurredAt) {
		t.Fatalf("unexpected projected event identity/timestamp: %+v", projected)
	}
	if string(projected.BeforeData) != string(event.BeforeData) || string(projected.AfterData) != string(event.AfterData) {
		t.Fatalf("unexpected projected semantic data: before=%s after=%s", projected.BeforeData, projected.AfterData)
	}
}

func TestRelayService_RetriesAfterMarkDeliveredFailure(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	event := domain.AuditEvent{
		EventID:    uuid.New(),
		EntityType: "disbursement",
		EntityID:   uuid.New(),
		Action:     "disbursement.created",
		ActorID:    uuid.New(),
		RequestID:  uuid.New(),
		BeforeData: json.RawMessage(`null`),
		AfterData:  json.RawMessage(`{"Status":"PENDING"}`),
		OccurredAt: time.Date(2026, time.August, 9, 7, 0, 0, 0, time.UTC),
	}
	fake := newStatefulRelayFake(event)
	fake.failFirstProjection = false
	fake.failFirstMark = true
	service, err := NewRelayService(fake, fake, nil, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})
	if err != nil {
		t.Fatalf("failed to create relay service: %v", err)
	}

	count, err := service.ProcessBatch(context.Background(), 1)
	if err != nil {
		t.Fatalf("first relay attempt failed unexpectedly: %v", err)
	}
	if count != 0 || fake.delivered[event.EventID] {
		t.Fatalf("expected mark failure to leave event pending, count=%d delivered=%t", count, fake.delivered[event.EventID])
	}
	if fake.projectionAttempts[event.EventID] != 1 || fake.markDeliveredCalls[event.EventID] != 1 || countProjectedEvents(fake.projected, event.EventID) != 1 {
		t.Fatalf("unexpected first attempt state: projection_attempts=%d mark_calls=%d projected=%d", fake.projectionAttempts[event.EventID], fake.markDeliveredCalls[event.EventID], countProjectedEvents(fake.projected, event.EventID))
	}
	if len(fake.failures) != 0 || !fake.nextAvailableAt[event.EventID].IsZero() {
		t.Fatalf("mark failure must not create projection backoff state, failures=%d next_available_at=%s", len(fake.failures), fake.nextAvailableAt[event.EventID])
	}
	if pending, err := fake.FetchPending(context.Background(), 1); err != nil {
		t.Fatalf("fetch pending before lease expiry failed: %v", err)
	} else if len(pending) != 0 {
		t.Fatalf("expected lease to suppress immediate retry, got %d pending events", len(pending))
	}
	fake.advance(statefulRelayLeaseDuration)

	count, err = service.ProcessBatch(context.Background(), 1)
	if err != nil {
		t.Fatalf("retry relay attempt failed: %v", err)
	}
	if count != 1 || !fake.delivered[event.EventID] {
		t.Fatalf("expected retry to deliver event, count=%d delivered=%t", count, fake.delivered[event.EventID])
	}
	if fake.projectionAttempts[event.EventID] != 2 || fake.markDeliveredCalls[event.EventID] != 2 {
		t.Fatalf("unexpected retry state: projection_attempts=%d mark_calls=%d", fake.projectionAttempts[event.EventID], fake.markDeliveredCalls[event.EventID])
	}
	if projectedCount := countProjectedEvents(fake.projected, event.EventID); projectedCount > 1 {
		t.Fatalf("expected at most one projected event for source event %s after mark retry, got %d", event.EventID, projectedCount)
	}
	if len(fake.projected) != 1 {
		t.Fatalf("expected one projected event after mark retry, got %d", len(fake.projected))
	}
	if fake.deliveredAt[event.EventID].IsZero() || fake.deliveredAt[event.EventID].Location() != time.UTC {
		t.Fatalf("expected UTC delivery timestamp after retry, got %s", fake.deliveredAt[event.EventID])
	}
	if fake.lastDeliveryError[event.EventID] != "" {
		t.Fatalf("expected delivery error to clear after successful retry, got %q", fake.lastDeliveryError[event.EventID])
	}
}

func TestRelayService_StartWorkerStopWorkerAndCleanup(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	t.Run("Cleanup passes correct retention parameter and returns deleted count", func(t *testing.T) {
		var receivedOlderThan time.Duration
		outboxStore := &mockAuditOutboxStore{
			cleanupDeliveredFunc: func(ctx context.Context, olderThan time.Duration) (int64, error) {
				receivedOlderThan = olderThan
				return 5, nil
			},
		}
		service, err := NewRelayService(outboxStore, &mockAuditProjectionStore{}, nil, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})
		if err != nil {
			t.Fatalf("failed to create relay service: %v", err)
		}

		count, err := service.Cleanup(context.Background())
		if err != nil || count != 5 {
			t.Fatalf("expected cleanup count 5, got count=%d err=%v", count, err)
		}
		expectedRetention := 30 * 24 * time.Hour
		if receivedOlderThan != expectedRetention {
			t.Fatalf("expected cleanup retention duration %v, got %v", expectedRetention, receivedOlderThan)
		}
	})

	t.Run("Cleanup error handling", func(t *testing.T) {
		outboxStoreErr := &mockAuditOutboxStore{
			cleanupDeliveredFunc: func(ctx context.Context, olderThan time.Duration) (int64, error) {
				return 0, errors.New("cleanup error")
			},
		}
		serviceErr, _ := NewRelayService(outboxStoreErr, &mockAuditProjectionStore{}, nil, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})
		_, err := serviceErr.Cleanup(context.Background())
		if err == nil {
			t.Fatal("expected error on cleanup failure, got nil")
		}
	})

	t.Run("StartWorker lifecycle and double start prevention", func(t *testing.T) {
		workerFetched := make(chan struct{}, 1)
		outboxStore := &mockAuditOutboxStore{
			fetchPendingFunc: func(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
				select {
				case workerFetched <- struct{}{}:
				default:
				}
				return nil, nil
			},
		}
		service, _ := NewRelayService(outboxStore, &mockAuditProjectionStore{}, nil, logger, RelayConfig{OutboxRetention: 30 * 24 * time.Hour, WarningAge: 5 * time.Minute, CriticalAge: 15 * time.Minute})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		if err := service.StartWorker(ctx, 10*time.Millisecond, 10); err != nil {
			t.Fatalf("failed to start worker: %v", err)
		}

		waitForRelaySignal(t, workerFetched, "worker initial fetch")

		// Double start should fail
		if err := service.StartWorker(ctx, 10*time.Millisecond, 10); err == nil {
			t.Fatal("expected error starting already running worker")
		}

		stopRelayWorker(t, service)
	})
}
