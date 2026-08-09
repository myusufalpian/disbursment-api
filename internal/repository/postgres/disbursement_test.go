package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/repository"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

func newTestTx(tx *sqlx.Tx) repository.Transaction {
	return &transaction{context: context.Background(), tx: tx}
}

func TestDisbursementStore_Insert(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	sqlxDB := sqlx.NewDb(db, "postgres")
	store := NewDisbursementStore(sqlxDB)

	d := domain.Disbursement{
		ID:            uuid.New(),
		RecipientName: "John Doe",
		AccountNumber: "1234567890",
		BankCode:      "BCA",
		Amount:        100000,
		AdminFee:      2500,
		Status:        domain.StatusPending,
		Note:          "Test note",
		CreatedBy:     uuid.New(),
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}

	t.Run("Insert success", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectExec("^INSERT INTO disbursements").
			WithArgs(
				d.ID, d.RecipientName, d.AccountNumber, d.BankCode, d.Amount, d.AdminFee,
				string(d.Status), sqlmock.AnyArg(), d.CreatedBy, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
				sqlmock.AnyArg(), d.CreatedAt, d.UpdatedAt,
			).
			WillReturnResult(sqlmock.NewResult(1, 1))

		tx, err := sqlxDB.BeginTxx(context.Background(), nil)
		if err != nil {
			t.Fatalf("BeginTxx failed: %v", err)
		}

		err = store.Insert(context.Background(), newTestTx(tx), d)
		if err != nil {
			t.Fatalf("Insert failed: %v", err)
		}
	})

	t.Run("Insert failure", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectExec("^INSERT INTO disbursements").
			WillReturnError(errors.New("db write failure"))

		tx, _ := sqlxDB.BeginTxx(context.Background(), nil)
		err := store.Insert(context.Background(), newTestTx(tx), d)
		if err == nil {
			t.Fatalf("expected error for db failure")
		}
	})
}

func TestDisbursementStore_FindByID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	sqlxDB := sqlx.NewDb(db, "postgres")
	store := NewDisbursementStore(sqlxDB)
	targetID := uuid.New()
	creatorID := uuid.New()
	now := time.Now().UTC()

	t.Run("FindByID found", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{
			"id", "recipient_name", "account_number", "bank_code", "amount", "admin_fee",
			"status", "note", "created_by", "decided_by", "decision_note", "decided_at",
			"deleted_at", "created_at", "updated_at",
		}).AddRow(
			targetID, "Jane Doe", "9876543210", "MANDIRI", 500000, 2500,
			"PENDING", "Payment", creatorID, nil, nil, nil,
			nil, now, now,
		)

		mock.ExpectQuery("^SELECT (.+) FROM disbursements WHERE id = \\$1 AND deleted_at IS NULL").
			WithArgs(targetID).
			WillReturnRows(rows)

		found, err := store.FindByID(context.Background(), targetID)
		if err != nil {
			t.Fatalf("FindByID failed: %v", err)
		}
		if found.ID != targetID {
			t.Errorf("expected %s, got %s", targetID, found.ID)
		}
	})

	t.Run("FindByID not found", func(t *testing.T) {
		mock.ExpectQuery("^SELECT (.+) FROM disbursements WHERE id = \\$1 AND deleted_at IS NULL").
			WithArgs(targetID).
			WillReturnError(sql.ErrNoRows)

		_, err := store.FindByID(context.Background(), targetID)
		if err == nil {
			t.Fatalf("expected error for non-existent ID")
		}
		if !repository.IsNotFound(err) {
			t.Errorf("expected IsNotFound error")
		}
	})
}

