package service

import (
	"context"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type optimizedNavigationStudentStore struct {
	calls   int
	student domain.Student
}

func (s *optimizedNavigationStudentStore) EnsureByTelegramID(context.Context, domain.EnsureStudentParams) (domain.Student, error) {
	s.calls++
	return s.student, nil
}
func (s *optimizedNavigationStudentStore) GetByID(context.Context, int64) (domain.Student, error) {
	s.calls++
	return s.student, nil
}
func (s *optimizedNavigationStudentStore) GetByTelegramID(context.Context, int64) (domain.Student, error) {
	s.calls++
	return s.student, nil
}
func (s *optimizedNavigationStudentStore) UpdateProfile(context.Context, domain.UpdateStudentProfileParams) (domain.Student, error) {
	s.calls++
	return s.student, nil
}

type optimizedNavigationEnrollmentStore struct {
	progress           domain.Progress
	navigateCalls       int
	openPositionCalls   int
	getProgressCalls    int
	getByIDCalls        int
	setCurrentCalls     int
}

func (s *optimizedNavigationEnrollmentStore) Start(context.Context, domain.StartEnrollmentParams) (domain.Enrollment, error) {
	return s.progress.Enrollment, nil
}
func (s *optimizedNavigationEnrollmentStore) GetByIDForStudent(context.Context, int64, int64) (domain.Enrollment, error) {
	s.getByIDCalls++
	return s.progress.Enrollment, nil
}
func (s *optimizedNavigationEnrollmentStore) GetProgress(context.Context, int64, int64) (domain.Progress, error) {
	s.getProgressCalls++
	return s.progress, nil
}
func (s *optimizedNavigationEnrollmentStore) SetCurrentQuestion(_ context.Context, _, _, questionID int64) error {
	s.setCurrentCalls++
	s.progress.Enrollment.CurrentQuestionID = int64Pointer(questionID)
	return nil
}
func (s *optimizedNavigationEnrollmentStore) Complete(context.Context, int64, int64) (domain.Enrollment, error) {
	return s.progress.Enrollment, nil
}
func (s *optimizedNavigationEnrollmentStore) NavigateQuestion(
	_ context.Context, _, _ , expectedQuestionID int64, step int,
) (domain.Progress, bool, error) {
	s.navigateCalls++
	currentID := int64(0)
	if s.progress.Enrollment.CurrentQuestionID != nil {
		currentID = *s.progress.Enrollment.CurrentQuestionID
	}
	if expectedQuestionID > 0 && expectedQuestionID != currentID {
		return s.progress, true, nil
	}
	switch {
	case step == 1 && currentID == 101:
		s.progress.Enrollment.CurrentQuestionID = int64Pointer(102)
	case step == -1 && currentID == 102:
		s.progress.Enrollment.CurrentQuestionID = int64Pointer(101)
	}
	return s.progress, false, nil
}
func (s *optimizedNavigationEnrollmentStore) OpenQuestionPosition(
	_ context.Context, _, _ , questionID int64,
) (domain.Progress, error) {
	s.openPositionCalls++
	s.progress.Enrollment.CurrentQuestionID = int64Pointer(questionID)
	return s.progress, nil
}

func int64Pointer(value int64) *int64 { return &value }

type optimizedNavigationQuestionStore struct {
	questions []domain.LessonQuestion
	calls     int
}

func (s *optimizedNavigationQuestionStore) ListQuestionsForVersion(context.Context, int64) ([]domain.LessonQuestion, error) {
	s.calls++
	return s.questions, nil
}

type optimizedNavigationAnswerStore struct {
	combinedCalls int
	savedCalls    int
	historyCalls  int
}

func (s *optimizedNavigationAnswerStore) Submit(context.Context, domain.AnswerSubmission) (domain.SavedAnswer, error) {
	return domain.SavedAnswer{}, nil
}
func (s *optimizedNavigationAnswerStore) GetSavedAnswer(context.Context, int64, int64, int64) (domain.SavedAnswer, error) {
	s.savedCalls++
	return domain.SavedAnswer{}, domain.ErrNotFound
}
func (s *optimizedNavigationAnswerStore) ListAttemptHistory(context.Context, int64, int64, int64) ([]domain.AnswerAttempt, error) {
	s.historyCalls++
	return nil, nil
}
func (s *optimizedNavigationAnswerStore) GetNavigationAnswerState(
	context.Context, int64, int64, int64,
) (*domain.SavedAnswer, *domain.AnswerAttempt, error) {
	s.combinedCalls++
	return nil, nil, nil
}

func TestOptimizedQuestionNavigationUsesSinglePositionOperation(t *testing.T) {
	const studentID, enrollmentID, versionID = int64(5), int64(9), int64(14)
	firstID, secondID := int64(101), int64(102)
	students := &optimizedNavigationStudentStore{student: domain.Student{
		ID: studentID, AccountStatus: "active",
	}}
	enrollments := &optimizedNavigationEnrollmentStore{progress: domain.Progress{
		Enrollment: domain.Enrollment{
			ID: enrollmentID, StudentID: studentID, LessonID: 4,
			LessonVersionID: versionID, Mode: "ai", Status: "in_progress",
			CurrentQuestionID: &firstID, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		},
		TotalQuestions: 2, RemainingQuestions: 2, NextQuestionID: &secondID,
	}}
	questions := &optimizedNavigationQuestionStore{questions: []domain.LessonQuestion{
		{ID: firstID, LessonVersionID: versionID, QuestionNumber: 1, Prompt: "Первый", AnswerType: "text"},
		{ID: secondID, LessonVersionID: versionID, QuestionNumber: 2, Prompt: "Второй", AnswerType: "text"},
	}}
	answers := &optimizedNavigationAnswerStore{}
	learning := NewLearningService(
		students, navigationCatalogStore{lesson: domain.Lesson{ID: 4, LessonVersionID: versionID}},
		questions, enrollments, answers,
	)

	state, err := learning.NavigateQuestion(context.Background(), studentID, enrollmentID, firstID, 1)
	if err != nil {
		t.Fatalf("перейти к следующему вопросу: %v", err)
	}
	if state.Question.ID != secondID || state.Progress.Enrollment.CurrentQuestionID == nil ||
		*state.Progress.Enrollment.CurrentQuestionID != secondID {
		t.Fatalf("неожиданное состояние после перехода: %+v", state)
	}
	if enrollments.navigateCalls != 1 || enrollments.getProgressCalls != 0 ||
		enrollments.getByIDCalls != 0 || enrollments.setCurrentCalls != 0 {
		t.Fatalf("навигация сделала лишние вызовы enrollment store: %+v", enrollments)
	}
	if students.calls != 0 {
		t.Fatalf("оптимизированный путь повторно проверил ученика вне атомарной операции: %d", students.calls)
	}
	if questions.calls != 1 || answers.combinedCalls != 1 ||
		answers.savedCalls != 0 || answers.historyCalls != 0 {
		t.Fatalf("неожиданное число загрузок экрана: questions=%d answers=%+v", questions.calls, answers)
	}
}

func TestOptimizedQuestionNavigationPreservesStaleCallback(t *testing.T) {
	const studentID, enrollmentID, versionID = int64(5), int64(9), int64(14)
	currentID, staleID := int64(102), int64(101)
	enrollments := &optimizedNavigationEnrollmentStore{progress: domain.Progress{
		Enrollment: domain.Enrollment{
			ID: enrollmentID, StudentID: studentID, LessonVersionID: versionID,
			Status: "in_progress", CurrentQuestionID: &currentID,
		},
		TotalQuestions: 2,
	}}
	questions := &optimizedNavigationQuestionStore{questions: []domain.LessonQuestion{
		{ID: staleID, LessonVersionID: versionID, QuestionNumber: 1, Prompt: "Первый"},
		{ID: currentID, LessonVersionID: versionID, QuestionNumber: 2, Prompt: "Второй"},
	}}
	learning := NewLearningService(
		&optimizedNavigationStudentStore{student: domain.Student{ID: studentID, AccountStatus: "active"}},
		navigationCatalogStore{lesson: domain.Lesson{ID: 4, LessonVersionID: versionID}},
		questions, enrollments, &optimizedNavigationAnswerStore{},
	)

	state, err := learning.OpenCurrentQuestion(context.Background(), studentID, enrollmentID, staleID)
	if err != domain.ErrConflict || !state.StaleCallback || state.Question.ID != currentID {
		t.Fatalf("устаревший callback должен вернуть текущий вопрос и конфликт: state=%+v err=%v", state, err)
	}
	if enrollments.navigateCalls != 1 || *enrollments.progress.Enrollment.CurrentQuestionID != currentID {
		t.Fatalf("устаревший callback изменил позицию: %+v", enrollments)
	}
}
