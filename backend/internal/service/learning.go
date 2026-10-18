package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type EnrollmentStore interface {
	Start(context.Context, domain.StartEnrollmentParams) (domain.Enrollment, error)
	GetByIDForStudent(context.Context, int64, int64) (domain.Enrollment, error)
	GetProgress(context.Context, int64, int64) (domain.Progress, error)
	SetCurrentQuestion(context.Context, int64, int64, int64) error
	Complete(context.Context, int64, int64) (domain.Enrollment, error)
}

type AnswerStore interface {
	Submit(context.Context, domain.AnswerSubmission) (domain.SavedAnswer, error)
	GetSavedAnswer(context.Context, int64, int64, int64) (domain.SavedAnswer, error)
	ListAttemptHistory(context.Context, int64, int64, int64) ([]domain.AnswerAttempt, error)
}

type LearningService struct {
	students    StudentStore
	courses     CatalogStore
	questions   QuestionStore
	enrollments EnrollmentStore
	answers     AnswerStore
}

func NewLearningService(
	students StudentStore,
	courses CatalogStore,
	questions QuestionStore,
	enrollments EnrollmentStore,
	answers AnswerStore,
) *LearningService {
	return &LearningService{
		students: students, courses: courses, questions: questions,
		enrollments: enrollments, answers: answers,
	}
}

type NavigationState struct {
	Question      domain.LessonQuestion
	SavedAnswer   *domain.SavedAnswer
	LatestAttempt *domain.AnswerAttempt
	Progress      domain.Progress
}

type SubmissionResult struct {
	Answer   domain.SavedAnswer
	Progress domain.Progress
}

// StartLesson создаёт или восстанавливает прохождение по текущей опубликованной версии.
func (s *LearningService) StartLesson(ctx context.Context, studentID, lessonID int64, mode string) (domain.Progress, error) {
	if studentID <= 0 || lessonID <= 0 || (mode != "ai" && mode != "curator") {
		return domain.Progress{}, domain.ErrInvalidInput
	}
	student, err := s.students.GetByID(ctx, studentID)
	if err != nil {
		return domain.Progress{}, err
	}
	if student.AccountStatus != "active" {
		return domain.Progress{}, domain.ErrInvalidState
	}
	lesson, err := s.courses.GetPublishedLessonByID(ctx, lessonID)
	if err != nil {
		return domain.Progress{}, err
	}
	enrollment, err := s.enrollments.Start(ctx, domain.StartEnrollmentParams{
		StudentID: studentID, LessonID: lesson.ID,
		LessonVersionID: lesson.LessonVersionID, Mode: mode,
	})
	if err != nil {
		return domain.Progress{}, err
	}
	if enrollment.Status == "cancelled" {
		return domain.Progress{}, domain.ErrInvalidState
	}

	progress, err := s.enrollments.GetProgress(ctx, studentID, enrollment.ID)
	if err != nil {
		return domain.Progress{}, err
	}
	if progress.Enrollment.CurrentQuestionID == nil && progress.FirstQuestionID != nil {
		if err := s.enrollments.SetCurrentQuestion(ctx, studentID, progress.Enrollment.ID, *progress.FirstQuestionID); err != nil {
			return domain.Progress{}, err
		}
		progress, err = s.enrollments.GetProgress(ctx, studentID, progress.Enrollment.ID)
		if err != nil {
			return domain.Progress{}, err
		}
	}
	return progress, nil
}

// CurrentQuestion восстанавливает активный вопрос ученика из PostgreSQL, не полагаясь на память процесса.
func (s *LearningService) CurrentQuestion(ctx context.Context, studentID int64) (NavigationState, error) {
	if err := s.requireActiveStudent(ctx, studentID); err != nil {
		return NavigationState{}, err
	}
	store, ok := s.enrollments.(interface {
		GetCurrentForStudent(context.Context, int64) (domain.Enrollment, error)
	})
	if !ok {
		return NavigationState{}, domain.ErrInvalidState
	}
	enrollment, err := store.GetCurrentForStudent(ctx, studentID)
	if err != nil {
		return NavigationState{}, err
	}
	if enrollment.Status != "in_progress" || enrollment.CurrentQuestionID == nil {
		return NavigationState{}, domain.ErrNotFound
	}
	return s.OpenQuestion(ctx, studentID, enrollment.ID, *enrollment.CurrentQuestionID)
}

