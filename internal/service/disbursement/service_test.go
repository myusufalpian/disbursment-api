package disbursement_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/repository"
	"disbursment-api/internal/service/disbursement"
	"disbursment-api/internal/service/idempotency"

	"github.com/google/uuid"
)

type mockDisbursementStore struct {
	items       map[uuid.UUID]domain.Disbursement
	injectError error
}

func newMockDisbursementStore() *mockDisbursementStore {
	return &mockDisbursementStore{
		items: make(map[uuid.UUID]domain.Disbursement),
	}
}

func (m *mockDisbursementStore) Insert(ctx context.Context, tx repository.Transaction, d domain.Disbursement) error {
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

type mockIdempotencyStore struct {
	claimResult domain.IdempotencyClaimResult
	claimErr    error
}

func (m *mockIdempotencyStore) Acquire(ctx context.Context, req domain.IdempotencyClaimRequest) (domain.IdempotencyClaimResult, error) {
	if m.claimErr != nil {
		return domain.IdempotencyClaimResult{}, m.claimErr
	}
	if m.claimResult.Outcome != "" {
		return m.claimResult, nil
	}
	return domain.IdempotencyClaimResult{Outcome: domain.ClaimAcquired, ClaimID: req.ClaimID}, nil
}
func (m *mockIdempotencyStore) VerifyOwnership(ctx context.Context, tx repository.Transaction, scope domain.IdempotencyScope, claimID uuid.UUID) error {
	return nil
}
func (m *mockIdempotencyStore) Complete(ctx context.Context, tx repository.Transaction, completion domain.IdempotencyCompletion) error {
	return nil
}
func (m *mockIdempotencyStore) Release(ctx context.Context, scope domain.IdempotencyScope, claimID uuid.UUID) error {
	return nil
}

type mockTransactor struct{}
type mockTransaction struct{}

func (m mockTransaction) Context() context.Context { return context.Background() }
func (m mockTransactor) WithinTransaction(ctx context.Context, fn func(context.Context, repository.Transaction) error) error {
	return fn(ctx, mockTransaction{})
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

	coordinator, _ := idempotency.NewDefaultCoordinator(&mockIdempotencyStore{}, 30*time.Second, 24*time.Hour)
	svc, err := disbursement.NewService(store, outboxStore, transactor, coordinator)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	actorID := uuid.New()
	requestID := uuid.New()

	var createdID uuid.UUID

	t.Run("Create disbursement without key emits outbox event", func(t *testing.T) {
		input := domain.CreateDisbursementInput{
			RecipientName: "Jane Doe",
			AccountNumber: "1234567890",
			BankCode:      "BCA",
			Amount:        100000,
			Note:          "Test",
		}
		res, err := svc.Create(ctx, actorID, requestID, "", input)
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
	})

	t.Run("Soft delete pending item and repeat delete idempotency", func(t *testing.T) {
		input := domain.CreateDisbursementInput{
			RecipientName: "Alice Smith",
			AccountNumber: "1122334455",
			BankCode:      "BNI",
			Amount:        300000,
		}
		res, _ := svc.Create(ctx, actorID, requestID, "", input)
		toDeleteID := res.Disbursement.ID

		outboxCountBefore := len(outboxStore.events)

		_, wasDeleted, err := svc.SoftDelete(ctx, actorID, requestID, toDeleteID)
		if err != nil {
			t.Fatalf("soft delete failed: %v", err)
		}
		if wasDeleted {
			t.Errorf("expected first delete to return wasAlreadyDeleted=false")
		}

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
		svc, _ := disbursement.NewService(store, &mockAuditOutboxStore{}, transactor, nil)

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
		coordinator, _ := idempotency.NewDefaultCoordinator(idempotencyStore, 30*time.Second, 24*time.Hour)
		svc, _ := disbursement.NewService(store, outboxStore, transactor, coordinator)

		res, err := svc.Create(ctx, actorID, requestID, key, input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.IsReplay {
			t.Errorf("expected IsReplay = true")
		}
	})

	t.Run("ClaimInProgress returns IdempotencyRequestInProgress error", func(t *testing.T) {
		idempotencyStore := &mockIdempotencyStore{
			claimResult: domain.IdempotencyClaimResult{Outcome: domain.ClaimInProgress},
		}
		coordinator, _ := idempotency.NewDefaultCoordinator(idempotencyStore, 30*time.Second, 24*time.Hour)
		svc, _ := disbursement.NewService(store, outboxStore, transactor, coordinator)

		_, err := svc.Create(ctx, actorID, requestID, key, input)
		if err == nil {
			t.Fatalf("expected error for ClaimInProgress")
		}
		if got := domain.AsError(err).Code; got != domain.CodeIdempotencyInProgress {
			t.Errorf("expected code %s, got %s", domain.CodeIdempotencyInProgress, got)
		}
	})

	t.Run("ClaimReused returns IdempotencyKeyReused error", func(t *testing.T) {
		idempotencyStore := &mockIdempotencyStore{
			claimResult: domain.IdempotencyClaimResult{Outcome: domain.ClaimReused},
		}
		coordinator, _ := idempotency.NewDefaultCoordinator(idempotencyStore, 30*time.Second, 24*time.Hour)
		svc, _ := disbursement.NewService(store, outboxStore, transactor, coordinator)

		_, err := svc.Create(ctx, actorID, requestID, key, input)
		if err == nil {
			t.Fatalf("expected error for ClaimReused")
		}
		if got := domain.AsError(err).Code; got != domain.CodeIdempotencyKeyReused {
			t.Errorf("expected code %s, got %s", domain.CodeIdempotencyKeyReused, got)
		}
	})
}
