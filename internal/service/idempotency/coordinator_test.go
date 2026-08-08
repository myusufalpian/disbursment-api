package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/repository"

	"github.com/google/uuid"
)

type fixedClock struct {
	now time.Time
}

func (clock fixedClock) Now() time.Time {
	return clock.now
}

type fixedUUIDGenerator struct {
	id  uuid.UUID
	err error
}

func (generator fixedUUIDGenerator) New() (uuid.UUID, error) {
	return generator.id, generator.err
}

type transactionToken struct{}

func (transactionToken) Context() context.Context {
	return context.Background()
}

type fakeIdempotencyStore struct {
	acquireCalls   int
	acquireRequest domain.IdempotencyClaimRequest
	acquireResult  domain.IdempotencyClaimResult
	acquireErr     error

	verifyCalls       int
	verifyContext     context.Context
	verifyTransaction repository.Transaction
	verifyScope       domain.IdempotencyScope
	verifyClaimID     uuid.UUID
	verifyErr         error

	completeCalls       int
	completeContext     context.Context
	completeTransaction repository.Transaction
	completion          domain.IdempotencyCompletion
	completeErr         error

	releaseCalls   int
	releaseContext context.Context
	releaseScope   domain.IdempotencyScope
	releaseClaimID uuid.UUID
	releaseErr     error
}

func (store *fakeIdempotencyStore) Acquire(_ context.Context, request domain.IdempotencyClaimRequest) (domain.IdempotencyClaimResult, error) {
	store.acquireCalls++
	store.acquireRequest = request
	return store.acquireResult, store.acquireErr
}

func (store *fakeIdempotencyStore) VerifyOwnership(ctx context.Context, transaction repository.Transaction, scope domain.IdempotencyScope, claimID uuid.UUID) error {
	store.verifyCalls++
	store.verifyContext = ctx
	store.verifyTransaction = transaction
	store.verifyScope = scope
	store.verifyClaimID = claimID
	return store.verifyErr
}

func (store *fakeIdempotencyStore) Complete(ctx context.Context, transaction repository.Transaction, completion domain.IdempotencyCompletion) error {
	store.completeCalls++
	store.completeContext = ctx
	store.completeTransaction = transaction
	store.completion = completion
	return store.completeErr
}

func (store *fakeIdempotencyStore) Release(ctx context.Context, scope domain.IdempotencyScope, claimID uuid.UUID) error {
	store.releaseCalls++
	store.releaseContext = ctx
	store.releaseScope = scope
	store.releaseClaimID = claimID
	return store.releaseErr
}

func TestNewCoordinatorRejectsInvalidConfiguration(t *testing.T) {
	validStore := &fakeIdempotencyStore{}
	validClock := fixedClock{now: time.Unix(100, 0)}
	validGenerator := fixedUUIDGenerator{id: uuid.MustParse("bca91f16-dc07-4c8d-a16d-b8bc2103df98")}

	tests := []struct {
		name      string
		store     repository.IdempotencyStore
		clock     Clock
		generator UUIDGenerator
		leaseTTL  time.Duration
		replayTTL time.Duration
	}{
		{name: "nil store", store: nil, clock: validClock, generator: validGenerator, leaseTTL: time.Second, replayTTL: 2 * time.Second},
		{name: "nil clock", store: validStore, clock: nil, generator: validGenerator, leaseTTL: time.Second, replayTTL: 2 * time.Second},
		{name: "nil UUID generator", store: validStore, clock: validClock, generator: nil, leaseTTL: time.Second, replayTTL: 2 * time.Second},
		{name: "zero lease TTL", store: validStore, clock: validClock, generator: validGenerator, leaseTTL: 0, replayTTL: 2 * time.Second},
		{name: "negative lease TTL", store: validStore, clock: validClock, generator: validGenerator, leaseTTL: -time.Second, replayTTL: 2 * time.Second},
		{name: "zero replay TTL", store: validStore, clock: validClock, generator: validGenerator, leaseTTL: time.Second, replayTTL: 0},
		{name: "replay TTL equals lease TTL", store: validStore, clock: validClock, generator: validGenerator, leaseTTL: time.Second, replayTTL: time.Second},
		{name: "replay TTL shorter than lease TTL", store: validStore, clock: validClock, generator: validGenerator, leaseTTL: 2 * time.Second, replayTTL: time.Second},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			coordinator, err := NewCoordinator(test.store, test.clock, test.generator, test.leaseTTL, test.replayTTL)
			if err == nil {
				t.Fatal("NewCoordinator() error = nil, want an error")
			}
			if coordinator != nil {
				t.Fatalf("NewCoordinator() coordinator = %#v, want nil", coordinator)
			}
		})
	}
}