// GetProgress восстанавливает состояние прохождения исключительно из PostgreSQL.
func (s *LearningService) GetProgress(ctx context.Context, studentID, enrollmentID int64) (domain.Progress, error) {
	if err := s.requireActiveStudent(ctx, studentID); err != nil {
		return domain.Progress{}, err
	}
	return s.enrollments.GetProgress(ctx, studentID, enrollmentID)
}

func (s *LearningService) FirstQuestion(ctx context.Context, studentID, enrollmentID int64) (NavigationState, error) {
	if err := s.requireActiveStudent(ctx, studentID); err != nil {
		return NavigationState{}, err
	}
	progress, err := s.enrollments.GetProgress(ctx, studentID, enrollmentID)
	if err != nil {
		return NavigationState{}, err
	}
	if progress.FirstQuestionID == nil {
		return NavigationState{}, domain.ErrInvalidState
	}
	return s.OpenQuestion(ctx, studentID, enrollmentID, *progress.FirstQuestionID)
}

func (s *LearningService) OpenQuestion(ctx context.Context, studentID, enrollmentID, questionID int64) (NavigationState, error) {
	if studentID <= 0 || enrollmentID <= 0 || questionID <= 0 {
		return NavigationState{}, domain.ErrInvalidInput
	}
	if err := s.requireActiveStudent(ctx, studentID); err != nil {
		return NavigationState{}, err
	}
	enrollment, err := s.enrollments.GetByIDForStudent(ctx, studentID, enrollmentID)
	if err != nil {
		return NavigationState{}, err
	}
	questions, err := s.questions.ListQuestionsForVersion(ctx, enrollment.LessonVersionID)
	if err != nil {
		return NavigationState{}, err
	}
	var selected *domain.LessonQuestion
	for index := range questions {
		if questions[index].ID == questionID {
			selected = &questions[index]
			break
		}
	}
	if selected == nil {
		return NavigationState{}, domain.ErrNotFound
	}
	if err := s.enrollments.SetCurrentQuestion(ctx, studentID, enrollmentID, questionID); err != nil {
		return NavigationState{}, err
	}
	progress, err := s.enrollments.GetProgress(ctx, studentID, enrollmentID)
	if err != nil {
		return NavigationState{}, err
	}
	return s.loadNavigationState(ctx, studentID, enrollmentID, *selected, progress)
}

func (s *LearningService) NextQuestion(ctx context.Context, studentID, enrollmentID int64) (NavigationState, error) {
	return s.navigate(ctx, studentID, enrollmentID, 1)
}

func (s *LearningService) PreviousQuestion(ctx context.Context, studentID, enrollmentID int64) (NavigationState, error) {
	return s.navigate(ctx, studentID, enrollmentID, -1)
}

func (s *LearningService) navigate(ctx context.Context, studentID, enrollmentID int64, step int) (NavigationState, error) {
	if err := s.requireActiveStudent(ctx, studentID); err != nil {
		return NavigationState{}, err
	}
	progress, err := s.enrollments.GetProgress(ctx, studentID, enrollmentID)
	if err != nil {
		return NavigationState{}, err
	}
	questions, err := s.questions.ListQuestionsForVersion(ctx, progress.Enrollment.LessonVersionID)
	if err != nil {
		return NavigationState{}, err
	}
	if len(questions) == 0 {
		return NavigationState{}, domain.ErrInvalidState
	}

	target := 0
	if progress.Enrollment.CurrentQuestionID == nil {
		if step < 0 {
			return NavigationState{}, domain.ErrNoPreviousQuestion
		}
	} else {
		currentIndex := -1
		for index := range questions {
			if questions[index].ID == *progress.Enrollment.CurrentQuestionID {
				currentIndex = index
				break
			}
		}
		if currentIndex < 0 {
			return NavigationState{}, domain.ErrNotFound
		}
		target = currentIndex + step
		if target < 0 {
			return NavigationState{}, domain.ErrNoPreviousQuestion
		}
		if target >= len(questions) {
			return NavigationState{}, domain.ErrNoNextQuestion
		}
	}
	return s.OpenQuestion(ctx, studentID, enrollmentID, questions[target].ID)
}

func (s *LearningService) loadNavigationState(
	ctx context.Context,
	studentID, enrollmentID int64,
	question domain.LessonQuestion,
	progress domain.Progress,
) (NavigationState, error) {
	state := NavigationState{Question: question, Progress: progress}
	answer, err := s.answers.GetSavedAnswer(ctx, studentID, enrollmentID, question.ID)
	if err == nil {
		state.SavedAnswer = &answer
	} else if !errors.Is(err, domain.ErrNotFound) {
		return NavigationState{}, err
	}
	history, err := s.answers.ListAttemptHistory(ctx, studentID, enrollmentID, question.ID)
	if err != nil {
		return NavigationState{}, err
	}
	if len(history) > 0 {
		state.LatestAttempt = &history[0]
	}
	return state, nil
}

