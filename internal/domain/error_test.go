package domain_test

import (
	"errors"
	"testing"

	"disbursment-api/internal/domain"
)

func TestDomainErrorConstructors(t *testing.T) {
	tests := []struct {
		name       string
		err        *domain.Error
		wantCode   domain.ErrorCode
		wantStatus int
	}{
		{name: "InvalidCredentials", err: domain.InvalidCredentials(), wantCode: domain.CodeInvalidCredentials, wantStatus: 401},
		{name: "Unauthorized", err: domain.Unauthorized(), wantCode: domain.CodeUnauthorized, wantStatus: 401},
		{name: "InvalidRefreshToken", err: domain.InvalidRefreshToken(), wantCode: domain.CodeInvalidRefreshToken, wantStatus: 401},
		{name: "Forbidden", err: domain.Forbidden(), wantCode: domain.CodeForbidden, wantStatus: 403},
		{name: "DisbursementNotFound", err: domain.DisbursementNotFound(), wantCode: domain.CodeDisbursementNotFound, wantStatus: 404},
		{name: "DisbursementAlreadyFinalized", err: domain.DisbursementAlreadyFinalized(), wantCode: domain.CodeDisbursementAlreadyFinalized, wantStatus: 409},
		{name: "DisbursementNotDeletable", err: domain.DisbursementNotDeletable(), wantCode: domain.CodeDisbursementNotDeletable, wantStatus: 400},
		{name: "ConcurrentModification", err: domain.ConcurrentModification(), wantCode: domain.CodeConcurrentModification, wantStatus: 409},
		{name: "IdempotencyKeyReused", err: domain.IdempotencyKeyReused(), wantCode: domain.CodeIdempotencyKeyReused, wantStatus: 409},
		{name: "IdempotencyRequestInProgress", err: domain.IdempotencyRequestInProgress(), wantCode: domain.CodeIdempotencyInProgress, wantStatus: 409},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err.Code != tc.wantCode {
				t.Errorf("expected code %s, got %s", tc.wantCode, tc.err.Code)
			}
			if tc.err.Status != tc.wantStatus {
				t.Errorf("expected HTTP status %d, got %d", tc.wantStatus, tc.err.Status)
			}
			if tc.err.Error() == "" {
				t.Errorf("expected non-empty error string")
			}
		})
	}
}

func TestAsErrorMapsUnknownErrorToInternal(t *testing.T) {
	stdErr := errors.New("something went wrong")
	domErr := domain.AsError(stdErr)
	if domErr.Code != domain.CodeInternalError {
		t.Errorf("expected %s, got %s", domain.CodeInternalError, domErr.Code)
	}
	if domErr.Status != 500 {
		t.Errorf("expected 500, got %d", domErr.Status)
	}
}
