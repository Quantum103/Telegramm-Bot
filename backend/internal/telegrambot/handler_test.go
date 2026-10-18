package telegrambot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/service"
)

func TestParseCommand(t *testing.T) {
	for input, want := range map[string]string{
		"/start": "start", "/start@demo_bot": "start", "/HELP": "help", "ordinary text": "", "": "",
	} {
		if got := parseCommand(input); got != want {
			t.Errorf("parseCommand(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestStartIsIdempotentAndDuplicateUpdateIsIgnored(t *testing.T) {
	h, api, students, _, _, states := newHarness([]domain.LessonQuestion{textQuestion(1)})
	update := messageUpdate(100, 900, "/start")

	if err := h.HandleUpdate(context.Background(), update); err != nil {
		t.Fatalf("first /start: %v", err)
	}
	if err := h.HandleUpdate(context.Background(), update); err != nil {
		t.Fatalf("duplicate /start update: %v", err)
	}
	if students.ensureCalls != 1 || len(students.byTelegramID) != 1 {
		t.Fatalf("duplicate update registered student again: calls=%d students=%d", students.ensureCalls, len(students.byTelegramID))
	}
	if api.sends != 1 {
		t.Fatalf("duplicate update sent additional main message: sends=%d", api.sends)
	}
	if err := h.HandleUpdate(context.Background(), messageUpdate(101, 900, "/start")); err != nil {
		t.Fatalf("second /start: %v", err)
	}
	if students.ensureCalls != 2 || len(students.byTelegramID) != 1 {
		t.Fatalf("repeat /start did not reuse student: calls=%d students=%d", students.ensureCalls, len(students.byTelegramID))
	}
	if _, err := states.Get(context.Background(), 42); err != nil {
		t.Fatalf("dialog state was not saved: %v", err)
	}
}

func TestCourseAndLessonCallbacksOpenFirstQuestion(t *testing.T) {
	q := choiceQuestion(1, "single_choice", 101, 102)
	h, api, _, _, _, states := newHarness([]domain.LessonQuestion{q})

	if err := h.HandleUpdate(context.Background(), messageUpdate(1, 900, "/start")); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleUpdate(context.Background(), callbackUpdate(2, 900, api.lastMessageID, "course:10")); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleUpdate(context.Background(), callbackUpdate(3, 900, api.lastMessageID, "lesson:10:20")); err != nil {
		t.Fatal(err)
	}
	state, err := states.Get(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if state.Screen != "question" || state.QuestionID == nil || *state.QuestionID != 1 || state.EnrollmentID == nil {
		t.Fatalf("lesson did not open first question: %+v", state)
	}
	if !strings.Contains(api.lastText, q.Prompt) {
		t.Fatalf("main message does not contain question prompt: %q", api.lastText)
	}
	if strings.Contains(api.lastText, q.ExpectedAnswer) {
		t.Fatal("main message leaked the expected answer")
	}
	if api.sends != 1 {
		t.Fatalf("navigation created duplicate messages; sends=%d", api.sends)
	}
}

func TestStaleCallbackCannotAnswerAnotherQuestion(t *testing.T) {
	q1 := choiceQuestion(1, "single_choice", 101, 102)
	q2 := choiceQuestion(2, "single_choice", 201, 202)
	h, api, _, learning, _, states := newHarness([]domain.LessonQuestion{q1, q2})
	startLesson(t, h, api)

	if err := h.HandleUpdate(context.Background(), callbackUpdate(4, 900, api.lastMessageID, "next:1")); err != nil {
		t.Fatal(err)
	}
	state, err := states.Get(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if state.QuestionID == nil || *state.QuestionID != 2 {
		t.Fatalf("expected question 2 after navigation, got %+v", state.QuestionID)
	}
	if err := h.HandleUpdate(context.Background(), callbackUpdate(5, 900, api.lastMessageID, "answer:1:101")); err != nil {
		t.Fatal(err)
	}
	if learning.attempts != 0 {
		t.Fatalf("stale callback created an answer attempt: %d", learning.attempts)
	}
	if state, _ = states.Get(context.Background(), 42); state.QuestionID == nil || *state.QuestionID != 2 {
		t.Fatalf("stale callback did not restore current question: %+v", state)
	}
}

func TestMultipleChoicePersistsSelectionsAndDoesNotDuplicateReplay(t *testing.T) {
	q := choiceQuestion(1, "multiple_choice", 101, 102)
	h, api, _, learning, _, states := newHarness([]domain.LessonQuestion{q})
	startLesson(t, h, api)
	for _, item := range []struct {
		updateID int64
		data     string
	}{
		{4, "toggle:1:101"}, {5, "toggle:1:102"}, {6, "submit:1"},
	} {
		if err := h.HandleUpdate(context.Background(), callbackUpdate(item.updateID, 900, api.lastMessageID, item.data)); err != nil {
			t.Fatalf("callback %s: %v", item.data, err)
		}
	}
	if learning.attempts != 1 {
		t.Fatalf("multiple choice attempts = %d, want 1", learning.attempts)
	}
	saved := learning.saved[1]
	if !sameIDs(saved.SelectedOptionIDs, []int64{101, 102}) || saved.ProcessingStatus != "pending" {
		t.Fatalf("wrong multiple choice answer stored: %+v", saved)
	}
	if err := h.HandleUpdate(context.Background(), callbackUpdate(6, 900, api.lastMessageID, "submit:1")); err != nil {
		t.Fatal(err)
	}
	if learning.attempts != 1 {
		t.Fatalf("replayed confirmation created another attempt: %d", learning.attempts)
	}
	state, err := states.Get(context.Background(), 42)
	if err != nil || !sameIDs(state.SelectedOptionIDs, []int64{101, 102}) {
		t.Fatalf("selected options were not persisted: state=%+v err=%v", state, err)
	}
}

func TestTextAnswerIsStoredEvenWhenTelegramCannotDeleteMessage(t *testing.T) {
	h, api, _, learning, _, states := newHarness([]domain.LessonQuestion{textQuestion(1)})
	api.deleteErr = errors.New("Telegram deletion not permitted")
	startLesson(t, h, api)

	answer := messageUpdate(4, 900, "My study answer")
	answer.Message.MessageID = 501
	if err := h.HandleUpdate(context.Background(), answer); err != nil {
		t.Fatalf("submit answer: %v", err)
	}
	if learning.attempts != 1 || api.deleteCalls != 1 {
		t.Fatalf("attempts=%d, deleteCalls=%d", learning.attempts, api.deleteCalls)
	}
	if !strings.Contains(api.lastText, "Ответ сохранён") || !strings.Contains(api.lastText, "проверка пока не выполнена") {
		t.Fatalf("unexpected post-submit text: %q", api.lastText)
	}
	if err := h.HandleUpdate(context.Background(), answer); err != nil {
		t.Fatalf("replay submitted update: %v", err)
	}
	if learning.attempts != 1 || api.deleteCalls != 1 {
		t.Fatalf("replayed update repeated side effects: attempts=%d deletes=%d", learning.attempts, api.deleteCalls)
	}
	saved, ok := learning.saved[1]
	if !ok || saved.AnswerText != "My study answer" || saved.ProcessingStatus != "pending" {
		t.Fatalf("answer not stored as pending: %+v", saved)
	}
	if state, err := states.Get(context.Background(), 42); err != nil || state.AwaitingText {
		t.Fatalf("text input was not closed after submit: state=%+v err=%v", state, err)
	}
}

func TestCancelStopsWaitingForTextWithoutChangingSavedAnswers(t *testing.T) {
	h, api, _, learning, _, states := newHarness([]domain.LessonQuestion{textQuestion(1)})
	startLesson(t, h, api)
	if err := h.HandleUpdate(context.Background(), messageUpdate(4, 900, "/cancel")); err != nil {
		t.Fatal(err)
	}
	state, err := states.Get(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if state.Screen != "question" || state.AwaitingText {
		t.Fatalf("/cancel should keep the question visible and clear pending text input: %+v", state)
	}
	answer := messageUpdate(5, 900, "not an answer after cancel")
	if err := h.HandleUpdate(context.Background(), answer); err != nil {
		t.Fatal(err)
	}
	if learning.attempts != 0 {
		t.Fatalf("text was saved while input was cancelled: attempts=%d", learning.attempts)
	}
	if !strings.Contains(api.lastText, "не ожидается") {
		t.Fatalf("expected input guidance after cancel, got %q", api.lastText)
	}
}

func TestHelpAndDatabaseFailureArePresentedSafely(t *testing.T) {
	h, api, _, _, _, states := newHarness([]domain.LessonQuestion{textQuestion(1)})
	if err := h.HandleUpdate(context.Background(), messageUpdate(1, 900, "/start")); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleUpdate(context.Background(), messageUpdate(2, 900, "/help")); err != nil {
		t.Fatal(err)
	}
	state, _ := states.Get(context.Background(), 42)
	if state.Screen != "help" || !strings.Contains(api.lastText, "Команда /cancel") {
		t.Fatalf("unexpected /help state or text: state=%+v text=%q", state, api.lastText)
	}
	states.getErr = errors.New("database unavailable: private details")
	if err := h.HandleUpdate(context.Background(), messageUpdate(3, 900, "another message")); err != nil {
		t.Fatalf("handler should show a safe user-facing DB error: %v", err)
	}
	if strings.Contains(api.lastText, "private details") {
		t.Fatalf("database internals leaked to user: %q", api.lastText)
	}
}

func startLesson(t *testing.T, h *Handler, api *fakeBotAPI) {
	t.Helper()
	ctx := context.Background()
	if err := h.HandleUpdate(ctx, messageUpdate(1, 900, "/start")); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleUpdate(ctx, callbackUpdate(2, 900, api.lastMessageID, "course:10")); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleUpdate(ctx, callbackUpdate(3, 900, api.lastMessageID, "lesson:10:20")); err != nil {
		t.Fatal(err)
	}
}

func messageUpdate(updateID, telegramID int64, text string) Update {
	user := &User{ID: telegramID, FirstName: "Test", LastName: "Student"}
	return Update{UpdateID: updateID, Message: &Message{
		MessageID: updateID + 500, From: user, Chat: Chat{ID: telegramID, Type: "private"}, Text: text,
	}}
}

func callbackUpdate(updateID, telegramID, mainMessageID int64, data string) Update {
	return Update{UpdateID: updateID, CallbackQuery: &CallbackQuery{
		ID: fmt.Sprintf("callback-%d", updateID), From: User{ID: telegramID},
		Message: &Message{MessageID: mainMessageID, Chat: Chat{ID: telegramID, Type: "private"}}, Data: data,
	}}
}

func textQuestion(id int64) domain.LessonQuestion {
	return domain.LessonQuestion{ID: id, LessonVersionID: 30, QuestionNumber: int(id), Prompt: "What did you learn?", ExpectedAnswer: "internal expected answer", AnswerType: "text"}
}

func choiceQuestion(id int64, answerType string, optionIDs ...int64) domain.LessonQuestion {
	options := make([]domain.QuestionOption, 0, len(optionIDs))
	for i, optionID := range optionIDs {
		options = append(options, domain.QuestionOption{ID: optionID, Key: fmt.Sprintf("opt-%d", i+1), Text: fmt.Sprintf("Option %d", i+1)})
	}
	return domain.LessonQuestion{ID: id, LessonVersionID: 30, QuestionNumber: int(id), Prompt: "Choose an option", ExpectedAnswer: "private answer", AnswerType: answerType, Options: options}
}

func newHarness(questions []domain.LessonQuestion) (*Handler, *fakeBotAPI, *fakeStudentService, *fakeLearningService, *fakeCatalogService, *memoryStateStore) {
	api := &fakeBotAPI{}
	students := &fakeStudentService{byTelegramID: make(map[int64]domain.Student)}
	catalog := &fakeCatalogService{
		course:    domain.Course{ID: 10, Slug: "test-course", Title: "Test course", Status: "published", LessonCount: 1},
		lesson:    domain.Lesson{ID: 20, CourseID: 10, CourseTitle: "Test course", LessonVersionID: 30, Title: "Test lesson", QuestionCount: len(questions)},
		questions: questions,
	}
	learning := &fakeLearningService{questions: questions, saved: make(map[int64]domain.SavedAnswer), keys: make(map[string]domain.SavedAnswer)}
	states := &memoryStateStore{states: make(map[int64]domain.TelegramDialogState), processed: make(map[int64]bool)}
	handler := NewHandler(api, students, catalog, learning, states, slogOrNil())
	return handler, api, students, learning, catalog, states
}

func slogOrNil() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fakeBotAPI struct {
	sends, edits, callbackAnswers, deleteCalls int
	lastMessageID                              int64
	lastText                                   string
	deleteErr                                  error
}

func (a *fakeBotAPI) GetMe(context.Context) (User, error) {
	return User{ID: 1, Username: "test_bot"}, nil
}
func (a *fakeBotAPI) GetUpdates(context.Context, int64, int) ([]Update, error) { return nil, nil }
func (a *fakeBotAPI) SendMessage(_ context.Context, _ int64, text string, _ *InlineKeyboardMarkup) (Message, error) {
	a.sends++
	a.lastMessageID++
	a.lastText = text
	return Message{MessageID: a.lastMessageID}, nil
}
func (a *fakeBotAPI) EditMessageText(_ context.Context, _ int64, _ int64, text string, _ *InlineKeyboardMarkup) error {
	a.edits++
	a.lastText = text
	return nil
}
func (a *fakeBotAPI) AnswerCallbackQuery(context.Context, string, string) error {
	a.callbackAnswers++
	return nil
}
func (a *fakeBotAPI) DeleteMessage(context.Context, int64, int64) error {
	a.deleteCalls++
	return a.deleteErr
}

type fakeStudentService struct {
	byTelegramID map[int64]domain.Student
	ensureCalls  int
}
func (s *fakeStudentService) EnsureByTelegramID(_ context.Context, params domain.EnsureStudentParams) (domain.Student, error) {
	s.ensureCalls++
	if student, ok := s.byTelegramID[params.TelegramID]; ok {
		student.DisplayName = params.DisplayName
		s.byTelegramID[params.TelegramID] = student
		return student, nil
	}
	student := domain.Student{ID: 42, TelegramID: params.TelegramID, DisplayName: params.DisplayName, AccountStatus: "active"}
	s.byTelegramID[params.TelegramID] = student
	return student, nil
}
func (s *fakeStudentService) GetByTelegramID(_ context.Context, telegramID int64) (domain.Student, error) {
	student, ok := s.byTelegramID[telegramID]
	if !ok {
		return domain.Student{}, domain.ErrNotFound
	}
	return student, nil
}

type fakeCatalogService struct {
	course    domain.Course
	lesson    domain.Lesson
	questions []domain.LessonQuestion
}

func (c *fakeCatalogService) ListCourses(context.Context) ([]domain.Course, error) {
	return []domain.Course{c.course}, nil
}
func (c *fakeCatalogService) GetCourse(_ context.Context, id int64) (domain.Course, error) {
	if id != c.course.ID {
		return domain.Course{}, domain.ErrNotFound
	}
	return c.course, nil
}
func (c *fakeCatalogService) ListCourseLessons(_ context.Context, id int64) ([]domain.Lesson, error) {
	if id != c.course.ID {
		return nil, domain.ErrNotFound
	}
	return []domain.Lesson{c.lesson}, nil
}
func (c *fakeCatalogService) GetLessonDetailInCourse(_ context.Context, courseID, lessonID int64) (domain.LessonDetail, error) {
	if courseID != c.course.ID || lessonID != c.lesson.ID {
		return domain.LessonDetail{}, domain.ErrNotFound
	}
	return domain.LessonDetail{Lesson: c.lesson, Questions: c.questions}, nil
}

type fakeLearningService struct {
	questions []domain.LessonQuestion
	saved     map[int64]domain.SavedAnswer
	keys      map[string]domain.SavedAnswer
	currentID int64
	attempts  int
	submitErr error
	completed bool
}
func (l *fakeLearningService) StartLesson(ctx context.Context, studentID, lessonID int64, mode string) (domain.Progress, error) {
	if studentID != 42 || lessonID != 20 {
		return domain.Progress{}, domain.ErrNotFound
	}
	if l.currentID == 0 && len(l.questions) > 0 {
		l.currentID = l.questions[0].ID
	}
	return l.progress(studentID), nil
}
func (l *fakeLearningService) GetProgress(_ context.Context, studentID, enrollmentID int64) (domain.Progress, error) {
	if studentID != 42 || enrollmentID != 50 {
		return domain.Progress{}, domain.ErrNotFound
	}
	return l.progress(studentID), nil
}
func (l *fakeLearningService) FirstQuestion(ctx context.Context, studentID, enrollmentID int64) (service.NavigationState, error) {
	if len(l.questions) == 0 {
		return service.NavigationState{}, domain.ErrInvalidState
	}
	return l.OpenQuestion(ctx, studentID, enrollmentID, l.questions[0].ID)
}
func (l *fakeLearningService) OpenQuestion(_ context.Context, studentID, enrollmentID, questionID int64) (service.NavigationState, error) {
	if studentID != 42 || enrollmentID != 50 {
		return service.NavigationState{}, domain.ErrNotFound
	}
	var question *domain.LessonQuestion
	for i := range l.questions {
		if l.questions[i].ID == questionID {
			question = &l.questions[i]
			break
		}
	}
	if question == nil {
		return service.NavigationState{}, domain.ErrNotFound
	}
	l.currentID = questionID
	progress := l.progress(studentID)
	nav := service.NavigationState{Question: *question, Progress: progress}
	if answer, ok := l.saved[questionID]; ok {
		copyAnswer := answer
		nav.SavedAnswer = &copyAnswer
	}
	return nav, nil
}
func (l *fakeLearningService) NextQuestion(ctx context.Context, studentID, enrollmentID int64) (service.NavigationState, error) {
	p, _ := l.GetProgress(ctx, studentID, enrollmentID)
	if p.NextQuestionID == nil {
		return service.NavigationState{}, domain.ErrNoNextQuestion
	}
	return l.OpenQuestion(ctx, studentID, enrollmentID, *p.NextQuestionID)
}
func (l *fakeLearningService) PreviousQuestion(ctx context.Context, studentID, enrollmentID int64) (service.NavigationState, error) {
	p, _ := l.GetProgress(ctx, studentID, enrollmentID)
	if p.PreviousQuestionID == nil {
		return service.NavigationState{}, domain.ErrNoPreviousQuestion
	}
	return l.OpenQuestion(ctx, studentID, enrollmentID, *p.PreviousQuestionID)
}
func (l *fakeLearningService) SubmitAnswer(_ context.Context, params domain.AnswerSubmission) (service.SubmissionResult, error) {
	if l.submitErr != nil {
		return service.SubmissionResult{}, l.submitErr
	}
	if result, ok := l.keys[params.IdempotencyKey]; ok {
		result.Replayed = true
		return service.SubmissionResult{Answer: result, Progress: l.progress(params.StudentID)}, nil
	}
	l.attempts++
	result := domain.SavedAnswer{AnswerID: 70, AttemptID: int64(l.attempts), AnswerText: params.AnswerText, SelectedOptionIDs: append([]int64{}, params.SelectedOptionIDs...), ProcessingStatus: "pending", UpdatedAt: time.Now()}
	l.keys[params.IdempotencyKey] = result
	l.saved[params.QuestionID] = result
	return service.SubmissionResult{Answer: result, Progress: l.progress(params.StudentID)}, nil
}
func (l *fakeLearningService) CompleteLesson(_ context.Context, studentID, enrollmentID int64) (domain.Enrollment, error) {
	if studentID != 42 || enrollmentID != 50 || len(l.saved) != len(l.questions) || len(l.questions) == 0 {
		return domain.Enrollment{}, domain.ErrInvalidState
	}
	l.completed = true
	p := l.progress(studentID)
	return p.Enrollment, nil
}
func (l *fakeLearningService) progress(studentID int64) domain.Progress {
	var current *int64
	if l.currentID > 0 {
		current = int64PtrTest(l.currentID)
	}
	p := domain.Progress{Enrollment: domain.Enrollment{ID: 50, StudentID: studentID, LessonID: 20, LessonVersionID: 30, Mode: "ai", CurrentQuestionID: current, Status: "in_progress"}, TotalQuestions: len(l.questions), AnsweredQuestions: len(l.saved)}
	p.RemainingQuestions = p.TotalQuestions - p.AnsweredQuestions
	if l.completed {
		p.Enrollment.Status = "completed"
		p.IsComplete = true
	}
	for i, q := range l.questions {
		if i == 0 {
			v := q.ID
			p.FirstQuestionID = &v
		}
		if q.ID == l.currentID {
			if i > 0 {
				v := l.questions[i-1].ID
				p.PreviousQuestionID = &v
			}
			if i+1 < len(l.questions) {
				v := l.questions[i+1].ID
				p.NextQuestionID = &v
			}
		}
	}
	return p
}
func int64PtrTest(v int64) *int64 { return &v }

type memoryStateStore struct {
	states    map[int64]domain.TelegramDialogState
	processed map[int64]bool
	getErr    error
}
func (s *memoryStateStore) Get(_ context.Context, id int64) (domain.TelegramDialogState, error) {
	if s.getErr != nil {
		return domain.TelegramDialogState{}, s.getErr
	}
	state, ok := s.states[id]
	if !ok {
		return domain.TelegramDialogState{}, domain.ErrNotFound
	}
	state.SelectedOptionIDs = append([]int64{}, state.SelectedOptionIDs...)
	return state, nil
}
func (s *memoryStateStore) Save(_ context.Context, state domain.TelegramDialogState) error {
	state.SelectedOptionIDs = append([]int64{}, state.SelectedOptionIDs...)
	s.states[state.StudentID] = state
	return nil
}

func (s *memoryStateStore) BeginUpdate(_ context.Context, id int64) (bool, error) {
	if s.processed[id] {
		return false, nil
	}
	return true, nil
}

func (s *memoryStateStore) CompleteUpdate(_ context.Context, id int64) error {
	s.processed[id] = true
	return nil
}

func (s *memoryStateStore) FailUpdate(context.Context, int64) error { return nil }
