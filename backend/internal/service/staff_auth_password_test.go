package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/auth"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type passwordChangeTestStore struct {
	credentials domain.StaffCredentials
	updatedHash string
	updateCalls int
	updateErr   error
}

func (s *passwordChangeTestStore) GetCredentialsByEmail(context.Context, string) (domain.StaffCredentials, error) {
	return s.credentials, nil
}
func (s *passwordChangeTestStore) CreateSession(context.Context, domain.StaffSession) error {
	return nil
}
func (s *passwordChangeTestStore) GetActiveSessionByTokenHash(context.Context, [32]byte) (domain.StaffSessionIdentity, error) {
	return domain.StaffSessionIdentity{}, nil
}
func (s *passwordChangeTestStore) RevokeSessionByTokenHash(context.Context, [32]byte) error {
	return nil
}
func (s *passwordChangeTestStore) GetCredentialsByID(_ context.Context, staffID int64) (domain.StaffCredentials, error) {
	if staffID != s.credentials.ID {
		return domain.StaffCredentials{}, domain.ErrNotFound
	}
	return s.credentials, nil
}
func (s *passwordChangeTestStore) UpdatePasswordAndRevokeSessions(_ context.Context, staffID int64, hash string) error {
	s.updateCalls++
	if staffID != s.credentials.ID {
		return domain.ErrNotFound
	}
	if s.updateErr != nil {
		return s.updateErr
	}
	s.updatedHash = hash
	s.credentials.PasswordHash = hash
	s.credentials.MustChangePassword = false
	return nil
}

func TestChangePasswordVerifiesCurrentPasswordAndUpdatesHash(t *testing.T) {
	const current = "temporary password for test"
	const next = "a strong replacement password"
	hash, err := auth.HashPassword(current)
	if err != nil {
		t.Fatal(err)
	}
	store := &passwordChangeTestStore{credentials: domain.StaffCredentials{
		ID: 71, Email: "staff@example.test", AccountStatus: "active",
		PasswordHash: hash, MustChangePassword: true,
	}}
	svc := NewStaffAuthService(store)

	if err := svc.ChangePassword(context.Background(), 71, "wrong password value", next); !errors.Is(err, ErrInvalidStaffCredentials) {
		t.Fatalf("wrong current password error = %v, want ErrInvalidStaffCredentials", err)
	}
	if store.updateCalls != 0 {
		t.Fatal("password was updated without verifying the current password")
	}
	if err := svc.ChangePassword(context.Background(), 71, current, next); err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}
	valid, err := auth.VerifyPassword(next, store.updatedHash)
	if err != nil || !valid {
		t.Fatalf("new password does not match stored hash: valid=%v err=%v", valid, err)
	}
	if store.updateCalls != 1 || store.credentials.MustChangePassword {
		t.Fatalf("password change was not persisted as expected: updates=%d must_change=%v", store.updateCalls, store.credentials.MustChangePassword)
	}
}

func TestChangePasswordRejectsWeakOrUnchangedPassword(t *testing.T) {
	hash, err := auth.HashPassword("valid current password")
	if err != nil {
		t.Fatal(err)
	}
	store := &passwordChangeTestStore{credentials: domain.StaffCredentials{
		ID: 4, Email: "staff@example.test", AccountStatus: "active", PasswordHash: hash,
	}}
	svc := NewStaffAuthService(store)
	for _, candidate := range []string{"short", "valid current password"} {
		if err := svc.ChangePassword(context.Background(), 4, "valid current password", candidate); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("ChangePassword(..., %q) error = %v, want ErrInvalidInput", candidate, err)
		}
	}
	if store.updateCalls != 0 {
		t.Fatal("invalid new password unexpectedly reached the store")
	}
}
