package telegrambot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/service"
)

type sentMessage struct {
	chatID  int64
	messageID int64
	text    string
	markup  *InlineKeyboardMarkup
}

type editedMessage struct {
	chatID    int64
	messageID int64
	text      string
	markup    *InlineKeyboardMarkup
}

type fakeAPI struct {
	getMeErr         error
	deleteWebhookErr error
	sendErr          error
	editErr          error
	answerErr        error
	messages         []sentMessage
	edits            []editedMessage
	answered         []string
	updateErr        error
	blockUpdates     bool
	updatesStarted   chan struct{}
	nextMessageID    int64
	mu               sync.Mutex
}

func (f *fakeAPI) GetMe(context.Context) (User, error) {
	if f.getMeErr != nil {
		return User{}, f.getMeErr
	}
	return User{ID: 321, Username: "study_bot"}, nil
}

func (f *fakeAPI) DeleteWebhook(context.Context) error {
	return f.deleteWebhookErr
}

func (f *fakeAPI) GetUpdates(ctx context.Context, _ int64, _ int) ([]Update, error) {
	if f.blockUpdates {
		if f.updatesStarted != nil {
			select {
			case f.updatesStarted <- struct{}{}:
			default:
			}
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return nil, nil
}

func (f *fakeAPI) SendMessage(_ context.Context, chatID int64, text string) error {
	_, err := f.SendMessageWithKeyboard(context.Background(), chatID, text, nil)
	return err
}

func (f *fakeAPI) SendMessageWithKeyboard(_ context.Context, chatID int64, text string, markup *InlineKeyboardMarkup) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return 0, f.sendErr
	}
	f.nextMessageID++
	if f.nextMessageID == 0 {
		f.nextMessageID = 100
	}
	messageID := f.nextMessageID
	f.messages = append(f.messages, sentMessage{chatID: chatID, messageID: messageID, text: text, markup: markup})
	return messageID, nil
}

func (f *fakeAPI) EditMessageText(_ context.Context, chatID, messageID int64, text string, markup *InlineKeyboardMarkup) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.editErr != nil {
		return f.editErr
	}
	f.edits = append(f.edits, editedMessage{chatID: chatID, messageID: messageID, text: text, markup: markup})
	return nil
}

func (f *fakeAPI) AnswerCallbackQuery(_ context.Context, callbackID, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answered = append(f.answered, callbackID)
	return f.answerErr
}

type fakeStudents struct {
	mu     sync.Mutex
	calls  int
	byID   map[int64]domain.Student
	params []domain.EnsureStudentParams
	err    error
}

func (f *fakeStudents) EnsureByTelegramID(_ context.Context, params domain.EnsureStudentParams) (domain.Student, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.params = append(f.params, params)
	if f.err != nil {
		return domain.Student{}, f.err
	}
	if f.byID == nil {
		f.byID = make(map[int64]domain.Student)
	}
	if student, exists := f.byID[params.TelegramID]; exists {
		student.DisplayName = params.DisplayName
		f.byID[params.TelegramID] = student
		return student, nil
	}
	student := domain.Student{
		ID:            int64(len(f.byID) + 1),
		TelegramID:    params.TelegramID,
		DisplayName:   params.DisplayName,
		AccountStatus: "active",
	}
	f.byID[params.TelegramID] = student
	return student, nil
}

type fakeCatalog struct {
	courses       []domain.Course
	lessons       map[int64][]domain.Lesson
	details       map[int64]domain.LessonDetail
	listErr       error
	courseErr     error
	lessonsErr    error
	detailErr     error
	detailCalls   []int64
}

func (f *fakeCatalog) ListCourses(context.Context) ([]domain.Course, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]domain.Course(nil), f.courses...), nil
}

func (f *fakeCatalog) GetCourse(_ context.Context, courseID int64) (domain.Course, error) {
	if f.courseErr != nil {
		return domain.Course{}, f.courseErr
	}
	for _, course := range f.courses {
		if course.ID == courseID {
			return course, nil
		}
	}
	return domain.Course{}, domain.ErrNotFound
}

