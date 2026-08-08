package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lib/pq"
)

type ErrorCategory string

const (
	ErrorNotFound      ErrorCategory = "NOT_FOUND"
	ErrorConflict      ErrorCategory = "CONFLICT"
	ErrorOwnershipLost ErrorCategory = "OWNERSHIP_LOST"
	ErrorConstraint    ErrorCategory = "CONSTRAINT"
	ErrorDependency    ErrorCategory = "DEPENDENCY"
)

type Error struct {
	Category ErrorCategory
	Cause    error
}

func (error *Error) Error() string {
	return fmt.Sprintf("repository %s", error.Category)
}

func (error *Error) Unwrap() error {
	return error.Cause
}

func NewError(category ErrorCategory, cause error) *Error {
	return &Error{Category: category, Cause: cause}
}

func Classify(err error) *Error {
	var repositoryError *Error
	if errors.As(err, &repositoryError) {
		return repositoryError
	}
	if errors.Is(err, sql.ErrNoRows) {
		return NewError(ErrorNotFound, err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return NewError(ErrorDependency, err)
	}
	var postgresError *pq.Error
	if errors.As(err, &postgresError) {
		switch string(postgresError.Code) {
		case "23505":
			return NewError(ErrorConflict, err)
		case "23503", "23514", "22001", "22003":
			return NewError(ErrorConstraint, err)
		}
	}
	return NewError(ErrorDependency, err)
}

func IsNotFound(err error) bool {
	var repoErr *Error
	if errors.As(err, &repoErr) {
		return repoErr.Category == ErrorNotFound
	}
	return errors.Is(err, sql.ErrNoRows)
}

func IsConflict(err error) bool {
	var repoErr *Error
	if errors.As(err, &repoErr) {
		return repoErr.Category == ErrorConflict
	}
	return false
}

func IsConstraint(err error) bool {
	var repoErr *Error
	if errors.As(err, &repoErr) {
		return repoErr.Category == ErrorConstraint
	}
	return false
}
