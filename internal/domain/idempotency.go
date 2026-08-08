package domain

import (
	"crypto/sha256"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type IdempotencyState string

const (
	IdempotencyInProgress IdempotencyState = "IN_PROGRESS"
	IdempotencyCompleted  IdempotencyState = "COMPLETED"
)

type ClaimOutcome string

const (
	ClaimAcquired   ClaimOutcome = "ACQUIRED"
	ClaimReplayed   ClaimOutcome = "REPLAYED"
	ClaimInProgress ClaimOutcome = "IN_PROGRESS"
	ClaimReused     ClaimOutcome = "REUSED"
)

type IdempotencyScope struct {
	UserID   uuid.UUID
	Endpoint string
	Key      uuid.UUID
}

type IdempotencyClaimRequest struct {
	Scope       IdempotencyScope
	Fingerprint [sha256.Size]byte
	ClaimID     uuid.UUID
	LeaseUntil  time.Time
	ExpiresAt   time.Time
	Now         time.Time
}

type ReplayResponse struct {
	StatusCode     int
	Body           json.RawMessage
	DisbursementID uuid.UUID
}

type IdempotencyClaimResult struct {
	Outcome    ClaimOutcome
	ClaimID    uuid.UUID
	Replay     *ReplayResponse
	RetryAfter time.Duration
}

type IdempotencyCompletion struct {
	Scope       IdempotencyScope
	ClaimID     uuid.UUID
	Response    ReplayResponse
	CompletedAt time.Time
}

func ParseIdempotencyKey(value string) (uuid.UUID, error) {
	key, err := uuid.Parse(value)
	if err != nil || key.Version() != 4 {
		return uuid.Nil, &Error{Code: CodeInvalidIdempotencyKey, Message: "Idempotency-Key harus UUID v4", Status: 400}
	}
	return key, nil
}

func (scope IdempotencyScope) Valid() bool {
	return scope.UserID != uuid.Nil && scope.Endpoint != "" && scope.Key != uuid.Nil
}

func (request IdempotencyClaimRequest) Valid() bool {
	return request.Scope.Valid() && request.ClaimID != uuid.Nil && request.LeaseUntil.After(request.Now) && request.ExpiresAt.After(request.LeaseUntil)
}

func (response ReplayResponse) Valid() bool {
	return response.StatusCode >= 200 && response.StatusCode <= 299 && response.DisbursementID != uuid.Nil && json.Valid(response.Body)
}

func (completion IdempotencyCompletion) Valid() bool {
	return completion.Scope.Valid() && completion.ClaimID != uuid.Nil && completion.Response.Valid() && !completion.CompletedAt.IsZero()
}