func (f *fakeCatalog) ListCourseLessons(_ context.Context, courseID int64) ([]domain.Lesson, error) {
	if f.lessonsErr != nil {
		return nil, f.lessonsErr
	}
	if _, err := f.GetCourse(context.Background(), courseID); err != nil {
		return nil, err
	}
	return append([]domain.Lesson(nil), f.lessons[courseID]...), nil
}

func (f *fakeCatalog) GetLessonDetailInCourse(_ context.Context, courseID, lessonID int64) (domain.LessonDetail, error) {
	f.detailCalls = append(f.detailCalls, lessonID)
	if f.detailErr != nil {
		return domain.LessonDetail{}, f.detailErr
	}
	detail, ok := f.details[lessonID]
	if !ok || detail.Lesson.CourseID != courseID {
		return domain.LessonDetail{}, domain.ErrNotFound
	}
	return detail, nil
}

func (f *fakeCatalog) GetLessonDetail(_ context.Context, lessonID int64) (domain.LessonDetail, error) {
	detail, ok := f.details[lessonID]
	if !ok {
		return domain.LessonDetail{}, domain.ErrNotFound
	}
	return detail, nil
}

type fakeLearning struct {
	mu               sync.Mutex
	catalog          *fakeCatalog
	nextEnrollmentID int64
	progress         map[int64]domain.Progress
	questions        map[int64][]domain.LessonQuestion
	startCalls       int
	openCalls        int
	startErr         error
	progressErr      error
	openErr          error
	nextErr          error
	previousErr      error
}

func newFakeLearning(catalog *fakeCatalog) *fakeLearning {
	return &fakeLearning{
		catalog:          catalog,
		nextEnrollmentID: 700,
		progress:         make(map[int64]domain.Progress),
		questions:        make(map[int64][]domain.LessonQuestion),
	}
}

func (f *fakeLearning) StartLesson(ctx context.Context, studentID, lessonID int64, mode string) (domain.Progress, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	if f.startErr != nil {
		return domain.Progress{}, f.startErr
	}
	detail, err := f.catalog.GetLessonDetail(ctx, lessonID)
	if err != nil {
		return domain.Progress{}, err
	}
	for _, existing := range f.progress {
		if existing.Enrollment.StudentID == studentID && existing.Enrollment.LessonID == lessonID {
			return existing, nil
		}
	}
	f.nextEnrollmentID++
	enrollmentID := f.nextEnrollmentID
	questions := append([]domain.LessonQuestion(nil), detail.Questions...)
	f.questions[enrollmentID] = questions
	var currentID *int64
	if len(questions) > 0 {
		id := questions[0].ID
		currentID = &id
	}
	p := domain.Progress{
		Enrollment: domain.Enrollment{
			ID: enrollmentID, StudentID: studentID, LessonID: lessonID,
			LessonVersionID: detail.Lesson.LessonVersionID, Mode: mode,
			CurrentQuestionID: currentID, Status: "in_progress",
		},
		TotalQuestions: len(questions),
	}
	f.updatePointers(&p)
	f.progress[enrollmentID] = p
	return p, nil
}

func (f *fakeLearning) GetProgress(_ context.Context, studentID, enrollmentID int64) (domain.Progress, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.progressErr != nil {
		return domain.Progress{}, f.progressErr
	}
	p, ok := f.progress[enrollmentID]
	if !ok || p.Enrollment.StudentID != studentID {
		return domain.Progress{}, domain.ErrNotFound
	}
	return p, nil
}

func (f *fakeLearning) OpenQuestion(_ context.Context, studentID, enrollmentID, questionID int64) (service.NavigationState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.openCalls++
	if f.openErr != nil {
		return service.NavigationState{}, f.openErr
	}
	p, ok := f.progress[enrollmentID]
	if !ok || p.Enrollment.StudentID != studentID {
		return service.NavigationState{}, domain.ErrNotFound
	}
	for _, question := range f.questions[enrollmentID] {
		if question.ID == questionID && question.LessonVersionID == p.Enrollment.LessonVersionID {
			p.Enrollment.CurrentQuestionID = int64Pointer(question.ID)
			f.updatePointers(&p)
			f.progress[enrollmentID] = p
			return service.NavigationState{Question: question, Progress: p}, nil
		}
	}
	return service.NavigationState{}, domain.ErrNotFound
}

