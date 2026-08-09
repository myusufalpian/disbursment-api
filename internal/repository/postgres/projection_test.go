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

func TestAuditProjectionStore_InsertProjection(t *testing.T) {
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
	store := NewAuditProjectionStore(sqlxDB)

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

	t.Run("Insert projection with direct DB connection", func(t *testing.T) {
		mock.ExpectExec("^INSERT INTO audit_logs").
			WithArgs(event.EventID, event.EntityType, event.EntityID, event.Action, event.ActorID, event.RequestID, string(event.BeforeData), string(event.AfterData), event.OccurredAt).
			WillReturnResult(sqlmock.NewResult(1, 1))

		err := store.InsertProjection(context.Background(), nil, event)
		if err != nil {
			t.Fatalf("InsertProjection failed: %v", err)
		}
	})

	t.Run("Insert projection with transaction", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectExec("^INSERT INTO audit_logs").
			WithArgs(event.EventID, event.EntityType, event.EntityID, event.Action, event.ActorID, event.RequestID, string(event.BeforeData), string(event.AfterData), event.OccurredAt).
			WillReturnResult(sqlmock.NewResult(1, 1))

		tx := beginSQLMockTx(t, mock, sqlxDB)
		err := store.InsertProjection(context.Background(), newTestTx(tx), event)
		if err != nil {
			t.Fatalf("InsertProjection with tx failed: %v", err)
		}
	})

	t.Run("Insert projection invalid event error", func(t *testing.T) {
		invalidEvent := domain.AuditEvent{}
		err := store.InsertProjection(context.Background(), nil, invalidEvent)
		if err == nil {
			t.Fatalf("expected error for invalid event")
		}
	})

	t.Run("Insert projection DB failure", func(t *testing.T) {
		mock.ExpectExec("^INSERT INTO audit_logs").
			WillReturnError(errors.New("db failure"))

		err := store.InsertProjection(context.Background(), nil, event)
		if err == nil {
			t.Fatalf("expected error for db failure")
		}
	})
}
