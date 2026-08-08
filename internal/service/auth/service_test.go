package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi/dto"
	"disbursment-api/internal/repository"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type mockUserStore struct {
	users       map[string]repository.User
	byID        map[uuid.UUID]repository.User
	errToReturn error
}

func newMockUserStore() *mockUserStore {
	return &mockUserStore{
		users: make(map[string]repository.User),
		byID:  make(map[uuid.UUID]repository.User),
	}
}

func (m *mockUserStore) FindByID(ctx context.Context, id uuid.UUID) (repository.User, error) {
	if m.errToReturn != nil {
		return repository.User{}, m.errToReturn
	}
	u, ok := m.byID[id]
	if !ok {
		return repository.User{}, repository.NewError(repository.ErrorNotFound, errors.New("not found"))
	}
	return u, nil
}

func (m *mockUserStore) FindByUsername(ctx context.Context, username string) (repository.User, error) {
	if m.errToReturn != nil {
		return repository.User{}, m.errToReturn
	}
	u, ok := m.users[username]
	if !ok {
		return repository.User{}, repository.NewError(repository.ErrorNotFound, errors.New("not found"))
	}
	return u, nil
}

type mockSessionStore struct {
	sessions    map[string]repository.RefreshSession
	errToReturn error
}

func newMockSessionStore() *mockSessionStore {
	return &mockSessionStore{
		sessions: make(map[string]repository.RefreshSession),
	}
}

func (m *mockSessionStore) Create(ctx context.Context, tx repository.Transaction, session repository.RefreshSession) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.sessions[session.TokenHash] = session
	return nil
}

func (m *mockSessionStore) FindByTokenHash(ctx context.Context, tx repository.Transaction, tokenHash string) (repository.RefreshSession, error) {
	if m.errToReturn != nil {
		return repository.RefreshSession{}, m.errToReturn
	}
	s, ok := m.sessions[tokenHash]
	if !ok {
		return repository.RefreshSession{}, repository.NewError(repository.ErrorNotFound, errors.New("not found"))
	}
	return s, nil
}

func (m *mockSessionStore) Rotate(ctx context.Context, tx repository.Transaction, oldTokenHash string, newSession repository.RefreshSession, now time.Time) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	old, ok := m.sessions[oldTokenHash]
	if !ok || old.RevokedAt != nil || !old.ExpiresAt.After(now) {
		return repository.NewError(repository.ErrorNotFound, errors.New("not found"))
	}
	old.RevokedAt = &now
	old.ReplacedByID = &newSession.ID
	m.sessions[oldTokenHash] = old
	m.sessions[newSession.TokenHash] = newSession
	return nil
}

func (m *mockSessionStore) RevokeByTokenHash(ctx context.Context, tx repository.Transaction, tokenHash string, now time.Time) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	s, ok := m.sessions[tokenHash]
	if ok {
		s.RevokedAt = &now
		m.sessions[tokenHash] = s
	}
	return nil
}

type mockTransactor struct{}

func (m *mockTransactor) WithinTransaction(ctx context.Context, fn func(context.Context, repository.Transaction) error) error {
	return fn(ctx, nil)
}

