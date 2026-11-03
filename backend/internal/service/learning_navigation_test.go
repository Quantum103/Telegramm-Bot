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

type navigationQuestionStore struct {
	questions []domain.LessonQuestion
	calls     int
}

func (s *navigationQuestionStore) ListQuestionsForVersion(context.Context, int64) ([]domain.LessonQuestion, error) {
	s.calls++
	return s.questions, nil
}

type navigationEnrollmentStore struct {
	progress       domain.Progress
	progressCalls  int
	getByIDCalls   int
	positionWrites int
}

func (s *navigationEnrollmentStore) Start(context.Context, domain.StartEnrollmentParams) (domain.Enrollment, error) {
	return s.progress.Enrollment, nil
}
func (s *navigationEnrollmentStore) GetByIDForStudent(_ context.Context, studentID, enrollmentID int64) (domain.Enrollment, error) {
	s.getByIDCalls++
	if s.progress.Enrollment.StudentID != studentID || s.progress.Enrollment.ID != enrollmentID {
		return domain.Enrollment{}, domain.ErrNotFound
	}
	return s.progress.Enrollment, nil
}
func (s *navigationEnrollmentStore) GetProgress(_ context.Context, studentID, enrollmentID int64) (domain.Progress, error) {
	s.progressCalls++
	if s.progress.Enrollment.StudentID != studentID || s.progress.Enrollment.ID != enrollmentID {
		return domain.Progress{}, domain.ErrNotFound
	}
	return s.progress, nil
}
func (s *navigationEnrollmentStore) SetCurrentQuestion(_ context.Context, studentID, enrollmentID, questionID int64) error {
	s.positionWrites++
	if s.progress.Enrollment.StudentID != studentID || s.progress.Enrollment.ID != enrollmentID {
		return domain.ErrNotFound
	}
	s.progress.Enrollment.CurrentQuestionID = &questionID
	s.recalculateNavigation()
	return nil
}
func (s *navigationEnrollmentStore) SetCurrentQuestionFrom(
	ctx context.Context,
	studentID, enrollmentID, expectedQuestionID, targetQuestionID int64,
) error {
	if s.progress.Enrollment.CurrentQuestionID == nil ||
		*s.progress.Enrollment.CurrentQuestionID != expectedQuestionID {
		return domain.ErrStaleNavigation
	}
	return s.SetCurrentQuestion(ctx, studentID, enrollmentID, targetQuestionID)
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

type navigationAnswerStore struct {
	savedCalls  int
	historyCalls int
}

func (*navigationAnswerStore) Submit(context.Context, domain.AnswerSubmission) (domain.SavedAnswer, error) {
	return domain.SavedAnswer{}, nil
}
func (s *navigationAnswerStore) GetSavedAnswer(context.Context, int64, int64, int64) (domain.SavedAnswer, error) {
	s.savedCalls++
	return domain.SavedAnswer{}, domain.ErrNotFound
}
func (s *navigationAnswerStore) ListAttemptHistory(context.Context, int64, int64, int64) ([]domain.AnswerAttempt, error) {
	s.historyCalls++
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
		&navigationQuestionStore{questions: questions},
		enrollmentStore,
		&navigationAnswerStore{},
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


// TestNavigateFromQuestionLoadsOneProgressAndOneQuestionSnapshot verifies the hot path
// does not re-read progress or reload the lesson through OpenQuestion.
func TestNavigateFromQuestionUsesOneProgressAndQuestionLoad(t *testing.T) {
	const studentID, enrollmentID, versionID = int64(5), int64(9), int64(14)
	firstID, secondID := int64(101), int64(102)
	questions := &navigationQuestionStore{questions: []domain.LessonQuestion{
		{ID: firstID, LessonVersionID: versionID, QuestionNumber: 1, Prompt: "Первый вопрос", AnswerType: "text"},
		{ID: secondID, LessonVersionID: versionID, QuestionNumber: 2, Prompt: "Второй вопрос", AnswerType: "text"},
	}}
	progress := domain.Progress{
		Enrollment: domain.Enrollment{
			ID: enrollmentID, StudentID: studentID, LessonID: 4,
			LessonVersionID: versionID, Mode: "ai", Status: "in_progress",
			CurrentQuestionID: &firstID,
		},
		TotalQuestions: 2,
	}
	enrollments := &navigationEnrollmentStore{progress: progress}
	answers := &navigationAnswerStore{}
	learning := NewLearningService(
		navigationStudentStore{student: domain.Student{ID: studentID, AccountStatus: "active"}},
		navigationCatalogStore{lesson: domain.Lesson{ID: 4, LessonVersionID: versionID}},
		questions,
		enrollments,
		answers,
	)

	state, err := learning.NavigateFromQuestion(context.Background(), studentID, enrollmentID, firstID, 1)
	if err != nil {
		t.Fatalf("navigate from expected question: %v", err)
	}
	if state.Question.ID != secondID || state.Progress.Enrollment.CurrentQuestionID == nil ||
		*state.Progress.Enrollment.CurrentQuestionID != secondID {
		t.Fatalf("navigation returned wrong question or position: %+v", state)
	}
	if enrollments.progressCalls != 1 || enrollments.getByIDCalls != 0 || enrollments.positionWrites != 1 {
		t.Fatalf("enrollment calls: progress=%d getByID=%d positionWrites=%d; want 1, 0, 1",
			enrollments.progressCalls, enrollments.getByIDCalls, enrollments.positionWrites)
	}
	if questions.calls != 1 {
		t.Fatalf("question snapshot loads = %d, want 1", questions.calls)
	}
	if answers.savedCalls != 1 || answers.historyCalls != 1 {
		t.Fatalf("answer reads: saved=%d history=%d; want 1 each", answers.savedCalls, answers.historyCalls)
	}
}

func TestNavigateFromQuestionReturnsCurrentStateForStaleCallbackWithoutWriting(t *testing.T) {
	const studentID, enrollmentID, versionID = int64(5), int64(9), int64(14)
	firstID, secondID := int64(101), int64(102)
	questions := &navigationQuestionStore{questions: []domain.LessonQuestion{
		{ID: firstID, LessonVersionID: versionID, QuestionNumber: 1, Prompt: "Первый вопрос", AnswerType: "text"},
		{ID: secondID, LessonVersionID: versionID, QuestionNumber: 2, Prompt: "Второй вопрос", AnswerType: "text"},
	}}
	progress := domain.Progress{
		Enrollment: domain.Enrollment{
			ID: enrollmentID, StudentID: studentID, LessonID: 4,
			LessonVersionID: versionID, Mode: "ai", Status: "in_progress",
			CurrentQuestionID: &secondID,
		},
		TotalQuestions: 2,
	}
	enrollments := &navigationEnrollmentStore{progress: progress}
	learning := NewLearningService(
		navigationStudentStore{student: domain.Student{ID: studentID, AccountStatus: "active"}},
		navigationCatalogStore{lesson: domain.Lesson{ID: 4, LessonVersionID: versionID}},
		questions,
		enrollments,
		&navigationAnswerStore{},
	)

	state, err := learning.NavigateFromQuestion(context.Background(), studentID, enrollmentID, firstID, 1)
	if !errors.Is(err, domain.ErrStaleNavigation) {
		t.Fatalf("stale callback error = %v, want ErrStaleNavigation", err)
	}
	if state.Question.ID != secondID {
		t.Fatalf("stale callback did not return current question: %+v", state)
	}
	if enrollments.positionWrites != 0 {
		t.Fatalf("stale callback wrote position %d times, want 0", enrollments.positionWrites)
	}
	if enrollments.progressCalls != 1 || questions.calls != 1 {
		t.Fatalf("stale callback repeated loads: progress=%d questions=%d", enrollments.progressCalls, questions.calls)
	}
}
