package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi/dto"
	"disbursment-api/internal/observability/metrics"
	"disbursment-api/internal/repository"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type Service struct {
	userStore       repository.UserStore
	sessionStore    repository.RefreshSessionStore
	transactor      repository.Transactor
	metrics         *metrics.MetricsCollector
	keyProvider     domain.KeyProvider
	jwtIssuer       string
	jwtAudience     string
	accessTokenTTL  time.Duration
	refreshTokenTTL time.Duration
	nowFunc         func() time.Time
}

func NewService(
	userStore repository.UserStore,
	sessionStore repository.RefreshSessionStore,
	transactor repository.Transactor,
	jwtSecret string,
	accessTokenTTL time.Duration,
	refreshTokenTTL time.Duration,
	metricsCollector *metrics.MetricsCollector,
) (*Service, error) {
	return NewServiceWithIssuerAudience(
		userStore,
		sessionStore,
		transactor,
		jwtSecret,
		"disbursement-api",
		"disbursement-api-users",
		accessTokenTTL,
		refreshTokenTTL,
		metricsCollector,
	)
}

func NewServiceWithIssuerAudience(
	userStore repository.UserStore,
	sessionStore repository.RefreshSessionStore,
	transactor repository.Transactor,
	jwtSecret string,
	jwtIssuer string,
	jwtAudience string,
	accessTokenTTL time.Duration,
	refreshTokenTTL time.Duration,
	metricsCollector *metrics.MetricsCollector,
) (*Service, error) {
	return NewServiceWithKeyProvider(
		userStore,
		sessionStore,
		transactor,
		domain.NewStaticKeyProvider("v1", jwtSecret, nil),
		jwtIssuer,
		jwtAudience,
		accessTokenTTL,
		refreshTokenTTL,
		metricsCollector,
	)
}

func NewServiceWithKeyProvider(
	userStore repository.UserStore,
	sessionStore repository.RefreshSessionStore,
	transactor repository.Transactor,
	keyProvider domain.KeyProvider,
	jwtIssuer string,
	jwtAudience string,
	accessTokenTTL time.Duration,
	refreshTokenTTL time.Duration,
	metricsCollector *metrics.MetricsCollector,
) (*Service, error) {
	if userStore == nil || sessionStore == nil || transactor == nil {
		return nil, fmt.Errorf("userStore, sessionStore, and transactor must not be nil")
	}
	if keyProvider == nil {
		return nil, fmt.Errorf("keyProvider must not be nil")
	}
	if _, secret := keyProvider.ActiveKey(); len(secret) == 0 {
		return nil, fmt.Errorf("keyProvider active secret must not be empty")
	}
	if jwtIssuer == "" {
		jwtIssuer = "disbursement-api"
	}
	if jwtAudience == "" {
		jwtAudience = "disbursement-api-users"
	}
	return &Service{
		userStore:       userStore,
		sessionStore:    sessionStore,
		transactor:      transactor,
		metrics:         metricsCollector,
		keyProvider:     keyProvider,
		jwtIssuer:       jwtIssuer,
		jwtAudience:     jwtAudience,
		accessTokenTTL:  accessTokenTTL,
		refreshTokenTTL: refreshTokenTTL,
		nowFunc:         time.Now,
	}, nil
}

var dummyPasswordHash, _ = bcrypt.GenerateFromPassword([]byte("login-timing-equalizer"), bcrypt.DefaultCost)

