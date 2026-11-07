package service

import (
	"context"
	"strings"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type StudentStore interface {
	EnsureByTelegramID(context.Context, domain.EnsureStudentParams) (domain.Student, error)
	GetByID(context.Context, int64) (domain.Student, error)
	GetByTelegramID(context.Context, int64) (domain.Student, error)
	UpdateProfile(context.Context, domain.UpdateStudentProfileParams) (domain.Student, error)
}

type StudentService struct {
	students StudentStore
}

func NewStudentService(students StudentStore) *StudentService {
	return &StudentService{students: students}
}

// EnsureByTelegramID безопасно повторяет регистрацию по стабильному Telegram ID.
func (s *StudentService) EnsureByTelegramID(ctx context.Context, params domain.EnsureStudentParams) (domain.Student, error) {
	params.DisplayName = strings.TrimSpace(params.DisplayName)
	if params.TelegramID <= 0 || params.DisplayName == "" {
		return domain.Student{}, domain.ErrInvalidInput
	}
	return s.students.EnsureByTelegramID(ctx, params)
}

type studentCuratorAssignmentEnsurer interface {
	EnsureCuratorAssigned(context.Context, int64) error
}

// EnsureCuratorAssigned preserves an existing curator assignment and balances
// only students who do not have an active assignment yet.
func (s *StudentService) EnsureCuratorAssigned(ctx context.Context, studentID int64) error {
	if s == nil || s.students == nil || studentID <= 0 {
		return domain.ErrInvalidInput
	}
	ensurer, ok := s.students.(studentCuratorAssignmentEnsurer)
	if !ok {
		return domain.ErrInvalidState
	}
	return ensurer.EnsureCuratorAssigned(ctx, studentID)
}

func (s *StudentService) GetByID(ctx context.Context, studentID int64) (domain.Student, error) {
	if studentID <= 0 {
		return domain.Student{}, domain.ErrInvalidInput
	}
	return s.students.GetByID(ctx, studentID)
}

func (s *StudentService) GetByTelegramID(ctx context.Context, telegramID int64) (domain.Student, error) {
	if telegramID <= 0 {
		return domain.Student{}, domain.ErrInvalidInput
	}
	return s.students.GetByTelegramID(ctx, telegramID)
}

func (s *StudentService) UpdateProfile(ctx context.Context, params domain.UpdateStudentProfileParams) (domain.Student, error) {
	params.DisplayName = strings.TrimSpace(params.DisplayName)
	if params.StudentID <= 0 || params.DisplayName == "" {
		return domain.Student{}, domain.ErrInvalidInput
	}
	return s.students.UpdateProfile(ctx, params)
}
