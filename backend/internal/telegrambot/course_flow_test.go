package telegrambot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type fakeCourseLearning struct {
	*fakeLearning

	courseMu         sync.Mutex
	runsByStudent    map[int64]domain.CourseEnrollmentProgress
	lessonsByRun     map[int64][]domain.CourseEnrollmentLesson
	nextRunID        int64
	startCourseCalls int
	curatorAvailable bool
}

func newFakeCourseLearning(catalog *fakeCatalog) *fakeCourseLearning {
	return &fakeCourseLearning{
		fakeLearning:     newFakeLearning(catalog),
		runsByStudent:    make(map[int64]domain.CourseEnrollmentProgress),
		lessonsByRun:     make(map[int64][]domain.CourseEnrollmentLesson),
		nextRunID:        800,
		curatorAvailable: true,
	}
}

func (f *fakeCourseLearning) StartCourse(
	ctx context.Context,
	studentID, courseID int64,
	mode string,
) (domain.CourseEnrollmentProgress, error) {
	f.courseMu.Lock()
	defer f.courseMu.Unlock()
	f.startCourseCalls++

	if mode != "ai" && mode != "curator" {
		return domain.CourseEnrollmentProgress{}, domain.ErrInvalidInput
	}
	if existing, ok := f.runsByStudent[studentID]; ok {
		if existing.Enrollment.CourseID != courseID {
			return existing, domain.ErrActiveCourse
		}
		// A repeated request must return the immutable mode and run ID.
		return existing, nil
	}
	if mode == "curator" && !f.curatorAvailable {
		return domain.CourseEnrollmentProgress{}, domain.ErrNoCurator
	}

	course, err := f.catalog.GetCourse(ctx, courseID)
	if err != nil {
		return domain.CourseEnrollmentProgress{}, err
	}
	lessons, err := f.catalog.ListCourseLessons(ctx, courseID)
	if err != nil {
		return domain.CourseEnrollmentProgress{}, err
	}
	if len(lessons) == 0 {
		return domain.CourseEnrollmentProgress{}, domain.ErrInvalidState
	}

	f.nextRunID++
	runID := f.nextRunID
	run := domain.CourseEnrollmentProgress{
		Enrollment: domain.CourseEnrollment{
			ID: runID, StudentID: studentID, CourseID: courseID,
			CourseTitle: course.Title, RunNumber: 1, Mode: mode, Status: "in_progress",
		},
		TotalLessons: len(lessons), RemainingLessons: len(lessons),
	}
	runLessons := make([]domain.CourseEnrollmentLesson, 0, len(lessons))
	for _, lesson := range lessons {
		runLessons = append(runLessons, domain.CourseEnrollmentLesson{
			LessonID: lesson.ID, LessonVersionID: lesson.LessonVersionID,
			Position: lesson.Position, Title: lesson.Title,
			Status: "not_started", TotalQuestions: lesson.QuestionCount,
		})
	}
	f.runsByStudent[studentID] = run
	f.lessonsByRun[runID] = runLessons
	return run, nil
}

func (f *fakeCourseLearning) ActiveCourse(
	_ context.Context,
	studentID int64,
) (domain.CourseEnrollmentProgress, error) {
	f.courseMu.Lock()
	defer f.courseMu.Unlock()
	run, ok := f.runsByStudent[studentID]
	if !ok || run.Enrollment.Status == "completed" {
		return domain.CourseEnrollmentProgress{}, domain.ErrNotFound
	}
	return run, nil
}

func (f *fakeCourseLearning) CourseLessons(
	_ context.Context,
	studentID, courseEnrollmentID int64,
) ([]domain.CourseEnrollmentLesson, error) {
	f.courseMu.Lock()
	defer f.courseMu.Unlock()
	run, ok := f.runsByStudent[studentID]
	if !ok || run.Enrollment.ID != courseEnrollmentID {
		return nil, domain.ErrNotFound
	}
	return append([]domain.CourseEnrollmentLesson(nil), f.lessonsByRun[courseEnrollmentID]...), nil
}

func (f *fakeCourseLearning) StartLessonForCourse(
	ctx context.Context,
	studentID, courseEnrollmentID, courseID, lessonID int64,
) (domain.Progress, error) {
	f.courseMu.Lock()
	run, ok := f.runsByStudent[studentID]
	if !ok || run.Enrollment.ID != courseEnrollmentID ||
		run.Enrollment.CourseID != courseID || run.Enrollment.Status != "in_progress" {
		f.courseMu.Unlock()
		return domain.Progress{}, domain.ErrInvalidState
	}
	index := -1
	for i := range f.lessonsByRun[courseEnrollmentID] {
		if f.lessonsByRun[courseEnrollmentID][i].LessonID == lessonID {
			index = i
			break
		}
	}
	if index < 0 {
		f.courseMu.Unlock()
		return domain.Progress{}, domain.ErrNotFound
	}
	mode := run.Enrollment.Mode
	f.courseMu.Unlock()

	progress, err := f.fakeLearning.StartLesson(ctx, studentID, lessonID, mode)
	if err != nil {
		return domain.Progress{}, err
	}

	f.courseMu.Lock()
	lessons := f.lessonsByRun[courseEnrollmentID]
	lessons[index].Status = "in_progress"
	lessonEnrollmentID := progress.Enrollment.ID
	lessons[index].EnrollmentID = &lessonEnrollmentID
	f.lessonsByRun[courseEnrollmentID] = lessons
	f.courseMu.Unlock()
	return progress, nil
}

