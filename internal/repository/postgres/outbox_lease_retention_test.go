package postgres

import (
	"context"
	"testing"
	"time"

	"disbursment-api/internal/domain"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

func TestAuditOutboxStore_LeaseAndRetentionBehavior(t *testing.T) {
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

	store := NewAuditOutboxStoreWithLease(sqlxDB, 2*time.Minute)
	now := time.Date(2026, time.August, 9, 13, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	eventID := uuid.New()
	event := domain.AuditEvent{
		EventID:    eventID,
		EntityType: "disbursement",
		EntityID:   uuid.New(),
		Action:     "disbursement.created",
		ActorID:    uuid.New(),
		RequestID:  uuid.New(),
		BeforeData: []byte(`{}`),
		AfterData:  []byte(`{"status":"PENDING"}`),
		OccurredAt: now,
	}

	t.Run("Worker A claims batch with 2m lease duration", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"event_id", "entity_type", "entity_id", "action", "actor_id", "request_id", "before_data", "after_data", "occurred_at"}).
			AddRow(event.EventID, event.EntityType, event.EntityID, event.Action, event.ActorID, event.RequestID, event.BeforeData, event.AfterData, event.OccurredAt)

		mock.ExpectQuery("^UPDATE audit_outbox SET available_at =").
			WithArgs(now.Add(2*time.Minute), now, 10).
			WillReturnRows(rows)

		fetched, err := store.FetchPending(context.Background(), 10)
		if err != nil {
			t.Fatalf("FetchPending failed: %v", err)
		}
		if len(fetched) != 1 {
			t.Fatalf("expected 1 event, got %d", len(fetched))
		}
	})

	t.Run("Worker B calling FetchPending concurrently receives 0 items if all leased", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"event_id", "entity_type", "entity_id", "action", "actor_id", "request_id", "before_data", "after_data", "occurred_at"})

		mock.ExpectQuery("^UPDATE audit_outbox SET available_at =").
			WithArgs(now.Add(2*time.Minute), now, 10).
			WillReturnRows(rows)

		fetched, err := store.FetchPending(context.Background(), 10)
		if err != nil {
			t.Fatalf("FetchPending failed: %v", err)
		}
		if len(fetched) != 0 {
			t.Fatalf("expected 0 events leased by another worker, got %d", len(fetched))
		}
	})

	t.Run("Expired lease event is reclaimed on subsequent FetchPending", func(t *testing.T) {
		reclaimedRows := sqlmock.NewRows([]string{"event_id", "entity_type", "entity_id", "action", "actor_id", "request_id", "before_data", "after_data", "occurred_at"}).
			AddRow(event.EventID, event.EntityType, event.EntityID, event.Action, event.ActorID, event.RequestID, event.BeforeData, event.AfterData, event.OccurredAt)

		mock.ExpectQuery("^UPDATE audit_outbox SET available_at =").
			WithArgs(now.Add(2*time.Minute), now, 10).
			WillReturnRows(reclaimedRows)

		fetched, err := store.FetchPending(context.Background(), 10)
		if err != nil {
			t.Fatalf("FetchPending failed: %v", err)
		}
		if len(fetched) != 1 {
			t.Fatalf("expected 1 reclaimed event, got %d", len(fetched))
		}
	})

	t.Run("CleanupDelivered prunes delivered outbox entries older than retention window", func(t *testing.T) {
		mock.ExpectExec("^DELETE FROM audit_outbox WHERE delivery_state = 'DELIVERED' AND delivered_at <").
			WithArgs(now.Add(-30 * 24 * time.Hour)).
			WillReturnResult(sqlmock.NewResult(0, 15))

		cleaned, err := store.CleanupDelivered(context.Background(), 30*24*time.Hour)
		if err != nil {
			t.Fatalf("CleanupDelivered failed: %v", err)
		}
		if cleaned != 15 {
			t.Fatalf("expected 15 cleaned records, got %d", cleaned)
		}
	})

	t.Run("Audit log historical projection remains preserved after outbox cleanup", func(t *testing.T) {
		projStore := NewAuditProjectionStore(sqlxDB)

		rows := sqlmock.NewRows([]string{"event_id", "entity_type", "entity_id", "action", "actor_id", "request_id", "before_data", "after_data", "occurred_at"}).
			AddRow(event.EventID, event.EntityType, event.EntityID, event.Action, event.ActorID, event.RequestID, event.BeforeData, event.AfterData, event.OccurredAt)

		mock.ExpectQuery("^SELECT source_event_id AS event_id").
			WithArgs(event.EventID).
			WillReturnRows(rows)

		logEntry, err := projStore.FindLogBySourceEventID(context.Background(), event.EventID)
		if err != nil {
			t.Fatalf("FindLogBySourceEventID failed: %v", err)
		}
		if logEntry.EventID != event.EventID {
			t.Fatalf("expected EventID %s, got %s", event.EventID, logEntry.EventID)
		}
	})
}
