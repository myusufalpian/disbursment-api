package repository_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"disbursment-api/internal/repository"

	"github.com/lib/pq"
)

func TestRepositoryErrorHelpers(t *testing.T) {
	notFoundErr := repository.NewError(repository.ErrorNotFound, sql.ErrNoRows)
	conflictErr := repository.NewError(repository.ErrorConflict, errors.New("conflict"))
	constraintErr := repository.NewError(repository.ErrorConstraint, errors.New("constraint"))
	dependencyErr := repository.NewError(repository.ErrorDependency, errors.New("dependency"))

	t.Run("IsNotFound", func(t *testing.T) {
		if !repository.IsNotFound(notFoundErr) {
			t.Errorf("expected IsNotFound(notFoundErr) = true")
		}
		if !repository.IsNotFound(sql.ErrNoRows) {
			t.Errorf("expected IsNotFound(sql.ErrNoRows) = true")
		}
		if repository.IsNotFound(conflictErr) {
			t.Errorf("expected IsNotFound(conflictErr) = false")
		}
	})

	t.Run("IsConflict", func(t *testing.T) {
		if !repository.IsConflict(conflictErr) {
			t.Errorf("expected IsConflict(conflictErr) = true")
		}
		if repository.IsConflict(notFoundErr) {
			t.Errorf("expected IsConflict(notFoundErr) = false")
		}
	})

	t.Run("IsConstraint", func(t *testing.T) {
		if !repository.IsConstraint(constraintErr) {
			t.Errorf("expected IsConstraint(constraintErr) = true")
		}
		if repository.IsConstraint(notFoundErr) {
			t.Errorf("expected IsConstraint(notFoundErr) = false")
		}
	})

	t.Run("Unwrap and Error", func(t *testing.T) {
		if notFoundErr.Unwrap() != sql.ErrNoRows {
			t.Errorf("expected unwrapped sql.ErrNoRows")
		}
		if notFoundErr.Error() == "" {
			t.Errorf("expected non-empty Error()")
		}
		if dependencyErr.Error() == "" {
			t.Errorf("expected non-empty Error()")
		}
	})

	t.Run("Classify Postgres and Context Errors", func(t *testing.T) {
		// 1. Existing *Error returns as-is
		if repository.Classify(notFoundErr) != notFoundErr {
			t.Errorf("expected original repository error returned")
		}

		// 2. Context errors
		if repository.Classify(context.Canceled).Category != repository.ErrorDependency {
			t.Errorf("expected ErrorDependency for context.Canceled")
		}
		if repository.Classify(context.DeadlineExceeded).Category != repository.ErrorDependency {
			t.Errorf("expected ErrorDependency for context.DeadlineExceeded")
		}

		// 3. Postgres error codes
		pqConflict := &pq.Error{Code: "23505"}
		if repository.Classify(pqConflict).Category != repository.ErrorConflict {
			t.Errorf("expected ErrorConflict for pq 23505")
		}

		pqConstraint := &pq.Error{Code: "23503"}
		if repository.Classify(pqConstraint).Category != repository.ErrorConstraint {
			t.Errorf("expected ErrorConstraint for pq 23503")
		}

		// 4. Fallback generic error
		genericErr := errors.New("unknown error")
		if repository.Classify(genericErr).Category != repository.ErrorDependency {
			t.Errorf("expected ErrorDependency for generic error")
		}
	})
}