func (f *fakeLearning) PreviousQuestion(ctx context.Context, studentID, enrollmentID int64) (service.NavigationState, error) {
	if f.previousErr != nil {
		return service.NavigationState{}, f.previousErr
	}
	return f.move(ctx, studentID, enrollmentID, -1)
}

func (f *fakeLearning) NextQuestion(ctx context.Context, studentID, enrollmentID int64) (service.NavigationState, error) {
	if f.nextErr != nil {
		return service.NavigationState{}, f.nextErr
	}
	return f.move(ctx, studentID, enrollmentID, 1)
}

func (f *fakeLearning) move(ctx context.Context, studentID, enrollmentID int64, step int) (service.NavigationState, error) {
	f.mu.Lock()
	p, ok := f.progress[enrollmentID]
	if !ok || p.Enrollment.StudentID != studentID || p.Enrollment.CurrentQuestionID == nil {
		f.mu.Unlock()
		return service.NavigationState{}, domain.ErrNotFound
	}
	questions := f.questions[enrollmentID]
	index := -1
	for i := range questions {
		if questions[i].ID == *p.Enrollment.CurrentQuestionID {
			index = i
			break
		}
	}
	target := index + step
	if index < 0 || target < 0 {
		f.mu.Unlock()
		return service.NavigationState{}, domain.ErrNoPreviousQuestion
	}
	if target >= len(questions) {
		f.mu.Unlock()
		return service.NavigationState{}, domain.ErrNoNextQuestion
	}
	question := questions[target]
	p.Enrollment.CurrentQuestionID = int64Pointer(question.ID)
	f.updatePointers(&p)
	f.progress[enrollmentID] = p
	f.mu.Unlock()
	return service.NavigationState{Question: question, Progress: p}, nil
}

func (f *fakeLearning) updatePointers(p *domain.Progress) {
	p.FirstQuestionID = nil
	p.PreviousQuestionID = nil
	p.NextQuestionID = nil
	questions := f.questions[p.Enrollment.ID]
	if len(questions) == 0 {
		return
	}
	first := questions[0].ID
	p.FirstQuestionID = &first
	index := 0
	if p.Enrollment.CurrentQuestionID != nil {
		for i := range questions {
			if questions[i].ID == *p.Enrollment.CurrentQuestionID {
				index = i
				break
			}
		}
	}
	if index > 0 {
		value := questions[index-1].ID
		p.PreviousQuestionID = &value
	}
	if index+1 < len(questions) {
		value := questions[index+1].ID
		p.NextQuestionID = &value
	}
}

func int64Pointer(value int64) *int64 { return &value }

func fixture() (*fakeCatalog, *fakeLearning) {
	catalog := &fakeCatalog{
		courses: []domain.Course{
			{ID: 10, Title: "Курс о Евангелиях", Status: "published"},
			{ID: 20, Title: "Курс о молитве", Status: "published"},
		},
		lessons: make(map[int64][]domain.Lesson),
		details: make(map[int64]domain.LessonDetail),
	}
	questions := []domain.LessonQuestion{
		{ID: 1001, LessonVersionID: 501, QuestionNumber: 1, Prompt: "Кто написал Евангелие от Марка?", AnswerType: "text"},
		{ID: 1002, LessonVersionID: 501, QuestionNumber: 2, Prompt: "Как начинается повествование?", AnswerType: "text"},
	}
	lesson := domain.Lesson{ID: 100, CourseID: 10, CourseTitle: catalog.courses[0].Title, LessonVersionID: 501, Title: "Евангелие от Марка", QuestionCount: len(questions)}
	catalog.lessons[10] = []domain.Lesson{lesson}
	catalog.lessons[20] = []domain.Lesson{{ID: 200, CourseID: 20, LessonVersionID: 601, Title: "Молитва", QuestionCount: 1}}
	catalog.details[100] = domain.LessonDetail{Lesson: lesson, Questions: questions}
	catalog.details[200] = domain.LessonDetail{
		Lesson: catalog.lessons[20][0],
		Questions: []domain.LessonQuestion{{ID: 2001, LessonVersionID: 601, QuestionNumber: 1, Prompt: "Что такое молитва?", AnswerType: "text"}},
	}
	return catalog, newFakeLearning(catalog)
}

