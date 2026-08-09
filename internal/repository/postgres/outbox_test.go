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

	sqlxDB := sqlx.NewDb(db, "sqlmock")
	store := NewAuditOutboxStore(sqlxDB)

	event := domain.AuditEvent{
		EventID:    uuid.New(),
		EntityType: "disbursement",
		EntityID:   uuid.New(),
		Action:     "disbursement.created",
		ActorID:    uuid.New(),
		RequestID:  uuid.New(),
		BeforeData: []byte(`null`),
		AfterData:  []byte(`{"amount":100000}`),
		OccurredAt: time.Now().UTC(),
	}

	t.Run("Insert outbox event success", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectExec("^INSERT INTO audit_outbox").
			WithArgs(event.EventID, event.EntityType, event.EntityID, event.Action, event.ActorID, event.RequestID, event.BeforeData, event.AfterData, event.OccurredAt).
			WillReturnResult(sqlmock.NewResult(1, 1))

		tx, _ := sqlxDB.BeginTxx(context.Background(), nil)
		err := store.Insert(context.Background(), newTestTx(tx), event)
		if err != nil {
			t.Fatalf("Insert outbox failed: %v", err)
		}
	})

	t.Run("Insert outbox event invalid event", func(t *testing.T) {
		invalidEvent := domain.AuditEvent{}
		tx, _ := sqlxDB.BeginTxx(context.Background(), nil)
		err := store.Insert(context.Background(), newTestTx(tx), invalidEvent)
		if err == nil {
			t.Fatalf("expected error for invalid event")
		}
	})

	t.Run("Insert outbox event failure", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectExec("^INSERT INTO audit_outbox").
			WillReturnError(errors.New("db insert failure"))

		tx, _ := sqlxDB.BeginTxx(context.Background(), nil)
		err := store.Insert(context.Background(), newTestTx(tx), event)
		if err == nil {
			t.Fatalf("expected error for db insert failure")
		}
	})

	t.Run("FetchPending success", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"event_id", "entity_type", "entity_id", "action", "actor_id", "request_id", "before_data", "after_data", "occurred_at"}).
			AddRow(event.EventID, event.EntityType, event.EntityID, event.Action, event.ActorID, event.RequestID, event.BeforeData, event.AfterData, event.OccurredAt)

		mock.ExpectQuery("^UPDATE audit_outbox SET available_at =").
			WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), 50).
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
		mock.ExpectExec("^UPDATE audit_outbox SET delivery_state = 'DELIVERED'").
			WithArgs(event.EventID, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(1, 1))

		err := store.MarkDelivered(context.Background(), event.EventID, time.Now().UTC())
		if err != nil {
			t.Fatalf("MarkDelivered failed: %v", err)
		}
	})

	t.Run("RecordFailure success", func(t *testing.T) {
		mock.ExpectExec("^UPDATE audit_outbox SET delivery_attempts = delivery_attempts \\+ 1").
			WithArgs(event.EventID, "connection timeout", sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(1, 1))

		err := store.RecordFailure(context.Background(), event.EventID, "connection timeout", time.Now().UTC())
		if err != nil {
			t.Fatalf("RecordFailure failed: %v", err)
		}
	})

	t.Run("ReconcilePending success", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"warning_count", "critical_count"}).AddRow(2, 1)
		mock.ExpectQuery("^SELECT COALESCE").
			WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
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
			WithArgs(sqlmock.AnyArg()).
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
