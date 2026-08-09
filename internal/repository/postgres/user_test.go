package postgres

import (
	"context"
	"database/sql"
	"testing"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/repository"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

func TestUserStore_FindByUsernameAndID(t *testing.T) {
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

	sqlxDB := sqlx.NewDb(db, "postgres")
	store := NewUserStore(sqlxDB)
	userID := uuid.New()

	t.Run("FindByUsername found", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"id", "username", "password_hash", "role"}).
			AddRow(userID, "admin_user", "$2a$10$xyz", "ADMIN")

		mock.ExpectQuery("^SELECT id, username, password_hash, role FROM users WHERE username = \\$1").
			WithArgs("admin_user").
			WillReturnRows(rows)

		user, err := store.FindByUsername(context.Background(), "admin_user")
		if err != nil {
			t.Fatalf("FindByUsername failed: %v", err)
		}
		if user.Username != "admin_user" || user.Role != string(domain.RoleAdmin) {
			t.Errorf("expected admin_user / ADMIN, got %s / %s", user.Username, user.Role)
		}
	})

	t.Run("FindByUsername not found", func(t *testing.T) {
		mock.ExpectQuery("^SELECT id, username, password_hash, role FROM users WHERE username = \\$1").
			WithArgs("nonexistent").
			WillReturnError(sql.ErrNoRows)

		_, err := store.FindByUsername(context.Background(), "nonexistent")
		if err == nil {
			t.Fatalf("expected error for nonexistent user")
		}
		if !repository.IsNotFound(err) {
			t.Errorf("expected IsNotFound error")
		}
	})

	t.Run("FindByID found", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"id", "username", "password_hash", "role"}).
			AddRow(userID, "admin_user", "$2a$10$xyz", "ADMIN")

		mock.ExpectQuery("^SELECT id, username, password_hash, role FROM users WHERE id = \\$1").
			WithArgs(userID).
			WillReturnRows(rows)

		user, err := store.FindByID(context.Background(), userID)
		if err != nil {
			t.Fatalf("FindByID failed: %v", err)
		}
		if user.ID != userID {
			t.Errorf("expected %s, got %s", userID, user.ID)
		}
	})
}
