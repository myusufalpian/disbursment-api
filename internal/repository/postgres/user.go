package postgres

import (
	"context"

	"disbursment-api/internal/repository"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type UserStore struct {
	db *sqlx.DB
}

func NewUserStore(db *sqlx.DB) *UserStore {
	return &UserStore{db: db}
}

func (store *UserStore) FindByID(ctx context.Context, id uuid.UUID) (repository.User, error) {
	const query = `
		SELECT id, username, password_hash, role
		  FROM users
		 WHERE id = $1
	`
	var user repository.User
	err := store.db.GetContext(ctx, &user, query, id)
	if err != nil {
		return repository.User{}, repository.Classify(err)
	}
	return user, nil
}

func (store *UserStore) FindByUsername(ctx context.Context, username string) (repository.User, error) {
	const query = `
		SELECT id, username, password_hash, role
		  FROM users
		 WHERE username = $1
	`
	var user repository.User
	err := store.db.GetContext(ctx, &user, query, username)
	if err != nil {
		return repository.User{}, repository.Classify(err)
	}
	return user, nil
}
