package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type navigationStudentStore struct{ student domain.Student }

func (s navigationStudentStore) EnsureByTelegramID(context.Context, domain.EnsureStudentParams) (domain.Student, error) {
	return s.student, nil
}
func (s navigationStudentStore) GetByID(context.Context, int64) (domain.Student, error) {
	return s.student, nil
}
func (s navigationStudentStore) GetByTelegramID(context.Context, int64) (domain.Student, error) {
	return s.student, nil
}
func (s navigationStudentStore) UpdateProfile(context.Context, domain.UpdateStudentProfileParams) (domain.Student, error) {
	return s.student, nil
}

type navigationCatalogStore struct{ lesson domain.Lesson }

func (s navigationCatalogStore) ListPublishedCourses(context.Context) ([]domain.Course, error) {
	return []domain.Course{}, nil
}
func (s navigationCatalogStore) GetPublishedCourseByID(context.Context, int64) (domain.Course, error) {
	return domain.Course{}, nil
}
func (s navigationCatalogStore) ListPublishedLessonsByCourseID(context.Context, int64) ([]domain.Lesson, error) {
	return []domain.Lesson{}, nil
}
func (s navigationCatalogStore) GetPublishedLessonByID(context.Context, int64) (domain.Lesson, error) {
	return s.lesson, nil
}
func (s navigationCatalogStore) GetPublishedLessonInCourse(context.Context, int64, int64) (domain.Lesson, error) {
	return s.lesson, nil
}

type navigationQuestionStore struct{ questions []domain.LessonQuestion }

func (s navigationQuestionStore) ListQuestionsForVersion(context.Context, int64) ([]domain.LessonQuestion, error) {
	return s.questions, nil
}

type navigationEnrollmentStore struct {
	progress domain.Progress
}

func (s *navigationEnrollmentStore) Start(context.Context, domain.StartEnrollmentParams) (domain.Enrollment, error) {
	return s.progress.Enrollment, nil
}
func (s *navigationEnrollmentStore) GetByIDForStudent(_ context.Context, studentID, enrollmentID int64) (domain.Enrollment, error) {
	if s.progress.Enrollment.StudentID != studentID || s.progress.Enrollment.ID != enrollmentID {
		return domain.Enrollment{}, domain.ErrNotFound
	}
	return s.progress.Enrollment, nil
}
func (s *navigationEnrollmentStore) GetProgress(_ context.Context, studentID, enrollmentID int64) (domain.Progress, error) {
	if s.progress.Enrollment.StudentID != studentID || s.progress.Enrollment.ID != enrollmentID {
		return domain.Progress{}, domain.ErrNotFound
	}
	return s.progress, nil
}
func (s *navigationEnrollmentStore) SetCurrentQuestion(_ context.Context, studentID, enrollmentID, questionID int64) error {
	if s.progress.Enrollment.StudentID != studentID || s.progress.Enrollment.ID != enrollmentID {
		return domain.ErrNotFound
	}
	s.progress.Enrollment.CurrentQuestionID = &questionID
	s.recalculateNavigation()
	return nil
}
func (s *navigationEnrollmentStore) Complete(context.Context, int64, int64) (domain.Enrollment, error) {
	return s.progress.Enrollment, nil
}
func (s *navigationEnrollmentStore) recalculateNavigation() {
	s.progress.PreviousQuestionID = nil
	s.progress.NextQuestionID = nil
	if s.progress.Enrollment.CurrentQuestionID == nil {
		return
	}
	switch *s.progress.Enrollment.CurrentQuestionID {
	case 101:
		id := int64(102)
		s.progress.NextQuestionID = &id
	case 102:
		id := int64(101)
		s.progress.PreviousQuestionID = &id
	}
}

type navigationAnswerStore struct{}

func (navigationAnswerStore) Submit(context.Context, domain.AnswerSubmission) (domain.SavedAnswer, error) {
	return domain.SavedAnswer{}, nil
}
func (navigationAnswerStore) GetSavedAnswer(context.Context, int64, int64, int64) (domain.SavedAnswer, error) {
	return domain.SavedAnswer{}, domain.ErrNotFound
}
func (navigationAnswerStore) ListAttemptHistory(context.Context, int64, int64, int64) ([]domain.AnswerAttempt, error) {
	return []domain.AnswerAttempt{}, nil
}

func TestQuestionNavigationHonoursFirstAndLastBoundaries(t *testing.T) {
	studentID, enrollmentID, versionID := int64(5), int64(9), int64(14)
	questions := []domain.LessonQuestion{
		{ID: 101, LessonVersionID: versionID, QuestionNumber: 1, Prompt: "Первый вопрос", AnswerType: "text"},
		{ID: 102, LessonVersionID: versionID, QuestionNumber: 2, Prompt: "Последний вопрос", AnswerType: "text"},
	}
	studentStore := navigationStudentStore{student: domain.Student{
		ID: studentID, TelegramID: 7001, DisplayName: "Ученик", AccountStatus: "active",
	}}
	progress := domain.Progress{
		Enrollment: domain.Enrollment{
			ID: enrollmentID, StudentID: studentID, LessonID: 4,
			LessonVersionID: versionID, Mode: "ai", Status: "in_progress",
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		},
		TotalQuestions: 2, AnsweredQuestions: 0, RemainingQuestions: 2,
	}
	firstID, secondID := int64(101), int64(102)
	progress.FirstQuestionID = &firstID
	progress.Enrollment.CurrentQuestionID = &firstID
	progress.NextQuestionID = &secondID
	enrollmentStore := &navigationEnrollmentStore{progress: progress}
	learning := NewLearningService(
		studentStore,
		navigationCatalogStore{lesson: domain.Lesson{ID: 4, LessonVersionID: versionID}},
		navigationQuestionStore{questions: questions},
		enrollmentStore,
		navigationAnswerStore{},
	)

	previous, err := learning.PreviousQuestion(context.Background(), studentID, enrollmentID)
	if !errors.Is(err, domain.ErrNoPreviousQuestion) || previous.Question.ID != 0 {
		t.Fatalf("у первого вопроса должна быть явная граница previous, result=%+v err=%v", previous, err)
	}
	next, err := learning.NextQuestion(context.Background(), studentID, enrollmentID)
	if err != nil {
		t.Fatalf("перейти к следующему вопросу: %v", err)
	}
	if next.Question.ID != secondID || next.Progress.Enrollment.CurrentQuestionID == nil ||
		*next.Progress.Enrollment.CurrentQuestionID != secondID {
		t.Fatalf("навигация не сохранила позицию второго вопроса: %+v", next)
	}
	if _, err := learning.NextQuestion(context.Background(), studentID, enrollmentID); !errors.Is(err, domain.ErrNoNextQuestion) {
		t.Fatalf("у последнего вопроса должна быть явная граница next, получено %v", err)
	}
	back, err := learning.PreviousQuestion(context.Background(), studentID, enrollmentID)
	if err != nil {
		t.Fatalf("вернуться к предыдущему вопросу: %v", err)
	}
	if back.Question.ID != firstID {
		t.Fatalf("предыдущий вопрос = %d, хотели %d", back.Question.ID, firstID)
	}
}
