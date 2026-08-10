package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi/dto"
	"disbursment-api/internal/repository"

	"github.com/golang-jwt/jwt/v5"
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

const (
	testJWTSecret       = "super-secret-key"
	testJWTIssuer       = "test-issuer"
	testJWTAudience     = "test-audience"
	testAccessTokenTTL  = 15 * time.Minute
	testRefreshTokenTTL = 7 * 24 * time.Hour
)

func fixedAuthTime() time.Time {
	return time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
}

func newAuthServiceFixture(t *testing.T) (*Service, *mockUserStore, *mockSessionStore, repository.User, string) {
	t.Helper()

	userStore := newMockUserStore()
	sessionStore := newMockSessionStore()
	password := "securepassword123"
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}

	user := repository.User{
		ID:           uuid.MustParse("110e8400-e29b-41d4-a716-446655440000"),
		Username:     "testoperator",
		PasswordHash: string(hashedPassword),
		Role:         "OPERATOR",
	}
	userStore.users[user.Username] = user
	userStore.byID[user.ID] = user

	authService, err := NewServiceWithIssuerAudience(
		userStore,
		sessionStore,
		&mockTransactor{},
		testJWTSecret,
		testJWTIssuer,
		testJWTAudience,
		testAccessTokenTTL,
		testRefreshTokenTTL,
		nil,
	)
	if err != nil {
		t.Fatalf("failed to create auth service fixture: %v", err)
	}
	authService.nowFunc = fixedAuthTime
	return authService, userStore, sessionStore, user, password
}

func assertAccessTokenClaims(t *testing.T, tokenString string, user repository.User) {
	t.Helper()

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(testJWTSecret), nil
	}, jwt.WithoutClaimsValidation())
	if err != nil {
		t.Fatalf("failed to parse access token: %v", err)
	}
	if !token.Valid {
		t.Fatal("expected access token to be valid")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("expected map claims, got %T", token.Claims)
	}
	wantNumeric := map[string]float64{
		"iat": float64(fixedAuthTime().Unix()),
		"exp": float64(fixedAuthTime().Add(testAccessTokenTTL).Unix()),
	}
	wantStrings := map[string]string{
		"sub":      user.ID.String(),
		"username": user.Username,
		"role":     user.Role,
		"iss":      testJWTIssuer,
		"aud":      testJWTAudience,
	}
	for claim, want := range wantStrings {
		if got, ok := claims[claim].(string); !ok || got != want {
			t.Errorf("claim %q = %v, want %q", claim, claims[claim], want)
		}
	}
	for claim, want := range wantNumeric {
		if got, ok := claims[claim].(float64); !ok || got != want {
			t.Errorf("claim %q = %v, want %v", claim, claims[claim], want)
		}
	}
}

type mockTransactor struct{}

func (m *mockTransactor) WithinTransaction(ctx context.Context, fn func(context.Context, repository.Transaction) error) error {
	return fn(ctx, nil)
}

