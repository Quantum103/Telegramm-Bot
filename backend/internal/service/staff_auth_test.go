package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/auth"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type fakeStaffAuthStore struct {
	credentials    domain.StaffCredentials
	getErr         error
	session        domain.StaffSession
	createErr      error
	lookupIdentity domain.StaffSessionIdentity
	lookupErr      error
	lookupHash     [32]byte
	lookupCalled   bool
	revokeErr      error
	revokedHash    [32]byte
	revoked        bool
	created        bool
}

func (s *fakeStaffAuthStore) GetCredentialsByEmail(_ context.Context, _ string) (domain.StaffCredentials, error) {
	return s.credentials, s.getErr
}

func (s *fakeStaffAuthStore) CreateSession(_ context.Context, session domain.StaffSession) error {
	s.created = true
	s.session = session
	return s.createErr
}

func (s *fakeStaffAuthStore) GetActiveSessionByTokenHash(_ context.Context, tokenHash [32]byte) (domain.StaffSessionIdentity, error) {
	s.lookupCalled = true
	s.lookupHash = tokenHash
	return s.lookupIdentity, s.lookupErr
}

func (s *fakeStaffAuthStore) RevokeSessionByTokenHash(_ context.Context, tokenHash [32]byte) error {
	s.revoked = true
	s.revokedHash = tokenHash
	return s.revokeErr
}

func TestStaffLoginCreatesHashedSessionForActiveAccount(t *testing.T) {
	password := "correct horse battery staple"
	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStaffAuthStore{credentials: domain.StaffCredentials{
		ID: 42, DisplayName: "Admin", Email: "admin@example.com",
		PasswordHash: passwordHash, AccountStatus: "active",
	}}
	svc := NewStaffAuthService(store)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	svc.now = func() time.Time { return now }

	result, err := svc.Login(context.Background(), " ADMIN@example.com ", password)
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if !store.created {
		t.Fatal("Login() did not persist a session")
	}
	if result.StaffID != 42 || result.SessionToken == "" || result.CSRFToken == "" {
		t.Fatalf("Login() returned incomplete result: %+v", result)
	}
	if result.ExpiresAt != now.Add(12*time.Hour) || !store.session.ExpiresAt.Equal(result.ExpiresAt) {
		t.Fatal("session expiration is not 12 hours from current time")
	}
	if store.session.TokenHash != auth.HashToken(result.SessionToken) {
		t.Fatal("stored session token digest does not match issued token")
	}
	if store.session.CSRFTokenHash != auth.HashToken(result.CSRFToken) {
		t.Fatal("stored CSRF digest does not match issued token")
	}
}

func TestStaffLoginRejectsInvalidCredentialsWithoutCreatingSession(t *testing.T) {
	passwordHash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		store    *fakeStaffAuthStore
		email    string
		password string
	}{
		{name: "unknown account", store: &fakeStaffAuthStore{getErr: domain.ErrNotFound}, email: "unknown@example.com", password: "correct horse battery staple"},
		{name: "wrong password", store: &fakeStaffAuthStore{credentials: domain.StaffCredentials{ID: 1, Email: "a@example.com", PasswordHash: passwordHash, AccountStatus: "active"}}, email: "a@example.com", password: "wrong password"},
		{name: "inactive account", store: &fakeStaffAuthStore{credentials: domain.StaffCredentials{ID: 1, Email: "a@example.com", PasswordHash: passwordHash, AccountStatus: "inactive"}}, email: "a@example.com", password: "correct horse battery staple"},
		{name: "locked account", store: &fakeStaffAuthStore{credentials: domain.StaffCredentials{ID: 1, Email: "a@example.com", PasswordHash: passwordHash, AccountStatus: "locked"}}, email: "a@example.com", password: "correct horse battery staple"},
		{name: "missing password hash", store: &fakeStaffAuthStore{credentials: domain.StaffCredentials{ID: 1, Email: "a@example.com", AccountStatus: "active"}}, email: "a@example.com", password: "correct horse battery staple"},
		{name: "invalid email", store: &fakeStaffAuthStore{}, email: " ", password: "correct horse battery staple"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewStaffAuthService(tt.store).Login(context.Background(), tt.email, tt.password)
			if !errors.Is(err, ErrInvalidStaffCredentials) {
				t.Fatalf("Login() error = %v, want ErrInvalidStaffCredentials", err)
			}
			if tt.store.created {
				t.Fatal("invalid login unexpectedly created a session")
			}
		})
	}
}

