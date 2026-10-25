package service

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/auth"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

var (
	ErrInvalidStaffCredentials = errors.New("invalid staff credentials")
	ErrInvalidStaffSession     = errors.New("invalid staff session")
)

const staffSessionTTL = 12 * time.Hour

type StaffAuthStore interface {
	GetCredentialsByEmail(context.Context, string) (domain.StaffCredentials, error)
	CreateSession(context.Context, domain.StaffSession) error
	GetActiveSessionByTokenHash(context.Context, [32]byte) (domain.StaffSessionIdentity, error)
	RevokeSessionByTokenHash(context.Context, [32]byte) error
}

type StaffLoginResult struct {
	StaffID      int64
	DisplayName  string
	Email        string
	SessionToken string
	CSRFToken    string
	ExpiresAt    time.Time
}

type StaffAuthService struct {
	store StaffAuthStore
	now   func() time.Time
}

func NewStaffAuthService(store StaffAuthStore) *StaffAuthService {
	return &StaffAuthService{store: store, now: time.Now}
}

// Login verifies credentials and persists a server-side session.
// The caller must send raw tokens only through appropriately secured cookies,
// must not log them, and must not expose SessionToken in JSON responses.
func (s *StaffAuthService) Login(ctx context.Context, email, password string) (StaffLoginResult, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || len(email) > 254 || password == "" || len(password) > 1024 {
		return StaffLoginResult{}, ErrInvalidStaffCredentials
	}

	credentials, err := s.store.GetCredentialsByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidInput) {
			return StaffLoginResult{}, ErrInvalidStaffCredentials
		}
		return StaffLoginResult{}, err
	}
	if credentials.AccountStatus != "active" || credentials.PasswordHash == "" {
		return StaffLoginResult{}, ErrInvalidStaffCredentials
	}

	valid, err := auth.VerifyPassword(password, credentials.PasswordHash)
	if err != nil || !valid {
		return StaffLoginResult{}, ErrInvalidStaffCredentials
	}

	sessionToken, sessionDigest, err := auth.NewSessionToken()
	if err != nil {
		return StaffLoginResult{}, err
	}
	csrfToken, csrfDigest, err := auth.NewSessionToken()
	if err != nil {
		return StaffLoginResult{}, err
	}

	expiresAt := s.now().Add(staffSessionTTL)
	session := domain.StaffSession{
		StaffID: credentials.ID, TokenHash: sessionDigest,
		CSRFTokenHash: csrfDigest, ExpiresAt: expiresAt,
	}
	if err := s.store.CreateSession(ctx, session); err != nil {
		return StaffLoginResult{}, err
	}

	return StaffLoginResult{
		StaffID: credentials.ID, DisplayName: credentials.DisplayName,
		Email: credentials.Email, SessionToken: sessionToken,
		CSRFToken: csrfToken, ExpiresAt: expiresAt,
	}, nil
}

// Logout revokes a server-side session by the hash of its opaque cookie token.
// Empty or implausibly long tokens are treated as an already-logged-out client.
func (s *StaffAuthService) Logout(ctx context.Context, sessionToken string) error {
	if sessionToken == "" || len(sessionToken) > 128 {
		return nil
	}
	return s.store.RevokeSessionByTokenHash(ctx, auth.HashToken(sessionToken))
}

// AuthenticateSession validates the opaque session token against the server-side
// session store and returns only the associated non-secret staff identity.
func (s *StaffAuthService) AuthenticateSession(ctx context.Context, sessionToken string) (domain.StaffSessionIdentity, error) {
	if len(sessionToken) != base64.RawURLEncoding.EncodedLen(32) {
		return domain.StaffSessionIdentity{}, ErrInvalidStaffSession
	}
	raw, err := base64.RawURLEncoding.DecodeString(sessionToken)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != sessionToken {
		return domain.StaffSessionIdentity{}, ErrInvalidStaffSession
	}

	identity, err := s.store.GetActiveSessionByTokenHash(ctx, auth.HashToken(sessionToken))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidInput) {
			return domain.StaffSessionIdentity{}, ErrInvalidStaffSession
		}
		return domain.StaffSessionIdentity{}, err
	}
	if identity.StaffID <= 0 || identity.Email == "" || identity.ExpiresAt.IsZero() || !identity.ExpiresAt.After(s.now()) {
		return domain.StaffSessionIdentity{}, ErrInvalidStaffSession
	}
	return identity, nil
}
