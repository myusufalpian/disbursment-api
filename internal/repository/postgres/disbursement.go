package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/repository"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

const (
	insertDisbursementQuery = `
INSERT INTO disbursements (
	id, recipient_name, account_number, bank_code, amount, admin_fee,
	status, note, created_by, decided_by, decision_note, decided_at,
	deleted_at, created_at, updated_at
) VALUES (
	$1, $2, $3, $4, $5, $6,
	$7, $8, $9, $10, $11, $12,
	$13, $14, $15
)`

	findDisbursementByIDQuery = `
SELECT id, recipient_name, account_number, bank_code, amount, admin_fee,
	status, note, created_by, decided_by, decision_note, decided_at,
	deleted_at, created_at, updated_at
FROM disbursements
WHERE id = $1 AND deleted_at IS NULL`

	updateDisbursementStatusQuery = `
UPDATE disbursements
SET status = $1, decided_by = $2, decision_note = $3, decided_at = $4, updated_at = $4
WHERE id = $5 AND status = 'PENDING' AND deleted_at IS NULL
RETURNING id, recipient_name, account_number, bank_code, amount, admin_fee,
	status, note, created_by, decided_by, decision_note, decided_at,
	deleted_at, created_at, updated_at`

	softDeleteDisbursementQuery = `
UPDATE disbursements
SET deleted_at = $1, updated_at = $1
WHERE id = $2 AND status = 'PENDING' AND deleted_at IS NULL
RETURNING id, recipient_name, account_number, bank_code, amount, admin_fee,
	status, note, created_by, decided_by, decision_note, decided_at,
	deleted_at, created_at, updated_at`

	findDisbursementRawByIDQuery = `
SELECT id, recipient_name, account_number, bank_code, amount, admin_fee,
	status, note, created_by, decided_by, decision_note, decided_at,
	deleted_at, created_at, updated_at
FROM disbursements
WHERE id = $1`
)

type dbDisbursement struct {
	ID            uuid.UUID  `db:"id"`
	RecipientName string     `db:"recipient_name"`
	AccountNumber string     `db:"account_number"`
	BankCode      string     `db:"bank_code"`
	Amount        int64      `db:"amount"`
	AdminFee      int64      `db:"admin_fee"`
	Status        string     `db:"status"`
	Note          *string    `db:"note"`
	CreatedBy     uuid.UUID  `db:"created_by"`
	DecidedBy     *uuid.UUID `db:"decided_by"`
	DecisionNote  *string    `db:"decision_note"`
	DecidedAt     *time.Time `db:"decided_at"`
	DeletedAt     *time.Time `db:"deleted_at"`
	CreatedAt     time.Time  `db:"created_at"`
	UpdatedAt     time.Time  `db:"updated_at"`
}

type DisbursementStore struct {
	database *sqlx.DB
	now      func() time.Time
}