func newTestBot(api *fakeAPI, students *fakeStudents, catalog *fakeCatalog, learning *fakeLearning) *Bot {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewBot(api, students, catalog, learning, logger)
}

func testUser() User {
	return User{ID: 987654, FirstName: "Анна", LastName: "Иванова", Username: "anna"}
}

func testUpdate(updateID int64, command string, user User) Update {
	return Update{
		UpdateID: updateID,
		Message: &Message{
			From: &user,
			Chat: Chat{ID: user.ID, Type: "private"},
			Text: command,
		},
	}
}

func testCallback(data string, user User, messageID int64) CallbackQuery {
	return CallbackQuery{
		ID: "callback-test",
		From: &user,
		Message: &Message{MessageID: messageID, Chat: Chat{ID: user.ID, Type: "private"}},
		Data: data,
	}
}

func TestStartRegistersStudentAndShowsCourses(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	bot := newTestBot(api, students, catalog, learning)
	user := testUser()

	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))

	if students.calls != 1 || len(students.byID) != 1 {
		t.Fatalf("expected one student registration, calls=%d records=%d", students.calls, len(students.byID))
	}
	if len(students.params) != 1 || students.params[0].TelegramID != user.ID {
		t.Fatalf("Telegram ID not registered: %+v", students.params)
	}
	if len(api.messages) != 1 {
		t.Fatalf("expected one main message, got %d", len(api.messages))
	}
	if !strings.Contains(api.messages[0].text, "Привет, Анна Иванова") {
		t.Errorf("greeting is missing: %q", api.messages[0].text)
	}
	if !hasButton(api.messages[0].markup, "course:10") || !hasButton(api.messages[0].markup, "course:20") {
		t.Fatalf("published courses not displayed: %+v", api.messages[0].markup)
	}
}

func TestRepeatedStartDoesNotDuplicateStudent(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	bot := newTestBot(api, students, catalog, learning)
	bot.handleUpdate(context.Background(), testUpdate(1, "/start", testUser()))
	bot.handleUpdate(context.Background(), testUpdate(2, "/start", testUser()))

	if len(students.byID) != 1 || len(api.messages) != 2 {
		t.Fatalf("repeated /start caused unexpected result: students=%d messages=%d", len(students.byID), len(api.messages))
	}
}

func TestEmptyCatalogIsExplained(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	catalog.courses = nil
	bot := newTestBot(api, students, catalog, learning)

	bot.handleUpdate(context.Background(), testUpdate(1, "/start", testUser()))

	if len(api.messages) != 1 || !strings.Contains(api.messages[0].text, "нет опубликованных курсов") {
		t.Fatalf("empty catalog message missing: %+v", api.messages)
	}
	if !hasButton(api.messages[0].markup, "courses") {
		t.Fatal("empty catalog should allow refreshing the list")
	}
}

func TestSelectingCourseShowsOnlyThatCoursesLessons(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	bot := newTestBot(api, students, catalog, learning)
	user := testUser()
	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	messageID := api.messages[0].messageID

	bot.handleCallback(context.Background(), testCallback("course:10", user, messageID))

	last := lastEdit(t, api)
	if !strings.Contains(last.text, "Курс о Евангелиях") || !hasButton(last.markup, "lesson:10:100") {
		t.Fatalf("selected course lessons not shown: %+v", last)
	}
	if hasButton(last.markup, "lesson:20:200") {
		t.Fatal("lesson from another course leaked into selected course")
	}
	if last.messageID != messageID || len(api.messages) != 1 {
		t.Fatalf("navigation must edit the main message, messages=%d last message id=%d", len(api.messages), last.messageID)
	}
}

func TestOpeningLessonShowsFirstQuestionAndNavigation(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	bot := newTestBot(api, students, catalog, learning)
	user := testUser()
	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	messageID := api.messages[0].messageID
	bot.handleCallback(context.Background(), testCallback("lesson:10:100", user, messageID))

	last := lastEdit(t, api)
	if !strings.Contains(last.text, "Евангелие от Марка") || !strings.Contains(last.text, "Вопрос 1 из 2") ||
		!strings.Contains(last.text, "Кто написал Евангелие от Марка?") {
		t.Fatalf("question screen was not rendered correctly: %q", last.text)
	}
	if !hasButton(last.markup, "nav:next:701:1001:10") {
		t.Fatalf("first question should have a next button: %+v", last.markup)
	}
	if hasButtonPrefix(last.markup, "nav:prev:") {
		t.Fatal("first question must not have a previous button")
	}
	if strings.Contains(last.text, "Предполагаемый") || strings.Contains(last.text, "критери") {
		t.Fatal("internal answer material leaked into the question screen")
	}
	if learning.startCalls != 1 || learning.openCalls != 1 {
		t.Fatalf("lesson should use learning service, start=%d open=%d", learning.startCalls, learning.openCalls)
	}
}

