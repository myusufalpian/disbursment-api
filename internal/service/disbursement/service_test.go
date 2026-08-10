package disbursement_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/observability/metrics"
	"disbursment-api/internal/repository"
	"disbursment-api/internal/service/disbursement"
	"disbursment-api/internal/service/idempotency"

	"github.com/google/uuid"
)

type mockDisbursementStore struct {
	items       map[uuid.UUID]domain.Disbursement
	injectError error
	trace       *[]string
}

func newMockDisbursementStore() *mockDisbursementStore {
	return &mockDisbursementStore{
		items: make(map[uuid.UUID]domain.Disbursement),
	}
}

func (m *mockDisbursementStore) Insert(ctx context.Context, tx repository.Transaction, d domain.Disbursement) error {
	if m.trace != nil {
		*m.trace = append(*m.trace, "insert")
	}
	if m.injectError != nil {
		return m.injectError
	}
	m.items[d.ID] = d
	return nil
}

func (m *mockDisbursementStore) FindByID(ctx context.Context, id uuid.UUID) (domain.Disbursement, error) {
	if m.injectError != nil {
		return domain.Disbursement{}, m.injectError
	}
	d, ok := m.items[id]
	if !ok || d.DeletedAt != nil {
		return domain.Disbursement{}, repository.NewError(repository.ErrorNotFound, nil)
	}
	return d, nil
}

func (m *mockDisbursementStore) List(ctx context.Context, filter repository.DisbursementFilter) ([]domain.Disbursement, int, error) {
	if m.injectError != nil {
		return nil, 0, m.injectError
	}
	var result []domain.Disbursement
	for _, d := range m.items {
		if d.DeletedAt != nil {
			continue
		}
		if filter.Status != "" && d.Status != filter.Status {
			continue
		}
		result = append(result, d)
	}
	return result, len(result), nil
}

func (m *mockDisbursementStore) UpdateStatus(ctx context.Context, tx repository.Transaction, id uuid.UUID, decision domain.Decision) (domain.Disbursement, error) {
	if m.injectError != nil {
		return domain.Disbursement{}, m.injectError
	}
	d, ok := m.items[id]
	if !ok || d.DeletedAt != nil {
		return domain.Disbursement{}, repository.NewError(repository.ErrorNotFound, nil)
	}
	if d.Status != domain.StatusPending {
		return domain.Disbursement{}, repository.NewError(repository.ErrorConflict, nil)
	}
	d.Status = decision.Status
	d.DecidedBy = decision.ActorID
	d.DecisionNote = decision.Note
	now := time.Now().UTC()
	d.DecidedAt = &now
	d.UpdatedAt = now
	m.items[id] = d
	return d, nil
}

func (m *mockDisbursementStore) SoftDelete(ctx context.Context, tx repository.Transaction, id uuid.UUID) (domain.Disbursement, bool, error) {
	if m.injectError != nil {
		return domain.Disbursement{}, false, m.injectError
	}
	d, ok := m.items[id]
	if !ok {
		return domain.Disbursement{}, false, repository.NewError(repository.ErrorNotFound, nil)
	}
	if d.DeletedAt != nil {
		return d, true, nil
	}
	if d.Status != domain.StatusPending {
		return domain.Disbursement{}, false, repository.NewError(repository.ErrorConstraint, nil)
	}
	now := d.CreatedAt
	d.DeletedAt = &now
	m.items[id] = d
	return d, false, nil
}

type mockAuditOutboxStore struct {
	events      []domain.AuditEvent
	injectError error
}

func (m *mockAuditOutboxStore) Insert(ctx context.Context, tx repository.Transaction, event domain.AuditEvent) error {
	if m.injectError != nil {
		return m.injectError
	}
	m.events = append(m.events, event)
	return nil
}

func (m *mockAuditOutboxStore) FetchPending(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
	return nil, nil
}
func (m *mockAuditOutboxStore) MarkDelivered(ctx context.Context, eventID uuid.UUID, deliveredAt time.Time) error {
	return nil
}
func (m *mockAuditOutboxStore) RecordFailure(ctx context.Context, eventID uuid.UUID, errMessage string, nextAvailableAt time.Time) error {
	return nil
}
func (m *mockAuditOutboxStore) ReconcilePending(ctx context.Context, minAge time.Duration, criticalAge time.Duration) (int, int, error) {
	return 0, 0, nil
}
func (m *mockAuditOutboxStore) CleanupDelivered(ctx context.Context, olderThan time.Duration) (int64, error) {
	return 0, nil
}

type idempotencyRelease struct {
	scope   domain.IdempotencyScope
	claimID uuid.UUID
}

type mockIdempotencyClaim struct {
	fingerprint [32]byte
	claimID     uuid.UUID
	state       domain.IdempotencyState
	response    *domain.ReplayResponse
}

type mockIdempotencyStore struct {
	claimResult             domain.IdempotencyClaimResult
	claimErr                error
	claimRequests           []domain.IdempotencyClaimRequest
	claimResults            []domain.IdempotencyClaimResult
	claims                  map[domain.IdempotencyScope]mockIdempotencyClaim
	completeCalls           []domain.IdempotencyCompletion
	releaseCalls            []idempotencyRelease
	loseOwnershipOnComplete bool
	verifyCalls             int
	trace                   *[]string
}

func (m *mockIdempotencyStore) recordClaimResult(result domain.IdempotencyClaimResult) domain.IdempotencyClaimResult {
	m.claimResults = append(m.claimResults, result)
	return result
}

