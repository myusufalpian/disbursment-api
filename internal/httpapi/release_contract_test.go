package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi"
	"disbursment-api/internal/httpapi/dto"
	"disbursment-api/internal/httpapi/validation"
	"disbursment-api/internal/observability/metrics"
	"disbursment-api/internal/repository"
	"disbursment-api/internal/service/auth"
	"disbursment-api/internal/service/disbursement"
	"disbursment-api/internal/service/idempotency"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const contractJWTSecret = "test-secret-key-12345"

func TestAuthContractPreservesErrorEnvelopeRequestIDAndNoContent(t *testing.T) {
	router, password := newAuthContractRouter(t)

	t.Run("malformed login returns the validation envelope with the supplied request ID", func(t *testing.T) {
		requestID := "2f5a0e23-1b3f-4a1b-89a3-9348ddc70001"
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Request-ID", requestID)
		response := httptest.NewRecorder()

		router.ServeHTTP(response, request)

		if response.Code != http.StatusBadRequest {
			t.Fatalf("expected malformed login to return 400, got %d: %s", response.Code, response.Body.String())
		}
		if response.Header().Get("X-Request-ID") != requestID {
			t.Errorf("expected request ID header %q, got %q", requestID, response.Header().Get("X-Request-ID"))
		}
		if !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
			t.Errorf("expected JSON content type, got %q", response.Header().Get("Content-Type"))
		}

		var envelope struct {
			Success bool `json:"success"`
			Error   struct {
				Code    domain.ErrorCode    `json:"code"`
				Message string              `json:"message"`
				Details []domain.FieldError `json:"details"`
			} `json:"error"`
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode error envelope: %v", err)
		}
		if envelope.Success {
			t.Error("expected success to be false")
		}
		if envelope.Error.Code != domain.CodeValidationError {
			t.Errorf("expected validation error code, got %q", envelope.Error.Code)
		}
		if envelope.Error.Message != "Input tidak valid" {
			t.Errorf("expected validation message, got %q", envelope.Error.Message)
		}
		if len(envelope.Error.Details) != 1 || envelope.Error.Details[0] != (domain.FieldError{Field: "body", Message: "format JSON tidak valid"}) {
			t.Errorf("expected malformed-body detail, got %#v", envelope.Error.Details)
		}
		if envelope.RequestID != requestID {
			t.Errorf("expected error request ID %q, got %q", requestID, envelope.RequestID)
		}
		var rawEnvelope map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &rawEnvelope); err != nil {
			t.Fatalf("decode raw error envelope: %v", err)
		}
		if len(rawEnvelope) != 3 || rawEnvelope["success"] == nil || rawEnvelope["error"] == nil || rawEnvelope["request_id"] == nil {
			t.Errorf("expected only success, error, and request_id fields, got %v", rawEnvelope)
		}
	})

	t.Run("logout returns a bodyless 204 and preserves the supplied request ID", func(t *testing.T) {
		loginBody, err := json.Marshal(dto.LoginRequest{
			Username: "contract_operator",
			Password: password,
		})
		if err != nil {
			t.Fatalf("marshal login request: %v", err)
		}
		loginRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginBody))
		loginRequest.Header.Set("Content-Type", "application/json")
		loginResponse := httptest.NewRecorder()
		router.ServeHTTP(loginResponse, loginRequest)
		if loginResponse.Code != http.StatusOK {
			t.Fatalf("expected login to return 200, got %d: %s", loginResponse.Code, loginResponse.Body.String())
		}

		var loginEnvelope struct {
			Success bool              `json:"success"`
			Data    dto.TokenResponse `json:"data"`
		}
		if err := json.Unmarshal(loginResponse.Body.Bytes(), &loginEnvelope); err != nil {
			t.Fatalf("decode login response: %v", err)
		}
		if !loginEnvelope.Success {
			t.Fatal("expected login success envelope")
		}
		if loginEnvelope.Data.RefreshToken == "" {
			t.Fatal("expected login to issue a refresh token before logout")
		}

		logoutBody, err := json.Marshal(dto.LogoutRequest{RefreshToken: loginEnvelope.Data.RefreshToken})
		if err != nil {
			t.Fatalf("marshal logout request: %v", err)
		}
		requestID := "2f5a0e23-1b3f-4a1b-89a3-9348ddc70002"
		logoutRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", bytes.NewReader(logoutBody))
		logoutRequest.Header.Set("Content-Type", "application/json")
		logoutRequest.Header.Set("X-Request-ID", requestID)
		logoutResponse := httptest.NewRecorder()

		router.ServeHTTP(logoutResponse, logoutRequest)

		if logoutResponse.Code != http.StatusNoContent {
			t.Fatalf("expected logout to return 204, got %d: %s", logoutResponse.Code, logoutResponse.Body.String())
		}
		if logoutResponse.Body.Len() != 0 {
			t.Errorf("expected bodyless logout response, got %q", logoutResponse.Body.String())
		}
		if logoutResponse.Header().Get("X-Request-ID") != requestID {
			t.Errorf("expected request ID header %q, got %q", requestID, logoutResponse.Header().Get("X-Request-ID"))
		}
	})
}

