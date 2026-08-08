package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi/dto"
	"disbursment-api/internal/repository"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type Service struct {
	userStore       repository.UserStore
	sessionStore    repository.RefreshSessionStore
	transactor      repository.Transactor
	jwtSecret       []byte
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
) *Service {
	return &Service{
		userStore:       userStore,
		sessionStore:    sessionStore,
		transactor:      transactor,
		jwtSecret:       []byte(jwtSecret),
		accessTokenTTL:  accessTokenTTL,
		refreshTokenTTL: refreshTokenTTL,
		nowFunc:         time.Now,
	}
}

func (s *Service) Login(ctx context.Context, req dto.LoginRequest) (*dto.TokenResponse, error) {
	user, err := s.userStore.FindByUsername(ctx, req.Username)
	if err != nil {
		var repoErr *repository.Error
		if errors.As(err, &repoErr) && repoErr.Category == repository.ErrorNotFound {
			return nil, domain.InvalidCredentials()
		}
		return nil, err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
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
	claims := jwt.MapClaims{
		"sub":      user.ID.String(),
		"username": user.Username,
		"role":     user.Role,
		"iat":      now.Unix(),
		"exp":      now.Add(s.accessTokenTTL).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.jwtSecret)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