func TestNewCoordinatorAcceptsValidDependenciesAndTTLs(t *testing.T) {
	store := &fakeIdempotencyStore{}
	clock := fixedClock{now: time.Unix(100, 0)}
	generator := fixedUUIDGenerator{id: uuid.MustParse("bca91f16-dc07-4c8d-a16d-b8bc2103df98")}
	leaseTTL := 30 * time.Second
	replayTTL := 24 * time.Hour

	coordinator, err := NewCoordinator(store, clock, generator, leaseTTL, replayTTL)

	if err != nil {
		t.Fatalf("NewCoordinator() error = %v", err)
	}
	if coordinator == nil {
		t.Fatal("NewCoordinator() coordinator = nil, want a coordinator")
	}
	if coordinator.store != store || coordinator.clock != clock || coordinator.generator != generator {
		t.Fatal("NewCoordinator() did not retain the supplied dependencies")
	}
	if coordinator.leaseTTL != leaseTTL || coordinator.replayTTL != replayTTL {
		t.Fatalf("NewCoordinator() TTLs = %s/%s, want %s/%s", coordinator.leaseTTL, coordinator.replayTTL, leaseTTL, replayTTL)
	}
}

func TestClaimRejectsInvalidUUIDBeforeStoreAccess(t *testing.T) {
	store := &fakeIdempotencyStore{}
	coordinator := newTestCoordinator(store, fixedUUIDGenerator{id: uuid.MustParse("bca91f16-dc07-4c8d-a16d-b8bc2103df98")})

	_, err := coordinator.Claim(context.Background(), uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"), "POST", "/disbursements", "not-a-v4-key", map[string]any{"amount": 100})

	if err == nil {
		t.Fatal("Claim() error = nil, want an invalid idempotency key error")
	}
	if store.acquireCalls != 0 {
		t.Fatalf("Acquire() calls = %d, want 0", store.acquireCalls)
	}
}

func TestClaimBuildsExpectedRequestAndPropagatesAcquiredResult(t *testing.T) {
	store := &fakeIdempotencyStore{
		acquireResult: domain.IdempotencyClaimResult{
			Outcome: domain.ClaimAcquired,
			ClaimID: uuid.MustParse("f47ac10b-58cc-4372-a567-0e02b2c3d479"),
		},
	}
	clockNow := time.Date(2026, 8, 8, 15, 2, 3, 400, time.FixedZone("WIB", 7*60*60))
	coordinator := newTestCoordinatorWithClock(store, fixedClock{now: clockNow}, fixedUUIDGenerator{id: store.acquireResult.ClaimID})
	userID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	keyValue := "550e8400-e29b-41d4-a716-446655440001"
	payload := map[string]any{"recipient_name": "Budi", "amount": 1250000}
	ctx := context.WithValue(context.Background(), "claim-context", "value")

	got, err := coordinator.Claim(ctx, userID, " post ", " /disbursements ", keyValue, payload)

	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if got != store.acquireResult {
		t.Fatalf("Claim() result = %#v, want %#v", got, store.acquireResult)
	}
	if store.acquireCalls != 1 {
		t.Fatalf("Acquire() calls = %d, want 1", store.acquireCalls)
	}

	request := store.acquireRequest
	wantFingerprint, err := Fingerprint("POST", "/disbursements", payload)
	if err != nil {
		t.Fatalf("Fingerprint() expected value error = %v", err)
	}
	wantNow := clockNow.UTC()
	wantScope := domain.IdempotencyScope{
		UserID:   userID,
		Endpoint: "/disbursements",
		Key:      uuid.MustParse(keyValue),
	}
	if request.Scope != wantScope {
		t.Errorf("Acquire() scope = %#v, want %#v", request.Scope, wantScope)
	}
	if request.Fingerprint != wantFingerprint {
		t.Errorf("Acquire() fingerprint = %x, want %x", request.Fingerprint, wantFingerprint)
	}
	if request.ClaimID != store.acquireResult.ClaimID {
		t.Errorf("Acquire() claim ID = %s, want %s", request.ClaimID, store.acquireResult.ClaimID)
	}
	if !request.Now.Equal(wantNow) {
		t.Errorf("Acquire() now = %s, want %s", request.Now, wantNow)
	}
	if !request.LeaseUntil.Equal(wantNow.Add(30 * time.Second)) {
		t.Errorf("Acquire() lease until = %s, want %s", request.LeaseUntil, wantNow.Add(30*time.Second))
	}
	if !request.ExpiresAt.Equal(wantNow.Add(24 * time.Hour)) {
		t.Errorf("Acquire() expires at = %s, want %s", request.ExpiresAt, wantNow.Add(24*time.Hour))
	}
	if request.Now.Location() != time.UTC {
		t.Errorf("Acquire() now location = %s, want UTC", request.Now.Location())
	}
}

func TestClaimPropagatesAcquireOutcomesAndErrors(t *testing.T) {
	wantReplay := &domain.ReplayResponse{
		StatusCode:     201,
		Body:           json.RawMessage(`{"id":"550e8400-e29b-41d4-a716-446655440000"}`),
		DisbursementID: uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
	}
	wantError := errors.New("acquire failed")
	tests := []struct {
		name       string
		result     domain.IdempotencyClaimResult
		storeError error
	}{
		{name: "acquired", result: domain.IdempotencyClaimResult{Outcome: domain.ClaimAcquired, ClaimID: uuid.MustParse("f47ac10b-58cc-4372-a567-0e02b2c3d479")}},
		{name: "replayed", result: domain.IdempotencyClaimResult{Outcome: domain.ClaimReplayed, Replay: wantReplay}},
		{name: "in progress", result: domain.IdempotencyClaimResult{Outcome: domain.ClaimInProgress, RetryAfter: 5 * time.Second}},
		{name: "reused", result: domain.IdempotencyClaimResult{Outcome: domain.ClaimReused}},
		{name: "error", storeError: wantError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeIdempotencyStore{acquireResult: test.result, acquireErr: test.storeError}
			coordinator := newTestCoordinator(store, fixedUUIDGenerator{id: uuid.MustParse("f47ac10b-58cc-4372-a567-0e02b2c3d479")})

			got, err := coordinator.Claim(context.Background(), uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"), "POST", "/disbursements", "550e8400-e29b-41d4-a716-446655440001", map[string]any{"amount": 100})

			if got != test.result {
				t.Errorf("Claim() result = %#v, want %#v", got, test.result)
			}
			if err != test.storeError {
				t.Errorf("Claim() error = %v, want %v", err, test.storeError)
			}
			if store.acquireCalls != 1 {
				t.Errorf("Acquire() calls = %d, want 1", store.acquireCalls)
			}
		})
	}
}

func TestClaimReturnsUUIDGeneratorErrorWithoutStoreAccess(t *testing.T) {
	wantError := errors.New("UUID source unavailable")
	store := &fakeIdempotencyStore{}
	coordinator := newTestCoordinator(store, fixedUUIDGenerator{err: wantError})

	_, err := coordinator.Claim(context.Background(), uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"), "POST", "/disbursements", "550e8400-e29b-41d4-a716-446655440001", map[string]any{"amount": 100})

	if !errors.Is(err, wantError) {
		t.Fatalf("Claim() error = %v, want an error wrapping %v", err, wantError)
	}
	if store.acquireCalls != 0 {
		t.Fatalf("Acquire() calls = %d, want 0", store.acquireCalls)
	}
}

func TestVerifyOwnershipForwardsExactValuesAndErrors(t *testing.T) {
	wantError := errors.New("ownership failed")
	tests := []struct {
		name string
		err  error
	}{
		{name: "success"},
		{name: "error", err: wantError},
	}
	ctx := context.WithValue(context.Background(), "verify-context", "value")
	transaction := transactionToken{}
	scope := domain.IdempotencyScope{
		UserID:   uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
		Endpoint: "/disbursements",
		Key:      uuid.MustParse("550e8400-e29b-41d4-a716-446655440001"),
	}
	claimID := uuid.MustParse("f47ac10b-58cc-4372-a567-0e02b2c3d479")

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeIdempotencyStore{verifyErr: test.err}
			coordinator := newTestCoordinator(store, fixedUUIDGenerator{id: claimID})

			err := coordinator.VerifyOwnership(ctx, transaction, scope, claimID)

			if err != test.err {
				t.Errorf("VerifyOwnership() error = %v, want %v", err, test.err)
			}
			if store.verifyCalls != 1 {
				t.Fatalf("VerifyOwnership() calls = %d, want 1", store.verifyCalls)
			}
			if store.verifyContext != ctx {
				t.Error("VerifyOwnership() context was not forwarded exactly")
			}
			if store.verifyTransaction != transaction {
				t.Error("VerifyOwnership() transaction was not forwarded exactly")
			}
			if store.verifyScope != scope {
				t.Errorf("VerifyOwnership() scope = %#v, want %#v", store.verifyScope, scope)
			}
			if store.verifyClaimID != claimID {
				t.Errorf("VerifyOwnership() claim ID = %s, want %s", store.verifyClaimID, claimID)
			}
		})
	}
}