func TestDisbursementRepeatDeleteContractPreservesDeletionAndSingleAuditEvent(t *testing.T) {
	disbursementID := uuid.MustParse("2f5a0e23-1b3f-4a1b-89a3-9348ddc70010")
	superadminID := uuid.MustParse("2f5a0e23-1b3f-4a1b-89a3-9348ddc70011")
	createdAt := time.Date(2026, time.August, 9, 0, 0, 0, 0, time.UTC)
	store := newMockDisbursementStore()
	store.items[disbursementID] = domain.Disbursement{
		ID:            disbursementID,
		RecipientName: "Contract Recipient",
		AccountNumber: "1234567890",
		BankCode:      "BCA",
		Amount:        100000,
		AdminFee:      domain.LowerTierAdminFee,
		Status:        domain.StatusPending,
		CreatedBy:     superadminID,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}
	outboxStore := &mockAuditOutboxStore{}
	service, err := disbursement.NewService(store, outboxStore, mockTransactor{}, nil, nil)
	if err != nil {
		t.Fatalf("create disbursement service: %v", err)
	}
	validator, err := validation.New()
	if err != nil {
		t.Fatalf("create validator: %v", err)
	}
	handler := httpapi.NewDisbursementHandler(service, validator)
	router, err := httpapi.NewRouter(
		1<<20,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		contractJWTSecret,
		"disbursement-api",
		"disbursement-api-users",
		nil,
		handler,
		nil,
		"test-metrics-token",
		nil,
	)
	if err != nil {
		t.Fatalf("create router: %v", err)
	}
	superadminToken := generateTestToken(contractJWTSecret, superadminID, domain.RoleSuperadmin)

	firstRequestID := "2f5a0e23-1b3f-4a1b-89a3-9348ddc70012"
	firstResponse := deleteDisbursement(t, router, disbursementID, superadminToken, firstRequestID)
	if firstResponse.Code != http.StatusNoContent {
		t.Fatalf("expected first delete to return 204, got %d: %s", firstResponse.Code, firstResponse.Body.String())
	}
	if firstResponse.Body.Len() != 0 {
		t.Errorf("expected first delete to have no body, got %q", firstResponse.Body.String())
	}
	if firstResponse.Header().Get("X-Request-ID") != firstRequestID {
		t.Errorf("expected first delete request ID %q, got %q", firstRequestID, firstResponse.Header().Get("X-Request-ID"))
	}
	firstDeletedAt := store.items[disbursementID].DeletedAt
	if firstDeletedAt == nil {
		t.Fatal("expected first delete to set deleted_at")
	}
	if len(outboxStore.events) != 1 {
		t.Fatalf("expected one delete audit event after first delete, got %d", len(outboxStore.events))
	}
	deleteEvent := outboxStore.events[0]
	if deleteEvent.EntityType != "disbursement" || deleteEvent.EntityID != disbursementID || deleteEvent.Action != "disbursement.deleted" || deleteEvent.ActorID != superadminID || deleteEvent.RequestID.String() != firstRequestID {
		t.Errorf("expected delete audit event for the request, got %+v", deleteEvent)
	}

	secondRequestID := "2f5a0e23-1b3f-4a1b-89a3-9348ddc70013"
	secondResponse := deleteDisbursement(t, router, disbursementID, superadminToken, secondRequestID)
	if secondResponse.Code != http.StatusNoContent {
		t.Fatalf("expected repeated delete to return 204, got %d: %s", secondResponse.Code, secondResponse.Body.String())
	}
	if secondResponse.Body.Len() != 0 {
		t.Errorf("expected repeated delete to have no body, got %q", secondResponse.Body.String())
	}
	if secondResponse.Header().Get("X-Request-ID") != secondRequestID {
		t.Errorf("expected repeated delete request ID %q, got %q", secondRequestID, secondResponse.Header().Get("X-Request-ID"))
	}
	secondDeletedAt := store.items[disbursementID].DeletedAt
	if secondDeletedAt == nil || !secondDeletedAt.Equal(*firstDeletedAt) {
		t.Errorf("expected repeated delete to preserve deleted_at %v, got %v", firstDeletedAt, secondDeletedAt)
	}
	if len(outboxStore.events) != 1 {
		t.Errorf("expected repeated delete to avoid a duplicate audit event, got %d events", len(outboxStore.events))
	}
}

