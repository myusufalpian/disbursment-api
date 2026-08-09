package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"disbursment-api/internal/domain"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

func TestAuditOutboxStore_InsertAndRelayMethods(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("sqlmock expectations: %v", err)
		}
	})

	sqlxDB := sqlx.NewDb(db, "sqlmock")
	store := NewAuditOutboxStore(sqlxDB)
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	event := domain.AuditEvent{
		EventID:    uuid.New(),
		EntityType: "disbursement",
		EntityID:   uuid.New(),
		Action:     "disbursement.created",
		ActorID:    uuid.New(),
		RequestID:  uuid.New(),
		BeforeData: []byte(`null`),
		AfterData:  []byte(`{"amount":100000}`),
		OccurredAt: now,
	}

	t.Run("Insert outbox event success", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectExec("^INSERT INTO audit_outbox").
			WithArgs(event.EventID, event.EntityType, event.EntityID, event.Action, event.ActorID, event.RequestID, string(event.BeforeData), string(event.AfterData), event.OccurredAt).
			WillReturnResult(sqlmock.NewResult(1, 1))

		tx := beginSQLMockTx(t, mock, sqlxDB)
		err := store.Insert(context.Background(), newTestTx(tx), event)
		if err != nil {
			t.Fatalf("Insert outbox failed: %v", err)
		}
	})

	t.Run("Insert outbox event invalid event", func(t *testing.T) {
		invalidEvent := domain.AuditEvent{}
		err := store.Insert(context.Background(), nil, invalidEvent)
		if err == nil {
			t.Fatalf("expected error for invalid event")
		}
	})

	t.Run("Insert outbox event failure", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectExec("^INSERT INTO audit_outbox").
			WillReturnError(errors.New("db insert failure"))

		tx := beginSQLMockTx(t, mock, sqlxDB)
		err := store.Insert(context.Background(), newTestTx(tx), event)
		if err == nil {
			t.Fatalf("expected error for db insert failure")
		}
	})

	t.Run("FetchPending success", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"event_id", "entity_type", "entity_id", "action", "actor_id", "request_id", "before_data", "after_data", "occurred_at"}).
			AddRow(event.EventID, event.EntityType, event.EntityID, event.Action, event.ActorID, event.RequestID, string(event.BeforeData), string(event.AfterData), event.OccurredAt)

		mock.ExpectQuery("^UPDATE audit_outbox SET available_at =").
			WithArgs(now.Add(5*time.Minute), now, 50).
			WillReturnRows(rows)

		fetched, err := store.FetchPending(context.Background(), 0)
		if err != nil {
			t.Fatalf("FetchPending failed: %v", err)
		}
		if len(fetched) != 1 {
			t.Fatalf("expected 1 event, got %d", len(fetched))
		}
	})

	t.Run("MarkDelivered success", func(t *testing.T) {
		deliveredAt := now.Add(time.Hour)
		mock.ExpectExec("^UPDATE audit_outbox SET delivery_state = 'DELIVERED'").
			WithArgs(event.EventID, deliveredAt).
			WillReturnResult(sqlmock.NewResult(1, 1))

		err := store.MarkDelivered(context.Background(), event.EventID, deliveredAt)
		if err != nil {
			t.Fatalf("MarkDelivered failed: %v", err)
		}
	})

	t.Run("RecordFailure success", func(t *testing.T) {
		nextAvailableAt := now.Add(2 * time.Minute)
		mock.ExpectExec("^UPDATE audit_outbox SET delivery_attempts = delivery_attempts \\+ 1").
			WithArgs(event.EventID, "connection timeout", nextAvailableAt).
			WillReturnResult(sqlmock.NewResult(1, 1))

		err := store.RecordFailure(context.Background(), event.EventID, "connection timeout", nextAvailableAt)
		if err != nil {
			t.Fatalf("RecordFailure failed: %v", err)
		}
	})

	t.Run("ReconcilePending success", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"warning_count", "critical_count"}).AddRow(2, 1)
		mock.ExpectQuery("^SELECT COALESCE").
			WithArgs(now.Add(-5*time.Minute), now.Add(-15*time.Minute)).
			WillReturnRows(rows)

		warn, crit, err := store.ReconcilePending(context.Background(), 5*time.Minute)
		if err != nil {
			t.Fatalf("ReconcilePending failed: %v", err)
		}
		if warn != 2 || crit != 1 {
			t.Fatalf("expected warn 2 crit 1, got warn %d crit %d", warn, crit)
		}
	})

	t.Run("CleanupDelivered success", func(t *testing.T) {
		mock.ExpectExec("^DELETE FROM audit_outbox WHERE delivery_state = 'DELIVERED'").
			WithArgs(now.Add(-30 * 24 * time.Hour)).
			WillReturnResult(sqlmock.NewResult(0, 5))

		cleaned, err := store.CleanupDelivered(context.Background(), 30*24*time.Hour)
		if err != nil {
			t.Fatalf("CleanupDelivered failed: %v", err)
		}
		if cleaned != 5 {
			t.Fatalf("expected 5 cleaned, got %d", cleaned)
		}
	})

	t.Run("Nil database returns error", func(t *testing.T) {
		nilStore := NewAuditOutboxStore(nil)
		_, err := nilStore.FetchPending(context.Background(), 10)
		if err == nil {
			t.Fatalf("expected error for nil db")
		}
		err = nilStore.MarkDelivered(context.Background(), event.EventID, time.Now())
		if err == nil {
			t.Fatalf("expected error for nil db")
		}
	})
}