func newCourseTestBot(
	api *fakeAPI,
	students *fakeStudents,
	catalog *fakeCatalog,
	learning *fakeCourseLearning,
) *Bot {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewBot(api, students, catalog, learning, logger)
}

func findButton(markup *InlineKeyboardMarkup, callbackData string) (InlineKeyboardButton, bool) {
	if markup == nil {
		return InlineKeyboardButton{}, false
	}
	for _, row := range markup.InlineKeyboard {
		for _, item := range row {
			if item.CallbackData == callbackData {
				return item, true
			}
		}
	}
	return InlineKeyboardButton{}, false
}

func TestCourseModeChoiceStartsAICourseAndFirstLesson(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, _ := fixture()
	learning := newFakeCourseLearning(catalog)
	bot := newCourseTestBot(api, students, catalog, learning)
	user := testUser()

	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	messageID := api.messages[0].messageID
	bot.handleCallback(context.Background(), testCallback("course:10", user, messageID))

	choice := lastEdit(t, api)
	if !strings.Contains(choice.text, "Как вы хотите проходить этот курс?") ||
		!strings.Contains(choice.text, "специалист будет проверять ваши ответы") ||
		!strings.Contains(choice.text, "искусственный интеллект будет проверять ваши ответы") {
		t.Fatalf("mode choice screen is incomplete: %q", choice.text)
	}
	curatorButton, ok := findButton(choice.markup, "course:start:curator:10")
	if !ok || curatorButton.Text != "⭐ С куратором" {
		t.Fatalf("curator choice must have a star: %+v", curatorButton)
	}
	if !hasButton(choice.markup, "course:start:ai:10") {
		t.Fatal("AI choice is missing")
	}

	bot.handleCallback(context.Background(), testCallback("course:start:ai:10", user, messageID))
	firstQuestion := lastEdit(t, api)
	if !strings.Contains(firstQuestion.text, "ВОПРОС 1 / 2") ||
		!strings.Contains(firstQuestion.text, "Вы выбрали прохождение курса с ИИ") ||
		!strings.Contains(firstQuestion.text, "Кто написал Евангелие от Марка?") {
		t.Fatalf("AI course did not open the first question: %q", firstQuestion.text)
	}
	active, err := learning.ActiveCourse(context.Background(), students.byID[user.ID].ID)
	if err != nil || active.Enrollment.Mode != "ai" || active.Enrollment.CourseID != 10 {
		t.Fatalf("backend course mode is not restored as AI: run=%+v err=%v", active, err)
	}
	if learning.startCourseCalls != 1 || learning.nextRunID != 801 {
		t.Fatalf("unexpected course creation count: calls=%d run_id=%d", learning.startCourseCalls, learning.nextRunID)
	}
}

func TestCuratorModeFailureDoesNotCreateRunOrSwitchToAI(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, _ := fixture()
	learning := newFakeCourseLearning(catalog)
	learning.curatorAvailable = false
	bot := newCourseTestBot(api, students, catalog, learning)
	user := testUser()

	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	messageID := api.messages[0].messageID
	bot.handleCallback(context.Background(), testCallback("course:10", user, messageID))
	bot.handleCallback(context.Background(), testCallback("course:start:curator:10", user, messageID))

	last := lastEdit(t, api)
	if !strings.Contains(last.text, "вам ещё не назначен активный куратор") ||
		!strings.Contains(last.text, "Ваш выбор не изменён") {
		t.Fatalf("curator failure is not explained safely: %q", last.text)
	}
	if _, err := learning.ActiveCourse(context.Background(), students.byID[user.ID].ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("course run must not exist after failed curator assignment, err=%v", err)
	}
	if learning.nextRunID != 800 {
		t.Fatalf("failed curator selection must not create a run: nextRunID=%d", learning.nextRunID)
	}
	if !hasButton(last.markup, "course:10") {
		t.Fatal("student must be able to retry the same choice after assignment")
	}
}

