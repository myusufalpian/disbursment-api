package postgres

import (
	"context"
	"fmt"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/repository"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

const insertAuditProjectionLog = `
INSERT INTO audit_logs (
    source_event_id, entity_type, entity_id, action, actor_id, request_id,
    before_data, after_data, occurred_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
)
ON CONFLICT (source_event_id) DO NOTHING`

type AuditProjectionStore struct {
	database *sqlx.DB
}

func NewAuditProjectionStore(database *sqlx.DB) *AuditProjectionStore {
	return &AuditProjectionStore{database: database}
}

func (store *AuditProjectionStore) InsertProjection(ctx context.Context, transaction repository.Transaction, event domain.AuditEvent) error {
	if event.EventID == uuid.Nil || event.EntityID == uuid.Nil || event.ActorID == uuid.Nil || event.RequestID == uuid.Nil || event.EntityType == "" || event.Action == "" || event.OccurredAt.IsZero() {
		return repository.NewError(repository.ErrorConstraint, fmt.Errorf("invalid audit event for projection"))
	}

	if transaction != nil {
		tx, err := unwrapTransaction(transaction)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(
			ctx,
			insertAuditProjectionLog,
			event.EventID,
			event.EntityType,
			event.EntityID,
			event.Action,
			event.ActorID,
			event.RequestID,
			nullableJSON(event.BeforeData),
			nullableJSON(event.AfterData),
			event.OccurredAt,
		)
		if err != nil {
			return repository.Classify(err)
		}
		return nil
	}

	if store.database == nil {
		return repository.NewError(repository.ErrorDependency, fmt.Errorf("database connection required"))
	}

	_, err := store.database.ExecContext(
		ctx,
		insertAuditProjectionLog,
		event.EventID,
		event.EntityType,
		event.EntityID,
		event.Action,
		event.ActorID,
		event.RequestID,
		nullableJSON(event.BeforeData),
		nullableJSON(event.AfterData),
		event.OccurredAt,
	)
	if err != nil {
		return repository.Classify(err)
	}
	return nil
}

const findAuditProjectionLogBySourceEventID = `
SELECT source_event_id AS event_id, entity_type, entity_id, action, actor_id, request_id, before_data, after_data, occurred_at
FROM audit_logs
WHERE source_event_id = $1`

func (store *AuditProjectionStore) FindLogBySourceEventID(ctx context.Context, sourceEventID uuid.UUID) (*domain.AuditEvent, error) {
	if store.database == nil {
		return nil, repository.NewError(repository.ErrorDependency, fmt.Errorf("database connection required"))
	}
	var row auditOutboxRow
	err := store.database.GetContext(ctx, &row, findAuditProjectionLogBySourceEventID, sourceEventID)
	if err != nil {
		return nil, repository.Classify(err)
	}
	return &domain.AuditEvent{
		EventID:    row.EventID,
		EntityType: row.EntityType,
		EntityID:   row.EntityID,
		Action:     row.Action,
		ActorID:    row.ActorID,
		RequestID:  row.RequestID,
		BeforeData: row.BeforeData,
		AfterData:  row.AfterData,
		OccurredAt: row.OccurredAt.UTC(),
	}, nil
}