func TestReleaseForwardsExactValuesAndErrors(t *testing.T) {
	wantError := errors.New("release failed")
	tests := []struct {
		name string
		err  error
	}{
		{name: "success"},
		{name: "error", err: wantError},
	}
	ctx := context.WithValue(context.Background(), "release-context", "value")
	scope := domain.IdempotencyScope{
		UserID:   uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
		Endpoint: "/disbursements",
		Key:      uuid.MustParse("550e8400-e29b-41d4-a716-446655440001"),
	}
	claimID := uuid.MustParse("f47ac10b-58cc-4372-a567-0e02b2c3d479")

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeIdempotencyStore{releaseErr: test.err}
			coordinator := newTestCoordinator(store, fixedUUIDGenerator{id: claimID})

			err := coordinator.Release(ctx, scope, claimID)

			if err != test.err {
				t.Errorf("Release() error = %v, want %v", err, test.err)
			}
			if store.releaseCalls != 1 {
				t.Fatalf("Release() calls = %d, want 1", store.releaseCalls)
			}
			if store.releaseContext != ctx {
				t.Error("Release() context was not forwarded exactly")
			}
			if store.releaseScope != scope {
				t.Errorf("Release() scope = %#v, want %#v", store.releaseScope, scope)
			}
			if store.releaseClaimID != claimID {
				t.Errorf("Release() claim ID = %s, want %s", store.releaseClaimID, claimID)
			}
		})
	}
}

