package postgres

import (
	"context"
	"fmt"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/repository"

	"github.com/google/uuid"
)

const insertAuditOutboxEvent = `
INSERT INTO audit_outbox (
    event_id, entity_type, entity_id, action, actor_id, request_id,
    before_data, after_data, occurred_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
)`

type AuditOutboxStore struct{}

func NewAuditOutboxStore() *AuditOutboxStore {
	return &AuditOutboxStore{}
}

func (store *AuditOutboxStore) Insert(ctx context.Context, transaction repository.Transaction, event domain.AuditEvent) error {
	if event.EventID == uuid.Nil || event.EntityID == uuid.Nil || event.ActorID == uuid.Nil || event.RequestID == uuid.Nil || event.EntityType == "" || event.Action == "" || event.OccurredAt.IsZero() {
		return repository.NewError(repository.ErrorConstraint, fmt.Errorf("invalid audit event"))
	}
	tx, err := unwrapTransaction(transaction)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(
		ctx,
		insertAuditOutboxEvent,
		event.EventID,
		event.EntityType,
		event.EntityID,
		event.Action,
		event.ActorID,
		event.RequestID,
		event.BeforeData,
		event.AfterData,
		event.OccurredAt,
	)
	if err != nil {
		return repository.Classify(err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return repository.Classify(err)
	}
	if rowsAffected != 1 {
		return repository.NewError(repository.ErrorConstraint, fmt.Errorf("audit outbox insert affected %d rows", rowsAffected))
	}
	return nil
}