func TestAuthService_Login(t *testing.T) {
	userStore := newMockUserStore()
	sessionStore := newMockSessionStore()
	transactor := &mockTransactor{}

	password := "securepassword123"
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}

	userID := uuid.New()
	user := repository.User{
		ID:           userID,
		Username:     "testoperator",
		PasswordHash: string(hashedPassword),
		Role:         "OPERATOR",
	}
	userStore.users[user.Username] = user
	userStore.byID[user.ID] = user

	authService := NewService(userStore, sessionStore, transactor, "super-secret-key", 15*time.Minute, 7*24*time.Hour)

	t.Run("successful login", func(t *testing.T) {
		resp, err := authService.Login(context.Background(), dto.LoginRequest{
			Username: "testoperator",
			Password: password,
		})
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if resp.AccessToken == "" {
			t.Errorf("expected access_token to be set")
		}
		if resp.RefreshToken == "" {
			t.Errorf("expected refresh_token to be set")
		}
		if resp.TokenType != "Bearer" {
			t.Errorf("expected token_type Bearer, got %s", resp.TokenType)
		}
		if resp.ExpiresIn != 900 {
			t.Errorf("expected expires_in 900, got %d", resp.ExpiresIn)
		}
		if resp.RefreshExpiresIn != 604800 {
			t.Errorf("expected refresh_expires_in 604800, got %d", resp.RefreshExpiresIn)
		}
	})

	t.Run("wrong password returns INVALID_CREDENTIALS", func(t *testing.T) {
		_, err := authService.Login(context.Background(), dto.LoginRequest{
			Username: "testoperator",
			Password: "wrongpassword",
		})
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		domainErr := domain.AsError(err)
		if domainErr.Code != domain.CodeInvalidCredentials {
			t.Errorf("expected INVALID_CREDENTIALS, got %s", domainErr.Code)
		}
	})

	t.Run("database error on find by username returns error", func(t *testing.T) {
		errStore := newMockUserStore()
		errStore.errToReturn = errors.New("db connection failure")
		svc := NewService(errStore, sessionStore, transactor, "secret", 15*time.Minute, 7*24*time.Hour)
		_, err := svc.Login(context.Background(), dto.LoginRequest{Username: "testadmin", Password: "any"})
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
	})

	t.Run("database error on session create returns error", func(t *testing.T) {
		errSessionStore := newMockSessionStore()
		errSessionStore.errToReturn = errors.New("db insert failure")
		svc := NewService(userStore, errSessionStore, transactor, "secret", 15*time.Minute, 7*24*time.Hour)
		_, err := svc.Login(context.Background(), dto.LoginRequest{Username: "testadmin", Password: password})
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
	})
}