func TestDisbursementStore_List(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	sqlxDB := sqlx.NewDb(db, "postgres")
	store := NewDisbursementStore(sqlxDB)
	targetID := uuid.New()
	creatorID := uuid.New()
	now := time.Now().UTC()

	t.Run("List with search and date range", func(t *testing.T) {
		countRows := sqlmock.NewRows([]string{"count"}).AddRow(1)
		mock.ExpectQuery("^SELECT COUNT\\(\\*\\) FROM disbursements WHERE").
			WillReturnRows(countRows)

		rows := sqlmock.NewRows([]string{
			"id", "recipient_name", "account_number", "bank_code", "amount", "admin_fee",
			"status", "note", "created_by", "decided_by", "decision_note", "decided_at",
			"deleted_at", "created_at", "updated_at",
		}).AddRow(
			targetID, "John Doe", "1234567890", "BCA", 100000, 2500,
			"PENDING", "Note", creatorID, nil, nil, nil,
			nil, now, now,
		)

		mock.ExpectQuery("^SELECT (.+) FROM disbursements WHERE").
			WillReturnRows(rows)

		dr, _ := domain.NewUTCDateRange(now.Add(-24*time.Hour), now.Add(24*time.Hour))
		filter := repository.DisbursementFilter{
			Page:      1,
			Limit:     10,
			Status:    domain.StatusPending,
			Search:    "John",
			SortBy:    "amount",
			SortOrder: "asc",
			DateRange: &dr,
		}

		items, total, err := store.List(context.Background(), filter)
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}
		if total != 1 || len(items) != 1 {
			t.Fatalf("expected 1 item, total 1, got total %d, items %d", total, len(items))
		}
	})
}

