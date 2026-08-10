package disbursement

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi/dto"
	"disbursment-api/internal/observability/metrics"
	"disbursment-api/internal/repository"
	"disbursment-api/internal/service/idempotency"

	"github.com/google/uuid"
)

type CreateResult struct {
	Disbursement   domain.Disbursement
	IsReplay       bool
	ReplayResponse *domain.ReplayResponse
}

type Service struct {
	disbursementStore repository.DisbursementStore
	auditOutboxStore  repository.AuditOutboxStore
	transactor        repository.Transactor
	coordinator       *idempotency.Coordinator
	metrics           *metrics.MetricsCollector
}

func NewService(
	disbursementStore repository.DisbursementStore,
	auditOutboxStore repository.AuditOutboxStore,
	transactor repository.Transactor,
	coordinator *idempotency.Coordinator,
	metricsCollector *metrics.MetricsCollector,
) (*Service, error) {
	if disbursementStore == nil || auditOutboxStore == nil || transactor == nil || coordinator == nil {
		return nil, fmt.Errorf("invalid disbursement service dependencies")
	}
	return &Service{
		disbursementStore: disbursementStore,
		auditOutboxStore:  auditOutboxStore,
		transactor:        transactor,
		coordinator:       coordinator,
		metrics:           metricsCollector,
	}, nil
}

func (s *Service) Create(
	ctx context.Context,
	actorID uuid.UUID,
	requestID uuid.UUID,
	idempotencyKey string,
	input domain.CreateDisbursementInput,
) (CreateResult, error) {
	if err := input.Validate(); err != nil {
		return CreateResult{}, err
	}

	adminFee, err := domain.CalculateAdminFee(input.Amount)
	if err != nil {
		return CreateResult{}, err
	}

	bankCode, err := domain.CanonicalBankCode(input.BankCode)
	if err != nil {
		return CreateResult{}, err
	}

	now := time.Now().UTC()
	disbursementID := uuid.New()

	disbursement := domain.Disbursement{
		ID:            disbursementID,
		RecipientName: input.RecipientName,
		AccountNumber: input.AccountNumber,
		BankCode:      bankCode,
		Amount:        input.Amount,
		AdminFee:      adminFee,
		Status:        domain.StatusPending,
		Note:          input.Note,
		CreatedBy:     actorID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	parsedKey, err := domain.ParseIdempotencyKey(idempotencyKey)
	if err != nil {
		return CreateResult{}, err
	}
	scope := domain.IdempotencyScope{
		UserID:   actorID,
		Endpoint: "/disbursements",
		Key:      parsedKey,
	}

	claimResult, err := s.coordinator.Claim(ctx, actorID, "POST", "/disbursements", idempotencyKey, input)
	if err != nil {
		return CreateResult{}, s.mapIdempotencyError(err)
	}

	switch claimResult.Outcome {
	case domain.ClaimReplayed:
		if claimResult.Replay != nil {
			return CreateResult{
				IsReplay:       true,
				ReplayResponse: claimResult.Replay,
			}, nil
		}
		return CreateResult{}, domain.Internal()
	case domain.ClaimInProgress:
		return CreateResult{}, domain.IdempotencyRequestInProgressWithRetryAfter(claimResult.RetryAfter)
	case domain.ClaimReused:
		return CreateResult{}, domain.IdempotencyKeyReused()
	}

	// Perform creation within transaction and complete idempotency claim
	err = s.transactor.WithinTransaction(ctx, func(ctx context.Context, tx repository.Transaction) error {
		if err := s.coordinator.VerifyOwnership(ctx, tx, scope, claimResult.ClaimID); err != nil {
			return err
		}
		if err := s.disbursementStore.Insert(ctx, tx, disbursement); err != nil {
			return err
		}

		event, err := domain.NewAuditEvent(
			uuid.New(),
			"disbursement",
			disbursement.ID,
			"disbursement.created",
			actorID,
			requestID,
			nil,
			disbursement,
			now,
		)
		if err != nil {
			return err
		}

		if err := s.auditOutboxStore.Insert(ctx, tx, event); err != nil {
			return err
		}

		bodyBytes, err := json.Marshal(dto.NewDisbursementResponse(disbursement))
		if err != nil {
			return err
		}

		completion := domain.IdempotencyCompletion{
			Scope:   scope,
			ClaimID: claimResult.ClaimID,
			Response: domain.ReplayResponse{
				StatusCode:     201,
				Body:           bodyBytes,
				DisbursementID: disbursement.ID,
			},
			CompletedAt: now,
		}

		return s.coordinator.Complete(ctx, tx, completion)
	})

	if err != nil {
		// Release lease on transaction failure
		_ = s.coordinator.Release(ctx, scope, claimResult.ClaimID)
		return CreateResult{}, s.mapRepositoryError(err)
	}

	return CreateResult{Disbursement: disbursement, IsReplay: false}, nil
}

func (s *Service) GetByID(ctx context.Context, id uuid.UUID) (domain.Disbursement, error) {
	d, err := s.disbursementStore.FindByID(ctx, id)
	if err != nil {
		return domain.Disbursement{}, s.mapRepositoryError(err)
	}
	return d, nil
}

func (s *Service) List(ctx context.Context, filter repository.DisbursementFilter) ([]domain.Disbursement, domain.Pagination, error) {
	items, total, err := s.disbursementStore.List(ctx, filter)
	if err != nil {
		return nil, domain.Pagination{}, s.mapRepositoryError(err)
	}

	pagination, err := domain.NewPagination(filter.Page, filter.Limit, total)
	if err != nil {
		return nil, domain.Pagination{}, err
	}

	return items, pagination, nil
}

func (s *Service) UpdateStatus(
	ctx context.Context,
	actorID uuid.UUID,
	requestID uuid.UUID,
	id uuid.UUID,
	decision domain.Decision,
) (domain.Disbursement, error) {
	decision.ActorID = actorID
	if err := decision.Validate(); err != nil {
		return domain.Disbursement{}, err
	}

	var before domain.Disbursement
	var updated domain.Disbursement

	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, tx repository.Transaction) error {
		var err error
		before, err = s.disbursementStore.FindByID(ctx, id)
		if err != nil {
			return err
		}

		updated, err = s.disbursementStore.UpdateStatus(ctx, tx, id, decision)
		if err != nil {
			return err
		}

		action := "disbursement.approved"
		if decision.Status == domain.StatusRejected {
			action = "disbursement.rejected"
		}

		occurredAt := time.Now().UTC()
		if updated.DecidedAt != nil {
			occurredAt = *updated.DecidedAt
		}

		event, err := domain.NewAuditEvent(
			uuid.New(),
			"disbursement",
			updated.ID,
			action,
			actorID,
			requestID,
			before,
			updated,
			occurredAt,
		)
		if err != nil {
			return err
		}

		return s.auditOutboxStore.Insert(ctx, tx, event)
	})

	if err != nil {
		mappedErr := s.mapRepositoryError(err)
		if domainErr, ok := mappedErr.(*domain.Error); ok && domainErr.Code == domain.CodeConcurrentModification && s.metrics != nil {
			s.metrics.RecordFinalizationOutcome("conflict")
		}
		return domain.Disbursement{}, mappedErr
	}

	if s.metrics != nil {
		if decision.Status == domain.StatusApproved {
			s.metrics.RecordFinalizationOutcome("approved")
		} else if decision.Status == domain.StatusRejected {
			s.metrics.RecordFinalizationOutcome("rejected")
		}
	}

	return updated, nil
}

