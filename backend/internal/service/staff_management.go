package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/mail"
	"strings"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/auth"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type StaffManagementStore interface {
	ListStaffAccounts(context.Context, int64) ([]domain.StaffAccountSummary, error)
	CreateStaffAccount(context.Context, int64, string, string, string, string) (domain.StaffAccountSummary, error)
	ListStudentsForStaff(context.Context, int64) ([]domain.AdminStudent, error)
	AssignStudentCurator(context.Context, int64, int64, int64) error
	EndStudentCuratorAssignment(context.Context, int64, int64) error
	SetStaffAccountStatus(context.Context, int64, int64, string) error
	ResetStaffPassword(context.Context, int64, int64, string) error
}

// StaffManagementService содержит операции управления сотрудниками и назначениями.
// Проверка ролей повторяется в репозитории и HTTP middleware: UI не является границей безопасности.
type StaffManagementService struct {
	store StaffManagementStore
}

func NewStaffManagementService(store StaffManagementStore) *StaffManagementService {
	return &StaffManagementService{store: store}
}

func (s *StaffManagementService) ListStaffAccounts(ctx context.Context, actorID int64) ([]domain.StaffAccountSummary, error) {
	if actorID <= 0 {
		return nil, domain.ErrInvalidInput
	}
	return s.store.ListStaffAccounts(ctx, actorID)
}

func (s *StaffManagementService) CreateStaffAccount(ctx context.Context, actorID int64, displayName, email, roleCode string) (domain.CreatedStaffAccount, error) {
	displayName = strings.TrimSpace(displayName)
	email = strings.ToLower(strings.TrimSpace(email))
	roleCode = strings.TrimSpace(roleCode)
	if actorID <= 0 || displayName == "" || len(displayName) > 200 ||
		!validStaffEmail(email) || (roleCode != "administrator" && roleCode != "curator") {
		return domain.CreatedStaffAccount{}, domain.ErrInvalidInput
	}

	temporaryPassword, err := newTemporaryStaffPassword()
	if err != nil {
		return domain.CreatedStaffAccount{}, err
	}
	passwordHash, err := auth.HashPassword(temporaryPassword)
	if err != nil {
		return domain.CreatedStaffAccount{}, err
	}
	account, err := s.store.CreateStaffAccount(ctx, actorID, displayName, email, passwordHash, roleCode)
	if err != nil {
		return domain.CreatedStaffAccount{}, err
	}
	return domain.CreatedStaffAccount{Account: account, TemporaryPassword: temporaryPassword}, nil
}

func (s *StaffManagementService) ListStudents(ctx context.Context, actorID int64) ([]domain.AdminStudent, error) {
	if actorID <= 0 {
		return nil, domain.ErrInvalidInput
	}
	return s.store.ListStudentsForStaff(ctx, actorID)
}

func (s *StaffManagementService) AssignCurator(ctx context.Context, actorID, studentID, curatorStaffID int64) error {
	if actorID <= 0 || studentID <= 0 || curatorStaffID <= 0 {
		return domain.ErrInvalidInput
	}
	return s.store.AssignStudentCurator(ctx, actorID, studentID, curatorStaffID)
}

func (s *StaffManagementService) SetAccountStatus(ctx context.Context, actorID, targetID int64, status string) error {
	if actorID <= 0 || targetID <= 0 || actorID == targetID || (status != "active" && status != "inactive") {
		return domain.ErrInvalidInput
	}
	return s.store.SetStaffAccountStatus(ctx, actorID, targetID, status)
}

// ResetPassword creates a new one-time credential. The store persists only its password hash.
func (s *StaffManagementService) ResetPassword(ctx context.Context, actorID, targetID int64) (string, error) {
	if actorID <= 0 || targetID <= 0 || actorID == targetID {
		return "", domain.ErrInvalidInput
	}
	temporaryPassword, err := newTemporaryStaffPassword()
	if err != nil {
		return "", err
	}
	passwordHash, err := auth.HashPassword(temporaryPassword)
	if err != nil {
		return "", err
	}
	if err := s.store.ResetStaffPassword(ctx, actorID, targetID, passwordHash); err != nil {
		return "", err
	}
	return temporaryPassword, nil
}

func (s *StaffManagementService) EndCuratorAssignment(ctx context.Context, actorID, studentID int64) error {
	if actorID <= 0 || studentID <= 0 {
		return domain.ErrInvalidInput
	}
	return s.store.EndStudentCuratorAssignment(ctx, actorID, studentID)
}

func validStaffEmail(value string) bool {
	if value == "" || len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value
}

func newTemporaryStaffPassword() (string, error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(randomBytes), nil
}