func (m *mockIdempotencyStore) Acquire(ctx context.Context, req domain.IdempotencyClaimRequest) (domain.IdempotencyClaimResult, error) {
	m.claimRequests = append(m.claimRequests, req)
	if m.claimErr != nil {
		return domain.IdempotencyClaimResult{}, m.claimErr
	}
	if m.claimResult.Outcome != "" {
		return m.recordClaimResult(m.claimResult), nil
	}
	if m.claims == nil {
		m.claims = make(map[domain.IdempotencyScope]mockIdempotencyClaim)
	}
	claim, ok := m.claims[req.Scope]
	if !ok {
		m.claims[req.Scope] = mockIdempotencyClaim{
			fingerprint: req.Fingerprint,
			claimID:     req.ClaimID,
			state:       domain.IdempotencyInProgress,
		}
		return m.recordClaimResult(domain.IdempotencyClaimResult{Outcome: domain.ClaimAcquired, ClaimID: req.ClaimID}), nil
	}
	if claim.fingerprint != req.Fingerprint {
		return m.recordClaimResult(domain.IdempotencyClaimResult{Outcome: domain.ClaimReused}), nil
	}
	if claim.state == domain.IdempotencyCompleted && claim.response != nil {
		return m.recordClaimResult(domain.IdempotencyClaimResult{Outcome: domain.ClaimReplayed, Replay: claim.response}), nil
	}
	return m.recordClaimResult(domain.IdempotencyClaimResult{Outcome: domain.ClaimInProgress, ClaimID: claim.claimID}), nil
}
func (m *mockIdempotencyStore) VerifyOwnership(ctx context.Context, tx repository.Transaction, scope domain.IdempotencyScope, claimID uuid.UUID) error {
	m.verifyCalls++
	if m.trace != nil {
		*m.trace = append(*m.trace, "verify")
	}
	claim, ok := m.claims[scope]
	if !ok || claim.state != domain.IdempotencyInProgress || claim.claimID != claimID {
		return repository.NewError(repository.ErrorOwnershipLost, errors.New("idempotency ownership lost"))
	}
	return nil
}
func (m *mockIdempotencyStore) Complete(ctx context.Context, tx repository.Transaction, completion domain.IdempotencyCompletion) error {
	m.completeCalls = append(m.completeCalls, completion)
	claim, ok := m.claims[completion.Scope]
	if !ok || claim.state != domain.IdempotencyInProgress || claim.claimID != completion.ClaimID {
		return repository.NewError(repository.ErrorOwnershipLost, errors.New("idempotency completion ownership lost"))
	}
	if m.loseOwnershipOnComplete {
		claim.claimID = uuid.New()
		m.claims[completion.Scope] = claim
		return repository.NewError(repository.ErrorOwnershipLost, errors.New("idempotency completion ownership lost"))
	}
	response := completion.Response
	response.Body = append(json.RawMessage(nil), completion.Response.Body...)
	claim.state = domain.IdempotencyCompleted
	claim.response = &response
	m.claims[completion.Scope] = claim
	return nil
}
func (m *mockIdempotencyStore) Release(ctx context.Context, scope domain.IdempotencyScope, claimID uuid.UUID) error {
	m.releaseCalls = append(m.releaseCalls, idempotencyRelease{scope: scope, claimID: claimID})
	claim, ok := m.claims[scope]
	if ok && claim.state == domain.IdempotencyInProgress && claim.claimID == claimID {
		delete(m.claims, scope)
	}
	return nil
}

type mockTransactor struct{}
type mockTransaction struct{}

func (m mockTransaction) Context() context.Context { return context.Background() }
func (m mockTransactor) WithinTransaction(ctx context.Context, fn func(context.Context, repository.Transaction) error) error {
	return fn(ctx, mockTransaction{})
}

func newDisbursementTestCoordinator() *idempotency.Coordinator {
	coordinator, err := idempotency.NewDefaultCoordinator(&mockIdempotencyStore{}, 30*time.Second, 24*time.Hour, nil)
	if err != nil {
		panic(err)
	}
	return coordinator
}

func auditSnapshot(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var snapshot map[string]any
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatalf("decode audit snapshot: %v", err)
	}
	return snapshot
}

func assertAuditIdentity(t *testing.T, event domain.AuditEvent, entityID, actorID, requestID uuid.UUID, action string, occurredAt time.Time) {
	t.Helper()
	if event.EventID == uuid.Nil || event.EntityType != "disbursement" || event.EntityID != entityID || event.ActorID != actorID || event.RequestID != requestID || event.Action != action {
		t.Fatalf("unexpected audit identity: event_id=%s entity_type=%s entity_id=%s actor_id=%s request_id=%s action=%s", event.EventID, event.EntityType, event.EntityID, event.ActorID, event.RequestID, event.Action)
	}
	if event.OccurredAt.Location() != time.UTC || !event.OccurredAt.Equal(occurredAt.UTC()) {
		t.Fatalf("unexpected audit timestamp: got %s (%s), want %s UTC", event.OccurredAt, event.OccurredAt.Location(), occurredAt.UTC())
	}
}

func assertAuditSnapshotTime(t *testing.T, snapshot map[string]any, key string, want time.Time) {
	t.Helper()
	value, ok := snapshot[key].(string)
	if !ok {
		t.Fatalf("snapshot[%q] = %#v, want RFC3339 timestamp", key, snapshot[key])
	}
	got, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("snapshot[%q] = %q is not RFC3339: %v", key, value, err)
	}
	if !got.Equal(want.UTC()) {
		t.Fatalf("snapshot[%q] = %s, want %s", key, got, want.UTC())
	}
}

