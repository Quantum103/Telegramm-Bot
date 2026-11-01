package service

import (
	"context"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

// CourseEnrollmentStore хранит выбор режима и агрегированный прогресс курса.
type CourseEnrollmentStore interface {
	Start(context.Context, int64, int64, string) (domain.CourseEnrollmentProgress, error)
	GetActiveForStudent(context.Context, int64) (domain.CourseEnrollmentProgress, error)
	GetForStudent(context.Context, int64, int64) (domain.CourseEnrollmentProgress, error)
}

type CourseEnrollmentService struct {
	store CourseEnrollmentStore
}

func NewCourseEnrollmentService(store CourseEnrollmentStore) *CourseEnrollmentService {
	return &CourseEnrollmentService{store: store}
}

// StartCourse не переключает режим существующего запуска. При конфликте возвращает
// текущий курс вместе с domain.ErrActiveCourse, чтобы клиент мог предложить продолжение.
func (s *CourseEnrollmentService) StartCourse(
	ctx context.Context,
	studentID, courseID int64,
	mode string,
) (domain.CourseEnrollmentProgress, error) {
	if studentID <= 0 || courseID <= 0 || (mode != "ai" && mode != "curator") {
		return domain.CourseEnrollmentProgress{}, domain.ErrInvalidInput
	}
	return s.store.Start(ctx, studentID, courseID, mode)
}

func (s *CourseEnrollmentService) GetActiveCourse(
	ctx context.Context,
	studentID int64,
) (domain.CourseEnrollmentProgress, error) {
	if studentID <= 0 {
		return domain.CourseEnrollmentProgress{}, domain.ErrInvalidInput
	}
	return s.store.GetActiveForStudent(ctx, studentID)
}

func (s *CourseEnrollmentService) GetCourseProgress(
	ctx context.Context,
	studentID, courseEnrollmentID int64,
) (domain.CourseEnrollmentProgress, error) {
	if studentID <= 0 || courseEnrollmentID <= 0 {
		return domain.CourseEnrollmentProgress{}, domain.ErrInvalidInput
	}
	return s.store.GetForStudent(ctx, studentID, courseEnrollmentID)
}
