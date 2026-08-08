package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi"
	"disbursment-api/internal/httpapi/dto"
	"disbursment-api/internal/httpapi/validation"
	"disbursment-api/internal/repository"
	"disbursment-api/internal/service/auth"

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

func TestAuthHTTPIntegration(t *testing.T) {
	userStore := newInMemoryUserStore()
	sessionStore := newInMemorySessionStore()
	transactor := &noopTransactor{}

	password := "operatorpass123"
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}

	userID := uuid.New()
	user := repository.User{
		ID:           userID,
		Username:     "local_test_operator",
		PasswordHash: string(hashedPassword),
		Role:         "OPERATOR",
	}
	userStore.users[user.Username] = user
	userStore.byID[user.ID] = user

	authService := auth.NewService(userStore, sessionStore, transactor, "test-secret-key-12345", 15*time.Minute, 7*24*time.Hour)
	validatorEngine, err := validation.New()
	if err != nil {
		t.Fatalf("validator init failed: %v", err)
	}

	authHandler := httpapi.NewAuthHandler(authService, validatorEngine)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := httpapi.NewRouter(1<<20, logger, "test-secret-key-12345", authHandler, nil)

	var refreshToken string

	t.Run("POST /auth/login - valid credentials", func(t *testing.T) {
		body, _ := json.Marshal(dto.LoginRequest{
			Username: "local_test_operator",
			Password: password,
		})
		req, _ := http.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var responseEnvelope struct {
			Success bool              `json:"success"`
			Data    dto.TokenResponse `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &responseEnvelope); err != nil {
			t.Fatalf("failed to parse login response: %v", err)
		}

		if !responseEnvelope.Success {
			t.Errorf("expected success: true")
		}
		if responseEnvelope.Data.AccessToken == "" {
			t.Errorf("expected access_token")
		}
		if responseEnvelope.Data.RefreshToken == "" {
			t.Errorf("expected refresh_token")
		}
		if responseEnvelope.Data.TokenType != "Bearer" {
			t.Errorf("expected Bearer, got %s", responseEnvelope.Data.TokenType)
		}

		refreshToken = responseEnvelope.Data.RefreshToken
	})

	t.Run("POST /auth/login - invalid credentials", func(t *testing.T) {
		body, _ := json.Marshal(dto.LoginRequest{
			Username: "local_test_operator",
			Password: "wrongpassword",
		})
		req, _ := http.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d: %s", w.Code, w.Body.String())
		}

		var errEnvelope struct {
			Success bool `json:"success"`
			Error   struct {
				Code domain.ErrorCode `json:"code"`
			} `json:"error"`
		}
		json.Unmarshal(w.Body.Bytes(), &errEnvelope)
		if errEnvelope.Error.Code != domain.CodeInvalidCredentials {
			t.Errorf("expected INVALID_CREDENTIALS, got %s", errEnvelope.Error.Code)
		}
	})

	t.Run("POST /auth/refresh - valid refresh token", func(t *testing.T) {
		body, _ := json.Marshal(dto.RefreshRequest{
			RefreshToken: refreshToken,
		})
		req, _ := http.NewRequest(http.MethodPost, "/auth/refresh", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var responseEnvelope struct {
			Success bool              `json:"success"`
			Data    dto.TokenResponse `json:"data"`
		}
		json.Unmarshal(w.Body.Bytes(), &responseEnvelope)

		if responseEnvelope.Data.RefreshToken == "" || responseEnvelope.Data.RefreshToken == refreshToken {
			t.Errorf("expected new rotated refresh token")
		}
		refreshToken = responseEnvelope.Data.RefreshToken
	})

	t.Run("POST /auth/logout - successful and idempotent", func(t *testing.T) {
		body, _ := json.Marshal(dto.LogoutRequest{
			RefreshToken: refreshToken,
		})

		// First logout
		req, _ := http.NewRequest(http.MethodPost, "/auth/logout", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusNoContent {
			t.Fatalf("expected status 204, got %d: %s", w.Code, w.Body.String())
		}

		// Second repeated logout (idempotent)
		req2, _ := http.NewRequest(http.MethodPost, "/auth/logout", bytes.NewReader(body))
		req2.Header.Set("Content-Type", "application/json")
		w2 := httptest.NewRecorder()
		router.ServeHTTP(w2, req2)

		if w2.Code != http.StatusNoContent {
			t.Fatalf("expected status 204 on repeated logout, got %d: %s", w2.Code, w2.Body.String())
		}
	})
}