func TestStaffLoginReturnsSessionPersistenceError(t *testing.T) {
	passwordHash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStaffAuthStore{
		credentials: domain.StaffCredentials{ID: 1, Email: "a@example.com", PasswordHash: passwordHash, AccountStatus: "active"},
		createErr:   errors.New("database unavailable"),
	}
	_, err = NewStaffAuthService(store).Login(context.Background(), "a@example.com", "correct horse battery staple")
	if !errors.Is(err, store.createErr) {
		t.Fatalf("Login() error = %v, want persistence error", err)
	}
}

func TestStaffLogoutRevokesSessionUsingTokenDigest(t *testing.T) {
	store := &fakeStaffAuthStore{}
	svc := NewStaffAuthService(store)
	token := "opaque-session-token"
	if err := svc.Logout(context.Background(), token); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if !store.revoked {
		t.Fatal("Logout() did not request session revocation")
	}
	if store.revokedHash != auth.HashToken(token) {
		t.Fatal("Logout() did not hash the raw token before persistence")
	}
}

func TestStaffLogoutWithoutTokenIsIdempotent(t *testing.T) {
	store := &fakeStaffAuthStore{}
	if err := NewStaffAuthService(store).Logout(context.Background(), ""); err != nil {
		t.Fatalf("Logout(empty token) error = %v, want nil", err)
	}
	if store.revoked {
		t.Fatal("Logout(empty token) unexpectedly queried the session store")
	}
}

func TestStaffLogoutPropagatesPersistenceError(t *testing.T) {
	store := &fakeStaffAuthStore{revokeErr: errors.New("database unavailable")}
	err := NewStaffAuthService(store).Logout(context.Background(), "opaque-session-token")
	if !errors.Is(err, store.revokeErr) {
		t.Fatalf("Logout() error = %v, want persistence error", err)
	}
}

func TestAuthenticateSessionLooksUpTokenDigest(t *testing.T) {
	token := strings.Repeat("A", 43)
	identity := domain.StaffSessionIdentity{
		StaffID: 19, DisplayName: "Curator", Email: "curator@example.test",
		ExpiresAt: time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC),
	}
	store := &fakeStaffAuthStore{lookupIdentity: identity}
	svc := NewStaffAuthService(store)
	svc.now = func() time.Time { return time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC) }

	got, err := svc.AuthenticateSession(context.Background(), token)
	if err != nil {
		t.Fatalf("AuthenticateSession() error = %v", err)
	}
	if got.StaffID != identity.StaffID || got.Email != identity.Email {
		t.Fatalf("identity = %+v, want staff ID %d and email %q", got, identity.StaffID, identity.Email)
	}
	if !store.lookupCalled || store.lookupHash != auth.HashToken(token) {
		t.Fatal("AuthenticateSession() did not look up the session by token digest")
	}
}

func TestAuthenticateSessionRejectsMalformedExpiredAndUnknownSessions(t *testing.T) {
	validShape := strings.Repeat("B", 43)
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		token string
		store *fakeStaffAuthStore
	}{
		{name: "empty token", token: "", store: &fakeStaffAuthStore{}},
		{name: "short token", token: "short", store: &fakeStaffAuthStore{}},
		{name: "invalid base64url", token: strings.Repeat("!", 43), store: &fakeStaffAuthStore{}},
		{name: "unknown session", token: validShape, store: &fakeStaffAuthStore{lookupErr: domain.ErrNotFound}},
		{name: "expired identity", token: validShape, store: &fakeStaffAuthStore{lookupIdentity: domain.StaffSessionIdentity{
			StaffID: 1, Email: "staff@example.test", ExpiresAt: now,
		}}},
		{name: "missing identity", token: validShape, store: &fakeStaffAuthStore{lookupIdentity: domain.StaffSessionIdentity{
			Email: "staff@example.test", ExpiresAt: now.Add(time.Hour),
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewStaffAuthService(tt.store)
			svc.now = func() time.Time { return now }
			if _, err := svc.AuthenticateSession(context.Background(), tt.token); !errors.Is(err, ErrInvalidStaffSession) {
				t.Fatalf("AuthenticateSession() error = %v, want ErrInvalidStaffSession", err)
			}
		})
	}
}
