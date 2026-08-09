package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
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

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type inMemoryUserStore struct {
	users map[string]repository.User
	byID  map[uuid.UUID]repository.User
}

func newInMemoryUserStore() *inMemoryUserStore {
	return &inMemoryUserStore{
		users: make(map[string]repository.User),
		byID:  make(map[uuid.UUID]repository.User),
	}
}

func (m *inMemoryUserStore) FindByID(ctx context.Context, id uuid.UUID) (repository.User, error) {
	u, ok := m.byID[id]
	if !ok {
		return repository.User{}, repository.NewError(repository.ErrorNotFound, http.ErrNoLocation)
	}
	return u, nil
}

func (m *inMemoryUserStore) FindByUsername(ctx context.Context, username string) (repository.User, error) {
	u, ok := m.users[username]
	if !ok {
		return repository.User{}, repository.NewError(repository.ErrorNotFound, http.ErrNoLocation)
	}
	return u, nil
}

type inMemorySessionStore struct {
	sessions map[string]repository.RefreshSession
}

func newInMemorySessionStore() *inMemorySessionStore {
	return &inMemorySessionStore{
		sessions: make(map[string]repository.RefreshSession),
	}
}

func (m *inMemorySessionStore) Create(ctx context.Context, tx repository.Transaction, session repository.RefreshSession) error {
	m.sessions[session.TokenHash] = session
	return nil
}

func (m *inMemorySessionStore) FindByTokenHash(ctx context.Context, tx repository.Transaction, tokenHash string) (repository.RefreshSession, error) {
	s, ok := m.sessions[tokenHash]
	if !ok {
		return repository.RefreshSession{}, repository.NewError(repository.ErrorNotFound, http.ErrNoLocation)
	}
	return s, nil
}

func (m *inMemorySessionStore) Rotate(ctx context.Context, tx repository.Transaction, oldTokenHash string, newSession repository.RefreshSession, now time.Time) error {
	old, ok := m.sessions[oldTokenHash]
	if !ok || old.RevokedAt != nil || !old.ExpiresAt.After(now) {
		return repository.NewError(repository.ErrorNotFound, http.ErrNoLocation)
	}
	old.RevokedAt = &now
	old.ReplacedByID = &newSession.ID
	m.sessions[oldTokenHash] = old
	m.sessions[newSession.TokenHash] = newSession
	return nil
}

func (m *inMemorySessionStore) RevokeByTokenHash(ctx context.Context, tx repository.Transaction, tokenHash string, now time.Time) error {
	s, ok := m.sessions[tokenHash]
	if ok && s.RevokedAt == nil {
		s.RevokedAt = &now
		m.sessions[tokenHash] = s
	}
	return nil
}

type noopTransactor struct{}

func (n *noopTransactor) WithinTransaction(ctx context.Context, fn func(context.Context, repository.Transaction) error) error {
	return fn(ctx, nil)
}

const integrationRequestID = "770e8400-e29b-41d4-a716-446655440000"

type authHTTPFixture struct {
	router   http.Handler
	password string
}