func TestDisbursementValidationAndFeeCalculation(t *testing.T) {
	t.Run("Fee calculation tier 1 (< 5,000,000 IDR)", func(t *testing.T) {
		fee, err := domain.CalculateAdminFee(100000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if fee != domain.LowerTierAdminFee {
			t.Errorf("expected %d, got %d", domain.LowerTierAdminFee, fee)
		}
	})

	t.Run("Fee calculation tier 2 (>= 5,000,000 IDR)", func(t *testing.T) {
		fee, err := domain.CalculateAdminFee(5000000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if fee != domain.UpperTierAdminFee {
			t.Errorf("expected %d, got %d", domain.UpperTierAdminFee, fee)
		}
	})

	t.Run("Validation invalid amount", func(t *testing.T) {
		input := domain.CreateDisbursementInput{
			RecipientName: "John Doe",
			AccountNumber: "1234567890",
			BankCode:      "BCA",
			Amount:        5000,
		}
		err := input.Validate()
		if err == nil {
			t.Fatal("expected validation error for small amount")
		}
	})
}

func TestDisbursementServiceMutationsAndOutbox(t *testing.T) {
	ctx := context.Background()
	store := newMockDisbursementStore()
	outboxStore := &mockAuditOutboxStore{}
	transactor := mockTransactor{}

	coordinator, _ := idempotency.NewDefaultCoordinator(&mockIdempotencyStore{}, 30*time.Second, 24*time.Hour, nil)
	svc, err := disbursement.NewService(store, outboxStore, transactor, coordinator, nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	actorID := uuid.New()
	requestID := uuid.New()

	var createdID uuid.UUID

	t.Run("Create disbursement with idempotency key emits outbox event", func(t *testing.T) {
		input := domain.CreateDisbursementInput{
			RecipientName: "Jane Doe",
			AccountNumber: "1234567890",
			BankCode:      "BCA",
			Amount:        100000,
			Note:          "Test",
		}
		res, err := svc.Create(ctx, actorID, requestID, uuid.New().String(), input)
		if err != nil {
			t.Fatalf("create failed: %v", err)
		}
		createdID = res.Disbursement.ID

		if len(outboxStore.events) != 1 {
			t.Fatalf("expected 1 outbox event, got %d", len(outboxStore.events))
		}
		if outboxStore.events[0].Action != "disbursement.created" {
			t.Errorf("expected action disbursement.created, got %s", outboxStore.events[0].Action)
		}
		event := outboxStore.events[0]
		assertAuditIdentity(t, event, createdID, actorID, requestID, "disbursement.created", res.Disbursement.CreatedAt)
		after := auditSnapshot(t, event.AfterData)
		if after["ID"] != createdID.String() || after["RecipientName"] != "Jane Doe" || after["BankCode"] != "BCA" || after["Amount"] != float64(100000) || after["AdminFee"] != float64(domain.LowerTierAdminFee) || after["Status"] != string(domain.StatusPending) || after["Note"] != "Test" || after["CreatedBy"] != actorID.String() {
			t.Fatalf("unexpected created audit data: %v", after)
		}
		assertAuditSnapshotTime(t, after, "CreatedAt", res.Disbursement.CreatedAt)
		assertAuditSnapshotTime(t, after, "UpdatedAt", res.Disbursement.UpdatedAt)
	})

	t.Run("Create disbursement with valid idempotency key", func(t *testing.T) {
		input := domain.CreateDisbursementInput{
			RecipientName: "Bob Marley",
			AccountNumber: "9876543210",
			BankCode:      "MANDIRI",
			Amount:        200000,
			Note:          "Idempotent create",
		}
		key := uuid.New().String()
		res, err := svc.Create(ctx, actorID, requestID, key, input)
		if err != nil {
			t.Fatalf("create with key failed: %v", err)
		}
		if res.IsReplay {
			t.Errorf("expected new creation, got replay")
		}
	})

	t.Run("GetByID found and not found", func(t *testing.T) {
		found, err := svc.GetByID(ctx, createdID)
		if err != nil {
			t.Fatalf("get by id failed: %v", err)
		}
		if found.ID != createdID {
			t.Errorf("expected %s, got %s", createdID, found.ID)
		}

		_, err = svc.GetByID(ctx, uuid.New())
		if err == nil {
			t.Fatalf("expected error for non-existent id")
		}
	})

	t.Run("List disbursements with status filter", func(t *testing.T) {
		items, page, err := svc.List(ctx, repository.DisbursementFilter{
			Page:   1,
			Limit:  10,
			Status: domain.StatusPending,
		})
		if err != nil {
			t.Fatalf("list failed: %v", err)
		}
		if len(items) == 0 || page.Total < 1 {
			t.Fatalf("expected at least 1 item in list")
		}
	})

	t.Run("Update status emits approved event", func(t *testing.T) {
		decision := domain.Decision{
			Status:  domain.StatusApproved,
			ActorID: actorID,
			Note:    "Looks good",
		}
		updated, err := svc.UpdateStatus(ctx, actorID, requestID, createdID, decision)
		if err != nil {
			t.Fatalf("update status failed: %v", err)
		}
		if updated.Status != domain.StatusApproved {
			t.Errorf("expected APPROVED, got %s", updated.Status)
		}
		event := outboxStore.events[len(outboxStore.events)-1]
		assertAuditIdentity(t, event, createdID, actorID, requestID, "disbursement.approved", *updated.DecidedAt)
		before := auditSnapshot(t, event.BeforeData)
		after := auditSnapshot(t, event.AfterData)
		if before["ID"] != createdID.String() || before["Status"] != string(domain.StatusPending) || after["ID"] != createdID.String() || after["Status"] != string(domain.StatusApproved) || after["DecisionNote"] != "Looks good" || after["DecidedBy"] != actorID.String() {
			t.Fatalf("unexpected status audit data: before=%v after=%v", before, after)
		}
		assertAuditSnapshotTime(t, after, "DecidedAt", *updated.DecidedAt)
		assertAuditSnapshotTime(t, after, "UpdatedAt", updated.UpdatedAt)
	})

	t.Run("Soft delete pending item and repeat delete idempotency", func(t *testing.T) {
		input := domain.CreateDisbursementInput{
			RecipientName: "Alice Smith",
			AccountNumber: "1122334455",
			BankCode:      "BNI",
			Amount:        300000,
		}
		res, _ := svc.Create(ctx, actorID, requestID, uuid.New().String(), input)
		toDeleteID := res.Disbursement.ID

		outboxCountBefore := len(outboxStore.events)

		deleted, wasDeleted, err := svc.SoftDelete(ctx, actorID, requestID, toDeleteID)
		if err != nil {
			t.Fatalf("soft delete failed: %v", err)
		}
		if wasDeleted {
			t.Errorf("expected first delete to return wasAlreadyDeleted=false")
		}
		event := outboxStore.events[len(outboxStore.events)-1]
		assertAuditIdentity(t, event, toDeleteID, actorID, requestID, "disbursement.deleted", *deleted.DeletedAt)
		before := auditSnapshot(t, event.BeforeData)
		after := auditSnapshot(t, event.AfterData)
		if before["ID"] != toDeleteID.String() || before["Status"] != string(domain.StatusPending) || after["ID"] != toDeleteID.String() || after["Status"] != string(domain.StatusPending) {
			t.Fatalf("unexpected delete audit data: before=%v after=%v", before, after)
		}
		assertAuditSnapshotTime(t, after, "DeletedAt", *deleted.DeletedAt)
		assertAuditSnapshotTime(t, after, "UpdatedAt", deleted.UpdatedAt)

		_, wasDeletedRepeat, err := svc.SoftDelete(ctx, actorID, requestID, toDeleteID)
		if err != nil {
			t.Fatalf("repeat soft delete failed: %v", err)
		}
		if !wasDeletedRepeat {
			t.Errorf("expected repeat delete to return wasAlreadyDeleted=true")
		}
		if len(outboxStore.events) != outboxCountBefore+1 {
			t.Errorf("expected outbox count to stay %d on repeat delete, got %d", outboxCountBefore+1, len(outboxStore.events))
		}
	})
}

func TestDisbursementServiceRepositoryErrorMappings(t *testing.T) {
	ctx := context.Background()
	transactor := mockTransactor{}

	repoErrors := []struct {
		repoErr  error
		wantCode domain.ErrorCode
	}{
		{repository.NewError(repository.ErrorNotFound, nil), domain.CodeDisbursementNotFound},
		{repository.NewError(repository.ErrorConflict, nil), domain.CodeDisbursementAlreadyFinalized},
		{repository.NewError(repository.ErrorConstraint, nil), domain.CodeDisbursementNotDeletable},
		{repository.NewError(repository.ErrorDependency, nil), domain.CodeInternalError},
		{errors.New("db crash"), domain.CodeInternalError},
	}

	for _, tc := range repoErrors {
		store := newMockDisbursementStore()
		store.injectError = tc.repoErr
		svc, _ := disbursement.NewService(store, &mockAuditOutboxStore{}, transactor, newDisbursementTestCoordinator(), nil)

		_, err := svc.GetByID(ctx, uuid.New())
		if err == nil {
			t.Fatalf("expected error for repoErr %v", tc.repoErr)
		}
		domErr := domain.AsError(err)
		if domErr.Code != tc.wantCode {
			t.Errorf("expected code %s, got %s for repoErr %v", tc.wantCode, domErr.Code, tc.repoErr)
		}
	}
}

func TestDisbursementServiceIdempotencyOutcomes(t *testing.T) {
	ctx := context.Background()
	store := newMockDisbursementStore()
	outboxStore := &mockAuditOutboxStore{}
	transactor := mockTransactor{}
	actorID := uuid.New()
	requestID := uuid.New()
	key := uuid.New().String()

	input := domain.CreateDisbursementInput{
		RecipientName: "Idempotent User",
		AccountNumber: "1234567890",
		BankCode:      "BCA",
		Amount:        100000,
	}

	t.Run("ClaimReplayed returns IsReplay=true", func(t *testing.T) {
		disbID := uuid.New()
		idempotencyStore := &mockIdempotencyStore{
			claimResult: domain.IdempotencyClaimResult{
				Outcome: domain.ClaimReplayed,
				Replay: &domain.ReplayResponse{
					StatusCode:     201,
					Body:           []byte(`{"id":"` + disbID.String() + `","status":"PENDING"}`),
					DisbursementID: disbID,
				},
			},
		}
		coordinator, _ := idempotency.NewDefaultCoordinator(idempotencyStore, 30*time.Second, 24*time.Hour, nil)
		svc, _ := disbursement.NewService(store, outboxStore, transactor, coordinator, nil)

		res, err := svc.Create(ctx, actorID, requestID, key, input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.IsReplay {
			t.Errorf("expected IsReplay = true")
		}
	})

	t.Run("Create with idempotency inProgress returns IdempotencyRequestInProgress error", func(t *testing.T) {
		idempotencyStore := &mockIdempotencyStore{
			claimResult: domain.IdempotencyClaimResult{Outcome: domain.ClaimInProgress},
		}
		coordinator, _ := idempotency.NewDefaultCoordinator(idempotencyStore, 30*time.Second, 24*time.Hour, nil)
		svc, _ := disbursement.NewService(store, outboxStore, transactor, coordinator, nil)
		_, err := svc.Create(context.Background(), actorID, requestID, uuid.New().String(), input)
		if err == nil {
			t.Fatalf("expected error on inProgress claim, got nil")
		}
	})

	t.Run("Create with idempotency reused returns IdempotencyKeyReused error", func(t *testing.T) {
		idempotencyStore := &mockIdempotencyStore{
			claimResult: domain.IdempotencyClaimResult{Outcome: domain.ClaimReused},
		}
		coordinator, _ := idempotency.NewDefaultCoordinator(idempotencyStore, 30*time.Second, 24*time.Hour, nil)
		svc, _ := disbursement.NewService(store, outboxStore, transactor, coordinator, nil)

		_, err := svc.Create(ctx, actorID, requestID, key, input)
		if got := domain.AsError(err).Code; got != domain.CodeIdempotencyKeyReused {
			t.Errorf("expected code %s, got %s", domain.CodeIdempotencyKeyReused, got)
		}
	})

	t.Run("Create with generic idempotency coordinator error maps to internal error", func(t *testing.T) {
		idempotencyStore := &mockIdempotencyStore{
			claimErr: errors.New("generic coordinator failure"),
		}
		coordinator, _ := idempotency.NewDefaultCoordinator(idempotencyStore, 30*time.Second, 24*time.Hour, nil)
		svc, _ := disbursement.NewService(store, outboxStore, transactor, coordinator, nil)

		_, err := svc.Create(ctx, actorID, requestID, key, input)
		if err == nil || domain.AsError(err).Code != domain.CodeInternalError {
			t.Fatalf("expected CodeInternalError, got %v", err)
		}
	})

	t.Run("UpdateStatus error handling", func(t *testing.T) {
		store := newMockDisbursementStore()
		outboxStore := &mockAuditOutboxStore{}
		transactor := mockTransactor{}
		svc, _ := disbursement.NewService(store, outboxStore, transactor, newDisbursementTestCoordinator(), nil)

		// 1. Update non-existent record -> 404 NOT_FOUND
		decision := domain.Decision{Status: domain.StatusApproved, ActorID: uuid.New()}
		_, err := svc.UpdateStatus(ctx, actorID, requestID, uuid.New(), decision)
		if err == nil || domain.AsError(err).Code != domain.CodeDisbursementNotFound {
			t.Fatalf("expected CodeDisbursementNotFound, got %v", err)
		}

		// 2. Update record with store failure -> DB error
		store.injectError = errors.New("db connection failure")
		_, err2 := svc.UpdateStatus(ctx, actorID, requestID, uuid.New(), decision)
		if err2 == nil {
			t.Fatalf("expected error on store failure, got nil")
		}
	})

	t.Run("UpdateStatus invalid decision validation error", func(t *testing.T) {
		svc, _ := disbursement.NewService(newMockDisbursementStore(), &mockAuditOutboxStore{}, mockTransactor{}, newDisbursementTestCoordinator(), nil)
		_, err := svc.UpdateStatus(ctx, uuid.Nil, requestID, uuid.New(), domain.Decision{Status: "INVALID_STATUS"})
		if err == nil {
			t.Fatal("expected validation error for invalid status, got nil")
		}
	})

	t.Run("UpdateStatus approved and rejected metrics recording", func(t *testing.T) {
		store := newMockDisbursementStore()
		outboxStore := &mockAuditOutboxStore{}
		transactor := mockTransactor{}
		metricsCollector := metrics.NewMetricsCollector()
		svc, _ := disbursement.NewService(store, outboxStore, transactor, newDisbursementTestCoordinator(), metricsCollector)

		item := domain.Disbursement{
			ID:            uuid.New(),
			RecipientName: "John Doe",
			AccountNumber: "1234567890",
			BankCode:      "BCA",
			Amount:        50000,
			Status:        domain.StatusPending,
			CreatedAt:     time.Now().UTC(),
		}
		store.items[item.ID] = item

		// Approve
		decApproved := domain.Decision{Status: domain.StatusApproved, ActorID: actorID}
		resApp, err := svc.UpdateStatus(ctx, actorID, requestID, item.ID, decApproved)
		if err != nil {
			t.Fatalf("expected no error approving disbursement, got %v", err)
		}
		if resApp.Status != domain.StatusApproved {
			t.Fatalf("expected status APPROVED, got %s", resApp.Status)
		}

		// Reset item to pending and reject
		item2 := domain.Disbursement{
			ID:            uuid.New(),
			RecipientName: "Jane Doe",
			AccountNumber: "0987654321",
			BankCode:      "BNI",
			Amount:        75000,
			Status:        domain.StatusPending,
			CreatedAt:     time.Now().UTC(),
		}
		store.items[item2.ID] = item2
		decRejected := domain.Decision{Status: domain.StatusRejected, ActorID: actorID}
		resRej, err := svc.UpdateStatus(ctx, actorID, requestID, item2.ID, decRejected)
		if err != nil {
			t.Fatalf("expected no error rejecting disbursement, got %v", err)
		}
		if resRej.Status != domain.StatusRejected {
			t.Fatalf("expected status REJECTED, got %s", resRej.Status)
		}
		snapshot := metricsCollector.Snapshot()
		if snapshot.FinalizationsTotal["approved"] != 1 || snapshot.FinalizationsTotal["rejected"] != 1 {
			t.Fatalf("expected finalization metrics (approved=1, rejected=1), got %+v", snapshot.FinalizationsTotal)
		}
	})

	t.Run("NewService nil parameter checks", func(t *testing.T) {
		store := newMockDisbursementStore()
		outboxStore := &mockAuditOutboxStore{}
		transactor := mockTransactor{}

		if _, err := disbursement.NewService(nil, outboxStore, transactor, newDisbursementTestCoordinator(), nil); err == nil {
			t.Errorf("expected error for nil disbursementStore")
		}
		if _, err := disbursement.NewService(store, nil, transactor, newDisbursementTestCoordinator(), nil); err == nil {
			t.Errorf("expected error for nil auditOutboxStore")
		}
		if _, err := disbursement.NewService(store, outboxStore, nil, newDisbursementTestCoordinator(), nil); err == nil {
			t.Errorf("expected error for nil transactor")
		}
		if _, err := disbursement.NewService(store, outboxStore, transactor, nil, nil); err == nil {
			t.Errorf("expected error for nil coordinator")
		}
	})

	t.Run("SoftDelete error mappings", func(t *testing.T) {
		store := newMockDisbursementStore()
		outboxStore := &mockAuditOutboxStore{}
		transactor := mockTransactor{}
		svc, _ := disbursement.NewService(store, outboxStore, transactor, newDisbursementTestCoordinator(), nil)

		// 1. SoftDelete non-existent record -> 404 NOT_FOUND
		_, _, err := svc.SoftDelete(ctx, actorID, requestID, uuid.New())
		if err == nil || domain.AsError(err).Code != domain.CodeDisbursementNotFound {
			t.Fatalf("expected CodeDisbursementNotFound, got %v", err)
		}

		// 2. SoftDelete store failure -> Internal error
		store.injectError = errors.New("db failure")
		_, _, err2 := svc.SoftDelete(ctx, actorID, requestID, uuid.New())
		if err2 == nil {
			t.Fatalf("expected error on store failure, got nil")
		}
	})
}

type rollbackAwareTransaction struct {
	ctx                 context.Context
	stagedDisbursements map[*rollbackAwareDisbursementStore]map[uuid.UUID]domain.Disbursement
	stagedOutboxEvents  map[*rollbackAwareAuditOutboxStore][]domain.AuditEvent
}

func (tx *rollbackAwareTransaction) Context() context.Context {
	return tx.ctx
}

func (tx *rollbackAwareTransaction) commit() {
	for store, staged := range tx.stagedDisbursements {
		for id, disbursement := range staged {
			store.items[id] = disbursement
		}
	}
	for store, staged := range tx.stagedOutboxEvents {
		store.events = append(store.events, staged...)
	}
}

type rollbackAwareTransactor struct{}

func (rollbackAwareTransactor) WithinTransaction(ctx context.Context, fn func(context.Context, repository.Transaction) error) error {
	tx := &rollbackAwareTransaction{
		ctx:                 ctx,
		stagedDisbursements: make(map[*rollbackAwareDisbursementStore]map[uuid.UUID]domain.Disbursement),
		stagedOutboxEvents:  make(map[*rollbackAwareAuditOutboxStore][]domain.AuditEvent),
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	tx.commit()
	return nil
}

type rollbackAwareDisbursementStore struct {
	items map[uuid.UUID]domain.Disbursement
}

func newRollbackAwareDisbursementStore() *rollbackAwareDisbursementStore {
	return &rollbackAwareDisbursementStore{items: make(map[uuid.UUID]domain.Disbursement)}
}

func (s *rollbackAwareDisbursementStore) Insert(ctx context.Context, transaction repository.Transaction, d domain.Disbursement) error {
	tx, err := rollbackTransaction(transaction)
	if err != nil {
		return err
	}
	s.stage(tx, d.ID, d)
	return nil
}

func (s *rollbackAwareDisbursementStore) FindByID(ctx context.Context, id uuid.UUID) (domain.Disbursement, error) {
	d, ok := s.items[id]
	if !ok || d.DeletedAt != nil {
		return domain.Disbursement{}, repository.NewError(repository.ErrorNotFound, nil)
	}
	return d, nil
}

func (s *rollbackAwareDisbursementStore) List(ctx context.Context, filter repository.DisbursementFilter) ([]domain.Disbursement, int, error) {
	return nil, 0, nil
}

func (s *rollbackAwareDisbursementStore) UpdateStatus(ctx context.Context, transaction repository.Transaction, id uuid.UUID, decision domain.Decision) (domain.Disbursement, error) {
	tx, err := rollbackTransaction(transaction)
	if err != nil {
		return domain.Disbursement{}, err
	}
	d, ok := s.state(tx, id)
	if !ok || d.DeletedAt != nil {
		return domain.Disbursement{}, repository.NewError(repository.ErrorNotFound, nil)
	}
	if d.Status != domain.StatusPending {
		return domain.Disbursement{}, repository.NewError(repository.ErrorConflict, nil)
	}
	now := d.CreatedAt.Add(time.Minute)
	d.Status = decision.Status
	d.DecidedBy = decision.ActorID
	d.DecisionNote = decision.Note
	d.DecidedAt = &now
	d.UpdatedAt = now
	s.stage(tx, id, d)
	return d, nil
}

func (s *rollbackAwareDisbursementStore) SoftDelete(ctx context.Context, transaction repository.Transaction, id uuid.UUID) (domain.Disbursement, bool, error) {
	tx, err := rollbackTransaction(transaction)
	if err != nil {
		return domain.Disbursement{}, false, err
	}
	d, ok := s.state(tx, id)
	if !ok {
		return domain.Disbursement{}, false, repository.NewError(repository.ErrorNotFound, nil)
	}
	if d.DeletedAt != nil {
		return d, true, nil
	}
	if d.Status != domain.StatusPending {
		return domain.Disbursement{}, false, repository.NewError(repository.ErrorConstraint, nil)
	}
	now := d.CreatedAt.Add(time.Minute)
	d.DeletedAt = &now
	d.UpdatedAt = now
	s.stage(tx, id, d)
	return d, false, nil
}

func (s *rollbackAwareDisbursementStore) state(tx *rollbackAwareTransaction, id uuid.UUID) (domain.Disbursement, bool) {
	if staged, ok := tx.stagedDisbursements[s]; ok {
		if d, ok := staged[id]; ok {
			return d, true
		}
	}
	d, ok := s.items[id]
	return d, ok
}

func (s *rollbackAwareDisbursementStore) stage(tx *rollbackAwareTransaction, id uuid.UUID, d domain.Disbursement) {
	staged, ok := tx.stagedDisbursements[s]
	if !ok {
		staged = make(map[uuid.UUID]domain.Disbursement)
		tx.stagedDisbursements[s] = staged
	}
	staged[id] = d
}

func rollbackTransaction(transaction repository.Transaction) (*rollbackAwareTransaction, error) {
	tx, ok := transaction.(*rollbackAwareTransaction)
	if !ok {
		return nil, errors.New("rollback-aware transaction required")
	}
	return tx, nil
}

type rollbackAwareAuditOutboxStore struct {
	events     []domain.AuditEvent
	failInsert bool
}

func (s *rollbackAwareAuditOutboxStore) Insert(ctx context.Context, transaction repository.Transaction, event domain.AuditEvent) error {
	if s.failInsert {
		return errors.New("outbox unavailable")
	}
	tx, err := rollbackTransaction(transaction)
	if err != nil {
		return err
	}
	tx.stagedOutboxEvents[s] = append(tx.stagedOutboxEvents[s], event)
	return nil
}

func (s *rollbackAwareAuditOutboxStore) FetchPending(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
	return nil, nil
}

func (s *rollbackAwareAuditOutboxStore) MarkDelivered(ctx context.Context, eventID uuid.UUID, deliveredAt time.Time) error {
	return nil
}

func (s *rollbackAwareAuditOutboxStore) RecordFailure(ctx context.Context, eventID uuid.UUID, errMessage string, nextAvailableAt time.Time) error {
	return nil
}

func (s *rollbackAwareAuditOutboxStore) ReconcilePending(ctx context.Context, minAge time.Duration, criticalAge time.Duration) (int, int, error) {
	return 0, 0, nil
}

func (s *rollbackAwareAuditOutboxStore) CleanupDelivered(ctx context.Context, olderThan time.Duration) (int64, error) {
	return 0, nil
}

func TestDisbursementServiceOutboxFailureRollsBackMutations(t *testing.T) {
	ctx := context.Background()
	actorID := uuid.New()
	requestID := uuid.New()

	t.Run("successful transaction commits staged state", func(t *testing.T) {
		store := newRollbackAwareDisbursementStore()
		outboxStore := &rollbackAwareAuditOutboxStore{}
		svc, err := disbursement.NewService(store, outboxStore, rollbackAwareTransactor{}, newDisbursementTestCoordinator(), nil)
		if err != nil {
			t.Fatalf("failed to create service: %v", err)
		}

		result, err := svc.Create(ctx, actorID, requestID, uuid.New().String(), domain.CreateDisbursementInput{
			RecipientName: "Committed Create",
			AccountNumber: "1234567890",
			BankCode:      "BCA",
			Amount:        100000,
		})
		if err != nil {
			t.Fatalf("create failed: %v", err)
		}
		if _, ok := store.items[result.Disbursement.ID]; !ok || len(outboxStore.events) != 1 {
			t.Fatalf("expected staged state to commit, got %d items and %d events", len(store.items), len(outboxStore.events))
		}
	})

	t.Run("create", func(t *testing.T) {
		store := newRollbackAwareDisbursementStore()
		outboxStore := &rollbackAwareAuditOutboxStore{failInsert: true}
		svc, err := disbursement.NewService(store, outboxStore, rollbackAwareTransactor{}, newDisbursementTestCoordinator(), nil)
		if err != nil {
			t.Fatalf("failed to create service: %v", err)
		}

		_, err = svc.Create(ctx, actorID, requestID, uuid.New().String(), domain.CreateDisbursementInput{
			RecipientName: "Rollback Create",
			AccountNumber: "1234567890",
			BankCode:      "BCA",
			Amount:        100000,
		})
		if err == nil {
			t.Fatal("expected create to fail when outbox insert fails")
		}
		if len(store.items) != 0 || len(outboxStore.events) != 0 {
			t.Fatalf("expected create and outbox state to roll back, got %d items and %d events", len(store.items), len(outboxStore.events))
		}
	})

	t.Run("update status", func(t *testing.T) {
		store := newRollbackAwareDisbursementStore()
		id := uuid.New()
		store.items[id] = rollbackTestDisbursement(id, actorID)
		outboxStore := &rollbackAwareAuditOutboxStore{failInsert: true}
		svc, err := disbursement.NewService(store, outboxStore, rollbackAwareTransactor{}, newDisbursementTestCoordinator(), nil)
		if err != nil {
			t.Fatalf("failed to create service: %v", err)
		}

		_, err = svc.UpdateStatus(ctx, actorID, requestID, id, domain.Decision{Status: domain.StatusApproved, Note: "approved"})
		if err == nil {
			t.Fatal("expected update to fail when outbox insert fails")
		}
		committed := store.items[id]
		if committed.Status != domain.StatusPending || committed.DecidedBy != uuid.Nil || committed.DecisionNote != "" {
			t.Fatalf("expected update state to roll back, got %+v", committed)
		}
		if len(outboxStore.events) != 0 {
			t.Fatalf("expected no committed outbox events, got %d", len(outboxStore.events))
		}
	})

	t.Run("soft delete", func(t *testing.T) {
		store := newRollbackAwareDisbursementStore()
		id := uuid.New()
		store.items[id] = rollbackTestDisbursement(id, actorID)
		outboxStore := &rollbackAwareAuditOutboxStore{failInsert: true}
		svc, err := disbursement.NewService(store, outboxStore, rollbackAwareTransactor{}, newDisbursementTestCoordinator(), nil)
		if err != nil {
			t.Fatalf("failed to create service: %v", err)
		}

		_, _, err = svc.SoftDelete(ctx, actorID, requestID, id)
		if err == nil {
			t.Fatal("expected delete to fail when outbox insert fails")
		}
		committed := store.items[id]
		if committed.DeletedAt != nil {
			t.Fatalf("expected delete state to roll back, got deleted_at %v", committed.DeletedAt)
		}
		if len(outboxStore.events) != 0 {
			t.Fatalf("expected no committed outbox events, got %d", len(outboxStore.events))
		}
	})
}

func rollbackTestDisbursement(id, actorID uuid.UUID) domain.Disbursement {
	createdAt := time.Date(2026, time.August, 9, 7, 0, 0, 0, time.UTC)
	return domain.Disbursement{
		ID:            id,
		RecipientName: "Rollback Target",
		AccountNumber: "1234567890",
		BankCode:      "BCA",
		Amount:        100000,
		AdminFee:      domain.LowerTierAdminFee,
		Status:        domain.StatusPending,
		CreatedBy:     actorID,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}
}

func TestDisbursementServiceIdempotentCreateOutboxFailureReleasesClaim(t *testing.T) {
	ctx := context.Background()
	actorID := uuid.New()
	requestID := uuid.New()
	key := uuid.New()
	store := newRollbackAwareDisbursementStore()
	outboxStore := &rollbackAwareAuditOutboxStore{failInsert: true}
	idempotencyStore := &mockIdempotencyStore{}
	coordinator, err := idempotency.NewDefaultCoordinator(idempotencyStore, 30*time.Second, 24*time.Hour, nil)
	if err != nil {
		t.Fatalf("failed to create coordinator: %v", err)
	}
	svc, err := disbursement.NewService(store, outboxStore, rollbackAwareTransactor{}, coordinator, nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	_, err = svc.Create(ctx, actorID, requestID, key.String(), domain.CreateDisbursementInput{
		RecipientName: "Idempotent Rollback",
		AccountNumber: "1234567890",
		BankCode:      "BCA",
		Amount:        100000,
	})
	if err == nil {
		t.Fatal("expected create to fail when outbox insert fails")
	}
	if len(store.items) != 0 || len(outboxStore.events) != 0 {
		t.Fatalf("expected failed idempotent create to leave no committed state, got %d items and %d events", len(store.items), len(outboxStore.events))
	}
	if len(idempotencyStore.claimRequests) != 1 {
		t.Fatalf("expected one idempotency claim, got %d", len(idempotencyStore.claimRequests))
	}
	if len(idempotencyStore.releaseCalls) != 1 {
		t.Fatalf("expected one idempotency release, got %d", len(idempotencyStore.releaseCalls))
	}
	release := idempotencyStore.releaseCalls[0]
	claim := idempotencyStore.claimRequests[0]
	if release.claimID != claim.ClaimID {
		t.Fatalf("released claim %s does not match acquired claim %s", release.claimID, claim.ClaimID)
	}
	if release.scope.UserID != actorID || release.scope.Endpoint != "/disbursements" || release.scope.Key != key {
		t.Fatalf("unexpected released scope: %+v", release.scope)
	}
	if len(idempotencyStore.completeCalls) != 0 {
		t.Fatalf("expected no completion after transaction failure, got %d", len(idempotencyStore.completeCalls))
	}
}

func TestDisbursementServiceIdempotentCreateCompletesAndReplays(t *testing.T) {
	ctx := context.Background()
	actorID := uuid.New()
	requestID := uuid.New()
	key := uuid.New().String()
	store := newRollbackAwareDisbursementStore()
	outboxStore := &rollbackAwareAuditOutboxStore{}
	idempotencyStore := &mockIdempotencyStore{}
	coordinator, err := idempotency.NewDefaultCoordinator(idempotencyStore, 30*time.Second, 24*time.Hour, nil)
	if err != nil {
		t.Fatalf("failed to create coordinator: %v", err)
	}
	svc, err := disbursement.NewService(store, outboxStore, rollbackAwareTransactor{}, coordinator, nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	input := domain.CreateDisbursementInput{
		RecipientName: "Stateful Idempotency",
		AccountNumber: "1234567890",
		BankCode:      "BCA",
		Amount:        100000,
	}

	first, err := svc.Create(ctx, actorID, requestID, key, input)
	if err != nil {
		t.Fatalf("first create failed: %v", err)
	}
	second, err := svc.Create(ctx, actorID, requestID, key, input)
	if err != nil {
		t.Fatalf("second create failed: %v", err)
	}

	if !second.IsReplay {
		t.Fatalf("expected second create to be a replay")
	}
	parsedKey, err := domain.ParseIdempotencyKey(key)
	if err != nil {
		t.Fatalf("parse test idempotency key: %v", err)
	}
	wantScope := domain.IdempotencyScope{UserID: actorID, Endpoint: "/disbursements", Key: parsedKey}
	if idempotencyStore.claimRequests[0].Scope != wantScope || idempotencyStore.claimRequests[1].Scope != wantScope {
		t.Fatalf("claim scopes = %v and %v, want %v", idempotencyStore.claimRequests[0].Scope, idempotencyStore.claimRequests[1].Scope, wantScope)
	}
	if idempotencyStore.claimRequests[0].Fingerprint != idempotencyStore.claimRequests[1].Fingerprint {
		t.Fatal("same payload produced different idempotency fingerprints")
	}
	if len(store.items) != 1 {
		t.Fatalf("expected one persisted disbursement, got %d", len(store.items))
	}
	if len(outboxStore.events) != 1 {
		t.Fatalf("expected one audit event, got %d", len(outboxStore.events))
	}
	if len(idempotencyStore.claimRequests) != 2 {
		t.Fatalf("expected two claim attempts, got %d", len(idempotencyStore.claimRequests))
	}
	if len(idempotencyStore.claimResults) != 2 || idempotencyStore.claimResults[0].Outcome != domain.ClaimAcquired || idempotencyStore.claimResults[1].Outcome != domain.ClaimReplayed {
		t.Fatalf("claim outcomes = %v, want ACQUIRED then REPLAYED", idempotencyStore.claimResults)
	}
	if idempotencyStore.claimResults[0].ClaimID != idempotencyStore.claimRequests[0].ClaimID || idempotencyStore.claimResults[1].Replay == nil {
		t.Fatalf("claim results do not preserve ownership/replay details: %+v", idempotencyStore.claimResults)
	}
	if len(idempotencyStore.completeCalls) != 1 {
		t.Fatalf("expected one completion, got %d", len(idempotencyStore.completeCalls))
	}
	completion := idempotencyStore.completeCalls[0]
	if completion.Scope != wantScope {
		t.Fatalf("completion scope = %v, want %v", completion.Scope, wantScope)
	}
	if completion.ClaimID != idempotencyStore.claimRequests[0].ClaimID {
		t.Fatalf("completion claim ID %s does not match first claim %s", completion.ClaimID, idempotencyStore.claimRequests[0].ClaimID)
	}
	if len(idempotencyStore.releaseCalls) != 0 {
		t.Fatalf("expected no lease release after successful completion, got %d", len(idempotencyStore.releaseCalls))
	}
	storedClaim := idempotencyStore.claims[wantScope]
	if storedClaim.state != domain.IdempotencyCompleted || storedClaim.claimID != completion.ClaimID || storedClaim.response == nil {
		t.Fatalf("stored claim = %+v, want completed first claim with response", storedClaim)
	}
	if second.ReplayResponse == nil {
		t.Fatal("expected replay response")
	}
	if second.ReplayResponse.StatusCode != completion.Response.StatusCode || second.ReplayResponse.DisbursementID != first.Disbursement.ID || string(second.ReplayResponse.Body) != string(completion.Response.Body) {
		t.Fatalf("replay response = %+v, want completion response = %+v", second.ReplayResponse, completion.Response)
	}
}

func TestDisbursementServiceIdempotentCreateVerifiesOwnershipBeforeInsert(t *testing.T) {
	ctx := context.Background()
	actorID := uuid.MustParse("c10e8400-e29b-41d4-a716-446655440000")
	requestID := uuid.MustParse("c20e8400-e29b-41d4-a716-446655440000")
	key := uuid.MustParse("c30e8400-e29b-41d4-a716-446655440000")
	trace := make([]string, 0, 2)
	disbursementStore := newMockDisbursementStore()
	disbursementStore.trace = &trace
	outboxStore := &mockAuditOutboxStore{}
	idempotencyStore := &mockIdempotencyStore{trace: &trace}
	coordinator, err := idempotency.NewDefaultCoordinator(idempotencyStore, 30*time.Second, 24*time.Hour, nil)
	if err != nil {
		t.Fatalf("failed to create coordinator: %v", err)
	}
	svc, err := disbursement.NewService(disbursementStore, outboxStore, mockTransactor{}, coordinator, nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	_, err = svc.Create(ctx, actorID, requestID, key.String(), domain.CreateDisbursementInput{
		RecipientName: "Ownership Fence",
		AccountNumber: "1234567890",
		BankCode:      "BCA",
		Amount:        100000,
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if idempotencyStore.verifyCalls != 1 {
		t.Fatalf("ownership verification calls = %d, want 1", idempotencyStore.verifyCalls)
	}
	if len(trace) != 2 || trace[0] != "verify" || trace[1] != "insert" {
		t.Fatalf("collaborator order = %v, want [verify insert]", trace)
	}
}

func TestDisbursementServiceIdempotentCreateLostOwnershipDoesNotMutate(t *testing.T) {
	ctx := context.Background()
	actorID := uuid.New()
	requestID := uuid.New()
	key := uuid.New()
	store := newRollbackAwareDisbursementStore()
	outboxStore := &rollbackAwareAuditOutboxStore{}
	idempotencyStore := &mockIdempotencyStore{loseOwnershipOnComplete: true}
	coordinator, err := idempotency.NewDefaultCoordinator(idempotencyStore, 30*time.Second, 24*time.Hour, nil)
	if err != nil {
		t.Fatalf("failed to create coordinator: %v", err)
	}
	svc, err := disbursement.NewService(store, outboxStore, rollbackAwareTransactor{}, coordinator, nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	_, err = svc.Create(ctx, actorID, requestID, key.String(), domain.CreateDisbursementInput{
		RecipientName: "Lost Ownership",
		AccountNumber: "1234567890",
		BankCode:      "BCA",
		Amount:        100000,
	})
	if err == nil {
		t.Fatal("expected create to fail after ownership was lost")
	}
	if len(store.items) != 0 || len(outboxStore.events) != 0 {
		t.Fatalf("expected ownership loss to leave no mutation, got %d items and %d events", len(store.items), len(outboxStore.events))
	}
	if len(idempotencyStore.claimRequests) != 1 || len(idempotencyStore.completeCalls) != 1 || len(idempotencyStore.releaseCalls) != 1 {
		t.Fatalf("expected one claim, completion, and release; got claims=%d completions=%d releases=%d", len(idempotencyStore.claimRequests), len(idempotencyStore.completeCalls), len(idempotencyStore.releaseCalls))
	}
	claim := idempotencyStore.claimRequests[0]
	completion := idempotencyStore.completeCalls[0]
	if completion.Scope != claim.Scope || completion.ClaimID != claim.ClaimID {
		t.Fatalf("completion ownership = scope %v claim %s, want scope %v claim %s", completion.Scope, completion.ClaimID, claim.Scope, claim.ClaimID)
	}
	release := idempotencyStore.releaseCalls[0]
	if release.scope != claim.Scope || release.claimID != claim.ClaimID {
		t.Fatalf("release ownership = scope %v claim %s, want scope %v claim %s", release.scope, release.claimID, claim.Scope, claim.ClaimID)
	}
	storedClaim := idempotencyStore.claims[claim.Scope]
	if storedClaim.state == domain.IdempotencyCompleted || storedClaim.claimID == claim.ClaimID {
		t.Fatalf("expected fenced claim ownership to be lost without completion, got %+v", storedClaim)
	}
}

func TestDisbursementServiceListAndUpdateStatusErrors(t *testing.T) {
	ctx := context.Background()
	store := newMockDisbursementStore()
	outboxStore := &mockAuditOutboxStore{}
	transactor := mockTransactor{}
	svc, _ := disbursement.NewService(store, outboxStore, transactor, newDisbursementTestCoordinator(), nil)

	// List repository error
	store.injectError = errors.New("db error on list")
	_, _, err := svc.List(ctx, repository.DisbursementFilter{})
	if err == nil {
		t.Fatal("expected error on List repository error, got nil")
	}

	// UpdateStatus conflict error -> DisbursementAlreadyFinalized
	store.injectError = repository.NewError(repository.ErrorConflict, errors.New("conflict"))
	decision := domain.Decision{Status: domain.StatusApproved, ActorID: uuid.New()}
	_, err = svc.UpdateStatus(ctx, uuid.New(), uuid.New(), uuid.New(), decision)
	if err == nil || domain.AsError(err).Code != domain.CodeDisbursementAlreadyFinalized {
		t.Fatalf("expected CodeDisbursementAlreadyFinalized, got %v", err)
	}
}