func TestCompleteCanonicalizesResponseBeforeForwarding(t *testing.T) {
	store := &fakeIdempotencyStore{}
	coordinator := newTestCoordinator(store, fixedUUIDGenerator{id: uuid.MustParse("f47ac10b-58cc-4372-a567-0e02b2c3d479")})
	ctx := context.WithValue(context.Background(), "complete-context", "value")
	transaction := transactionToken{}
	completion := domain.IdempotencyCompletion{
		Scope: domain.IdempotencyScope{
			UserID:   uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
			Endpoint: "/disbursements",
			Key:      uuid.MustParse("550e8400-e29b-41d4-a716-446655440001"),
		},
		ClaimID:     uuid.MustParse("f47ac10b-58cc-4372-a567-0e02b2c3d479"),
		Response:    domain.ReplayResponse{StatusCode: 201, Body: json.RawMessage(`{"b":2,"a":1}`), DisbursementID: uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")},
		CompletedAt: time.Date(2026, 8, 8, 8, 2, 3, 0, time.UTC),
	}

	err := coordinator.Complete(ctx, transaction, completion)

	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if store.completeCalls != 1 {
		t.Fatalf("Complete() calls = %d, want 1", store.completeCalls)
	}
	if store.completeContext != ctx {
		t.Error("Complete() context was not forwarded exactly")
	}
	if store.completeTransaction != transaction {
		t.Error("Complete() transaction was not forwarded exactly")
	}
	if string(store.completion.Response.Body) != `{"a":1,"b":2}` {
		t.Errorf("Complete() body = %s, want canonical JSON", store.completion.Response.Body)
	}
	if store.completion.Scope != completion.Scope || store.completion.ClaimID != completion.ClaimID || store.completion.Response.StatusCode != completion.Response.StatusCode || store.completion.Response.DisbursementID != completion.Response.DisbursementID || !store.completion.CompletedAt.Equal(completion.CompletedAt) {
		t.Error("Complete() changed completion fields other than response body")
	}
}

func TestCompleteRejectsMalformedResponseWithoutStoreAccess(t *testing.T) {
	store := &fakeIdempotencyStore{}
	coordinator := newTestCoordinator(store, fixedUUIDGenerator{id: uuid.MustParse("f47ac10b-58cc-4372-a567-0e02b2c3d479")})
	completion := domain.IdempotencyCompletion{
		Response: domain.ReplayResponse{Body: json.RawMessage(`{"a":`)},
	}

	err := coordinator.Complete(context.Background(), transactionToken{}, completion)

	if err == nil {
		t.Fatal("Complete() error = nil, want malformed JSON error")
	}
	if store.completeCalls != 0 {
		t.Fatalf("Complete() calls = %d, want 0", store.completeCalls)
	}
}

func newTestCoordinator(store *fakeIdempotencyStore, generator UUIDGenerator) *Coordinator {
	return newTestCoordinatorWithClock(store, fixedClock{now: time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)}, generator)
}

func newTestCoordinatorWithClock(store *fakeIdempotencyStore, clock Clock, generator UUIDGenerator) *Coordinator {
	coordinator, err := NewCoordinator(store, clock, generator, 30*time.Second, 24*time.Hour)
	if err != nil {
		panic(err)
	}
	return coordinator
}
