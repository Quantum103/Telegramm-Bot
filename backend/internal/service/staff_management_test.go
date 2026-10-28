package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/auth"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type staffManagementTestStore struct {
	createCalls  int
	actorID      int64
	displayName  string
	email        string
	passwordHash string
	roleCode     string
	createErr    error
}

func (s *staffManagementTestStore) ListStaffAccounts(context.Context, int64) ([]domain.StaffAccountSummary, error) {
	return []domain.StaffAccountSummary{}, nil
}
func (s *staffManagementTestStore) CreateStaffAccount(_ context.Context, actorID int64, displayName, email, passwordHash, roleCode string) (domain.StaffAccountSummary, error) {
	s.createCalls++
	s.actorID, s.displayName, s.email, s.passwordHash, s.roleCode = actorID, displayName, email, passwordHash, roleCode
	if s.createErr != nil {
		return domain.StaffAccountSummary{}, s.createErr
	}
	return domain.StaffAccountSummary{
		ID: 81, DisplayName: displayName, Email: email, AccountStatus: "active",
		Roles: []string{roleCode}, MustChangePassword: true,
	}, nil
}
func (s *staffManagementTestStore) ListStudentsForStaff(context.Context, int64) ([]domain.AdminStudent, error) {
	return []domain.AdminStudent{}, nil
}
func (s *staffManagementTestStore) AssignStudentCurator(context.Context, int64, int64, int64) error {
	return nil
}
func (s *staffManagementTestStore) EndStudentCuratorAssignment(context.Context, int64, int64) error {
	return nil
}

func (s *staffManagementTestStore) SetStaffAccountStatus(context.Context, int64, int64, string) error {
	return nil
}
func (s *staffManagementTestStore) ResetStaffPassword(context.Context, int64, int64, string) error {
	return nil
}

func TestCreateStaffAccountGeneratesTemporaryPasswordAndStoresOnlyHash(t *testing.T) {
	store := &staffManagementTestStore{}
	service := NewStaffManagementService(store)

	created, err := service.CreateStaffAccount(context.Background(), 9, "  New Curator  ", "  CURATOR@EXAMPLE.TEST ", "curator")
	if err != nil {
		t.Fatalf("CreateStaffAccount() error = %v", err)
	}
	if store.createCalls != 1 || store.actorID != 9 || store.displayName != "New Curator" ||
		store.email != "curator@example.test" || store.roleCode != "curator" {
		t.Fatalf("unexpected arguments passed to store: %+v", store)
	}
	if len(created.TemporaryPassword) < 32 || created.Account.ID != 81 ||
		!created.Account.MustChangePassword || len(created.Account.Roles) != 1 || created.Account.Roles[0] != "curator" {
		t.Fatalf("unexpected creation response: %+v", created)
	}
	if store.passwordHash == "" || store.passwordHash == created.TemporaryPassword {
		t.Fatal("store must receive a password hash, not the temporary password")
	}
	valid, err := auth.VerifyPassword(created.TemporaryPassword, store.passwordHash)
	if err != nil || !valid {
		t.Fatalf("temporary password does not match stored hash: valid=%v err=%v", valid, err)
	}
}

func TestCreateStaffAccountRejectsInvalidInputBeforeStore(t *testing.T) {
	tests := []struct {
		name, displayName, email, role string
		actorID                        int64
	}{
		{name: "missing actor", actorID: 0, displayName: "Curator", email: "a@example.test", role: "curator"},
		{name: "invalid email", actorID: 1, displayName: "Curator", email: "not an email", role: "curator"},
		{name: "unsupported role", actorID: 1, displayName: "Curator", email: "a@example.test", role: "owner"},
		{name: "empty name", actorID: 1, displayName: "  ", email: "a@example.test", role: "curator"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &staffManagementTestStore{}
			_, err := NewStaffManagementService(store).CreateStaffAccount(
				context.Background(), tt.actorID, tt.displayName, tt.email, tt.role,
			)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("CreateStaffAccount() error = %v, want ErrInvalidInput", err)
			}
			if store.createCalls != 0 {
				t.Fatal("invalid input unexpectedly reached the store")
			}
		})
	}
}