type errorResponseEnvelope struct {
	Success bool `json:"success"`
	Error   struct {
		Code    domain.ErrorCode    `json:"code"`
		Message string              `json:"message"`
		Details []domain.FieldError `json:"details"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}

func newAuthHTTPFixture(t *testing.T) authHTTPFixture {
	t.Helper()

	userStore := newInMemoryUserStore()
	sessionStore := newInMemorySessionStore()
	password := "operatorpass123"
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}

	userID := uuid.MustParse("880e8400-e29b-41d4-a716-446655440000")
	user := repository.User{
		ID:           userID,
		Username:     "local_test_operator",
		PasswordHash: string(hashedPassword),
		Role:         "OPERATOR",
	}
	userStore.users[user.Username] = user
	userStore.byID[user.ID] = user

	const secret = "test-secret-key-12345"
	authService := auth.NewService(userStore, sessionStore, &noopTransactor{}, secret, 15*time.Minute, 7*24*time.Hour, nil)
	validatorEngine, err := validation.New()
	if err != nil {
		t.Fatalf("validator init failed: %v", err)
	}
	authHandler := httpapi.NewAuthHandler(authService, validatorEngine)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metricsCollector := metrics.NewMetricsCollector()
	router, err := httpapi.NewRouter(1<<20, logger, secret, "disbursement-api", "disbursement-api-users", authHandler, nil, metricsCollector, "test-metrics-token", nil)
	if err != nil {
		t.Fatalf("router init failed: %v", err)
	}
	return authHTTPFixture{router: router, password: password}
}

func newJSONRequest(t *testing.T, method string, path string, payload any) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}
	request, err := http.NewRequest(method, path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", integrationRequestID)
	return request
}

func newRawRequest(t *testing.T, method string, path string, body string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", integrationRequestID)
	return request
}

func newCanonicalDisbursementHTTPFixture(t *testing.T) (http.Handler, uuid.UUID) {
	t.Helper()

	const secret = "test-secret-key-12345"
	actorID := uuid.MustParse("990e8400-e29b-41d4-a716-446655440000")
	store := newMockDisbursementStore()
	outboxStore := &mockAuditOutboxStore{}
	disbursementService, err := disbursement.NewService(store, outboxStore, &noopTransactor{}, nil, nil)
	if err != nil {
		t.Fatalf("failed to create disbursement service: %v", err)
	}
	validatorEngine, err := validation.New()
	if err != nil {
		t.Fatalf("validator init failed: %v", err)
	}
	disbursementHandler := httpapi.NewDisbursementHandler(disbursementService, validatorEngine)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router, err := httpapi.NewRouter(1<<20, logger, secret, "disbursement-api", "disbursement-api-users", nil, disbursementHandler, nil, "test-metrics-token", nil)
	if err != nil {
		t.Fatalf("router init failed: %v", err)
	}
	return router, actorID
}

func loginHTTP(t *testing.T, fixture authHTTPFixture) dto.TokenResponse {
	t.Helper()
	request := newJSONRequest(t, http.MethodPost, "/api/v1/auth/login", dto.LoginRequest{
		Username: "local_test_operator",
		Password: fixture.password,
	})
	response := httptest.NewRecorder()
	fixture.router.ServeHTTP(response, request)
	return assertTokenSuccessResponse(t, response, "")
}

func assertExactJSONKeys(t *testing.T, raw map[string]json.RawMessage, want ...string) {
	t.Helper()
	if len(raw) != len(want) {
		t.Fatalf("expected JSON keys %v, got %v", want, raw)
	}
	for _, key := range want {
		if _, ok := raw[key]; !ok {
			t.Fatalf("expected JSON key %q in %v", key, raw)
		}
	}
}

func assertTokenSuccessResponse(t *testing.T, response *httptest.ResponseRecorder, previousRefreshToken string) dto.TokenResponse {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("expected exact JSON content type, got %q", got)
	}

	var rawEnvelope map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &rawEnvelope); err != nil {
		t.Fatalf("failed to parse token response: %v", err)
	}
	assertExactJSONKeys(t, rawEnvelope, "success", "data")

	var envelope struct {
		Success bool              `json:"success"`
		Data    dto.TokenResponse `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to parse token response: %v", err)
	}
	if !envelope.Success || envelope.Data.AccessToken == "" || envelope.Data.RefreshToken == "" {
		t.Fatalf("expected successful token response, got %+v", envelope)
	}
	if previousRefreshToken != "" && envelope.Data.RefreshToken == previousRefreshToken {
		t.Fatalf("expected rotated refresh token, got the previous token")
	}
	if envelope.Data.TokenType != "Bearer" || envelope.Data.ExpiresIn != 900 || envelope.Data.RefreshExpiresIn != 604800 {
		t.Fatalf("unexpected token response TTL/type: %+v", envelope.Data)
	}

	var rawData map[string]json.RawMessage
	if err := json.Unmarshal(rawEnvelope["data"], &rawData); err != nil {
		t.Fatalf("failed to parse token data: %v", err)
	}
	assertExactJSONKeys(t, rawData, "access_token", "refresh_token", "token_type", "expires_in", "refresh_expires_in")
	return envelope.Data
}

