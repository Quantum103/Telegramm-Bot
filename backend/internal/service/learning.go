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

type studentCabinetStore interface {
	GetStudentCabinet(context.Context, int64) (domain.StudentCabinet, error)
}

type CourseRunLearningStore interface {
	Start(context.Context, int64, int64, string) (domain.CourseEnrollmentProgress, error)
	GetActiveForStudent(context.Context, int64) (domain.CourseEnrollmentProgress, error)
	GetForStudent(context.Context, int64, int64) (domain.CourseEnrollmentProgress, error)
	GetLessonForRun(context.Context, int64, int64, int64) (domain.Lesson, error)
	ListLessons(context.Context, int64, int64) ([]domain.CourseEnrollmentLesson, error)
}

type LearningService struct {
	students          StudentStore
	courses           CatalogStore
	questions         QuestionStore
	enrollments       EnrollmentStore
	answers           AnswerStore
	courseEnrollments CourseRunLearningStore
}

func NewLearningService(
	students StudentStore,
	courses CatalogStore,
	questions QuestionStore,
	enrollments EnrollmentStore,
	answers AnswerStore,
	courseRuns ...CourseRunLearningStore,
) *LearningService {
	service := &LearningService{
		students: students, courses: courses, questions: questions,
		enrollments: enrollments, answers: answers,
	}
	if len(courseRuns) > 0 {
		service.courseEnrollments = courseRuns[0]
	}
	return service
}

type NavigationState struct {
	Question      domain.LessonQuestion
	SavedAnswer   *domain.SavedAnswer
	LatestAttempt *domain.AnswerAttempt
	Progress      domain.Progress
	StaleCallback bool
}

// LessonQuestionsState contains every question and its persisted answer/review state.
// Loading this view does not change the learner's current question position.
type LessonQuestionsState struct {
	Progress  domain.Progress
	Questions []NavigationState
}

type questionPositionStore interface {
	NavigateQuestion(context.Context, int64, int64, int64, int) (domain.Progress, bool, error)
	OpenQuestionPosition(context.Context, int64, int64, int64) (domain.Progress, error)
}

type navigationAnswerStateStore interface {
	GetNavigationAnswerState(context.Context, int64, int64, int64) (*domain.SavedAnswer, *domain.AnswerAttempt, error)
}

type SubmissionResult struct {
	Answer   domain.SavedAnswer
	Progress domain.Progress
}