func NewDisbursementStore(database *sqlx.DB) *DisbursementStore {
	return &DisbursementStore{
		database: database,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

func (store *DisbursementStore) Insert(ctx context.Context, transaction repository.Transaction, d domain.Disbursement) error {
	tx, err := unwrapTransaction(transaction)
	if err != nil {
		return err
	}

	var note *string
	if d.Note != "" {
		note = &d.Note
	}
	var decisionNote *string
	if d.DecisionNote != "" {
		decisionNote = &d.DecisionNote
	}
	var decidedBy *uuid.UUID
	if d.DecidedBy != uuid.Nil {
		decidedBy = &d.DecidedBy
	}

	_, err = tx.ExecContext(
		ctx,
		insertDisbursementQuery,
		d.ID,
		d.RecipientName,
		d.AccountNumber,
		d.BankCode,
		d.Amount,
		d.AdminFee,
		string(d.Status),
		note,
		d.CreatedBy,
		decidedBy,
		decisionNote,
		d.DecidedAt,
		d.DeletedAt,
		d.CreatedAt,
		d.UpdatedAt,
	)
	if err != nil {
		return repository.Classify(err)
	}
	return nil
}

func (store *DisbursementStore) FindByID(ctx context.Context, id uuid.UUID) (domain.Disbursement, error) {
	var row dbDisbursement
	err := store.database.GetContext(ctx, &row, findDisbursementByIDQuery, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Disbursement{}, repository.NewError(repository.ErrorNotFound, fmt.Errorf("disbursement not found"))
		}
		return domain.Disbursement{}, repository.Classify(err)
	}
	return mapDBDisbursementToDomain(row), nil
}

func (store *DisbursementStore) List(ctx context.Context, filter repository.DisbursementFilter) ([]domain.Disbursement, int, error) {
	var conditions []string
	var args []any
	argIndex := 1

	conditions = append(conditions, "deleted_at IS NULL")

	if filter.Status != "" {
		conditions = append(conditions, fmt.Sprintf("status = $%d", argIndex))
		args = append(args, string(filter.Status))
		argIndex++
	}

	if strings.TrimSpace(filter.Search) != "" {
		searchTerm := "%" + strings.TrimSpace(filter.Search) + "%"
		conditions = append(conditions, fmt.Sprintf("(recipient_name ILIKE $%d OR account_number ILIKE $%d)", argIndex, argIndex))
		args = append(args, searchTerm)
		argIndex++
	}

	if filter.DateRange != nil {
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", argIndex))
		args = append(args, filter.DateRange.FromInclusive)
		argIndex++

		conditions = append(conditions, fmt.Sprintf("created_at < $%d", argIndex))
		args = append(args, filter.DateRange.ToExclusive)
		argIndex++
	}

	whereClause := strings.Join(conditions, " AND ")

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM disbursements WHERE %s", whereClause)
	var total int
	err := store.database.GetContext(ctx, &total, countQuery, args...)
	if err != nil {
		return nil, 0, repository.Classify(err)
	}

	if total == 0 {
		return []domain.Disbursement{}, 0, nil
	}

	sortColumn := "created_at"
	switch filter.SortBy {
	case "amount":
		sortColumn = "amount"
	case "recipient_name":
		sortColumn = "recipient_name"
	case "status":
		sortColumn = "status"
	case "created_at":
		sortColumn = "created_at"
	}

	sortOrder := "DESC"
	if strings.ToLower(filter.SortOrder) == "asc" {
		sortOrder = "ASC"
	}

	page := filter.Page
	if page < 1 {
		page = domain.DefaultPage
	}
	limit := filter.Limit
	if limit < 1 || limit > domain.MaximumLimit {
		limit = domain.DefaultLimit
	}
	offset := (page - 1) * limit

	listQuery := fmt.Sprintf(`
SELECT id, recipient_name, account_number, bank_code, amount, admin_fee,
	status, note, created_by, decided_by, decision_note, decided_at,
	deleted_at, created_at, updated_at
FROM disbursements
WHERE %s
ORDER BY %s %s
LIMIT $%d OFFSET $%d`, whereClause, sortColumn, sortOrder, argIndex, argIndex+1)

	listArgs := append(args, limit, offset)

	var rows []dbDisbursement
	err = store.database.SelectContext(ctx, &rows, listQuery, listArgs...)
	if err != nil {
		return nil, 0, repository.Classify(err)
	}

	result := make([]domain.Disbursement, len(rows))
	for i, row := range rows {
		result[i] = mapDBDisbursementToDomain(row)
	}

	return result, total, nil
}

func (store *DisbursementStore) UpdateStatus(ctx context.Context, transaction repository.Transaction, id uuid.UUID, decision domain.Decision) (domain.Disbursement, error) {
	tx, err := unwrapTransaction(transaction)
	if err != nil {
		return domain.Disbursement{}, err
	}

	now := store.now()
	var decisionNote *string
	if decision.Note != "" {
		decisionNote = &decision.Note
	}

	var updated dbDisbursement
	err = tx.GetContext(ctx, &updated, updateDisbursementStatusQuery, string(decision.Status), decision.ActorID, decisionNote, now, id)
	if err == nil {
		return mapDBDisbursementToDomain(updated), nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Disbursement{}, repository.Classify(err)
	}

	// Lock & check reason for failure
	var existing dbDisbursement
	checkErr := tx.GetContext(ctx, &existing, findDisbursementRawByIDQuery, id)
	if checkErr != nil {
		if errors.Is(checkErr, sql.ErrNoRows) {
			return domain.Disbursement{}, repository.NewError(repository.ErrorNotFound, fmt.Errorf("disbursement not found"))
		}
		return domain.Disbursement{}, repository.Classify(checkErr)
	}

	if existing.DeletedAt != nil {
		return domain.Disbursement{}, repository.NewError(repository.ErrorNotFound, fmt.Errorf("disbursement deleted"))
	}

	if existing.Status != string(domain.StatusPending) {
		return domain.Disbursement{}, repository.NewError(repository.ErrorConflict, fmt.Errorf("disbursement already finalized"))
	}

	return domain.Disbursement{}, repository.NewError(repository.ErrorConflict, fmt.Errorf("concurrent status update conflict"))
}

func (store *DisbursementStore) SoftDelete(ctx context.Context, transaction repository.Transaction, id uuid.UUID) (domain.Disbursement, bool, error) {
	tx, err := unwrapTransaction(transaction)
	if err != nil {
		return domain.Disbursement{}, false, err
	}

	now := store.now()
	var deleted dbDisbursement
	err = tx.GetContext(ctx, &deleted, softDeleteDisbursementQuery, now, id)
	if err == nil {
		return mapDBDisbursementToDomain(deleted), false, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Disbursement{}, false, repository.Classify(err)
	}

	// Check existing record
	var existing dbDisbursement
	checkErr := tx.GetContext(ctx, &existing, findDisbursementRawByIDQuery, id)
	if checkErr != nil {
		if errors.Is(checkErr, sql.ErrNoRows) {
			return domain.Disbursement{}, false, repository.NewError(repository.ErrorNotFound, fmt.Errorf("disbursement not found"))
		}
		return domain.Disbursement{}, false, repository.Classify(checkErr)
	}

	if existing.DeletedAt != nil {
		// Repeat delete is idempotent!
		return mapDBDisbursementToDomain(existing), true, nil
	}

	if existing.Status != string(domain.StatusPending) {
		return domain.Disbursement{}, false, repository.NewError(repository.ErrorConstraint, fmt.Errorf("cannot delete finalized disbursement"))
	}

	return domain.Disbursement{}, false, repository.NewError(repository.ErrorConflict, fmt.Errorf("concurrent delete conflict"))
}

func mapDBDisbursementToDomain(row dbDisbursement) domain.Disbursement {
	d := domain.Disbursement{
		ID:            row.ID,
		RecipientName: row.RecipientName,
		AccountNumber: row.AccountNumber,
		BankCode:      row.BankCode,
		Amount:        row.Amount,
		AdminFee:      row.AdminFee,
		Status:        domain.DisbursementStatus(row.Status),
		CreatedBy:     row.CreatedBy,
		DecidedAt:     row.DecidedAt,
		DeletedAt:     row.DeletedAt,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
	if row.Note != nil {
		d.Note = *row.Note
	}
	if row.DecidedBy != nil {
		d.DecidedBy = *row.DecidedBy
	}
	if row.DecisionNote != nil {
		d.DecisionNote = *row.DecisionNote
	}
	return d
}
