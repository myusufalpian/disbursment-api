package postgres

import (
	"context"
	"fmt"

	"disbursment-api/internal/repository"

	"github.com/jmoiron/sqlx"
)

type Transactor struct {
	database *sqlx.DB
}

type transaction struct {
	context context.Context
	tx      *sqlx.Tx
}

func NewTransactor(database *sqlx.DB) *Transactor {
	return &Transactor{database: database}
}

func (transactor *Transactor) WithinTransaction(ctx context.Context, operation func(context.Context, repository.Transaction) error) error {
	sqlTransaction, err := transactor.database.BeginTxx(ctx, nil)
	if err != nil {
		return repository.Classify(err)
	}
	wrapped := &transaction{context: ctx, tx: sqlTransaction}
	defer sqlTransaction.Rollback()

	if err := operation(ctx, wrapped); err != nil {
		return err
	}
	if err := sqlTransaction.Commit(); err != nil {
		return repository.Classify(err)
	}
	return nil
}

func (transaction *transaction) Context() context.Context {
	return transaction.context
}

func unwrapTransaction(value repository.Transaction) (*sqlx.Tx, error) {
	transaction, ok := value.(*transaction)
	if !ok || transaction.tx == nil {
		return nil, repository.NewError(repository.ErrorDependency, fmt.Errorf("unsupported transaction"))
	}
	return transaction.tx, nil
}