func TestNavigationMovesForwardAndBackwardInSingleMessage(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	bot := newTestBot(api, students, catalog, learning)
	user := testUser()
	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	messageID := api.messages[0].messageID
	bot.handleCallback(context.Background(), testCallback("lesson:10:100", user, messageID))

	bot.handleCallback(context.Background(), testCallback("nav:next:701:1001:10", user, messageID))
	last := lastEdit(t, api)
	if !strings.Contains(last.text, "Вопрос 2 из 2") || !strings.Contains(last.text, "Как начинается повествование?") {
		t.Fatalf("next question not shown: %q", last.text)
	}
	if hasButtonPrefix(last.markup, "nav:next:") {
		t.Fatal("last question must not have a next button")
	}
	if !hasButton(last.markup, "nav:prev:701:1002:10") {
		t.Fatal("last question should have a previous button")
	}

	bot.handleCallback(context.Background(), testCallback("nav:prev:701:1002:10", user, messageID))
	last = lastEdit(t, api)
	if !strings.Contains(last.text, "Вопрос 1 из 2") {
		t.Fatalf("previous question not shown: %q", last.text)
	}
	if len(api.messages) != 1 {
		t.Fatalf("ordinary navigation created duplicate main messages: %d", len(api.messages))
	}
	for _, edit := range api.edits {
		if edit.messageID != messageID {
			t.Fatalf("edit used another message ID: %+v", edit)
		}
	}
}

func TestLessonWithoutQuestionsShowsFriendlyMessage(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	detail := catalog.details[100]
	detail.Questions = nil
	detail.Lesson.QuestionCount = 0
	catalog.details[100] = detail
	catalog.lessons[10][0].QuestionCount = 0
	bot := newTestBot(api, students, catalog, learning)
	user := testUser()
	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	messageID := api.messages[0].messageID

	bot.handleCallback(context.Background(), testCallback("lesson:10:100", user, messageID))

	last := lastEdit(t, api)
	if !strings.Contains(last.text, "не содержит вопросов") {
		t.Fatalf("empty lesson message missing: %q", last.text)
	}
	if learning.startCalls != 0 {
		t.Fatalf("empty lesson must not create progress, calls=%d", learning.startCalls)
	}
	if !hasButton(last.markup, "lessons:10") {
		t.Fatal("empty lesson should have a return-to-lessons button")
	}
}

func TestEmptyCourseShowsFriendlyMessage(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	catalog.lessons[10] = nil
	bot := newTestBot(api, students, catalog, learning)
	user := testUser()
	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	bot.handleCallback(context.Background(), testCallback("course:10", user, api.messages[0].messageID))

	last := lastEdit(t, api)
	if !strings.Contains(last.text, "нет опубликованных уроков") {
		t.Fatalf("empty course message missing: %q", last.text)
	}
	if !hasButton(last.markup, "courses") {
		t.Fatal("empty course should provide navigation to courses")
	}
}

func TestInvalidCallbackIsAcknowledgedAndShowsCatalog(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	bot := newTestBot(api, students, catalog, learning)
	user := testUser()
	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))

	bot.handleCallback(context.Background(), testCallback("course:not-an-id", user, api.messages[0].messageID))

	if len(api.answered) != 1 || api.answered[0] != "callback-test" {
		t.Fatalf("callback was not acknowledged: %+v", api.answered)
	}
	if !strings.Contains(lastEdit(t, api).text, "Выберите курс") {
		t.Fatalf("stale callback did not return to catalog: %q", lastEdit(t, api).text)
	}
}

