package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/auth"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type staffManagementTestStore struct {
	createCalls    int
	actorID        int64
	displayName    string
	email          string
	passwordHash   string
	roleCode       string
	createErr      error
	assignCalls    int
	assignErr      error
	endCalls       int
	endErr         error
	labelCalls     int
	labelActorID   int64
	labelStudentID int64
	label          string
	labelErr       error
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
	s.assignCalls++
	return s.assignErr
}
func (s *staffManagementTestStore) EndStudentCuratorAssignment(context.Context, int64, int64) error {
	s.endCalls++
	return s.endErr
}
func (s *staffManagementTestStore) SetStudentLabel(_ context.Context, actorID, studentID int64, label string) error {
	s.labelCalls++
	s.labelActorID, s.labelStudentID, s.label = actorID, studentID, label
	return s.labelErr
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

type conversationEventPublisherTestStub struct {
	calls int
}

func (s *conversationEventPublisherTestStub) PublishConversationChanged() {
	s.calls++
}

func TestStaffManagementPublishesConversationChangesAfterAssignmentMutations(t *testing.T) {
	store := &staffManagementTestStore{}
	publisher := &conversationEventPublisherTestStub{}
	management := NewStaffManagementService(store, publisher)

	if err := management.AssignCurator(context.Background(), 1, 2, 3); err != nil {
		t.Fatalf("AssignCurator() error = %v", err)
	}
	if store.assignCalls != 1 || publisher.calls != 1 {
		t.Fatalf("assignment should publish exactly once after persistence: store=%d events=%d", store.assignCalls, publisher.calls)
	}

	if err := management.EndCuratorAssignment(context.Background(), 1, 2); err != nil {
		t.Fatalf("EndCuratorAssignment() error = %v", err)
	}
	if store.endCalls != 1 || publisher.calls != 2 {
		t.Fatalf("ending assignment should publish exactly once: store=%d events=%d", store.endCalls, publisher.calls)
	}

	store.assignErr = errors.New("database unavailable")
	if err := management.AssignCurator(context.Background(), 1, 2, 4); err == nil {
		t.Fatal("expected assignment persistence error")
	}
	store.endErr = errors.New("database unavailable")
	if err := management.EndCuratorAssignment(context.Background(), 1, 2); err == nil {
		t.Fatal("expected unassignment persistence error")
	}
	if publisher.calls != 2 {
		t.Fatalf("failed assignment mutations must not publish events, got %d", publisher.calls)
	}
}

func TestSetStudentLabelTrimsAndPersistsInternalLabel(t *testing.T) {
	store := &staffManagementTestStore{}
	management := NewStaffManagementService(store)
	if err := management.SetStudentLabel(context.Background(), 7, 19, "  Ученик из Ростова  "); err != nil {
		t.Fatalf("SetStudentLabel() error = %v", err)
	}
	if store.labelCalls != 1 || store.labelActorID != 7 || store.labelStudentID != 19 || store.label != "Ученик из Ростова" {
		t.Fatalf("unexpected persisted label arguments: %+v", store)
	}
	if err := management.SetStudentLabel(context.Background(), 7, 19, "   "); err != nil {
		t.Fatalf("empty label should clear the override: %v", err)
	}
	if store.label != "" {
		t.Fatalf("blank label should be normalized to empty string, got %q", store.label)
	}
}

func TestSetStudentLabelRejectsInvalidInputBeforeStore(t *testing.T) {
	store := &staffManagementTestStore{}
	management := NewStaffManagementService(store)
	tests := []struct {
		name               string
		actorID, studentID int64
		label              string
	}{
		{name: "missing actor", actorID: 0, studentID: 1, label: "Имя"},
		{name: "missing student", actorID: 1, studentID: 0, label: "Имя"},
		{name: "too long", actorID: 1, studentID: 1, label: strings.Repeat("я", 121)},
		{name: "multiline", actorID: 1, studentID: 1, label: "Имя\nвторая строка"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := management.SetStudentLabel(context.Background(), tt.actorID, tt.studentID, tt.label); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("SetStudentLabel() error = %v, want ErrInvalidInput", err)
			}
		})
	}
	if store.labelCalls != 0 {
		t.Fatalf("invalid labels unexpectedly reached store %d times", store.labelCalls)
	}
}
