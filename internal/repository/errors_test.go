package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/lib/pq"
)

func TestClassify(t *testing.T) {
	alreadyClassified := NewError(ErrorConflict, errors.New("duplicate disbursement"))
	wrappedNoRows := fmt.Errorf("query disbursement: %w", sql.ErrNoRows)
	wrappedCanceled := fmt.Errorf("query canceled: %w", context.Canceled)
	wrappedDeadline := fmt.Errorf("query deadline: %w", context.DeadlineExceeded)
	duplicateKey := &pq.Error{Code: pq.ErrorCode("23505"), Message: "duplicate key"}
	foreignKey := &pq.Error{Code: pq.ErrorCode("23503"), Message: "foreign key violation"}
	checkViolation := &pq.Error{Code: pq.ErrorCode("23514"), Message: "check violation"}
	stringTooLong := &pq.Error{Code: pq.ErrorCode("22001"), Message: "value too long"}
	numericOutOfRange := &pq.Error{Code: pq.ErrorCode("22003"), Message: "numeric value out of range"}
	unknownPostgresError := &pq.Error{Code: pq.ErrorCode("99999"), Message: "unknown database error"}
	ordinaryError := errors.New("database unavailable")

	tests := []struct {
		name     string
		input    error
		want     ErrorCategory
		identity bool
	}{
		{name: "already classified", input: alreadyClassified, want: ErrorConflict, identity: true},
		{name: "no rows", input: wrappedNoRows, want: ErrorNotFound},
		{name: "context canceled", input: wrappedCanceled, want: ErrorDependency},
		{name: "context deadline exceeded", input: wrappedDeadline, want: ErrorDependency},
		{name: "duplicate key", input: duplicateKey, want: ErrorConflict},
		{name: "foreign key violation", input: foreignKey, want: ErrorConstraint},
		{name: "check violation", input: checkViolation, want: ErrorConstraint},
		{name: "string too long", input: stringTooLong, want: ErrorConstraint},
		{name: "numeric out of range", input: numericOutOfRange, want: ErrorConstraint},
		{name: "unknown PostgreSQL code", input: unknownPostgresError, want: ErrorDependency},
		{name: "ordinary error", input: ordinaryError, want: ErrorDependency},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Classify(test.input)

			if test.identity {
				if got != test.input {
					t.Fatalf("Classify() returned %p, want original error %p", got, test.input)
				}
			} else if got == test.input {
				t.Fatalf("Classify() returned original error %p, want classified error", got)
			}
			if got.Category != test.want {
				t.Errorf("Classify().Category = %q, want %q", got.Category, test.want)
			}
			if !test.identity && got.Unwrap() != test.input {
				t.Errorf("Classify().Unwrap() = %v, want original error %v", got.Unwrap(), test.input)
			}
			if !errors.Is(got, test.input) {
				t.Errorf("errors.Is(Classify(input), input) = false, want true")
			}
		})
	}
}

func TestErrorFormatsCategoryAndUnwrapsCause(t *testing.T) {
	cause := errors.New("duplicate disbursement")
	repositoryError := NewError(ErrorConflict, cause)

	if got := repositoryError.Error(); got != "repository CONFLICT" {
		t.Errorf("Error() = %q, want %q", got, "repository CONFLICT")
	}
	if got := repositoryError.Unwrap(); got != cause {
		t.Errorf("Unwrap() = %v, want cause %v", got, cause)
	}
}