func (s *Service) SoftDelete(
	ctx context.Context,
	actorID uuid.UUID,
	requestID uuid.UUID,
	id uuid.UUID,
) (domain.Disbursement, bool, error) {
	var before domain.Disbursement
	var deleted domain.Disbursement
	var wasAlreadyDeleted bool

	err := s.transactor.WithinTransaction(ctx, func(ctx context.Context, tx repository.Transaction) error {
		var err error
		// Fetch current state
		before, err = s.disbursementStore.FindByID(ctx, id)
		if err != nil && !repository.IsNotFound(err) {
			return err
		}

		deleted, wasAlreadyDeleted, err = s.disbursementStore.SoftDelete(ctx, tx, id)
		if err != nil {
			return err
		}

		// If repeat delete, emit no outbox event
		if wasAlreadyDeleted {
			return nil
		}

		occurredAt := time.Now().UTC()
		if deleted.DeletedAt != nil {
			occurredAt = *deleted.DeletedAt
		}

		event, err := domain.NewAuditEvent(
			uuid.New(),
			"disbursement",
			deleted.ID,
			"disbursement.deleted",
			actorID,
			requestID,
			before,
			deleted,
			occurredAt,
		)
		if err != nil {
			return err
		}

		return s.auditOutboxStore.Insert(ctx, tx, event)
	})

	if err != nil {
		return domain.Disbursement{}, false, s.mapRepositoryError(err)
	}

	return deleted, wasAlreadyDeleted, nil
}

func (s *Service) mapRepositoryError(err error) error {
	if err == nil {
		return nil
	}
	if domainErr, ok := err.(*domain.Error); ok {
		return domainErr
	}
	if repository.IsNotFound(err) {
		return domain.DisbursementNotFound()
	}
	if repository.IsConflict(err) {
		return domain.DisbursementAlreadyFinalized()
	}
	if repository.IsConstraint(err) {
		return domain.DisbursementNotDeletable()
	}
	return domain.Internal()
}

func (s *Service) mapIdempotencyError(err error) error {
	if err == nil {
		return nil
	}
	if domainErr, ok := err.(*domain.Error); ok {
		return domainErr
	}
	return domain.Internal()
}