func TestLessonFromAnotherCourseCannotBeOpened(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	bot := newTestBot(api, students, catalog, learning)
	user := testUser()
	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))

	bot.handleCallback(context.Background(), testCallback("lesson:20:100", user, api.messages[0].messageID))

	last := lastEdit(t, api)
	if !strings.Contains(last.text, "недоступен в выбранном курсе") {
		t.Fatalf("course-membership validation did not reject lesson: %q", last.text)
	}
	if learning.startCalls != 0 {
		t.Fatalf("foreign lesson should not start an enrollment, start calls=%d", learning.startCalls)
	}
}

func TestStaleNavigationDoesNotSkipQuestion(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	bot := newTestBot(api, students, catalog, learning)
	user := testUser()
	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	messageID := api.messages[0].messageID
	bot.handleCallback(context.Background(), testCallback("lesson:10:100", user, messageID))
	bot.handleCallback(context.Background(), testCallback("nav:next:701:1001:10", user, messageID))

	// Повторно доставленная старая кнопка не должна переводить ученика с вопроса 2 на другой вопрос.
	bot.handleCallback(context.Background(), testCallback("nav:next:701:1001:10", user, messageID))
	if !strings.Contains(lastEdit(t, api).text, "Вопрос 2 из 2") {
		t.Fatalf("stale callback changed the saved position: %q", lastEdit(t, api).text)
	}
}

func TestProgressPositionRestoredAfterNewBotInstance(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	bot := newTestBot(api, students, catalog, learning)
	user := testUser()
	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	messageID := api.messages[0].messageID
	bot.handleCallback(context.Background(), testCallback("lesson:10:100", user, messageID))
	bot.handleCallback(context.Background(), testCallback("nav:next:701:1001:10", user, messageID))

	// Новый обработчик получает тот же сервис состояния, как после перезапуска приложения с общей PostgreSQL.
	restartedBot := newTestBot(api, students, catalog, learning)
	restartedBot.handleUpdate(context.Background(), testUpdate(2, "/start", user))
	newMessageID := api.messages[len(api.messages)-1].messageID
	restartedBot.handleCallback(context.Background(), testCallback("course:10", user, newMessageID))
	restartedBot.handleCallback(context.Background(), testCallback("lesson:10:100", user, newMessageID))

	last := lastEdit(t, api)
	if !strings.Contains(last.text, "Вопрос 2 из 2") {
		t.Fatalf("saved question position was not restored: %q", last.text)
	}
}

func TestDatabaseErrorDoesNotLeakDetailsToStudent(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	catalog.listErr = errors.New("postgres: private detail")
	bot := newTestBot(api, students, catalog, learning)

	bot.handleUpdate(context.Background(), testUpdate(1, "/start", testUser()))

	if len(api.messages) != 1 {
		t.Fatalf("expected a safe response, messages=%d", len(api.messages))
	}
	if strings.Contains(api.messages[0].text, "private detail") || !strings.Contains(api.messages[0].text, "Не удалось загрузить каталог") {
		t.Fatalf("database details leaked or friendly message missing: %q", api.messages[0].text)
	}
}

func TestTelegramEditErrorCreatesReplacementMessage(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	bot := newTestBot(api, students, catalog, learning)
	bot.handleUpdate(context.Background(), testUpdate(1, "/start", testUser()))
	api.editErr = &APIError{Code: 400, Description: "Bad Request: message to edit not found"}

	bot.handleCallback(context.Background(), testCallback("course:10", testUser(), api.messages[0].messageID))

	if len(api.messages) != 2 {
		t.Fatalf("failed edit should fall back to one new main message, got %d", len(api.messages))
	}
	if !hasButton(api.messages[1].markup, "lesson:10:100") {
		t.Fatalf("replacement screen lost its buttons: %+v", api.messages[1].markup)
	}
}

func TestMessageNotModifiedIsNotFatalAndCreatesNoDuplicate(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	bot := newTestBot(api, students, catalog, learning)
	bot.handleUpdate(context.Background(), testUpdate(1, "/start", testUser()))
	api.editErr = &APIError{Code: 400, Description: "Bad Request: message is not modified"}

	bot.handleCallback(context.Background(), testCallback("courses", testUser(), api.messages[0].messageID))

	if len(api.messages) != 1 {
		t.Fatalf("message-not-modified should not create a replacement, messages=%d", len(api.messages))
	}
}