// SubmitAnswer сохраняет ответ и непроверенную попытку. Здесь намеренно нет эвристической оценки.
func (s *LearningService) SubmitAnswer(ctx context.Context, params domain.AnswerSubmission) (SubmissionResult, error) {
	if params.StudentID <= 0 || params.EnrollmentID <= 0 || params.QuestionID <= 0 {
		return SubmissionResult{}, domain.ErrInvalidInput
	}
	student, err := s.students.GetByID(ctx, params.StudentID)
	if err != nil {
		return SubmissionResult{}, err
	}
	if student.AccountStatus != "active" {
		return SubmissionResult{}, domain.ErrInvalidState
	}
	enrollment, err := s.enrollments.GetByIDForStudent(ctx, params.StudentID, params.EnrollmentID)
	if err != nil {
		return SubmissionResult{}, err
	}
	questions, err := s.questions.ListQuestionsForVersion(ctx, enrollment.LessonVersionID)
	if err != nil {
		return SubmissionResult{}, err
	}
	var question *domain.LessonQuestion
	for index := range questions {
		if questions[index].ID == params.QuestionID {
			question = &questions[index]
			break
		}
	}
	if question == nil {
		return SubmissionResult{}, domain.ErrNotFound
	}
	if err := validateSubmission(*question, params); err != nil {
		return SubmissionResult{}, err
	}
	answer, err := s.answers.Submit(ctx, params)
	if err != nil {
		return SubmissionResult{}, err
	}
	progress, err := s.enrollments.GetProgress(ctx, params.StudentID, params.EnrollmentID)
	if err != nil {
		return SubmissionResult{}, err
	}
	return SubmissionResult{Answer: answer, Progress: progress}, nil
}

func validateSubmission(question domain.LessonQuestion, params domain.AnswerSubmission) error {
	switch question.AnswerType {
	case "text":
		if strings.TrimSpace(params.AnswerText) == "" || len(params.SelectedOptionIDs) != 0 {
			return fmt.Errorf("%w: для вопроса со свободным текстом требуется текстовый ответ", domain.ErrInvalidInput)
		}
	case "single_choice":
		if strings.TrimSpace(params.AnswerText) != "" || len(params.SelectedOptionIDs) != 1 {
			return fmt.Errorf("%w: выберите ровно один вариант", domain.ErrInvalidInput)
		}
	case "multiple_choice":
		if strings.TrimSpace(params.AnswerText) != "" || len(params.SelectedOptionIDs) == 0 {
			return fmt.Errorf("%w: требуется выбрать один или несколько вариантов", domain.ErrInvalidInput)
		}
	default:
		return domain.ErrInvalidState
	}
	optionIDs := make(map[int64]struct{}, len(params.SelectedOptionIDs))
	for _, id := range params.SelectedOptionIDs {
		if id <= 0 {
			return domain.ErrInvalidInput
		}
		if _, exists := optionIDs[id]; exists {
			return fmt.Errorf("%w: варианты ответа не должны повторяться", domain.ErrInvalidInput)
		}
		optionIDs[id] = struct{}{}
	}
	if len(optionIDs) > 0 {
		allowed := make(map[int64]struct{}, len(question.Options))
		for _, option := range question.Options {
			allowed[option.ID] = struct{}{}
		}
		for _, id := range params.SelectedOptionIDs {
			if _, exists := allowed[id]; !exists {
				return fmt.Errorf("%w: вариант не принадлежит вопросу", domain.ErrInvalidInput)
			}
		}
	}
	return nil
}

func (s *LearningService) CompleteLesson(ctx context.Context, studentID, enrollmentID int64) (domain.Enrollment, error) {
	if err := s.requireActiveStudent(ctx, studentID); err != nil {
		return domain.Enrollment{}, err
	}
	return s.enrollments.Complete(ctx, studentID, enrollmentID)
}

func (s *LearningService) requireActiveStudent(ctx context.Context, studentID int64) error {
	if studentID <= 0 {
		return domain.ErrInvalidInput
	}
	student, err := s.students.GetByID(ctx, studentID)
	if err != nil {
		return err
	}
	if student.AccountStatus != "active" {
		return domain.ErrInvalidState
	}
	return nil
}
