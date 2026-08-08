package repository_test

import (
	"database/sql"
	"errors"
	"testing"

	"disbursment-api/internal/repository"
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
}