func TestStartRestoresSavedCourseAndQuestionAfterBotRestart(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, _ := fixture()
	learning := newFakeCourseLearning(catalog)
	user := testUser()

	firstBot := newCourseTestBot(api, students, catalog, learning)
	firstBot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	messageID := api.messages[0].messageID
	firstBot.handleCallback(context.Background(), testCallback("course:10", user, messageID))
	firstBot.handleCallback(context.Background(), testCallback("course:start:ai:10", user, messageID))
	firstBot.handleCallback(context.Background(), testCallback("nav:next:701:1001:10", user, messageID))
	if !strings.Contains(lastEdit(t, api).text, "ВОПРОС 2 / 2") {
		t.Fatal("test setup did not advance to the second question")
	}

	// A new bot instance shares only the database-backed service state.
	restartedBot := newCourseTestBot(api, students, catalog, learning)
	restartedBot.handleUpdate(context.Background(), testUpdate(2, "/start", user))
	home := api.messages[len(api.messages)-1]
	if !strings.Contains(home.text, "Ваш текущий курс: „Курс о Евангелиях“") ||
		!strings.Contains(home.text, "Прогресс: 0 из 1 уроков завершено") ||
		!strings.Contains(home.text, "Способ обучения: с ИИ") ||
		!hasButton(home.markup, "course:continue") {
		t.Fatalf("restart did not restore the current course: %+v", home)
	}

	restartedBot.handleCallback(context.Background(), testCallback("course:continue", user, home.messageID))
	continued := lastEdit(t, api)
	if !strings.Contains(continued.text, "ВОПРОС 2 / 2") ||
		!strings.Contains(continued.text, "Как начинается повествование?") {
		t.Fatalf("course continuation did not restore the exact question: %q", continued.text)
	}
	active, err := learning.ActiveCourse(context.Background(), students.byID[user.ID].ID)
	if err != nil || active.Enrollment.ID != 801 || active.Enrollment.Mode != "ai" {
		t.Fatalf("resume created a duplicate or changed mode: run=%+v err=%v", active, err)
	}
	if learning.nextRunID != 801 {
		t.Fatalf("resume created another course run, nextRunID=%d", learning.nextRunID)
	}
}

func TestSecondCourseIsBlockedButCatalogRemainsAvailable(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, _ := fixture()
	learning := newFakeCourseLearning(catalog)
	bot := newCourseTestBot(api, students, catalog, learning)
	user := testUser()

	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	messageID := api.messages[0].messageID
	bot.handleCallback(context.Background(), testCallback("course:10", user, messageID))
	bot.handleCallback(context.Background(), testCallback("course:start:ai:10", user, messageID))

	bot.handleCallback(context.Background(), testCallback("courses", user, messageID))
	catalogScreen := lastEdit(t, api)
	if !hasButton(catalogScreen.markup, "course:10") || !hasButton(catalogScreen.markup, "course:20") {
		t.Fatalf("catalog must remain available during an active course: %+v", catalogScreen.markup)
	}

	bot.handleCallback(context.Background(), testCallback("course:20", user, messageID))
	blocked := lastEdit(t, api)
	if !strings.Contains(blocked.text, "У вас уже есть незавершённый курс „Курс о Евангелиях“. Сначала завершите его, чтобы перейти к следующему") ||
		!hasButton(blocked.markup, "course:continue") {
		t.Fatalf("second course was not blocked with the expected message: %q", blocked.text)
	}
	if learning.nextRunID != 801 {
		t.Fatalf("second course attempt created a duplicate run: nextRunID=%d", learning.nextRunID)
	}
}

func TestRedeliveredModeCallbackReusesTheSameCourseRun(t *testing.T) {
	api := &fakeAPI{}
	students := &fakeStudents{}
	catalog, _ := fixture()
	learning := newFakeCourseLearning(catalog)
	bot := newCourseTestBot(api, students, catalog, learning)
	user := testUser()

	bot.handleUpdate(context.Background(), testUpdate(1, "/start", user))
	messageID := api.messages[0].messageID
	bot.handleCallback(context.Background(), testCallback("course:10", user, messageID))
	callback := testCallback("course:start:ai:10", user, messageID)
	update := Update{UpdateID: 77, CallbackQuery: &callback}

	if !bot.handleUpdate(context.Background(), update) || !bot.handleUpdate(context.Background(), update) {
		t.Fatal("redelivered callback update should be safely acknowledged")
	}
	active, err := learning.ActiveCourse(context.Background(), students.byID[user.ID].ID)
	if err != nil || active.Enrollment.ID != 801 || active.Enrollment.Mode != "ai" {
		t.Fatalf("duplicate update changed the saved course run: run=%+v err=%v", active, err)
	}
	if learning.nextRunID != 801 {
		t.Fatalf("duplicate Telegram update created another course run: nextRunID=%d", learning.nextRunID)
	}
	if learning.nextEnrollmentID != 701 || len(learning.progress) != 1 {
		t.Fatalf("duplicate callback created another lesson enrollment: next_id=%d progress_records=%d", learning.nextEnrollmentID, len(learning.progress))
	}
	if learning.startCourseCalls != 2 {
		t.Fatalf("both deliveries should be confirmed by backend idempotency, StartCourse calls=%d", learning.startCourseCalls)
	}
}
