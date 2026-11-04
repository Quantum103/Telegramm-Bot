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
	chatID    int64
	messageID int64
	text      string
	markup    *InlineKeyboardMarkup
	parseMode string
}

type editedMessage struct {
	chatID    int64
	messageID int64
	text      string
	markup    *InlineKeyboardMarkup
	parseMode string
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
	answerHook       func()
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

func (f *fakeAPI) SendMessageWithKeyboard(ctx context.Context, chatID int64, text string, markup *InlineKeyboardMarkup) (int64, error) {
	return f.sendMessageWithKeyboard(chatID, text, markup, "")
}

func (f *fakeAPI) SendHTMLMessageWithKeyboard(ctx context.Context, chatID int64, text string, markup *InlineKeyboardMarkup) (int64, error) {
	return f.sendMessageWithKeyboard(chatID, text, markup, "HTML")
}

func (f *fakeAPI) sendMessageWithKeyboard(chatID int64, text string, markup *InlineKeyboardMarkup, parseMode string) (int64, error) {
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
	f.messages = append(f.messages, sentMessage{chatID: chatID, messageID: messageID, text: text, markup: markup, parseMode: parseMode})
	return messageID, nil
}

func (f *fakeAPI) EditMessageText(ctx context.Context, chatID, messageID int64, text string, markup *InlineKeyboardMarkup) error {
	return f.editMessageText(chatID, messageID, text, markup, "")
}

func (f *fakeAPI) EditHTMLMessageText(ctx context.Context, chatID, messageID int64, text string, markup *InlineKeyboardMarkup) error {
	return f.editMessageText(chatID, messageID, text, markup, "HTML")
}

func (f *fakeAPI) editMessageText(chatID, messageID int64, text string, markup *InlineKeyboardMarkup, parseMode string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.editErr != nil {
		return f.editErr
	}
	f.edits = append(f.edits, editedMessage{chatID: chatID, messageID: messageID, text: text, markup: markup, parseMode: parseMode})
	return nil
}

func (f *fakeAPI) AnswerCallbackQuery(_ context.Context, callbackID, _ string) error {
	f.mu.Lock()
	f.answered = append(f.answered, callbackID)
	hook := f.answerHook
	err := f.answerErr
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}

type fakeStudents struct {
	mu     sync.Mutex
	calls  int
	byID   map[int64]domain.Student
	params []domain.EnsureStudentParams
	err    error
}

type fakeStudentReferralAssigner struct {
	calls     int
	studentID []int64
	codes     []string
	result    domain.ReferralAssignmentResult
	err       error
}

func (f *fakeStudentReferralAssigner) AssignStudentByReferral(_ context.Context, studentID int64, code string) (domain.ReferralAssignmentResult, error) {
	f.calls++
	f.studentID = append(f.studentID, studentID)
	f.codes = append(f.codes, code)
	return f.result, f.err
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
	courses     []domain.Course
	lessons     map[int64][]domain.Lesson
	details     map[int64]domain.LessonDetail
	listErr     error
	courseErr   error
	lessonsErr  error
	detailErr   error
	detailCalls []int64
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
	completeCalls    int
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

func (f *fakeLearning) CompleteLesson(_ context.Context, studentID, enrollmentID int64) (domain.Enrollment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	progress, ok := f.progress[enrollmentID]
	if !ok || progress.Enrollment.StudentID != studentID {
		return domain.Enrollment{}, domain.ErrNotFound
	}
	if progress.Enrollment.Status != "in_progress" {
		return domain.Enrollment{}, domain.ErrInvalidState
	}
	f.completeCalls++
	progress.Enrollment.Status = "completed"
	completedAt := time.Now()
	progress.Enrollment.CompletedAt = &completedAt
	f.progress[enrollmentID] = progress
	return progress.Enrollment, nil
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
		Lesson:    catalog.lessons[20][0],
		Questions: []domain.LessonQuestion{{ID: 2001, LessonVersionID: 601, QuestionNumber: 1, Prompt: "Что такое молитва?", AnswerType: "text"}},
	}
	return catalog, newFakeLearning(catalog)
}

func newTestBot(api *fakeAPI, students *fakeStudents, catalog *fakeCatalog, learning *fakeLearning) *Bot {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewBot(api, students, catalog, learning, logger)
}

type fakeChapterPublisher struct {
	url              string
	err              error
	references       []ChapterReference
	cachedReferences []ChapterReference
	publishCalls     int
}

func (f *fakeChapterPublisher) PublishChapter(_ context.Context, reference ChapterReference) (string, error) {
	f.publishCalls++
	f.references = append(f.references, reference)
	return f.url, f.err
}

func (f *fakeChapterPublisher) CachedChapterURL(_ context.Context, reference ChapterReference) (string, error) {
	f.cachedReferences = append(f.cachedReferences, reference)
	return f.url, f.err
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
		ID:      "callback-test",
		From:    &user,
		Message: &Message{MessageID: messageID, Chat: Chat{ID: user.ID, Type: "private"}},
		Data:    data,
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

func TestStartWithReferralAssignsCuratorWithoutChangingCourseMode(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	bot := newTestBot(api, students, catalog, learning)
	referrals := &fakeStudentReferralAssigner{
		result: domain.ReferralAssignmentResult{CuratorStaffID: 42, Assigned: true},
	}
	bot.studentReferrals = referrals
	user := testUser()

	bot.handleUpdate(context.Background(), testUpdate(1, "/start c_42", user))

	if referrals.calls != 1 || len(referrals.studentID) != 1 || referrals.studentID[0] != 1 || referrals.codes[0] != "c_42" {
		t.Fatalf("referral was not applied to the registered student: %+v", referrals)
	}
	if len(api.messages) != 1 || !strings.Contains(api.messages[0].text, "Куратор закреплён за вами") {
		t.Fatalf("successful referral result missing: %+v", api.messages)
	}
	if !strings.Contains(api.messages[0].text, "не меняет способ прохождения курса") {
		t.Fatalf("referral must not select or change the course mode: %q", api.messages[0].text)
	}
	if !hasButton(api.messages[0].markup, "course:10") || learning.startCalls != 0 {
		t.Fatalf("referral must show the catalog without starting a course: message=%+v starts=%d", api.messages[0], learning.startCalls)
	}
}

func TestStartWithInvalidReferralKeepsNormalCatalogFlow(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	bot := newTestBot(api, students, catalog, learning)
	referrals := &fakeStudentReferralAssigner{err: domain.ErrNotFound}
	bot.studentReferrals = referrals

	bot.handleUpdate(context.Background(), testUpdate(1, "/start c_999", testUser()))

	if referrals.calls != 1 || len(referrals.codes) != 1 || referrals.codes[0] != "c_999" {
		t.Fatalf("invalid referral was not checked: %+v", referrals)
	}
	if len(api.messages) != 1 || !strings.Contains(api.messages[0].text, "недействительна") {
		t.Fatalf("friendly invalid-referral response missing: %+v", api.messages)
	}
	if !hasButton(api.messages[0].markup, "course:10") || learning.startCalls != 0 {
		t.Fatalf("invalid referral must not block the ordinary catalog: message=%+v starts=%d", api.messages[0], learning.startCalls)
	}
}

func TestStartWithReferralConflictDoesNotReplaceCurator(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	bot := newTestBot(api, students, catalog, learning)
	referrals := &fakeStudentReferralAssigner{err: domain.ErrConflict}
	bot.studentReferrals = referrals

	bot.handleUpdate(context.Background(), testUpdate(1, "/start c_42", testUser()))

	if referrals.calls != 1 || !strings.Contains(api.messages[0].text, "Текущее назначение не изменено") {
		t.Fatalf("existing curator assignment was not preserved: %+v", api.messages)
	}
	if !hasButton(api.messages[0].markup, "course:10") || learning.startCalls != 0 {
		t.Fatalf("referral conflict must preserve normal catalog flow: message=%+v starts=%d", api.messages[0], learning.startCalls)
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
	if !strings.Contains(last.text, "Евангелие от Марка") || !strings.Contains(last.text, "ВОПРОС 1 / 2") ||
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

func TestOpeningLessonRendersFormattedPassageAndInlineChapterLink(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()

	passageText := "Он же сказал ему в ответ: написано: не хлебом одним будет жить человек <и словом>."
	detail := catalog.details[100]
	detail.Questions[0].Prompt = "Почему Иисус ответил словами Писания <и не иначе>?"
	detail.Questions[0].BiblePassages = []domain.BiblePassage{{
		BookName: "Матфея", Chapter: 4, StartVerse: 4, EndVerse: 7, PassageText: &passageText,
	}}
	catalog.details[100] = detail

	publisher := &fakeChapterPublisher{url: "https://telegra.ph/Matfeya-4-01-01"}
	bot := NewBotWithChapterPublisher(api, students, catalog, learning, nil, publisher)
	user := testUser()
	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	messageID := api.messages[0].messageID
	bot.handleCallback(context.Background(), testCallback("lesson:10:100", user, messageID))

	last := lastEdit(t, api)
	for _, expected := range []string{
		"<b>ВОПРОС</b>",
		"<blockquote>Почему Иисус ответил словами Писания &lt;и не иначе&gt;?</blockquote>",
		"Откуда взято: Матфея 4:4-7",
		"Библейский текст:",
		"<blockquote>Он же сказал ему в ответ: написано: не хлебом одним будет жить человек &lt;и словом&gt;.</blockquote>",
	} {
		if !strings.Contains(last.text, expected) {
			t.Fatalf("formatted question is missing %q: %s", expected, last.text)
		}
	}
	if strings.Contains(last.text, "bible.by") || strings.Contains(last.text, "<и не иначе>") {
		t.Fatal("question message must not contain an external Bible site URL or unescaped HTML")
	}
	if last.parseMode != "HTML" {
		t.Fatalf("question screen parse mode = %q, want HTML", last.parseMode)
	}

	if !strings.Contains(last.text, `<a href="https://telegra.ph/Matfeya-4-01-01"><u>📖 Прочитать всю главу</u></a>`) {
		t.Fatalf("chapter reader should be an underlined inline link in the message: %s", last.text)
	}
	for _, row := range last.markup.InlineKeyboard {
		for _, item := range row {
			if item.URL != "" {
				t.Fatalf("chapter URL must not be rendered as a keyboard button: %+v", item)
			}
		}
	}
	if len(publisher.cachedReferences) != 1 || publisher.cachedReferences[0].BookID != 40 || publisher.cachedReferences[0].Chapter != 4 {
		t.Fatalf("cached chapter references = %+v, want Matthew 4", publisher.cachedReferences)
	}
	if publisher.publishCalls != 0 {
		t.Fatalf("question rendering called background publisher %d times, want 0", publisher.publishCalls)
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
	if !strings.Contains(last.text, "ВОПРОС 2 / 2") || !strings.Contains(last.text, "Как начинается повествование?") {
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
	if !strings.Contains(last.text, "ВОПРОС 1 / 2") {
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
	if !strings.Contains(lastEdit(t, api).text, "ВОПРОС 2 / 2") {
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
	if !strings.Contains(last.text, "ВОПРОС 2 / 2") {
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

func TestHTTPClientUsesHTMLParseModeAndDisablesLinkPreviews(t *testing.T) {
	var parseModes []string
	var linkPreviewsDisabled []bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			ParseMode          string `json:"parse_mode"`
			LinkPreviewOptions struct {
				IsDisabled bool `json:"is_disabled"`
			} `json:"link_preview_options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode Telegram request: %v", err)
		}
		parseModes = append(parseModes, payload.ParseMode)
		linkPreviewsDisabled = append(linkPreviewsDisabled, payload.LinkPreviewOptions.IsDisabled)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/bottoken/sendMessage" {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()

	client := newHTTPClient("token", server.URL, server.Client())
	if _, err := client.SendHTMLMessageWithKeyboard(context.Background(), 10, "<b>Вопрос</b>", nil); err != nil {
		t.Fatalf("SendHTMLMessageWithKeyboard: %v", err)
	}
	if err := client.EditHTMLMessageText(context.Background(), 10, 42, "<u>Ссылка</u>", nil); err != nil {
		t.Fatalf("EditHTMLMessageText: %v", err)
	}
	if len(parseModes) != 2 || parseModes[0] != "HTML" || parseModes[1] != "HTML" {
		t.Fatalf("parse modes = %#v, want HTML for send and edit", parseModes)
	}
	if len(linkPreviewsDisabled) != 2 || !linkPreviewsDisabled[0] || !linkPreviewsDisabled[1] {
		t.Fatalf("link previews disabled = %#v, want true for both send and edit", linkPreviewsDisabled)
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

func TestLessonCanBeCompletedOnlyAfterAllAnswersAreSaved(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	bot := newTestBot(api, students, catalog, learning)
	user := testUser()
	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	messageID := api.messages[0].messageID
	bot.handleCallback(context.Background(), testCallback("lesson:10:100", user, messageID))

	last := lastEdit(t, api)
	if hasButtonPrefix(last.markup, "lesson:finish:") {
		t.Fatal("кнопка завершения не должна появляться до ответов на все вопросы")
	}

	learning.mu.Lock()
	progress := learning.progress[701]
	progress.AnsweredQuestions = progress.TotalQuestions
	learning.progress[701] = progress
	learning.mu.Unlock()
	bot.handleCallback(context.Background(), testCallback("nav:next:701:1001:10", user, messageID))
	last = lastEdit(t, api)
	if !hasButtonPrefix(last.markup, "lesson:finish:") {
		t.Fatalf("после сохранения всех ответов должна появиться кнопка завершения: %+v", last.markup)
	}
	bot.handleCallback(context.Background(), testCallback("lesson:finish:701", user, messageID))

	last = lastEdit(t, api)
	if learning.completeCalls != 1 || !strings.Contains(last.text, "Урок завершён") ||
		!hasButton(last.markup, "courses") || !hasButton(last.markup, "chat:start") {
		t.Fatalf("завершение должно вернуть экран с рабочими кнопками: calls=%d edit=%+v", learning.completeCalls, last)
	}
}

func TestHTTPClientUsesMethodSpecificTimeouts(t *testing.T) {
	deadlines := make(map[string]time.Duration)
	transport := workerRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok {
			t.Errorf("%s request has no deadline", req.URL.Path)
		} else {
			deadlines[req.URL.Path] = time.Until(deadline)
		}
		body := `{"ok":true,"result":true}`
		if req.URL.Path == "/bottoken/getUpdates" {
			body = `{"ok":true,"result":[]}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})
	client := newHTTPClient("token", "http://telegram.test", &http.Client{Transport: transport})
	if _, err := client.GetUpdates(context.Background(), 0, longPollTimeout); err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if err := client.SendMessage(context.Background(), 10, "обычное сообщение"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if err := client.EditMessageText(context.Background(), 10, 20, "изменить сообщение", nil); err != nil {
		t.Fatalf("EditMessageText: %v", err)
	}
	if err := client.AnswerCallbackQuery(context.Background(), "callback-id", ""); err != nil {
		t.Fatalf("AnswerCallbackQuery: %v", err)
	}

	if got := deadlines["/bottoken/getUpdates"]; got < 34*time.Second || got > 36*time.Second {
		t.Errorf("getUpdates deadline = %s, want approximately 35s", got)
	}
	if got := deadlines["/bottoken/sendMessage"]; got < 9*time.Second || got > 11*time.Second {
		t.Errorf("sendMessage deadline = %s, want approximately 10s", got)
	}
	if got := deadlines["/bottoken/editMessageText"]; got < 9*time.Second || got > 11*time.Second {
		t.Errorf("editMessageText deadline = %s, want approximately 10s", got)
	}
	if got := deadlines["/bottoken/answerCallbackQuery"]; got < 2*time.Second || got > 4*time.Second {
		t.Errorf("answerCallbackQuery deadline = %s, want approximately 3s", got)
	}
}

func TestHTTPClientSlowResponseRespectsEarlierCallerTimeout(t *testing.T) {
	requestSeen := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestSeen <- struct{}{}
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()

	client := newHTTPClient("token", server.URL, server.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	started := time.Now()
	err := client.SendMessage(ctx, 10, "медленный ответ")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendMessage error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("caller timeout was not respected promptly: %s", elapsed)
	}
	select {
	case <-requestSeen:
	case <-time.After(time.Second):
		t.Fatal("HTTP request did not reach the test server")
	}
}

func TestRenderDoesNotFallbackForAmbiguousOrTransientEditErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "network error", err: errors.New("temporary network failure")},
		{name: "request timeout", err: context.DeadlineExceeded},
		{name: "rate limit", err: &APIError{Code: http.StatusTooManyRequests, Description: "Too Many Requests"}},
		{name: "server error", err: &APIError{Code: http.StatusBadGateway, Description: "Bad Gateway"}},
		{name: "message unchanged", err: &APIError{Code: http.StatusBadRequest, Description: "message is not modified"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			api := &fakeAPI{editErr: test.err}
			catalog, learning := fixture()
			bot := newTestBot(api, &fakeStudents{}, catalog, learning)

			bot.render(context.Background(), 123, 456, "экран", keyboard(button("К курсам", "courses")))

			if len(api.messages) != 0 {
				t.Fatalf("неоднозначная/временная ошибка редактирования создала новое сообщение: %+v", api.messages)
			}
		})
	}
}

func TestRenderFallsBackOnlyForExplicitlyUnavailableMessage(t *testing.T) {
	tests := []struct {
		name        string
		description string
	}{
		{name: "message not found", description: "Bad Request: message to edit not found"},
		{name: "message cannot be edited", description: "Bad Request: message can't be edited"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			api := &fakeAPI{editErr: &APIError{Code: http.StatusBadRequest, Description: test.description}}
			catalog, learning := fixture()
			bot := newTestBot(api, &fakeStudents{}, catalog, learning)

			bot.render(context.Background(), 123, 456, "замена", keyboard(button("К курсам", "courses")))

			if len(api.messages) != 1 || api.messages[0].text != "замена" ||
				!hasButton(api.messages[0].markup, "courses") {
				t.Fatalf("после явного отказа редактирования ожидалось одно сообщение с исходной клавиатурой: %+v", api.messages)
			}
		})
	}
}

func TestRenderHTMLDoesNotFallbackAfterAmbiguousEditError(t *testing.T) {
	api := &fakeAPI{editErr: context.DeadlineExceeded}
	catalog, learning := fixture()
	bot := newTestBot(api, &fakeStudents{}, catalog, learning)

	bot.renderHTML(context.Background(), 123, 456, "<b>вопрос</b>", keyboard(button("К курсам", "courses")))

	if len(api.messages) != 0 {
		t.Fatalf("timeout HTML-редактирования создал дублирующее сообщение: %+v", api.messages)
	}
}

func TestRenderDoesNotFallbackWhenConnectionIsLostAfterEditRequest(t *testing.T) {
	requests := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		_, _ = io.Copy(io.Discard, r.Body)
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("test server does not support connection hijacking")
			return
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack connection: %v", err)
			return
		}
		_ = connection.Close()
	}))
	defer server.Close()

	api := newHTTPClient("secret-token", server.URL, server.Client())
	catalog, learning := fixture()
	bot := NewBot(api, &fakeStudents{}, catalog, learning, nil)
	bot.render(context.Background(), 123, 456, "новый экран", keyboard(button("К курсам", "courses")))

	select {
	case path := <-requests:
		if path != "/botsecret-token/editMessageText" {
			t.Fatalf("first request path = %q, want editMessageText", path)
		}
	case <-time.After(time.Second):
		t.Fatal("edit request did not reach test server")
	}

	select {
	case path := <-requests:
		t.Fatalf("after lost edit response an unsafe duplicate request was sent: %q", path)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestHTTPClientClassifiesTelegramAPIResponses(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		code        int
		description string
		wantKind    TelegramErrorKind
	}{
		{name: "rate limited", status: http.StatusTooManyRequests, code: 429, description: "Too Many Requests", wantKind: TelegramErrorRateLimited},
		{name: "server failure", status: http.StatusInternalServerError, code: 500, description: "Internal Server Error", wantKind: TelegramErrorServerFailure},
		{name: "message unchanged", status: http.StatusBadRequest, code: 400, description: "message is not modified", wantKind: TelegramErrorMessageUnchanged},
		{name: "message not found", status: http.StatusBadRequest, code: 400, description: "message to edit not found", wantKind: TelegramErrorMessageNotFound},
		{name: "message cannot be edited", status: http.StatusBadRequest, code: 400, description: "message can't be edited", wantKind: TelegramErrorMessageNotEditable},
		{name: "server text cannot authorize fallback", status: http.StatusInternalServerError, code: 500, description: "message to edit not found", wantKind: TelegramErrorServerFailure},
		{name: "5xx status overrides misleading body code", status: http.StatusInternalServerError, code: 400, description: "message to edit not found", wantKind: TelegramErrorServerFailure},
		{name: "5xx status overrides message unchanged text", status: http.StatusInternalServerError, code: 400, description: "message is not modified", wantKind: TelegramErrorServerFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok": false, "error_code": test.code, "description": test.description,
				})
			}))
			defer server.Close()

			client := newHTTPClient("token", server.URL, server.Client())
			_, err := client.GetMe(context.Background())
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("GetMe error = %v, want *APIError", err)
			}
			if apiErr.HTTPStatus != test.status {
				t.Errorf("HTTP status = %d, want %d", apiErr.HTTPStatus, test.status)
			}
			if got := classifyTelegramAPIError(err); got != test.wantKind {
				t.Errorf("error kind = %q, want %q", got, test.wantKind)
			}
		})
	}
}