func TestAuthService_Refresh(t *testing.T) {
	userStore := newMockUserStore()
	sessionStore := newMockSessionStore()
	transactor := &mockTransactor{}

	userID := uuid.New()
	user := repository.User{
		ID:           userID,
		Username:     "testadmin",
		PasswordHash: "hash",
		Role:         "ADMIN",
	}
	userStore.users[user.Username] = user
	userStore.byID[user.ID] = user

	authService := NewService(userStore, sessionStore, transactor, "super-secret-key", 15*time.Minute, 7*24*time.Hour)

	refreshTokenStr := "initial-refresh-token"
	h := sha256.Sum256([]byte(refreshTokenStr))
	initialHash := hex.EncodeToString(h[:])

	sessionStore.sessions[initialHash] = repository.RefreshSession{
		ID:        uuid.New(),
		UserID:    userID,
		TokenHash: initialHash,
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	t.Run("successful refresh rotation", func(t *testing.T) {
		resp, err := authService.Refresh(context.Background(), dto.RefreshRequest{
			RefreshToken: refreshTokenStr,
		})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if resp.AccessToken == "" || resp.RefreshToken == "" {
			t.Errorf("expected new access and refresh tokens")
		}

		// Verify old token is revoked
		oldSession := sessionStore.sessions[initialHash]
		if oldSession.RevokedAt == nil {
			t.Errorf("expected old session to be revoked")
		}
	})

	t.Run("reusing revoked refresh token fails with INVALID_REFRESH_TOKEN", func(t *testing.T) {
		_, err := authService.Refresh(context.Background(), dto.RefreshRequest{
			RefreshToken: refreshTokenStr,
		})
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		domainErr := domain.AsError(err)
		if domainErr.Code != domain.CodeInvalidRefreshToken {
			t.Errorf("expected INVALID_REFRESH_TOKEN, got %s", domainErr.Code)
		}
	})

	t.Run("empty refresh token fails with INVALID_REFRESH_TOKEN", func(t *testing.T) {
		_, err := authService.Refresh(context.Background(), dto.RefreshRequest{
			RefreshToken: "",
		})
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		domainErr := domain.AsError(err)
		if domainErr.Code != domain.CodeInvalidRefreshToken {
			t.Errorf("expected INVALID_REFRESH_TOKEN, got %s", domainErr.Code)
		}
	})

	t.Run("expired refresh token fails with INVALID_REFRESH_TOKEN", func(t *testing.T) {
		expiredTokenStr := "expired-token"
		hExp := sha256.Sum256([]byte(expiredTokenStr))
		expHash := hex.EncodeToString(hExp[:])
		sessionStore.sessions[expHash] = repository.RefreshSession{
			ID:        uuid.New(),
			UserID:    userID,
			TokenHash: expHash,
			ExpiresAt: time.Now().Add(-1 * time.Hour), // Expired
		}

		_, err := authService.Refresh(context.Background(), dto.RefreshRequest{
			RefreshToken: expiredTokenStr,
		})
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		domainErr := domain.AsError(err)
		if domainErr.Code != domain.CodeInvalidRefreshToken {
			t.Errorf("expected INVALID_REFRESH_TOKEN, got %s", domainErr.Code)
		}
	})

	t.Run("non existent user for session fails with INVALID_REFRESH_TOKEN", func(t *testing.T) {
		orphanTokenStr := "orphan-token"
		hOrphan := sha256.Sum256([]byte(orphanTokenStr))
		orphanHash := hex.EncodeToString(hOrphan[:])
		sessionStore.sessions[orphanHash] = repository.RefreshSession{
			ID:        uuid.New(),
			UserID:    uuid.New(), // User not in userStore
			TokenHash: orphanHash,
			ExpiresAt: time.Now().Add(1 * time.Hour),
		}

		_, err := authService.Refresh(context.Background(), dto.RefreshRequest{
			RefreshToken: orphanTokenStr,
		})
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		domainErr := domain.AsError(err)
		if domainErr.Code != domain.CodeInvalidRefreshToken {
			t.Errorf("expected INVALID_REFRESH_TOKEN, got %s", domainErr.Code)
		}
	})

	t.Run("database error on FindByTokenHash propagates error", func(t *testing.T) {
		errSessionStore := newMockSessionStore()
		errSessionStore.errToReturn = errors.New("db failure")
		svc := NewService(userStore, errSessionStore, transactor, "secret", 15*time.Minute, 7*24*time.Hour)
		_, err := svc.Refresh(context.Background(), dto.RefreshRequest{RefreshToken: "valid-token"})
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
	})
}

func TestAuthService_Logout(t *testing.T) {
	userStore := newMockUserStore()
	sessionStore := newMockSessionStore()
	transactor := &mockTransactor{}

	authService := NewService(userStore, sessionStore, transactor, "super-secret-key", 15*time.Minute, 7*24*time.Hour)

	refreshTokenStr := "logout-refresh-token"
	h := sha256.Sum256([]byte(refreshTokenStr))
	tokenHash := hex.EncodeToString(h[:])

	sessionStore.sessions[tokenHash] = repository.RefreshSession{
		ID:        uuid.New(),
		UserID:    uuid.New(),
		TokenHash: tokenHash,
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	t.Run("logout revokes session", func(t *testing.T) {
		err := authService.Logout(context.Background(), dto.LogoutRequest{
			RefreshToken: refreshTokenStr,
		})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		session := sessionStore.sessions[tokenHash]
		if session.RevokedAt == nil {
			t.Errorf("expected session to be revoked")
		}
	})

	t.Run("repeated logout is idempotent success", func(t *testing.T) {
		err := authService.Logout(context.Background(), dto.LogoutRequest{
			RefreshToken: refreshTokenStr,
		})
		if err != nil {
			t.Fatalf("expected no error on repeated logout, got %v", err)
		}
	})

	t.Run("empty refresh token on logout is no-op success", func(t *testing.T) {
		err := authService.Logout(context.Background(), dto.LogoutRequest{
			RefreshToken: "",
		})
		if err != nil {
			t.Fatalf("expected no error on empty logout request, got %v", err)
		}
	})

	t.Run("database error on logout propagates error", func(t *testing.T) {
		errSessionStore := newMockSessionStore()
		errSessionStore.errToReturn = errors.New("db failure")
		svc := NewService(userStore, errSessionStore, transactor, "secret", 15*time.Minute, 7*24*time.Hour)
		err := svc.Logout(context.Background(), dto.LogoutRequest{RefreshToken: "valid-token"})
		if err == nil {
			t.Fatalf("expected error on logout db failure, got nil")
		}
	})
}