func assertHTTPError(t *testing.T, response *httptest.ResponseRecorder, status int, code domain.ErrorCode, message string, details []domain.FieldError) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("expected status %d, got %d: %s", status, response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("expected exact JSON content type, got %q", got)
	}

	var envelope errorResponseEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to parse error response: %v", err)
	}
	if envelope.Success || envelope.Error.Code != code || envelope.Error.Message != message || envelope.RequestID != integrationRequestID {
		t.Fatalf("unexpected error envelope: %+v", envelope)
	}
	if !reflect.DeepEqual(envelope.Error.Details, details) {
		t.Fatalf("expected error details %#v, got %#v", details, envelope.Error.Details)
	}

	var rawEnvelope map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &rawEnvelope); err != nil {
		t.Fatalf("failed to parse raw error response: %v", err)
	}
	assertExactJSONKeys(t, rawEnvelope, "success", "error", "request_id")
	var rawError map[string]json.RawMessage
	if err := json.Unmarshal(rawEnvelope["error"], &rawError); err != nil {
		t.Fatalf("failed to parse raw error body: %v", err)
	}
	if len(details) == 0 {
		assertExactJSONKeys(t, rawError, "code", "message")
	} else {
		assertExactJSONKeys(t, rawError, "code", "message", "details")
	}
}

