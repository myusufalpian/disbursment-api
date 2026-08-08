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

func TestAuditOutboxStore_Insert(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	sqlxDB := sqlx.NewDb(db, "sqlmock")
	store := NewAuditOutboxStore()

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
}