func TestHelpAndCancelCommands(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	bot := newTestBot(api, students, catalog, learning)

	bot.handleUpdate(context.Background(), testUpdate(1, "/help", testUser()))
	bot.handleUpdate(context.Background(), testUpdate(2, "/cancel", testUser()))

	if len(api.messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(api.messages))
	}
	if !strings.Contains(api.messages[0].text, "/start") || !strings.Contains(api.messages[1].text, "нет активного действия") {
		t.Fatalf("help/cancel responses are unexpected: %+v", api.messages)
	}
}

func TestStartRegistrationErrorReturnsSafeMessage(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{err: errors.New("database connection refused")}
	catalog, learning := fixture()
	bot := newTestBot(api, students, catalog, learning)

	bot.handleUpdate(context.Background(), testUpdate(1, "/start", testUser()))

	if len(api.messages) != 1 || strings.Contains(api.messages[0].text, "database") ||
		!strings.Contains(api.messages[0].text, "Попробуйте ещё раз") {
		t.Fatalf("safe registration error message missing: %+v", api.messages)
	}
}

func TestInvalidUpdatesAreIgnored(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	bot := newTestBot(api, students, catalog, learning)

	bot.handleUpdate(context.Background(), Update{UpdateID: 1})
	bot.handleUpdate(context.Background(), Update{UpdateID: 2, Message: &Message{Chat: Chat{ID: 10, Type: "private"}, Text: "/start"}})
	bot.handleUpdate(context.Background(), testUpdate(3, "/start", User{ID: 10}))

	if students.calls != 1 {
		t.Errorf("registration calls = %d, want 1 for the valid update only", students.calls)
	}
}

func TestRunStopsOnContextCancellation(t *testing.T) {
	api := &fakeAPI{blockUpdates: true, updatesStarted: make(chan struct{}, 1)}
	catalog, learning := fixture()
	bot := newTestBot(api, &fakeStudents{}, catalog, learning)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)

	go func() {
		done <- bot.Run(ctx)
	}()
	select {
	case <-api.updatesStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("long polling did not start")
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error during cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
}

func TestRunStopsOnPollingConflict(t *testing.T) {
	api := &fakeAPI{updateErr: &APIError{Code: 409, Description: "Conflict: another getUpdates request"}}
	catalog, learning := fixture()
	bot := newTestBot(api, &fakeStudents{}, catalog, learning)

	err := bot.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("Run error = %v, want a fatal 409 polling-conflict error", err)
	}
}

func TestHTTPClientHandlesTelegramAuthorizationErrorWithoutLeakingToken(t *testing.T) {
	const token = "secret-token-for-test"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": false, "error_code": 401, "description": "Unauthorized",
		})
	}))
	defer server.Close()

	client := newHTTPClient(token, server.URL, server.Client())
	_, err := client.GetMe(context.Background())
	if err == nil {
		t.Fatal("expected authorization error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected API error: %v", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatal("API error exposed the bot token")
	}
}

func TestHTTPClientSanitizesNetworkErrors(t *testing.T) {
	const token = "secret-token-for-test"
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	serverURL := server.URL
	server.Close()

	client := newHTTPClient(token, serverURL, &http.Client{Timeout: time.Second})
	_, err := client.GetMe(context.Background())
	if err == nil {
		t.Fatal("expected network error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatal("network error exposed the bot token")
	}
}

func hasButton(markup *InlineKeyboardMarkup, callbackData string) bool {
	if markup == nil {
		return false
	}
	for _, row := range markup.InlineKeyboard {
		for _, item := range row {
			if item.CallbackData == callbackData {
				return true
			}
		}
	}
	return false
}

func hasButtonPrefix(markup *InlineKeyboardMarkup, prefix string) bool {
	if markup == nil {
		return false
	}
	for _, row := range markup.InlineKeyboard {
		for _, item := range row {
			if strings.HasPrefix(item.CallbackData, prefix) {
				return true
			}
		}
	}
	return false
}

func lastEdit(t *testing.T, api *fakeAPI) editedMessage {
	t.Helper()
	if len(api.edits) == 0 {
		t.Fatal("expected an edited message")
	}
	return api.edits[len(api.edits)-1]
}