// StartLesson сохраняет совместимость со старыми вызовами. В production-режиме
// урок всегда открывается внутри отдельного запуска курса с сохранённым режимом.
func (s *LearningService) StartLesson(ctx context.Context, studentID, lessonID int64, mode string) (domain.Progress, error) {
	if studentID <= 0 || lessonID <= 0 || (mode != "ai" && mode != "curator") {
		return domain.Progress{}, domain.ErrInvalidInput
	}
	lesson, err := s.courses.GetPublishedLessonByID(ctx, lessonID)
	if err != nil {
		return domain.Progress{}, err
	}
	if s.courseEnrollments != nil {
		return s.StartLessonInCourse(ctx, studentID, lesson.CourseID, lessonID, mode)
	}
	if err := s.requireActiveStudent(ctx, studentID); err != nil {
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
	return s.loadInitialProgress(ctx, studentID, enrollment.ID)
}

// StartLessonInCourse проверяет принадлежность урока конкретному курсу и использует
// только режим, сохранённый в course_enrollments.
func (s *LearningService) StartLessonInCourse(
	ctx context.Context,
	studentID, courseID, lessonID int64,
	mode string,
) (domain.Progress, error) {
	if studentID <= 0 || courseID <= 0 || lessonID <= 0 ||
		(mode != "ai" && mode != "curator") {
		return domain.Progress{}, domain.ErrInvalidInput
	}
	if err := s.requireActiveStudent(ctx, studentID); err != nil {
		return domain.Progress{}, err
	}
	if s.courseEnrollments == nil {
		return domain.Progress{}, domain.ErrInvalidState
	}
	lesson, err := s.courses.GetPublishedLessonInCourse(ctx, courseID, lessonID)
	if err != nil {
		return domain.Progress{}, err
	}
	run, err := s.StartCourse(ctx, studentID, courseID, mode)
	if err != nil {
		return domain.Progress{}, err
	}
	if run.Enrollment.Status != "in_progress" {
		return domain.Progress{}, domain.ErrInvalidState
	}
	return s.StartLessonForCourse(ctx, studentID, run.Enrollment.ID, courseID, lesson.ID)
}

func (s *LearningService) loadInitialProgress(
	ctx context.Context,
	studentID, enrollmentID int64,
) (domain.Progress, error) {
	progress, err := s.enrollments.GetProgress(ctx, studentID, enrollmentID)
	if err != nil {
		return domain.Progress{}, err
	}
	if progress.Enrollment.CurrentQuestionID == nil && progress.FirstQuestionID != nil {
		if err := s.enrollments.SetCurrentQuestion(ctx, studentID, enrollmentID, *progress.FirstQuestionID); err != nil {
			return domain.Progress{}, err
		}
		progress, err = s.enrollments.GetProgress(ctx, studentID, enrollmentID)
		if err != nil {
			return domain.Progress{}, err
		}
	}
	return progress, nil
}

// StartCourse сохраняет режим и возвращает существующий запуск, если он уже есть.
// При попытке открыть другой курс возвращает ErrActiveCourse вместе с его прогрессом.
func (s *LearningService) StartCourse(
	ctx context.Context,
	studentID, courseID int64,
	mode string,
) (domain.CourseEnrollmentProgress, error) {
	if studentID <= 0 || courseID <= 0 || (mode != "ai" && mode != "curator") {
		return domain.CourseEnrollmentProgress{}, domain.ErrInvalidInput
	}
	if err := s.requireActiveStudent(ctx, studentID); err != nil {
		return domain.CourseEnrollmentProgress{}, err
	}
	if s.courseEnrollments == nil {
		return domain.CourseEnrollmentProgress{}, domain.ErrInvalidState
	}
	return s.courseEnrollments.Start(ctx, studentID, courseID, mode)
}

func (s *LearningService) ActiveCourse(
	ctx context.Context,
	studentID int64,
) (domain.CourseEnrollmentProgress, error) {
	if err := s.requireActiveStudent(ctx, studentID); err != nil {
		return domain.CourseEnrollmentProgress{}, err
	}
	if s.courseEnrollments == nil {
		return domain.CourseEnrollmentProgress{}, domain.ErrInvalidState
	}
	return s.courseEnrollments.GetActiveForStudent(ctx, studentID)
}

func (s *LearningService) CourseLessons(
	ctx context.Context,
	studentID, courseEnrollmentID int64,
) ([]domain.CourseEnrollmentLesson, error) {
	if err := s.requireActiveStudent(ctx, studentID); err != nil {
		return nil, err
	}
	if s.courseEnrollments == nil {
		return nil, domain.ErrInvalidState
	}
	return s.courseEnrollments.ListLessons(ctx, studentID, courseEnrollmentID)
}

// StartLessonForCourse начинает или восстанавливает урок только внутри ранее созданного
// запуска курса. Режим берётся из PostgreSQL и не принимается от клиента.
func (s *LearningService) StartLessonForCourse(
	ctx context.Context,
	studentID, courseEnrollmentID, courseID, lessonID int64,
) (domain.Progress, error) {
	if studentID <= 0 || courseEnrollmentID <= 0 || courseID <= 0 || lessonID <= 0 {
		return domain.Progress{}, domain.ErrInvalidInput
	}
	if err := s.requireActiveStudent(ctx, studentID); err != nil {
		return domain.Progress{}, err
	}
	if s.courseEnrollments == nil {
		return domain.Progress{}, domain.ErrInvalidState
	}
	course, err := s.courseEnrollments.GetForStudent(ctx, studentID, courseEnrollmentID)
	if err != nil {
		return domain.Progress{}, err
	}
	if course.Enrollment.CourseID != courseID || course.Enrollment.Status != "in_progress" {
		return domain.Progress{}, domain.ErrInvalidState
	}
	lesson, err := s.courseEnrollments.GetLessonForRun(ctx, studentID, courseEnrollmentID, lessonID)
	if err != nil {
		return domain.Progress{}, err
	}
	if lesson.CourseID != courseID || lesson.QuestionCount <= 0 {
		return domain.Progress{}, domain.ErrInvalidState
	}
	enrollment, err := s.enrollments.Start(ctx, domain.StartEnrollmentParams{
		StudentID: studentID, LessonID: lesson.ID,
		LessonVersionID:    lesson.LessonVersionID,
		CourseEnrollmentID: courseEnrollmentID,
		Mode:               course.Enrollment.Mode,
	})
	if err != nil {
		return domain.Progress{}, err
	}
	if enrollment.Status != "in_progress" {
		return domain.Progress{}, domain.ErrInvalidState
	}
	progress, err := s.enrollments.GetProgress(ctx, studentID, enrollment.ID)
	if err != nil {
		return domain.Progress{}, err
	}
	if progress.Enrollment.CurrentQuestionID == nil && progress.FirstQuestionID != nil {
		if err := s.enrollments.SetCurrentQuestion(ctx, studentID, enrollment.ID, *progress.FirstQuestionID); err != nil {
			return domain.Progress{}, err
		}
		progress, err = s.enrollments.GetProgress(ctx, studentID, enrollment.ID)
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
	if store, ok := s.enrollments.(questionPositionStore); ok {
		progress, err := store.OpenQuestionPosition(ctx, studentID, enrollmentID, questionID)
		if err != nil {
			return NavigationState{}, err
		}
		return s.loadNavigationStateForProgress(ctx, studentID, enrollmentID, questionID, progress)
	}
	if err := s.requireActiveStudent(ctx, studentID); err != nil {
		return NavigationState{}, err
	}
	enrollment, err := s.enrollments.GetByIDForStudent(ctx, studentID, enrollmentID)
	if err != nil {
		return NavigationState{}, err
	}
	if err := s.ensureCourseRunActive(ctx, enrollment); err != nil {
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

// OpenCurrentQuestion загружает именно текущую позицию, не меняя её. При устаревшем
// callback возвращает актуальный экран и ErrConflict, не применяя действие к новому вопросу.
func (s *LearningService) OpenCurrentQuestion(
	ctx context.Context,
	studentID, enrollmentID, expectedQuestionID int64,
) (NavigationState, error) {
	state, err := s.NavigateQuestion(ctx, studentID, enrollmentID, expectedQuestionID, 0)
	if state.StaleCallback {
		return state, domain.ErrConflict
	}
	return state, err
}

func (s *LearningService) loadNavigationStateForProgress(
	ctx context.Context,
	studentID, enrollmentID, questionID int64,
	progress domain.Progress,
) (NavigationState, error) {
	questions, err := s.questions.ListQuestionsForVersion(ctx, progress.Enrollment.LessonVersionID)
	if err != nil {
		return NavigationState{}, err
	}
	for _, question := range questions {
		if question.ID == questionID {
			return s.loadNavigationState(ctx, studentID, enrollmentID, question, progress)
		}
	}
	return NavigationState{}, domain.ErrNotFound
}

func (s *LearningService) NextQuestion(ctx context.Context, studentID, enrollmentID int64) (NavigationState, error) {
	return s.NavigateQuestion(ctx, studentID, enrollmentID, 0, 1)
}

func (s *LearningService) PreviousQuestion(ctx context.Context, studentID, enrollmentID int64) (NavigationState, error) {
	return s.NavigateQuestion(ctx, studentID, enrollmentID, 0, -1)
}

// NavigateQuestion — единый путь callback-навигации. Проверка ожидаемой позиции,
// смена вопроса и получение прогресса выполняются одной операцией хранилища.
func (s *LearningService) NavigateQuestion(
	ctx context.Context,
	studentID, enrollmentID, expectedQuestionID int64,
	step int,
) (NavigationState, error) {
	if studentID <= 0 || enrollmentID <= 0 || step < -1 || step > 1 {
		return NavigationState{}, domain.ErrInvalidInput
	}
	if store, ok := s.enrollments.(questionPositionStore); ok {
		progress, stale, navigationErr := store.NavigateQuestion(
			ctx, studentID, enrollmentID, expectedQuestionID, step,
		)
		if navigationErr != nil &&
			!errors.Is(navigationErr, domain.ErrNoNextQuestion) &&
			!errors.Is(navigationErr, domain.ErrNoPreviousQuestion) {
			return NavigationState{}, navigationErr
		}
		if progress.Enrollment.CurrentQuestionID == nil {
			if navigationErr != nil {
				return NavigationState{}, navigationErr
			}
			return NavigationState{}, domain.ErrInvalidState
		}
		state, err := s.loadNavigationStateForProgress(
			ctx, studentID, enrollmentID, *progress.Enrollment.CurrentQuestionID, progress,
		)
		if err != nil {
			return NavigationState{}, err
		}
		state.StaleCallback = stale
		return state, navigationErr
	}

	if err := s.requireActiveStudent(ctx, studentID); err != nil {
		return NavigationState{}, err
	}
	progress, err := s.enrollments.GetProgress(ctx, studentID, enrollmentID)
	if err != nil {
		return NavigationState{}, err
	}
	if err := s.ensureCourseRunActive(ctx, progress.Enrollment); err != nil {
		return NavigationState{}, err
	}
	if expectedQuestionID > 0 &&
		(progress.Enrollment.CurrentQuestionID == nil ||
			*progress.Enrollment.CurrentQuestionID != expectedQuestionID) {
		if progress.Enrollment.CurrentQuestionID == nil {
			return NavigationState{}, domain.ErrInvalidState
		}
		state, openErr := s.OpenQuestion(ctx, studentID, enrollmentID, *progress.Enrollment.CurrentQuestionID)
		state.StaleCallback = true
		return state, openErr
	}
	if step == 0 {
		if progress.Enrollment.CurrentQuestionID == nil {
			return NavigationState{}, domain.ErrInvalidState
		}
		return s.OpenQuestion(ctx, studentID, enrollmentID, *progress.Enrollment.CurrentQuestionID)
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
	if store, ok := s.answers.(navigationAnswerStateStore); ok {
		answer, latest, err := store.GetNavigationAnswerState(ctx, studentID, enrollmentID, question.ID)
		if err != nil {
			return NavigationState{}, err
		}
		state.SavedAnswer = answer
		state.LatestAttempt = latest
		return state, nil
	}
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

// ListLessonQuestions returns all questions and their saved/review states for an owned,
 // active lesson enrollment. It deliberately does not change current_question_id.
func (s *LearningService) ListLessonQuestions(
	ctx context.Context,
	studentID, enrollmentID int64,
) (LessonQuestionsState, error) {
	if studentID <= 0 || enrollmentID <= 0 {
		return LessonQuestionsState{}, domain.ErrInvalidInput
	}
	if err := s.requireActiveStudent(ctx, studentID); err != nil {
		return LessonQuestionsState{}, err
	}
	progress, err := s.enrollments.GetProgress(ctx, studentID, enrollmentID)
	if err != nil {
		return LessonQuestionsState{}, err
	}
	if err := s.ensureCourseRunActive(ctx, progress.Enrollment); err != nil {
		return LessonQuestionsState{}, err
	}
	questions, err := s.questions.ListQuestionsForVersion(ctx, progress.Enrollment.LessonVersionID)
	if err != nil {
		return LessonQuestionsState{}, err
	}
	result := LessonQuestionsState{
		Progress: progress,
		Questions: make([]NavigationState, 0, len(questions)),
	}
	for _, question := range questions {
		state, stateErr := s.loadNavigationState(ctx, studentID, enrollmentID, question, progress)
		if stateErr != nil {
			return LessonQuestionsState{}, stateErr
		}
		result.Questions = append(result.Questions, state)
	}
	return result, nil
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
	if err := s.ensureCourseRunActive(ctx, enrollment); err != nil {
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
	enrollment, err := s.enrollments.GetByIDForStudent(ctx, studentID, enrollmentID)
	if err != nil {
		return domain.Enrollment{}, err
	}
	if err := s.ensureCourseRunActive(ctx, enrollment); err != nil {
		return domain.Enrollment{}, err
	}
	return s.enrollments.Complete(ctx, studentID, enrollmentID)
}

func (s *LearningService) ensureCourseRunActive(ctx context.Context, enrollment domain.Enrollment) error {
	if enrollment.CourseEnrollmentID == nil {
		return nil
	}
	if s.courseEnrollments == nil {
		return domain.ErrInvalidState
	}
	course, err := s.courseEnrollments.GetForStudent(ctx, enrollment.StudentID, *enrollment.CourseEnrollmentID)
	if err != nil {
		return err
	}
	if course.Enrollment.Status != "in_progress" || course.Enrollment.Mode != enrollment.Mode {
		return domain.ErrInvalidState
	}
	lesson, err := s.courseEnrollments.GetLessonForRun(
		ctx, enrollment.StudentID, *enrollment.CourseEnrollmentID, enrollment.LessonID,
	)
	if err != nil {
		return err
	}
	if lesson.LessonVersionID != enrollment.LessonVersionID {
		return domain.ErrInvalidState
	}
	return nil
}

// GetStudentCabinet returns a compact student overview without loading lesson content.
func (s *LearningService) GetStudentCabinet(ctx context.Context, studentID int64) (domain.StudentCabinet, error) {
	if studentID <= 0 {
		return domain.StudentCabinet{}, domain.ErrInvalidInput
	}
	store, ok := s.courseEnrollments.(studentCabinetStore)
	if !ok {
		return domain.StudentCabinet{}, domain.ErrInvalidState
	}
	return store.GetStudentCabinet(ctx, studentID)
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
