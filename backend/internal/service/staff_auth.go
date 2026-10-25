package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/auth"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

var ErrInvalidStaffCredentials = errors.New("invalid staff credentials")

const staffSessionTTL = 12 * time.Hour

type StaffAuthStore interface {
	GetCredentialsByEmail(context.Context, string) (domain.StaffCredentials, error)
	CreateSession(context.Context, domain.StaffSession) error
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
// The caller must send the raw tokens only through appropriately secured cookies,
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
