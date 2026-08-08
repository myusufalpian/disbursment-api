package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestParseIdempotencyKeyAcceptsUUIDv4AndRejectsOtherValues(t *testing.T) {
	validKey := "550e8400-e29b-41d4-a716-446655440030"
	tests := []struct {
		name      string
		value     string
		want      uuid.UUID
		wantError bool
	}{
		{name: "UUID v4", value: validKey, want: uuid.MustParse(validKey)},
		{name: "UUID v1", value: "6ba7b810-9dad-11d1-80b4-00c04fd430c8", wantError: true},
		{name: "malformed", value: "not-a-uuid", wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseIdempotencyKey(test.value)
			if test.wantError {
				if err == nil {
					t.Fatal("ParseIdempotencyKey() error = nil, want error")
				}
				domainErr := AsError(err)
				if domainErr.Code != CodeInvalidIdempotencyKey || domainErr.Status != 400 {
					t.Errorf("ParseIdempotencyKey() error = %#v, want invalid idempotency key status 400", domainErr)
				}
				if got != uuid.Nil {
					t.Errorf("ParseIdempotencyKey() UUID = %s, want nil UUID", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIdempotencyKey() error = %v", err)
			}
			if got != test.want {
				t.Errorf("ParseIdempotencyKey() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestIdempotencyScopeValidityBoundaries(t *testing.T) {
	validScope := IdempotencyScope{
		UserID:   uuid.MustParse("550e8400-e29b-41d4-a716-446655440031"),
		Endpoint: "/disbursements",
		Key:      uuid.MustParse("550e8400-e29b-41d4-a716-446655440032"),
	}
	tests := []struct {
		name  string
		scope IdempotencyScope
		want  bool
	}{
		{name: "valid", scope: validScope, want: true},
		{name: "nil user", scope: IdempotencyScope{Endpoint: validScope.Endpoint, Key: validScope.Key}},
		{name: "blank endpoint", scope: IdempotencyScope{UserID: validScope.UserID, Key: validScope.Key}},
		{name: "nil key", scope: IdempotencyScope{UserID: validScope.UserID, Endpoint: validScope.Endpoint}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.scope.Valid(); got != test.want {
				t.Errorf("IdempotencyScope.Valid() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestIdempotencyClaimRequestValidityBoundaries(t *testing.T) {
	now := time.Date(2026, time.August, 8, 15, 0, 0, 0, time.UTC)
	validRequest := IdempotencyClaimRequest{
		Scope:      validTestIdempotencyScope(),
		ClaimID:    uuid.MustParse("550e8400-e29b-41d4-a716-446655440033"),
		LeaseUntil: now.Add(30 * time.Second),
		ExpiresAt:  now.Add(24 * time.Hour),
		Now:        now,
	}
	tests := []struct {
		name    string
		request IdempotencyClaimRequest
		want    bool
	}{
		{name: "valid lease and expiry", request: validRequest, want: true},
		{name: "nil claim", request: func() IdempotencyClaimRequest { value := validRequest; value.ClaimID = uuid.Nil; return value }()},
		{name: "lease equals now", request: func() IdempotencyClaimRequest { value := validRequest; value.LeaseUntil = now; return value }()},
		{name: "expiry equals lease", request: func() IdempotencyClaimRequest {
			value := validRequest
			value.ExpiresAt = value.LeaseUntil
			return value
		}()},
		{name: "expiry before lease", request: func() IdempotencyClaimRequest {
			value := validRequest
			value.ExpiresAt = value.LeaseUntil.Add(-time.Nanosecond)
			return value
		}()},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.request.Valid(); got != test.want {
				t.Errorf("IdempotencyClaimRequest.Valid() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestReplayResponseValidityBoundaries(t *testing.T) {
	validResponse := ReplayResponse{
		StatusCode:     200,
		Body:           []byte(`{"success":true}`),
		DisbursementID: uuid.MustParse("550e8400-e29b-41d4-a716-446655440034"),
	}
	tests := []struct {
		name     string
		response ReplayResponse
		want     bool
	}{
		{name: "status 200", response: validResponse, want: true},
		{name: "status 299", response: func() ReplayResponse { value := validResponse; value.StatusCode = 299; return value }(), want: true},
		{name: "status 199", response: func() ReplayResponse { value := validResponse; value.StatusCode = 199; return value }()},
		{name: "status 300", response: func() ReplayResponse { value := validResponse; value.StatusCode = 300; return value }()},
		{name: "nil disbursement ID", response: func() ReplayResponse { value := validResponse; value.DisbursementID = uuid.Nil; return value }()},
		{name: "invalid JSON body", response: func() ReplayResponse { value := validResponse; value.Body = []byte(`{"success":`); return value }()},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.response.Valid(); got != test.want {
				t.Errorf("ReplayResponse.Valid() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestIdempotencyCompletionValidityBoundaries(t *testing.T) {
	completedAt := time.Date(2026, time.August, 8, 15, 1, 0, 0, time.UTC)
	validCompletion := IdempotencyCompletion{
		Scope:       validTestIdempotencyScope(),
		ClaimID:     uuid.MustParse("550e8400-e29b-41d4-a716-446655440035"),
		Response:    ReplayResponse{StatusCode: 201, Body: []byte(`{"id":"550e8400-e29b-41d4-a716-446655440034"}`), DisbursementID: uuid.MustParse("550e8400-e29b-41d4-a716-446655440034")},
		CompletedAt: completedAt,
	}
	tests := []struct {
		name       string
		completion IdempotencyCompletion
		want       bool
	}{
		{name: "valid", completion: validCompletion, want: true},
		{name: "invalid scope", completion: func() IdempotencyCompletion { value := validCompletion; value.Scope = IdempotencyScope{}; return value }()},
		{name: "nil claim", completion: func() IdempotencyCompletion { value := validCompletion; value.ClaimID = uuid.Nil; return value }()},
		{name: "invalid response", completion: func() IdempotencyCompletion { value := validCompletion; value.Response.StatusCode = 500; return value }()},
		{name: "zero completed time", completion: func() IdempotencyCompletion { value := validCompletion; value.CompletedAt = time.Time{}; return value }()},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.completion.Valid(); got != test.want {
				t.Errorf("IdempotencyCompletion.Valid() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestAsErrorPreservesDomainErrorsAndMapsUnknownErrors(t *testing.T) {
	original := Validation([]FieldError{{Field: "amount", Message: "invalid"}})
	if got := AsError(fmt.Errorf("wrapped: %w", original)); got != original {
		t.Errorf("AsError() domain error pointer = %p, want %p", got, original)
	}

	mapped := AsError(errors.New("database unavailable"))
	if mapped.Code != CodeInternalError || mapped.Status != 500 {
		t.Errorf("AsError() unknown error = %#v, want internal error status 500", mapped)
	}
}

func validTestIdempotencyScope() IdempotencyScope {
	return IdempotencyScope{
		UserID:   uuid.MustParse("550e8400-e29b-41d4-a716-446655440031"),
		Endpoint: "/disbursements",
		Key:      uuid.MustParse("550e8400-e29b-41d4-a716-446655440032"),
	}
}