func TestAuthService_Login(t *testing.T) {
	t.Run("successful login returns exact TTLs and claims", func(t *testing.T) {
		authService, _, _, user, password := newAuthServiceFixture(t)

		resp, err := authService.Login(context.Background(), dto.LoginRequest{
			Username: user.Username,
			Password: password,
		})
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if resp.AccessToken == "" || resp.RefreshToken == "" {
			t.Fatal("expected access and refresh tokens to be set")
		}
		if resp.TokenType != "Bearer" {
			t.Errorf("expected token_type Bearer, got %s", resp.TokenType)
		}
		if resp.ExpiresIn != int64(testAccessTokenTTL/time.Second) {
			t.Errorf("expected expires_in %d, got %d", int64(testAccessTokenTTL/time.Second), resp.ExpiresIn)
		}
		if resp.RefreshExpiresIn != int64(testRefreshTokenTTL/time.Second) {
			t.Errorf("expected refresh_expires_in %d, got %d", int64(testRefreshTokenTTL/time.Second), resp.RefreshExpiresIn)
		}
		assertAccessTokenClaims(t, resp.AccessToken, user)
	})

	t.Run("wrong password returns INVALID_CREDENTIALS", func(t *testing.T) {
		authService, _, _, user, _ := newAuthServiceFixture(t)

		_, err := authService.Login(context.Background(), dto.LoginRequest{
			Username: user.Username,
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
		userStore := newMockUserStore()
		userStore.errToReturn = errors.New("db connection failure")
		svc, _ := NewService(userStore, newMockSessionStore(), &mockTransactor{}, testJWTSecret, testAccessTokenTTL, testRefreshTokenTTL, nil)
		_, err := svc.Login(context.Background(), dto.LoginRequest{Username: "test", Password: "pwd"})
		if err == nil {
			t.Error("expected error on user store failure")
		}
	})

	t.Run("session store create error", func(t *testing.T) {
		authService, _, _, user, password := newAuthServiceFixture(t)
		errSessionStore := newMockSessionStore()
		errSessionStore.errToReturn = errors.New("db error")
		authService.sessionStore = errSessionStore
		_, err := authService.Login(context.Background(), dto.LoginRequest{Username: user.Username, Password: password})
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
	})
}

func TestAuthService_Refresh(t *testing.T) {
	t.Run("successful rotation rejects the old token", func(t *testing.T) {
		authService, _, sessionStore, user, _ := newAuthServiceFixture(t)
		refreshToken := "initial-refresh-token"
		initialHash := hashToken(refreshToken)
		sessionStore.sessions[initialHash] = repository.RefreshSession{
			ID:        uuid.MustParse("220e8400-e29b-41d4-a716-446655440000"),
			UserID:    user.ID,
			TokenHash: initialHash,
			ExpiresAt: fixedAuthTime().Add(time.Hour),
		}

		resp, err := authService.Refresh(context.Background(), dto.RefreshRequest{RefreshToken: refreshToken})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if resp.AccessToken == "" || resp.RefreshToken == "" {
			t.Fatal("expected new access and refresh tokens")
		}
		if resp.ExpiresIn != int64(testAccessTokenTTL/time.Second) || resp.RefreshExpiresIn != int64(testRefreshTokenTTL/time.Second) {
			t.Fatalf("unexpected TTL response: expires_in=%d refresh_expires_in=%d", resp.ExpiresIn, resp.RefreshExpiresIn)
		}
		assertAccessTokenClaims(t, resp.AccessToken, user)
		if sessionStore.sessions[initialHash].RevokedAt == nil {
			t.Fatal("expected old session to be revoked")
		}

		replacement, err := authService.Refresh(context.Background(), dto.RefreshRequest{RefreshToken: resp.RefreshToken})
		if err != nil {
			t.Fatalf("expected returned replacement token to be usable, got %v", err)
		}
		if replacement.AccessToken == "" || replacement.RefreshToken == "" {
			t.Fatal("expected replacement refresh to issue new access and refresh tokens")
		}
		assertAccessTokenClaims(t, replacement.AccessToken, user)

		_, err = authService.Refresh(context.Background(), dto.RefreshRequest{RefreshToken: refreshToken})
		if err == nil || domain.AsError(err).Code != domain.CodeInvalidRefreshToken {
			t.Fatalf("expected INVALID_REFRESH_TOKEN for old token, got %v", err)
		}
	})

	t.Run("empty refresh token fails with INVALID_REFRESH_TOKEN", func(t *testing.T) {
		authService, _, _, _, _ := newAuthServiceFixture(t)
		_, err := authService.Refresh(context.Background(), dto.RefreshRequest{RefreshToken: ""})
		if err == nil || domain.AsError(err).Code != domain.CodeInvalidRefreshToken {
			t.Fatalf("expected INVALID_REFRESH_TOKEN, got %v", err)
		}
	})

	t.Run("expired refresh token fails with INVALID_REFRESH_TOKEN", func(t *testing.T) {
		authService, _, sessionStore, user, _ := newAuthServiceFixture(t)
		expiredToken := "expired-token"
		expiredHash := hashToken(expiredToken)
		sessionStore.sessions[expiredHash] = repository.RefreshSession{
			ID:        uuid.MustParse("330e8400-e29b-41d4-a716-446655440000"),
			UserID:    user.ID,
			TokenHash: expiredHash,
			ExpiresAt: fixedAuthTime().Add(-time.Hour),
		}

		_, err := authService.Refresh(context.Background(), dto.RefreshRequest{RefreshToken: expiredToken})
		if err == nil || domain.AsError(err).Code != domain.CodeInvalidRefreshToken {
			t.Fatalf("expected INVALID_REFRESH_TOKEN, got %v", err)
		}
	})

	t.Run("non existent user for session fails with INVALID_REFRESH_TOKEN", func(t *testing.T) {
		authService, _, sessionStore, _, _ := newAuthServiceFixture(t)
		orphanToken := "orphan-token"
		orphanHash := hashToken(orphanToken)
		sessionStore.sessions[orphanHash] = repository.RefreshSession{
			ID:        uuid.MustParse("440e8400-e29b-41d4-a716-446655440000"),
			UserID:    uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
			TokenHash: orphanHash,
			ExpiresAt: fixedAuthTime().Add(time.Hour),
		}

		_, err := authService.Refresh(context.Background(), dto.RefreshRequest{RefreshToken: orphanToken})
		if err == nil || domain.AsError(err).Code != domain.CodeInvalidRefreshToken {
			t.Fatalf("expected INVALID_REFRESH_TOKEN, got %v", err)
		}
	})

	t.Run("database error on FindByTokenHash propagates error", func(t *testing.T) {
		authService, _, _, _, _ := newAuthServiceFixture(t)
		errSessionStore := newMockSessionStore()
		errSessionStore.errToReturn = errors.New("db failure")
		authService.sessionStore = errSessionStore
		_, err := authService.Refresh(context.Background(), dto.RefreshRequest{RefreshToken: "valid-token"})
		if err == nil {
			t.Error("expected error on session find failure")
		}
	})
}

func TestAuthService_Logout(t *testing.T) {
	t.Run("logout revokes session and rejects the old token", func(t *testing.T) {
		authService, _, sessionStore, user, _ := newAuthServiceFixture(t)
		refreshToken := "logout-refresh-token"
		tokenHash := hashToken(refreshToken)
		sessionStore.sessions[tokenHash] = repository.RefreshSession{
			ID:        uuid.MustParse("660e8400-e29b-41d4-a716-446655440000"),
			UserID:    user.ID,
			TokenHash: tokenHash,
			ExpiresAt: fixedAuthTime().Add(time.Hour),
		}

		if err := authService.Logout(context.Background(), dto.LogoutRequest{RefreshToken: refreshToken}); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if sessionStore.sessions[tokenHash].RevokedAt == nil {
			t.Fatal("expected session to be revoked")
		}

		if err := authService.Logout(context.Background(), dto.LogoutRequest{RefreshToken: refreshToken}); err != nil {
			t.Fatalf("expected no error on repeated logout, got %v", err)
		}
		_, err := authService.Refresh(context.Background(), dto.RefreshRequest{RefreshToken: refreshToken})
		if err == nil || domain.AsError(err).Code != domain.CodeInvalidRefreshToken {
			t.Fatalf("expected INVALID_REFRESH_TOKEN for logged-out token, got %v", err)
		}
	})

	t.Run("empty refresh token on logout is no-op success", func(t *testing.T) {
		authService, _, _, _, _ := newAuthServiceFixture(t)
		if err := authService.Logout(context.Background(), dto.LogoutRequest{RefreshToken: ""}); err != nil {
			t.Fatalf("expected no error on empty logout request, got %v", err)
		}
	})

	t.Run("database error on logout propagates error", func(t *testing.T) {
		authService, _, _, _, _ := newAuthServiceFixture(t)
		errSessionStore := newMockSessionStore()
		errSessionStore.errToReturn = errors.New("db failure")
		authService.sessionStore = errSessionStore
		err := authService.Logout(context.Background(), dto.LogoutRequest{RefreshToken: "valid-token"})
		if err == nil {
			t.Fatalf("expected error on logout db failure, got nil")
		}
	})

	t.Run("NewServiceWithKeyProvider fail closed on nil or empty secret keyProvider", func(t *testing.T) {
		_, err := NewServiceWithKeyProvider(
			newMockUserStore(),
			newMockSessionStore(),
			&mockTransactor{},
			nil,
			"",
			"",
			15*time.Minute,
			7*24*time.Hour,
			nil,
		)
		if err == nil {
			t.Fatal("expected error on nil keyProvider, got nil")
		}

		emptyProvider := domain.NewStaticKeyProvider("v1", "", nil)
		_, err = NewServiceWithKeyProvider(
			newMockUserStore(),
			newMockSessionStore(),
			&mockTransactor{},
			emptyProvider,
			"",
			"",
			15*time.Minute,
			7*24*time.Hour,
			nil,
		)
		if err == nil {
			t.Fatal("expected error on empty keyProvider active secret, got nil")
		}
	})

	t.Run("Login issues JWT with active key kid header claim", func(t *testing.T) {
		keyProvider := domain.NewStaticKeyProvider("v2", "secret-v2", nil)
		userStore := newMockUserStore()
		sessionStore := newMockSessionStore()
		password := "password123"
		hashedPassword, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)

		user := repository.User{
			ID:           uuid.New(),
			Username:     "rotuser",
			PasswordHash: string(hashedPassword),
			Role:         "ADMIN",
		}
		userStore.users[user.Username] = user
		userStore.byID[user.ID] = user

		svc, err := NewServiceWithKeyProvider(
			userStore,
			sessionStore,
			&mockTransactor{},
			keyProvider,
			"test-iss",
			"test-aud",
			15*time.Minute,
			7*24*time.Hour,
			nil,
		)
		if err != nil {
			t.Fatalf("failed to create service: %v", err)
		}

		resp, err := svc.Login(context.Background(), dto.LoginRequest{Username: "rotuser", Password: password})
		if err != nil {
			t.Fatalf("login failed: %v", err)
		}

		parsedToken, err := jwt.Parse(resp.AccessToken, func(token *jwt.Token) (interface{}, error) {
			return []byte("secret-v2"), nil
		})
		if err != nil || !parsedToken.Valid {
			t.Fatalf("failed to parse issued access token: %v", err)
		}
		if kid, ok := parsedToken.Header["kid"].(string); !ok || kid != "v2" {
			t.Fatalf("expected JWT header kid='v2', got %v", parsedToken.Header["kid"])
		}
	})

	t.Run("Login database error propagates repository error", func(t *testing.T) {
		userStore := newMockUserStore()
		userStore.errToReturn = errors.New("internal database error")
		svc, _ := NewService(userStore, newMockSessionStore(), &mockTransactor{}, "secret", 15*time.Minute, 7*24*time.Hour, nil)

		_, err := svc.Login(context.Background(), dto.LoginRequest{Username: "user", Password: "pwd"})
		if err == nil || err.Error() != "internal database error" {
			t.Fatalf("expected internal database error, got %v", err)
		}
	})

	t.Run("Refresh user not found error returns InvalidRefreshToken", func(t *testing.T) {
		authService, userStore, sessionStore, _, _ := newAuthServiceFixture(t)
		sessionID := uuid.New()
		userID := uuid.New()
		rawToken := "valid-refresh"
		tokenHash := hashToken(rawToken)

		sessionStore.sessions[tokenHash] = repository.RefreshSession{
			ID:        sessionID,
			UserID:    userID,
			TokenHash: tokenHash,
			ExpiresAt: time.Now().Add(1 * time.Hour),
		}

		delete(userStore.byID, userID)

		_, err := authService.Refresh(context.Background(), dto.RefreshRequest{RefreshToken: rawToken})
		if err == nil || domain.AsError(err).Code != domain.CodeInvalidRefreshToken {
			t.Fatalf("expected INVALID_REFRESH_TOKEN when user not found, got %v", err)
		}
	})
}