func (s *Service) Login(ctx context.Context, req dto.LoginRequest) (*dto.TokenResponse, error) {
	user, err := s.userStore.FindByUsername(ctx, req.Username)
	if err != nil {
		var repoErr *repository.Error
		if errors.As(err, &repoErr) && repoErr.Category == repository.ErrorNotFound {
			_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(req.Password))
			if s.metrics != nil {
				s.metrics.RecordAuthFailure("invalid_credentials")
			}
			return nil, domain.InvalidCredentials()
		}
		return nil, err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		if s.metrics != nil {
			s.metrics.RecordAuthFailure("invalid_credentials")
		}
		return nil, domain.InvalidCredentials()
	}

	now := s.nowFunc()
	accessToken, err := s.generateAccessToken(user, now)
	if err != nil {
		return nil, err
	}

	refreshTokenString := uuid.New().String()
	tokenHash := hashToken(refreshTokenString)

	session := repository.RefreshSession{
		ID:        uuid.New(),
		UserID:    user.ID,
		TokenHash: tokenHash,
		ExpiresAt: now.Add(s.refreshTokenTTL),
	}

	err = s.transactor.WithinTransaction(ctx, func(txCtx context.Context, tx repository.Transaction) error {
		return s.sessionStore.Create(txCtx, tx, session)
	})
	if err != nil {
		return nil, err
	}

	return &dto.TokenResponse{
		AccessToken:      accessToken,
		RefreshToken:     refreshTokenString,
		TokenType:        "Bearer",
		ExpiresIn:        int64(s.accessTokenTTL.Seconds()),
		RefreshExpiresIn: int64(s.refreshTokenTTL.Seconds()),
	}, nil
}

func (s *Service) Refresh(ctx context.Context, req dto.RefreshRequest) (*dto.TokenResponse, error) {
	if req.RefreshToken == "" {
		return nil, domain.InvalidRefreshToken()
	}

	oldTokenHash := hashToken(req.RefreshToken)
	now := s.nowFunc()

	var newRefreshTokenString string
	var accessToken string

	err := s.transactor.WithinTransaction(ctx, func(txCtx context.Context, tx repository.Transaction) error {
		oldSession, err := s.sessionStore.FindByTokenHash(txCtx, tx, oldTokenHash)
		if err != nil {
			var repoErr *repository.Error
			if errors.As(err, &repoErr) && repoErr.Category == repository.ErrorNotFound {
				return domain.InvalidRefreshToken()
			}
			return err
		}

		if oldSession.RevokedAt != nil || !oldSession.ExpiresAt.After(now) {
			return domain.InvalidRefreshToken()
		}

		user, err := s.userStore.FindByID(txCtx, oldSession.UserID)
		if err != nil {
			return domain.InvalidRefreshToken()
		}

		newRefreshTokenString = uuid.New().String()
		newTokenHash := hashToken(newRefreshTokenString)

		newSession := repository.RefreshSession{
			ID:        uuid.New(),
			UserID:    user.ID,
			TokenHash: newTokenHash,
			ExpiresAt: now.Add(s.refreshTokenTTL),
		}

		if err := s.sessionStore.Rotate(txCtx, tx, oldTokenHash, newSession, now); err != nil {
			var repoErr *repository.Error
			if errors.As(err, &repoErr) && repoErr.Category == repository.ErrorNotFound {
				return domain.InvalidRefreshToken()
			}
			return err
		}

		token, err := s.generateAccessToken(user, now)
		if err != nil {
			return err
		}
		accessToken = token
		return nil
	})

	if err != nil {
		if s.metrics != nil {
			s.metrics.RecordAuthFailure("invalid_refresh_token")
		}
		return nil, err
	}

	return &dto.TokenResponse{
		AccessToken:      accessToken,
		RefreshToken:     newRefreshTokenString,
		TokenType:        "Bearer",
		ExpiresIn:        int64(s.accessTokenTTL.Seconds()),
		RefreshExpiresIn: int64(s.refreshTokenTTL.Seconds()),
	}, nil
}

func (s *Service) Logout(ctx context.Context, req dto.LogoutRequest) error {
	if req.RefreshToken == "" {
		return nil
	}

	tokenHash := hashToken(req.RefreshToken)
	now := s.nowFunc()

	return s.transactor.WithinTransaction(ctx, func(txCtx context.Context, tx repository.Transaction) error {
		return s.sessionStore.RevokeByTokenHash(txCtx, tx, tokenHash, now)
	})
}

func (s *Service) generateAccessToken(user repository.User, now time.Time) (string, error) {
	keyID, secret := s.keyProvider.ActiveKey()
	claims := jwt.MapClaims{
		"sub":      user.ID.String(),
		"username": user.Username,
		"role":     user.Role,
		"iss":      s.jwtIssuer,
		"aud":      s.jwtAudience,
		"iat":      now.Unix(),
		"exp":      now.Add(s.accessTokenTTL).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	if keyID != "" {
		token.Header["kid"] = keyID
	}
	return token.SignedString(secret)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
