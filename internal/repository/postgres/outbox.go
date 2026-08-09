package postgres

import (
	"context"
	"fmt"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/repository"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

const (
	insertAuditOutboxEvent = `
INSERT INTO audit_outbox (
    event_id, entity_type, entity_id, action, actor_id, request_id,
    before_data, after_data, occurred_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
)`

	fetchPendingOutboxEvents = `
UPDATE audit_outbox
SET available_at = $1
WHERE event_id IN (
    SELECT event_id
    FROM audit_outbox
    WHERE delivery_state = 'PENDING' AND available_at <= $2
    ORDER BY available_at ASC, occurred_at ASC
    LIMIT $3
    FOR UPDATE SKIP LOCKED
)
RETURNING event_id, entity_type, entity_id, action, actor_id, request_id, before_data, after_data, occurred_at`

	markOutboxDelivered = `
UPDATE audit_outbox
SET delivery_state = 'DELIVERED', delivered_at = $2, delivery_attempts = delivery_attempts + 1, last_delivery_error = NULL
WHERE event_id = $1 AND delivery_state = 'PENDING'`

	recordOutboxFailure = `
UPDATE audit_outbox
SET delivery_attempts = delivery_attempts + 1, last_delivery_error = $2, available_at = $3
WHERE event_id = $1 AND delivery_state = 'PENDING'`

	reconcileOutboxPending = `
SELECT
    COALESCE(COUNT(*) FILTER (WHERE delivery_state = 'PENDING' AND occurred_at <= $1), 0) AS warning_count,
    COALESCE(COUNT(*) FILTER (WHERE delivery_state = 'PENDING' AND occurred_at <= $2), 0) AS critical_count
FROM audit_outbox`

	cleanupDeliveredOutbox = `
DELETE FROM audit_outbox
WHERE delivery_state = 'DELIVERED' AND delivered_at < $1`
)

type auditOutboxRow struct {
	EventID    uuid.UUID `db:"event_id"`
	EntityType string    `db:"entity_type"`
	EntityID   uuid.UUID `db:"entity_id"`
	Action     string    `db:"action"`
	ActorID    uuid.UUID `db:"actor_id"`
	RequestID  uuid.UUID `db:"request_id"`
	BeforeData []byte    `db:"before_data"`
	AfterData  []byte    `db:"after_data"`
	OccurredAt time.Time `db:"occurred_at"`
}

type AuditOutboxStore struct {
	database      *sqlx.DB
	leaseDuration time.Duration
}

func NewAuditOutboxStore(database *sqlx.DB) *AuditOutboxStore {
	return NewAuditOutboxStoreWithLease(database, 5*time.Minute)
}

func NewAuditOutboxStoreWithLease(database *sqlx.DB, leaseDuration time.Duration) *AuditOutboxStore {
	if leaseDuration <= 0 {
		leaseDuration = 5 * time.Minute
	}
	return &AuditOutboxStore{
		database:      database,
		leaseDuration: leaseDuration,
	}
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

func (store *AuditOutboxStore) FetchPending(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
	if store.database == nil {
		return nil, repository.NewError(repository.ErrorDependency, fmt.Errorf("database connection required"))
	}
	if limit <= 0 {
		limit = 50
	}
	leaseTTL := store.leaseDuration
	if leaseTTL <= 0 {
		leaseTTL = 5 * time.Minute
	}
	now := time.Now().UTC()
	leaseUntil := now.Add(leaseTTL)
	var rows []auditOutboxRow
	err := store.database.SelectContext(ctx, &rows, fetchPendingOutboxEvents, leaseUntil, now, limit)
	if err != nil {
		return nil, repository.Classify(err)
	}
	events := make([]domain.AuditEvent, 0, len(rows))
	for _, r := range rows {
		events = append(events, domain.AuditEvent{
			EventID:    r.EventID,
			EntityType: r.EntityType,
			EntityID:   r.EntityID,
			Action:     r.Action,
			ActorID:    r.ActorID,
			RequestID:  r.RequestID,
			BeforeData: r.BeforeData,
			AfterData:  r.AfterData,
			OccurredAt: r.OccurredAt.UTC(),
		})
	}
	return events, nil
}

func (store *AuditOutboxStore) MarkDelivered(ctx context.Context, eventID uuid.UUID, deliveredAt time.Time) error {
	if store.database == nil {
		return repository.NewError(repository.ErrorDependency, fmt.Errorf("database connection required"))
	}
	if eventID == uuid.Nil {
		return repository.NewError(repository.ErrorConstraint, fmt.Errorf("event ID required"))
	}
	if deliveredAt.IsZero() {
		deliveredAt = time.Now().UTC()
	}
	_, err := store.database.ExecContext(ctx, markOutboxDelivered, eventID, deliveredAt.UTC())
	if err != nil {
		return repository.Classify(err)
	}
	return nil
}

func (store *AuditOutboxStore) RecordFailure(ctx context.Context, eventID uuid.UUID, errMessage string, nextAvailableAt time.Time) error {
	if store.database == nil {
		return repository.NewError(repository.ErrorDependency, fmt.Errorf("database connection required"))
	}
	if eventID == uuid.Nil {
		return repository.NewError(repository.ErrorConstraint, fmt.Errorf("event ID required"))
	}
	if nextAvailableAt.IsZero() {
		nextAvailableAt = time.Now().UTC().Add(5 * time.Second)
	}
	_, err := store.database.ExecContext(ctx, recordOutboxFailure, eventID, errMessage, nextAvailableAt.UTC())
	if err != nil {
		return repository.Classify(err)
	}
	return nil
}

func (store *AuditOutboxStore) ReconcilePending(ctx context.Context, minAge time.Duration) (int, int, error) {
	if store.database == nil {
		return 0, 0, repository.NewError(repository.ErrorDependency, fmt.Errorf("database connection required"))
	}
	if minAge <= 0 {
		minAge = 5 * time.Minute
	}
	now := time.Now().UTC()
	warningThreshold := now.Add(-minAge)
	criticalThreshold := now.Add(-3 * minAge)

	var warningCount, criticalCount int
	row := store.database.QueryRowContext(ctx, reconcileOutboxPending, warningThreshold, criticalThreshold)
	if err := row.Scan(&warningCount, &criticalCount); err != nil {
		return 0, 0, repository.Classify(err)
	}
	return warningCount, criticalCount, nil
}

func (store *AuditOutboxStore) CleanupDelivered(ctx context.Context, olderThan time.Duration) (int64, error) {
	if store.database == nil {
		return 0, repository.NewError(repository.ErrorDependency, fmt.Errorf("database connection required"))
	}
	if olderThan <= 0 {
		olderThan = 30 * 24 * time.Hour
	}
	cutoff := time.Now().UTC().Add(-olderThan)
	res, err := store.database.ExecContext(ctx, cleanupDeliveredOutbox, cutoff)
	if err != nil {
		return 0, repository.Classify(err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, repository.Classify(err)
	}
	return affected, nil
}