func TestAuthHTTPIntegration(t *testing.T) {
	t.Run("POST /api/v1/auth/login - valid credentials returns exact TTLs", func(t *testing.T) {
		fixture := newAuthHTTPFixture(t)
		request := newJSONRequest(t, http.MethodPost, "/api/v1/auth/login", dto.LoginRequest{
			Username: "local_test_operator",
			Password: fixture.password,
		})
		response := httptest.NewRecorder()
		fixture.router.ServeHTTP(response, request)
		assertTokenSuccessResponse(t, response, "")
	})

	t.Run("POST /api/v1/disbursements - signed operator creates pending disbursement", func(t *testing.T) {
		router, actorID := newCanonicalDisbursementHTTPFixture(t)
		request := newJSONRequest(t, http.MethodPost, "/api/v1/disbursements", dto.CreateDisbursementRequest{
			RecipientName: "Canonical Recipient",
			AccountNumber: "1234567890",
			BankCode:      "bca",
			Amount:        500000,
			Note:          "Canonical contract",
		})
		request.Header.Set("Authorization", "Bearer "+generateTestToken("test-secret-key-12345", actorID, domain.RoleOperator))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)

		if response.Code != http.StatusCreated {
			t.Fatalf("expected status 201, got %d: %s", response.Code, response.Body.String())
		}
		if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
			t.Fatalf("expected exact JSON content type, got %q", got)
		}
		var rawEnvelope map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &rawEnvelope); err != nil {
			t.Fatalf("failed to parse disbursement response: %v", err)
		}
		assertExactJSONKeys(t, rawEnvelope, "success", "data")

		var envelope struct {
			Success bool                     `json:"success"`
			Data    dto.DisbursementResponse `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("failed to parse disbursement response: %v", err)
		}
		if !envelope.Success {
			t.Fatal("expected successful disbursement response")
		}
		if envelope.Data.ID == uuid.Nil || envelope.Data.CreatedBy != actorID || envelope.Data.RecipientName != "Canonical Recipient" || envelope.Data.AccountNumber != "1234567890" || envelope.Data.BankCode != "BCA" || envelope.Data.Amount != 500000 || envelope.Data.AdminFee != domain.LowerTierAdminFee || envelope.Data.Status != domain.StatusPending || envelope.Data.Note != "Canonical contract" {
			t.Fatalf("unexpected disbursement response: %+v", envelope.Data)
		}
	})

	t.Run("POST /api/v1/auth/login - helper fixture returns usable token", func(t *testing.T) {
		loginHTTP(t, newAuthHTTPFixture(t))
	})

	t.Run("POST /api/v1/auth/login - invalid credentials returns exact error envelope", func(t *testing.T) {
		fixture := newAuthHTTPFixture(t)
		request := newJSONRequest(t, http.MethodPost, "/api/v1/auth/login", dto.LoginRequest{
			Username: "local_test_operator",
			Password: "wrongpassword",
		})
		response := httptest.NewRecorder()
		fixture.router.ServeHTTP(response, request)
		assertHTTPError(t, response, http.StatusUnauthorized, domain.CodeInvalidCredentials, "Kredensial tidak valid", nil)
	})

	t.Run("POST /api/v1/auth/refresh - rotation rejects old token", func(t *testing.T) {
		fixture := newAuthHTTPFixture(t)
		initial := loginHTTP(t, fixture)
		request := newJSONRequest(t, http.MethodPost, "/api/v1/auth/refresh", dto.RefreshRequest{RefreshToken: initial.RefreshToken})
		response := httptest.NewRecorder()
		fixture.router.ServeHTTP(response, request)
		refreshed := assertTokenSuccessResponse(t, response, initial.RefreshToken)

		replacementRequest := newJSONRequest(t, http.MethodPost, "/api/v1/auth/refresh", dto.RefreshRequest{RefreshToken: refreshed.RefreshToken})
		replacementResponse := httptest.NewRecorder()
		fixture.router.ServeHTTP(replacementResponse, replacementRequest)
		assertTokenSuccessResponse(t, replacementResponse, refreshed.RefreshToken)

		oldRequest := newJSONRequest(t, http.MethodPost, "/api/v1/auth/refresh", dto.RefreshRequest{RefreshToken: initial.RefreshToken})
		oldResponse := httptest.NewRecorder()
		fixture.router.ServeHTTP(oldResponse, oldRequest)
		assertHTTPError(t, oldResponse, http.StatusUnauthorized, domain.CodeInvalidRefreshToken, "Refresh token tidak valid", nil)
	})

	t.Run("POST /api/v1/auth/logout - idempotent and rejects logged-out token", func(t *testing.T) {
		fixture := newAuthHTTPFixture(t)
		tokens := loginHTTP(t, fixture)
		request := newJSONRequest(t, http.MethodPost, "/api/v1/auth/logout", dto.LogoutRequest{RefreshToken: tokens.RefreshToken})
		response := httptest.NewRecorder()
		fixture.router.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
			t.Fatalf("expected empty 204 logout response, got %d with body %q", response.Code, response.Body.String())
		}

		repeatedResponse := httptest.NewRecorder()
		fixture.router.ServeHTTP(repeatedResponse, newJSONRequest(t, http.MethodPost, "/api/v1/auth/logout", dto.LogoutRequest{RefreshToken: tokens.RefreshToken}))
		if repeatedResponse.Code != http.StatusNoContent || repeatedResponse.Body.Len() != 0 {
			t.Fatalf("expected empty 204 repeated logout response, got %d with body %q", repeatedResponse.Code, repeatedResponse.Body.String())
		}

		refreshResponse := httptest.NewRecorder()
		fixture.router.ServeHTTP(refreshResponse, newJSONRequest(t, http.MethodPost, "/api/v1/auth/refresh", dto.RefreshRequest{RefreshToken: tokens.RefreshToken}))
		assertHTTPError(t, refreshResponse, http.StatusUnauthorized, domain.CodeInvalidRefreshToken, "Refresh token tidak valid", nil)
	})

	t.Run("auth endpoints reject malformed JSON with exact error envelope", func(t *testing.T) {
		fixture := newAuthHTTPFixture(t)
		for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/refresh", "/api/v1/auth/logout"} {
			response := httptest.NewRecorder()
			fixture.router.ServeHTTP(response, newRawRequest(t, http.MethodPost, path, `{invalid-json`))
			assertHTTPError(t, response, http.StatusBadRequest, domain.CodeValidationError, "Input tidak valid", []domain.FieldError{{Field: "body", Message: "format JSON tidak valid"}})
		}
	})

	t.Run("GET /metrics - authentication token check", func(t *testing.T) {
		fixture := newAuthHTTPFixture(t)
		missingRequest := newJSONRequest(t, http.MethodGet, "/metrics", struct{}{})
		missingRequest.Body = http.NoBody
		missingResponse := httptest.NewRecorder()
		fixture.router.ServeHTTP(missingResponse, missingRequest)
		if missingResponse.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 Unauthorized for metrics without header, got %d", missingResponse.Code)
		}

		validRequest := newJSONRequest(t, http.MethodGet, "/metrics", struct{}{})
		validRequest.Body = http.NoBody
		validRequest.Header.Set("X-Metrics-Token", "test-metrics-token")
		validResponse := httptest.NewRecorder()
		fixture.router.ServeHTTP(validResponse, validRequest)
		if validResponse.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for metrics with valid token, got %d", validResponse.Code)
		}
	})
}