func newAuthContractRouter(t *testing.T) (http.Handler, string) {
	t.Helper()

	password := "contract-password-123"
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash contract password: %v", err)
	}
	userStore := newInMemoryUserStore()
	userID := uuid.MustParse("2f5a0e23-1b3f-4a1b-89a3-9348ddc70020")
	user := repository.User{
		ID:           userID,
		Username:     "contract_operator",
		PasswordHash: string(hashedPassword),
		Role:         string(domain.RoleOperator),
	}
	userStore.users[user.Username] = user
	userStore.byID[user.ID] = user

	authService := auth.NewService(
		userStore,
		newInMemorySessionStore(),
		&noopTransactor{},
		contractJWTSecret,
		15*time.Minute,
		7*24*time.Hour,
		nil,
	)
	validator, err := validation.New()
	if err != nil {
		t.Fatalf("create validator: %v", err)
	}
	router, err := httpapi.NewRouter(
		1<<20,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		contractJWTSecret,
		"disbursement-api",
		"disbursement-api-users",
		httpapi.NewAuthHandler(authService, validator),
		nil,
		metrics.NewMetricsCollector(),
		"test-metrics-token",
		nil,
	)
	if err != nil {
		t.Fatalf("create router: %v", err)
	}
	return router, password
}

func deleteDisbursement(t *testing.T, router http.Handler, id uuid.UUID, token string, requestID string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodDelete, "/disbursements/"+id.String(), nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Request-ID", requestID)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

type httpReplayClaim struct {
	fingerprint [32]byte
	claimID     uuid.UUID
	state       domain.IdempotencyState
	response    *domain.ReplayResponse
}

type httpReplayStore struct {
	claims     map[domain.IdempotencyScope]httpReplayClaim
	retryAfter time.Duration
}

func newHTTPReplayStore() *httpReplayStore {
	return &httpReplayStore{claims: make(map[domain.IdempotencyScope]httpReplayClaim)}
}

func (s *httpReplayStore) Acquire(ctx context.Context, request domain.IdempotencyClaimRequest) (domain.IdempotencyClaimResult, error) {
	if s.retryAfter > 0 {
		return domain.IdempotencyClaimResult{Outcome: domain.ClaimInProgress, RetryAfter: s.retryAfter}, nil
	}
	claim, exists := s.claims[request.Scope]
	if !exists {
		s.claims[request.Scope] = httpReplayClaim{
			fingerprint: request.Fingerprint,
			claimID:     request.ClaimID,
			state:       domain.IdempotencyInProgress,
		}
		return domain.IdempotencyClaimResult{Outcome: domain.ClaimAcquired, ClaimID: request.ClaimID}, nil
	}
	if claim.fingerprint != request.Fingerprint {
		return domain.IdempotencyClaimResult{Outcome: domain.ClaimReused}, nil
	}
	if claim.state == domain.IdempotencyCompleted && claim.response != nil {
		return domain.IdempotencyClaimResult{Outcome: domain.ClaimReplayed, Replay: claim.response}, nil
	}
	return domain.IdempotencyClaimResult{Outcome: domain.ClaimInProgress, ClaimID: claim.claimID}, nil
}

func (s *httpReplayStore) VerifyOwnership(ctx context.Context, tx repository.Transaction, scope domain.IdempotencyScope, claimID uuid.UUID) error {
	claim, exists := s.claims[scope]
	if !exists || claim.state != domain.IdempotencyInProgress || claim.claimID != claimID {
		return repository.NewError(repository.ErrorOwnershipLost, errors.New("idempotency ownership lost"))
	}
	return nil
}

func (s *httpReplayStore) Complete(ctx context.Context, tx repository.Transaction, completion domain.IdempotencyCompletion) error {
	claim, exists := s.claims[completion.Scope]
	if !exists || claim.state != domain.IdempotencyInProgress || claim.claimID != completion.ClaimID {
		return repository.NewError(repository.ErrorOwnershipLost, errors.New("idempotency completion ownership lost"))
	}
	response := completion.Response
	response.Body = append(json.RawMessage(nil), completion.Response.Body...)
	claim.state = domain.IdempotencyCompleted
	claim.response = &response
	s.claims[completion.Scope] = claim
	return nil
}

func (s *httpReplayStore) Release(ctx context.Context, scope domain.IdempotencyScope, claimID uuid.UUID) error {
	claim, exists := s.claims[scope]
	if exists && claim.state == domain.IdempotencyInProgress && claim.claimID == claimID {
		delete(s.claims, scope)
	}
	return nil
}

func newReplayContractRouter(t *testing.T) (http.Handler, *mockDisbursementStore, *mockAuditOutboxStore, uuid.UUID) {
	return newReplayContractRouterWithStore(t, newHTTPReplayStore())
}

func newReplayContractRouterWithStore(t *testing.T, replayStore *httpReplayStore) (http.Handler, *mockDisbursementStore, *mockAuditOutboxStore, uuid.UUID) {
	t.Helper()

	store := newMockDisbursementStore()
	outboxStore := &mockAuditOutboxStore{}
	coordinator, err := idempotency.NewDefaultCoordinator(replayStore, 30*time.Second, 24*time.Hour, nil)
	if err != nil {
		t.Fatalf("create idempotency coordinator: %v", err)
	}
	service, err := disbursement.NewService(store, outboxStore, mockTransactor{}, coordinator, nil)
	if err != nil {
		t.Fatalf("create disbursement service: %v", err)
	}
	validator, err := validation.New()
	if err != nil {
		t.Fatalf("create validator: %v", err)
	}
	handler := httpapi.NewDisbursementHandler(service, validator)
	actorID := uuid.MustParse("2f5a0e23-1b3f-4a1b-89a3-9348ddc70030")
	router, err := httpapi.NewRouter(
		1<<20,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		contractJWTSecret,
		"disbursement-api",
		"disbursement-api-users",
		nil,
		handler,
		nil,
		"test-metrics-token",
		nil,
	)
	if err != nil {
		t.Fatalf("create router: %v", err)
	}
	return router, store, outboxStore, actorID
}

func TestDisbursementReplayContractUsesCanonicalHeaderAndNoDuplicateSideEffects(t *testing.T) {
	router, store, outboxStore, actorID := newReplayContractRouter(t)
	idempotencyKey := "2f5a0e23-1b3f-4a1b-89a3-9348ddc70031"
	input := dto.CreateDisbursementRequest{
		RecipientName: "Replay Recipient",
		AccountNumber: "1234567890",
		BankCode:      "BCA",
		Amount:        100000,
	}
	token := generateTestToken(contractJWTSecret, actorID, domain.RoleOperator)

	request := func() *httptest.ResponseRecorder {
		req := newJSONRequest(t, http.MethodPost, "/api/v1/disbursements", input)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", idempotencyKey)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}

	firstResponse := request()
	if firstResponse.Code != http.StatusCreated {
		t.Fatalf("first create status = %d, want 201: %s", firstResponse.Code, firstResponse.Body.String())
	}
	if firstResponse.Header().Get("X-Idempotent-Replayed") != "" {
		t.Fatalf("first create unexpectedly marked as replay: %q", firstResponse.Header().Get("X-Idempotent-Replayed"))
	}

	secondResponse := request()
	if secondResponse.Code != http.StatusCreated {
		t.Fatalf("replay status = %d, want 201: %s", secondResponse.Code, secondResponse.Body.String())
	}
	if secondResponse.Header().Get("X-Idempotent-Replayed") != "true" {
		t.Fatalf("replay header = %q, want true", secondResponse.Header().Get("X-Idempotent-Replayed"))
	}
	if secondResponse.Header().Get("X-Cache") != "" {
		t.Fatalf("legacy replay header must be absent, got %q", secondResponse.Header().Get("X-Cache"))
	}
	if len(store.items) != 1 {
		t.Fatalf("persisted disbursements = %d, want 1", len(store.items))
	}
	if len(outboxStore.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(outboxStore.events))
	}
	if !json.Valid(secondResponse.Body.Bytes()) {
		t.Fatalf("replay response is not valid JSON: %s", secondResponse.Body.String())
	}

	var firstEnvelope struct {
		Success bool                     `json:"success"`
		Data    dto.DisbursementResponse `json:"data"`
	}
	if err := json.Unmarshal(firstResponse.Body.Bytes(), &firstEnvelope); err != nil {
		t.Fatalf("decode first response envelope: %v", err)
	}
	if !firstEnvelope.Success {
		t.Fatal("expected first response success envelope")
	}

	var replayEnvelope struct {
		Success bool                     `json:"success"`
		Data    dto.DisbursementResponse `json:"data"`
	}
	if err := json.Unmarshal(secondResponse.Body.Bytes(), &replayEnvelope); err != nil {
		t.Fatalf("decode replay response envelope: %v", err)
	}
	if !replayEnvelope.Success {
		t.Fatal("expected replay response success envelope")
	}
	if !reflect.DeepEqual(replayEnvelope.Data, firstEnvelope.Data) {
		t.Fatalf("replay response data differs from first response: first=%+v replay=%+v", firstEnvelope.Data, replayEnvelope.Data)
	}
}

func TestIdempotencyInProgressContractIncludesRetryAfter(t *testing.T) {
	store := &httpReplayStore{retryAfter: 5 * time.Second}
	router, _, _, actorID := newReplayContractRouterWithStore(t, store)
	request := newJSONRequest(t, http.MethodPost, "/api/v1/disbursements", dto.CreateDisbursementRequest{
		RecipientName: "Retry Recipient",
		AccountNumber: "1234567890",
		BankCode:      "BCA",
		Amount:        100000,
	})
	request.Header.Set("Authorization", "Bearer "+generateTestToken(contractJWTSecret, actorID, domain.RoleOperator))
	request.Header.Set("Idempotency-Key", "2f5a0e23-1b3f-4a1b-89a3-9348ddc70032")

	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("in-progress status = %d, want 409: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Retry-After") != "5" {
		t.Fatalf("Retry-After = %q, want 5", response.Header().Get("Retry-After"))
	}
	var envelope struct {
		Success bool `json:"success"`
		Error   struct {
			Code domain.ErrorCode `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode in-progress response: %v", err)
	}
	if envelope.Success || envelope.Error.Code != domain.CodeIdempotencyInProgress {
		t.Fatalf("unexpected in-progress response: %+v", envelope)
	}
}