func TestDisbursementStore_UpdateStatusAndSoftDelete(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	sqlxDB := sqlx.NewDb(db, "postgres")
	store := NewDisbursementStore(sqlxDB)
	targetID := uuid.New()
	actorID := uuid.New()
	now := time.Now().UTC()

	t.Run("UpdateStatus success", func(t *testing.T) {
		mock.ExpectBegin()
		rows := sqlmock.NewRows([]string{
			"id", "recipient_name", "account_number", "bank_code", "amount", "admin_fee",
			"status", "note", "created_by", "decided_by", "decision_note", "decided_at",
			"deleted_at", "created_at", "updated_at",
		}).AddRow(
			targetID, "Jane Doe", "1234567890", "BCA", 100000, 2500,
			"APPROVED", "Note", actorID, actorID, "Looks good", &now,
			nil, now, now,
		)

		mock.ExpectQuery("^UPDATE disbursements SET status = \\$1").
			WithArgs(string(domain.StatusApproved), actorID, "Looks good", sqlmock.AnyArg(), targetID).
			WillReturnRows(rows)

		tx, _ := sqlxDB.BeginTxx(context.Background(), nil)
		decision := domain.Decision{
			Status:  domain.StatusApproved,
			ActorID: actorID,
			Note:    "Looks good",
		}

		res, err := store.UpdateStatus(context.Background(), newTestTx(tx), targetID, decision)
		if err != nil {
			t.Fatalf("UpdateStatus failed: %v", err)
		}
		if res.Status != domain.StatusApproved {
			t.Errorf("expected APPROVED, got %s", res.Status)
		}
	})

	t.Run("SoftDelete success", func(t *testing.T) {
		mock.ExpectBegin()
		rows := sqlmock.NewRows([]string{
			"id", "recipient_name", "account_number", "bank_code", "amount", "admin_fee",
			"status", "note", "created_by", "decided_by", "decision_note", "decided_at",
			"deleted_at", "created_at", "updated_at",
		}).AddRow(
			targetID, "Jane Doe", "1234567890", "BCA", 100000, 2500,
			"PENDING", "Note", actorID, nil, nil, nil,
			&now, now, now,
		)

		mock.ExpectQuery("^UPDATE disbursements SET deleted_at = \\$1").
			WithArgs(sqlmock.AnyArg(), targetID).
			WillReturnRows(rows)

		tx, _ := sqlxDB.BeginTxx(context.Background(), nil)
		_, wasDeleted, err := store.SoftDelete(context.Background(), newTestTx(tx), targetID)
		if err != nil {
			t.Fatalf("SoftDelete failed: %v", err)
		}
		if wasDeleted {
			t.Errorf("expected wasDeleted = false on first delete")
		}
	})

	t.Run("UpdateStatus error handling when no rows updated", func(t *testing.T) {
		// 1. ErrNoRows on UPDATE status -> Check query returns record that is already APPROVED
		mock.ExpectBegin()
		mock.ExpectQuery("^UPDATE disbursements SET status = \\$1").
			WillReturnError(sql.ErrNoRows)

		checkRows := sqlmock.NewRows([]string{
			"id", "recipient_name", "account_number", "bank_code", "amount", "admin_fee",
			"status", "note", "created_by", "decided_by", "decision_note", "decided_at",
			"deleted_at", "created_at", "updated_at",
		}).AddRow(
			targetID, "Jane Doe", "1234567890", "BCA", 100000, 2500,
			"APPROVED", "Note", actorID, actorID, "Already approved", &now,
			nil, now, now,
		)

		mock.ExpectQuery("^SELECT id, recipient_name").
			WithArgs(targetID).
			WillReturnRows(checkRows)

		tx, _ := sqlxDB.BeginTxx(context.Background(), nil)
		decision := domain.Decision{Status: domain.StatusApproved, ActorID: actorID}
		_, err := store.UpdateStatus(context.Background(), newTestTx(tx), targetID, decision)
		if err == nil {
			t.Fatalf("expected conflict error for already approved disbursement")
		}
	})

	t.Run("SoftDelete error handling when already deleted", func(t *testing.T) {
		// 1. ErrNoRows on DELETE -> Check query returns record with deleted_at set
		mock.ExpectBegin()
		mock.ExpectQuery("^UPDATE disbursements SET deleted_at = \\$1").
			WillReturnError(sql.ErrNoRows)

		checkRows := sqlmock.NewRows([]string{
			"id", "recipient_name", "account_number", "bank_code", "amount", "admin_fee",
			"status", "note", "created_by", "decided_by", "decision_note", "decided_at",
			"deleted_at", "created_at", "updated_at",
		}).AddRow(
			targetID, "Jane Doe", "1234567890", "BCA", 100000, 2500,
			"PENDING", "Note", actorID, nil, nil, nil,
			&now, now, now,
		)

		mock.ExpectQuery("^SELECT id, recipient_name").
			WithArgs(targetID).
			WillReturnRows(checkRows)

		tx, _ := sqlxDB.BeginTxx(context.Background(), nil)
		res, wasDeleted, err := store.SoftDelete(context.Background(), newTestTx(tx), targetID)
		if err != nil {
			t.Fatalf("expected nil error when already deleted, got %v", err)
		}
		if !wasDeleted {
			t.Fatalf("expected wasDeleted = true")
		}
		if res.ID != targetID {
			t.Fatalf("unexpected res ID: %v", res.ID)
		}
	})

	t.Run("SoftDelete error handling when not found or finalized", func(t *testing.T) {
		// 1. ErrNoRows on DELETE -> Check query returns ErrNoRows -> 404 NOT_FOUND
		mock.ExpectBegin()
		mock.ExpectQuery("^UPDATE disbursements SET deleted_at = \\$1").
			WillReturnError(sql.ErrNoRows)

		mock.ExpectQuery("^SELECT id, recipient_name").
			WithArgs(targetID).
			WillReturnError(sql.ErrNoRows)

		tx, _ := sqlxDB.BeginTxx(context.Background(), nil)
		_, _, err := store.SoftDelete(context.Background(), newTestTx(tx), targetID)
		if err == nil || !repository.IsNotFound(err) {
			t.Fatalf("expected IsNotFound error, got %v", err)
		}

		// 2. ErrNoRows on DELETE -> Check query returns APPROVED record -> 409 CONFLICT
		mock.ExpectBegin()
		mock.ExpectQuery("^UPDATE disbursements SET deleted_at = \\$1").
			WillReturnError(sql.ErrNoRows)

		checkRows := sqlmock.NewRows([]string{
			"id", "recipient_name", "account_number", "bank_code", "amount", "admin_fee",
			"status", "note", "created_by", "decided_by", "decision_note", "decided_at",
			"deleted_at", "created_at", "updated_at",
		}).AddRow(
			targetID, "Jane Doe", "1234567890", "BCA", 100000, 2500,
			"APPROVED", "Note", actorID, actorID, "Note", &now,
			nil, now, now,
		)

		mock.ExpectQuery("^SELECT id, recipient_name").
			WithArgs(targetID).
			WillReturnRows(checkRows)

		tx2, _ := sqlxDB.BeginTxx(context.Background(), nil)
		_, _, err2 := store.SoftDelete(context.Background(), newTestTx(tx2), targetID)
		if err2 == nil || !repository.IsConstraint(err2) {
			t.Fatalf("expected IsConstraint error for approved record, got %v", err2)
		}
	})
}