func TestNewHTTPClientDoesNotUseOneGlobalTimeout(t *testing.T) {
	client := NewHTTPClient("test-token")
	if client.httpClient.Timeout != 0 {
		t.Fatalf("HTTP client global timeout = %s, want no global timeout; per-method contexts own deadlines", client.httpClient.Timeout)
	}
}

func TestRenderHTMLFallsBackWhenTelegramConfirmsMessageCannotBeEdited(t *testing.T) {
	api := &fakeAPI{editErr: &APIError{
		Code: http.StatusBadRequest, Description: "Bad Request: message can't be edited",
	}}
	catalog, learning := fixture()
	bot := newTestBot(api, &fakeStudents{}, catalog, learning)

	bot.renderHTML(context.Background(), 123, 456, "<b>вопрос</b>", keyboard(button("К курсам", "courses")))

	if len(api.messages) != 1 || api.messages[0].parseMode != "HTML" ||
		api.messages[0].text != "<b>вопрос</b>" || !hasButton(api.messages[0].markup, "courses") {
		t.Fatalf("HTML fallback must preserve formatting and keyboard: %+v", api.messages)
	}
}

type temporaryChatModeFailure struct{}

func (temporaryChatModeFailure) CreateStudentMessage(context.Context, domain.CreateStudentConversationMessageParams) (domain.ConversationMessage, error) {
	return domain.ConversationMessage{}, errors.New("temporary database failure")
}

