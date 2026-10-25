package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/auth"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type fakeStaffAuthStore struct {
	credentials domain.StaffCredentials
	getErr      error
	session     domain.StaffSession
	createErr   error
	created     bool
}

func (s *fakeStaffAuthStore) GetCredentialsByEmail(_ context.Context, _ string) (domain.StaffCredentials, error) {
	return s.credentials, s.getErr
}

func (s *fakeStaffAuthStore) CreateSession(_ context.Context, session domain.StaffSession) error {
	s.created = true
	s.session = session
	return s.createErr
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