func (temporaryChatModeFailure) SetStudentChatMode(context.Context, int64, bool) error {
	return nil
}

func (temporaryChatModeFailure) SetStudentLessonMode(context.Context, int64, bool) error {
	return nil
}

func (temporaryChatModeFailure) IsStudentChatMode(context.Context, int64) (bool, error) {
	return false, errors.New("temporary database failure")
}

type replayPollingAPI struct {
	*fakeAPI

	pollMu     sync.Mutex
	offsets    []int64
	secondPoll chan struct{}
	update     Update
}

func (a *replayPollingAPI) GetUpdates(ctx context.Context, offset int64, _ int) ([]Update, error) {
	a.pollMu.Lock()
	a.offsets = append(a.offsets, offset)
	pollNumber := len(a.offsets)
	a.pollMu.Unlock()

	if pollNumber == 1 {
		return []Update{a.update}, nil
	}
	if pollNumber == 2 {
		close(a.secondPoll)
	}

	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRunDoesNotAdvanceOffsetAfterTemporaryProcessingFailure(t *testing.T) {
	api := &replayPollingAPI{
		fakeAPI:    &fakeAPI{},
		secondPoll: make(chan struct{}),
		update:     testUpdate(41, "обычное сообщение", testUser()),
	}
	students := &fakeStudents{}
	catalog, learning := fixture()
	bot := NewBotWithChapterPublisherAndConversations(api, students, catalog, learning, nil, nil, temporaryChatModeFailure{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bot.Run(ctx)
	}()

	select {
	case <-api.secondPoll:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("Run did not retry polling after the temporary processing failure")
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

	api.pollMu.Lock()
	offsets := append([]int64(nil), api.offsets...)
	api.pollMu.Unlock()
	if len(offsets) != 2 {
		t.Fatalf("getUpdates calls = %d, want 2; offsets=%v", len(offsets), offsets)
	}
	if offsets[0] != 0 || offsets[1] != 0 {
		t.Fatalf("offsets = %v, want [0 0]: failed update must remain unconfirmed", offsets)
	}
}

func TestCallbackIsAcknowledgedBeforeBusinessProcessing(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, learning := fixture()
	bot := newTestBot(api, students, catalog, learning)
	api.answerHook = func() {
		if students.calls != 0 {
			t.Errorf("student lookup started before callback acknowledgement")
		}
	}

	user := testUser()
	callback := testCallback("unknown:action", user, 1)
	callback.ID = "callback-early-ack"
	update := Update{UpdateID: 9100, CallbackQuery: &callback}
	if handled := bot.handleUpdate(context.Background(), update); !handled {
		t.Fatal("unknown callback should be treated as a terminal update")
	}
	if len(api.answered) != 1 || api.answered[0] != callback.ID {
		t.Fatalf("callback acknowledgement calls = %v, want exactly [%q]", api.answered, callback.ID)
	}
	if students.calls != 1 {
		t.Fatalf("business processing did not run after acknowledgement: student lookups=%d", students.calls)
	}
}

func TestCallbackAcknowledgementIsNotRepeatedWhenBusinessUpdateRetries(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{err: errors.New("temporary database failure")}
	catalog, learning := fixture()
	bot := newTestBot(api, students, catalog, learning)

	user := testUser()
	callback := testCallback("courses", user, 1)
	callback.ID = "callback-business-retry"
	update := Update{UpdateID: 9101, CallbackQuery: &callback}
	for delivery := 0; delivery < 2; delivery++ {
		if handled := bot.handleUpdate(context.Background(), update); handled {
			t.Fatalf("delivery %d should remain unconfirmed after temporary registration error", delivery+1)
		}
	}
	if students.calls != 2 {
		t.Fatalf("business processing attempts = %d, want 2", students.calls)
	}
	if len(api.answered) != 1 || api.answered[0] != callback.ID {
		t.Fatalf("callback was acknowledged more than once: %v", api.answered)
	}
}

func TestConcurrentCallbackAcknowledgementIsClaimedOnce(t *testing.T) {
	api := &fakeAPI{}
	catalog, learning := fixture()
	bot := newTestBot(api, &fakeStudents{}, catalog, learning)
	callback := CallbackQuery{ID: "callback-concurrent-ack"}

	const deliveries = 16
	var wait sync.WaitGroup
	wait.Add(deliveries)
	for i := 0; i < deliveries; i++ {
		go func() {
			defer wait.Done()
			bot.acknowledgeCallback(context.Background(), callback)
		}()
	}
	wait.Wait()

	if len(api.answered) != 1 || api.answered[0] != callback.ID {
		t.Fatalf("concurrent acknowledgement calls = %v, want exactly one", api.answered)
	}
}

func TestCallbackAcknowledgementCacheIsBounded(t *testing.T) {
	api := &fakeAPI{}
	catalog, learning := fixture()
	bot := newTestBot(api, &fakeStudents{}, catalog, learning)
	for i := 0; i < callbackAckCacheCapacity+1; i++ {
		bot.claimCallbackAcknowledgement(fmt.Sprintf("callback-%d", i))
	}
	if got := len(bot.callbackAckSeen); got != callbackAckCacheCapacity {
		t.Fatalf("callback acknowledgement cache size = %d, want %d", got, callbackAckCacheCapacity)
	}
	if _, exists := bot.callbackAckSeen["callback-0"]; exists {
		t.Fatal("oldest callback acknowledgement was not evicted from bounded cache")
	}
}

