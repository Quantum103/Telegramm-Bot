package telegrambot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/service"
)

const maxAnswerRunes = 4096

type StudentService interface {
	EnsureByTelegramID(context.Context, domain.EnsureStudentParams) (domain.Student, error)
	GetByTelegramID(context.Context, int64) (domain.Student, error)
}

type CatalogService interface {
	ListCourses(context.Context) ([]domain.Course, error)
	GetCourse(context.Context, int64) (domain.Course, error)
	ListCourseLessons(context.Context, int64) ([]domain.Lesson, error)
	GetLessonDetailInCourse(context.Context, int64, int64) (domain.LessonDetail, error)
}

type LearningService interface {
	StartLesson(context.Context, int64, int64, string) (domain.Progress, error)
	GetProgress(context.Context, int64, int64) (domain.Progress, error)
	FirstQuestion(context.Context, int64, int64) (service.NavigationState, error)
	OpenQuestion(context.Context, int64, int64, int64) (service.NavigationState, error)
	NextQuestion(context.Context, int64, int64) (service.NavigationState, error)
	PreviousQuestion(context.Context, int64, int64) (service.NavigationState, error)
	SubmitAnswer(context.Context, domain.AnswerSubmission) (service.SubmissionResult, error)
	CompleteLesson(context.Context, int64, int64) (domain.Enrollment, error)
}

type StateStore interface {
	Get(context.Context, int64) (domain.TelegramDialogState, error)
	Save(context.Context, domain.TelegramDialogState) error
	BeginUpdate(context.Context, int64) (bool, error)
	CompleteUpdate(context.Context, int64) error
	FailUpdate(context.Context, int64) error
}

type Handler struct {
	api      BotAPI
	students StudentService
	catalog  CatalogService
	learning LearningService
	states   StateStore
	logger   *slog.Logger
}

func NewHandler(api BotAPI, students StudentService, catalog CatalogService, learning LearningService, states StateStore, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{api: api, students: students, catalog: catalog, learning: learning, states: states, logger: logger}
}

// HandleUpdate сохраняет отметку update_id до обработки, чтобы повторная доставка не создавала вторую попытку.
func (h *Handler) HandleUpdate(ctx context.Context, update Update) error {
	if update.UpdateID < 0 {
		return nil
	}
	claimed, err := h.states.BeginUpdate(ctx, update.UpdateID)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	if err := h.handleUpdate(ctx, update); err != nil {
		if storeErr := h.states.FailUpdate(ctx, update.UpdateID); storeErr != nil {
			h.logger.Error("не удалось пометить обновление Telegram как неудачное", "update_id", update.UpdateID, "error", storeErr)
		}
		return err
	}
	return h.states.CompleteUpdate(ctx, update.UpdateID)
}

func (h *Handler) handleUpdate(ctx context.Context, update Update) error {
	if update.CallbackQuery != nil {
		if err := h.api.AnswerCallbackQuery(ctx, update.CallbackQuery.ID, ""); err != nil {
			h.logger.Warn("не удалось подтвердить callback Telegram", "error", err)
		}
		return h.handleCallback(ctx, *update.CallbackQuery, update.UpdateID)
	}
	if update.Message == nil || update.Message.From == nil {
		return nil
	}
	message := *update.Message
	if message.Chat.Type != "private" || message.Chat.ID != message.From.ID || message.From.ID <= 0 {
		return nil
	}
	switch parseCommand(message.Text) {
	case "start":
		return h.handleStart(ctx, message)
	case "help":
		return h.handleHelp(ctx, message.From.ID, message.Chat.ID)
	case "cancel":
		return h.handleCancel(ctx, message.From.ID, message.Chat.ID)
	case "":
		if strings.HasPrefix(strings.TrimSpace(message.Text), "/") {
			return h.handleHelp(ctx, message.From.ID, message.Chat.ID)
		}
		return h.handleTextAnswer(ctx, message, update.UpdateID)
	default:
		return h.handleHelp(ctx, message.From.ID, message.Chat.ID)
	}
}

func parseCommand(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return ""
	}
	command := strings.TrimPrefix(fields[0], "/")
	if at := strings.IndexByte(command, '@'); at >= 0 {
		command = command[:at]
	}
	return strings.ToLower(command)
}

func (h *Handler) handleStart(ctx context.Context, message Message) error {
	user := message.From
	if user == nil || user.ID <= 0 {
		return nil
	}
	displayName := strings.TrimSpace(strings.TrimSpace(user.FirstName) + " " + strings.TrimSpace(user.LastName))
	if displayName == "" {
		displayName = fmt.Sprintf("Ученик %d", user.ID)
	}
	var username *string
	if strings.TrimSpace(user.Username) != "" {
		value := strings.TrimSpace(user.Username)
		username = &value
	}
	student, err := h.students.EnsureByTelegramID(ctx, domain.EnsureStudentParams{
		TelegramID: user.ID, DisplayName: displayName, TelegramUsername: username,
	})
	if err != nil {
		h.logger.Error("не удалось зарегистрировать ученика Telegram", "telegram_id", user.ID, "error", err)
		return h.apiErrorMessage(ctx, message.Chat.ID, 0, "Не удалось открыть обучение. Попробуйте ещё раз чуть позже.")
	}
	state, err := h.loadOrNewState(ctx, student.ID, message.Chat.ID)
	if err != nil {
		h.logger.Error("не удалось загрузить состояние Telegram-диалога", "student_id", student.ID, "error", err)
		return err
	}
	state.Screen = "home"
	text, markup := h.homeScreen(ctx, student.ID, state, user.FirstName)
	return h.render(ctx, state, text, markup)
}

func (h *Handler) handleHelp(ctx context.Context, telegramID, chatID int64) error {
	_, state, err := h.studentAndState(ctx, telegramID, chatID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return h.apiErrorMessage(ctx, chatID, 0, "Сначала выполните /start, чтобы зарегистрироваться.")
		}
		return err
	}
	state.Screen = "help"
	state.AwaitingText = false
	return h.render(ctx, state, "Как проходить уроки:\n\n1. Выберите курс, затем урок.\n2. Отвечайте текстом или нажимайте варианты под вопросом. При множественном выборе отметьте пункты и подтвердите их.\n3. Кнопки «Назад» и «Далее» перемещают между вопросами. Сохранённый ответ можно изменить.\n4. Ответы и прогресс сохраняются в PostgreSQL. Автоматическая проверка пока не подключена.\n\nКоманда /cancel отменяет ожидающий ввод, не удаляя ответы.", keyboard(
		[]InlineKeyboardButton{button("▶️ Продолжить обучение", "continue")},
		[]InlineKeyboardButton{button("📚 К курсам", "courses"), button("🏠 Главное меню", "home")},
	))
}

func (h *Handler) handleCancel(ctx context.Context, telegramID, chatID int64) error {
	student, state, err := h.studentAndState(ctx, telegramID, chatID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return h.apiErrorMessage(ctx, chatID, 0, "Нет активного ввода. Выполните /start, чтобы начать.")
		}
		return err
	}
	state.AwaitingText = false
	state.SelectedOptionIDs = []int64{}
	if state.Screen == "question" && state.EnrollmentID != nil && state.QuestionID != nil {
		nav, err := h.learning.OpenQuestion(ctx, student.ID, *state.EnrollmentID, *state.QuestionID)
		if err != nil {
			return h.showOperationError(ctx, state, "открыть текущий вопрос", err)
		}
		return h.showNavigation(ctx, student.ID, state, nav, "Ввод отменён. Сохранённые ответы не удалены.")
	}
	text, markup := h.homeScreen(ctx, student.ID, state, student.DisplayName)
	return h.render(ctx, state, "Ожидающий ввод отменён.\n\n"+text, markup)
}

func (h *Handler) handleTextAnswer(ctx context.Context, message Message, updateID int64) error {
	student, state, err := h.studentAndState(ctx, message.From.ID, message.Chat.ID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return h.apiErrorMessage(ctx, message.Chat.ID, 0, "Чтобы начать обучение, выполните /start.")
		}
		return err
	}
	answer := strings.TrimSpace(message.Text)
	if state.Screen != "question" || !state.AwaitingText || state.EnrollmentID == nil || state.QuestionID == nil {
		return h.render(ctx, state, "Сейчас текстовый ответ не ожидается. Используйте кнопки основного сообщения или нажмите «Ответить текстом».", h.questionOrHomeKeyboard(state))
	}
	if answer == "" {
		return h.render(ctx, state, "Ответ не должен быть пустым. Напишите ответ или используйте /cancel.", h.questionOrHomeKeyboard(state))
	}
	if utf8.RuneCountInString(answer) > maxAnswerRunes {
		return h.render(ctx, state, "Ответ слишком длинный: максимум 4096 символов. Отправьте более короткий ответ или используйте /cancel.", h.questionOrHomeKeyboard(state))
	}
	nav, err := h.learning.OpenQuestion(ctx, student.ID, *state.EnrollmentID, *state.QuestionID)
	if err != nil {
		return h.showOperationError(ctx, state, "проверить текущий вопрос", err)
	}
	if nav.Question.AnswerType != "text" {
		state.AwaitingText = false
		return h.render(ctx, state, "Для этого вопроса нужно выбрать вариант кнопкой. Текст не сохранён как учебный ответ.", h.questionOrHomeKeyboard(state))
	}
	_, err = h.learning.SubmitAnswer(ctx, domain.AnswerSubmission{
		StudentID: student.ID, EnrollmentID: *state.EnrollmentID, QuestionID: *state.QuestionID,
		AnswerText: answer, IdempotencyKey: idempotencyKey(updateID),
	})
	if err != nil {
		h.logger.Error("не удалось сохранить текстовый ответ", "student_id", student.ID, "enrollment_id", *state.EnrollmentID, "question_id", *state.QuestionID, "error", err)
		return h.render(ctx, state, "Не удалось сохранить ответ. Он не подтверждён как сохранённый — отправьте его ещё раз. Ранее сохранённые ответы не затронуты.", h.questionOrHomeKeyboard(state))
	}
	state.AwaitingText = false
	state.SelectedOptionIDs = []int64{}
	if err := h.api.DeleteMessage(ctx, message.Chat.ID, message.MessageID); err != nil {
		h.logger.Warn("ответ сохранён, но исходное сообщение удалить не удалось", "student_id", student.ID, "message_id", message.MessageID, "error", err)
	}
	nav, err = h.learning.OpenQuestion(ctx, student.ID, *state.EnrollmentID, *state.QuestionID)
	if err != nil {
		return h.showOperationError(ctx, state, "восстановить вопрос после сохранения", err)
	}
	return h.showNavigation(ctx, student.ID, state, nav, "Ответ сохранён. Автоматическая проверка пока не выполнена.")
}

func (h *Handler) handleCallback(ctx context.Context, callback CallbackQuery, updateID int64) error {
	if callback.Message == nil || callback.Message.Chat.Type != "private" ||
		callback.Message.Chat.ID != callback.From.ID || callback.From.ID <= 0 {
		return nil
	}
	student, state, err := h.studentAndState(ctx, callback.From.ID, callback.Message.Chat.ID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return h.apiErrorMessage(ctx, callback.Message.Chat.ID, callback.Message.MessageID, "Сначала выполните /start.")
		}
		return err
	}
	// Актуальный message ID заменяется лишь после подтверждённой отправки/редактирования.
	if state.MessageID == nil {
		state.MessageID = int64Pointer(callback.Message.MessageID)
	}
	parts := strings.Split(callback.Data, ":")
	switch parts[0] {
	case "home":
		state.Screen = "home"
		state.AwaitingText = false
		text, markup := h.homeScreen(ctx, student.ID, state, student.DisplayName)
		return h.render(ctx, state, text, markup)
	case "help":
		return h.handleHelp(ctx, callback.From.ID, callback.Message.Chat.ID)
	case "courses":
		return h.showCourses(ctx, student.ID, state, "")
	case "continue":
		return h.continueLearning(ctx, student.ID, state)
	case "course":
		if len(parts) != 2 {
			return h.staleCallback(ctx, student.ID, state)
		}
		courseID, err := parseID(parts[1])
		if err != nil {
			return h.staleCallback(ctx, student.ID, state)
		}
		return h.showLessons(ctx, student.ID, state, courseID, "")
	case "lessons":
		if state.CourseID == nil {
			return h.staleCallback(ctx, student.ID, state)
		}
		return h.showLessons(ctx, student.ID, state, *state.CourseID, "")
	case "lesson":
		if len(parts) != 3 {
			return h.staleCallback(ctx, student.ID, state)
		}
		courseID, err1 := parseID(parts[1])
		lessonID, err2 := parseID(parts[2])
		if err1 != nil || err2 != nil {
			return h.staleCallback(ctx, student.ID, state)
		}
		return h.openLesson(ctx, student.ID, state, courseID, lessonID)
	case "text":
		if len(parts) != 2 || !stateHasQuestion(state, parts[1]) {
			return h.staleCallback(ctx, student.ID, state)
		}
		nav, err := h.currentQuestion(ctx, student.ID, state)
		if err != nil {
			return h.showOperationError(ctx, state, "открыть текстовый ответ", err)
		}
		if nav.Question.AnswerType != "text" {
			return h.staleCallback(ctx, student.ID, state)
		}
		state.AwaitingText = true
		return h.showNavigation(ctx, student.ID, state, nav, "Напишите ответ отдельным сообщением.")
	case "prev", "next":
		if len(parts) != 2 || !stateHasQuestion(state, parts[1]) {
			return h.staleCallback(ctx, student.ID, state)
		}
		nav, err := h.currentQuestion(ctx, student.ID, state)
		if err != nil {
			return h.showOperationError(ctx, state, "проверить вопрос перед навигацией", err)
		}
		if parts[0] == "prev" {
			if nav.Progress.PreviousQuestionID == nil {
				return h.showNavigation(ctx, student.ID, state, nav, "Это первый вопрос.")
			}
			nav, err = h.learning.PreviousQuestion(ctx, student.ID, nav.Progress.Enrollment.ID)
		} else {
			if nav.Progress.NextQuestionID == nil {
				if nav.Progress.AnsweredQuestions == nav.Progress.TotalQuestions {
					return h.showNavigation(ctx, student.ID, state, nav, "Ответы на все вопросы сохранены. Можно завершить урок.")
				}
				return h.showNavigation(ctx, student.ID, state, nav, "Это последний вопрос. Сохраните ответы на оставшиеся вопросы, чтобы завершить урок.")
			}
			nav, err = h.learning.NextQuestion(ctx, student.ID, nav.Progress.Enrollment.ID)
		}
		if err != nil {
			return h.showOperationError(ctx, state, "перейти к соседнему вопросу", err)
		}
		return h.showNavigation(ctx, student.ID, state, nav, "")
	case "answer":
		if len(parts) != 3 || !stateHasQuestion(state, parts[1]) {
			return h.staleCallback(ctx, student.ID, state)
		}
		questionID, err1 := parseID(parts[1])
		optionID, err2 := parseID(parts[2])
		if err1 != nil || err2 != nil {
			return h.staleCallback(ctx, student.ID, state)
		}
		return h.submitChoice(ctx, student.ID, state, questionID, optionID, updateID)
	case "toggle":
		if len(parts) != 3 || !stateHasQuestion(state, parts[1]) {
			return h.staleCallback(ctx, student.ID, state)
		}
		questionID, err1 := parseID(parts[1])
		optionID, err2 := parseID(parts[2])
		if err1 != nil || err2 != nil {
			return h.staleCallback(ctx, student.ID, state)
		}
		return h.toggleChoice(ctx, student.ID, state, questionID, optionID)
	case "submit":
		if len(parts) != 2 || !stateHasQuestion(state, parts[1]) {
			return h.staleCallback(ctx, student.ID, state)
		}
		return h.submitMultipleChoice(ctx, student.ID, state, updateID)
	case "finish":
		if len(parts) != 2 || !stateHasQuestion(state, parts[1]) {
			return h.staleCallback(ctx, student.ID, state)
		}
		return h.finishLesson(ctx, student.ID, state)
	default:
		return h.staleCallback(ctx, student.ID, state)
	}
}

func (h *Handler) studentAndState(ctx context.Context, telegramID, chatID int64) (domain.Student, domain.TelegramDialogState, error) {
	student, err := h.students.GetByTelegramID(ctx, telegramID)
	if err != nil {
		return domain.Student{}, domain.TelegramDialogState{}, err
	}
	state, err := h.loadOrNewState(ctx, student.ID, chatID)
	return student, state, err
}

func (h *Handler) loadOrNewState(ctx context.Context, studentID, chatID int64) (domain.TelegramDialogState, error) {
	state, err := h.states.Get(ctx, studentID)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return domain.TelegramDialogState{}, err
	}
	if errors.Is(err, domain.ErrNotFound) {
		state = domain.TelegramDialogState{StudentID: studentID, Screen: "home", SelectedOptionIDs: []int64{}}
	}
	state.StudentID = studentID
	state.ChatID = chatID
	if state.SelectedOptionIDs == nil {
		state.SelectedOptionIDs = []int64{}
	}
	return state, nil
}

func (h *Handler) homeScreen(ctx context.Context, studentID int64, state domain.TelegramDialogState, firstName string) (string, *InlineKeyboardMarkup) {
	name := strings.TrimSpace(firstName)
	if name == "" {
		name = "друг"
	}
	lines := "Здравствуйте, " + name + "!\n\nЗдесь можно проходить опубликованные библейские уроки. Ответы сохраняются, а автоматическая проверка пока не подключена."
	rows := [][]InlineKeyboardButton{{button("📚 Выбрать курс", "courses")}}
	if state.EnrollmentID != nil {
		if progress, err := h.learning.GetProgress(ctx, studentID, *state.EnrollmentID); err == nil && progress.Enrollment.Status != "cancelled" {
			rows = append([][]InlineKeyboardButton{{button("▶️ Продолжить обучение", "continue")}}, rows...)
		}
	}
	rows = append(rows, []InlineKeyboardButton{button("ℹ️ Помощь", "help")})
	return lines, &InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (h *Handler) showCourses(ctx context.Context, studentID int64, state domain.TelegramDialogState, notice string) error {
	courses, err := h.catalog.ListCourses(ctx)
	if err != nil {
		return h.showOperationError(ctx, state, "получить каталог курсов", err)
	}
	state.Screen = "courses"
	state.AwaitingText = false
	rows := make([][]InlineKeyboardButton, 0, len(courses)+1)
	for _, course := range courses {
		label := course.Title
		if course.LessonCount > 0 {
			label = fmt.Sprintf("%s · %d урок.", label, course.LessonCount)
		}
		rows = append(rows, []InlineKeyboardButton{button(label, fmt.Sprintf("course:%d", course.ID))})
	}
	rows = append(rows, []InlineKeyboardButton{button("🏠 Главное меню", "home")})
	text := "📚 Доступные курсы"
	if len(courses) == 0 {
		text += "\n\nПока нет опубликованных курсов. Загляните позже."
	}
	if notice != "" {
		text = notice + "\n\n" + text
	}
	return h.render(ctx, state, text, &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (h *Handler) showLessons(ctx context.Context, studentID int64, state domain.TelegramDialogState, courseID int64, notice string) error {
	course, err := h.catalog.GetCourse(ctx, courseID)
	if err != nil {
		return h.showOperationError(ctx, state, "проверить доступность курса", err)
	}
	lessons, err := h.catalog.ListCourseLessons(ctx, courseID)
	if err != nil {
		return h.showOperationError(ctx, state, "получить уроки курса", err)
	}
	state.Screen = "lessons"
	state.CourseID = int64Pointer(courseID)
	state.AwaitingText = false
	rows := make([][]InlineKeyboardButton, 0, len(lessons)+1)
	for _, lesson := range lessons {
		rows = append(rows, []InlineKeyboardButton{button(fmt.Sprintf("%s · %d вопр.", lesson.Title, lesson.QuestionCount), fmt.Sprintf("lesson:%d:%d", courseID, lesson.ID))})
	}
	rows = append(rows, []InlineKeyboardButton{button("⬅️ К курсам", "courses")}, []InlineKeyboardButton{button("🏠 Главное меню", "home")})
	text := "📖 Курс: " + course.Title
	if len(lessons) == 0 {
		text += "\n\nВ этом курсе пока нет опубликованных уроков."
	}
	if notice != "" {
		text = notice + "\n\n" + text
	}
	return h.render(ctx, state, text, &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (h *Handler) openLesson(ctx context.Context, studentID int64, state domain.TelegramDialogState, courseID, lessonID int64) error {
	detail, err := h.catalog.GetLessonDetailInCourse(ctx, courseID, lessonID)
	if err != nil {
		return h.showOperationError(ctx, state, "открыть урок курса", err)
	}
	state.CourseID = int64Pointer(courseID)
	state.LessonID = int64Pointer(lessonID)
	state.AwaitingText = false
	if len(detail.Questions) == 0 {
		state.Screen = "lessons"
		return h.render(ctx, state, "В этом уроке пока нет вопросов. Выберите другой урок.", keyboard(
			[]InlineKeyboardButton{button("⬅️ К урокам", fmt.Sprintf("lessons:%d", courseID))},
		))
	}
	progress, err := h.learning.StartLesson(ctx, studentID, lessonID, "ai")
	if err != nil {
		return h.showOperationError(ctx, state, "начать или восстановить прохождение", err)
	}
	state.EnrollmentID = int64Pointer(progress.Enrollment.ID)
	state.Screen = "question"
	questionID := progress.Enrollment.CurrentQuestionID
	if questionID == nil {
		questionID = progress.FirstQuestionID
	}
	if questionID == nil {
		state.Screen = "lessons"
		return h.render(ctx, state, "Не удалось определить первый вопрос урока. Вернитесь к урокам и попробуйте ещё раз.", keyboard(
			[]InlineKeyboardButton{button("⬅️ К урокам", fmt.Sprintf("lessons:%d", courseID))},
		))
	}
	nav, err := h.learning.OpenQuestion(ctx, studentID, progress.Enrollment.ID, *questionID)
	if err != nil {
		return h.showOperationError(ctx, state, "открыть первый вопрос", err)
	}
	return h.showNavigation(ctx, studentID, state, nav, "")
}

func (h *Handler) continueLearning(ctx context.Context, studentID int64, state domain.TelegramDialogState) error {
	if state.EnrollmentID == nil {
		return h.showCourses(ctx, studentID, state, "Начните обучение, выбрав курс и урок.")
	}
	progress, err := h.learning.GetProgress(ctx, studentID, *state.EnrollmentID)
	if err != nil || progress.Enrollment.Status == "cancelled" {
		state.EnrollmentID = nil
		state.QuestionID = nil
		return h.showCourses(ctx, studentID, state, "Не удалось восстановить прежнее прохождение. Выберите урок, чтобы открыть доступную версию.")
	}
	if progress.Enrollment.CurrentQuestionID == nil {
		if progress.FirstQuestionID == nil {
			return h.render(ctx, state, "В этом уроке нет доступных вопросов. Вернитесь к списку уроков.", keyboard(
				[]InlineKeyboardButton{button("📚 К урокам", "lessons")},
			))
		}
		progress.Enrollment.CurrentQuestionID = progress.FirstQuestionID
	}
	nav, err := h.learning.OpenQuestion(ctx, studentID, progress.Enrollment.ID, *progress.Enrollment.CurrentQuestionID)
	if err != nil {
		return h.showOperationError(ctx, state, "восстановить текущий вопрос", err)
	}
	return h.showNavigation(ctx, studentID, state, nav, "")
}

func (h *Handler) showNavigation(ctx context.Context, studentID int64, state domain.TelegramDialogState, nav service.NavigationState, notice string) error {
	question := nav.Question
	oldQuestionID := state.QuestionID
	sameQuestion := oldQuestionID != nil && *oldQuestionID == question.ID
	state.Screen = "question"
	state.LessonID = int64Pointer(nav.Progress.Enrollment.LessonID)
	state.EnrollmentID = int64Pointer(nav.Progress.Enrollment.ID)
	state.QuestionID = int64Pointer(question.ID)
	if question.AnswerType != "text" || nav.SavedAnswer != nil {
		state.AwaitingText = false
	} else if !sameQuestion {
		state.AwaitingText = true
	}
	if sameQuestion && state.SelectedOptionIDs != nil {
		state.SelectedOptionIDs = orderedOptions(question, state.SelectedOptionIDs)
	} else {
		state.SelectedOptionIDs = []int64{}
		if nav.SavedAnswer != nil {
			state.SelectedOptionIDs = append(state.SelectedOptionIDs, nav.SavedAnswer.SelectedOptionIDs...)
		}
	}
	var text strings.Builder
	fmt.Fprintf(&text, "Вопрос %d из %d\n\n%s", question.QuestionNumber, nav.Progress.TotalQuestions, question.Prompt)
	if question.AnswerType == "text" {
		if nav.SavedAnswer != nil {
			text.WriteString("\n\nВаш ответ сохранён. Можно оставить его или изменить.")
		} else {
			text.WriteString("\n\nНапишите ответ отдельным сообщением.")
		}
	} else if nav.SavedAnswer != nil {
		text.WriteString("\n\nВаш выбор сохранён. Автоматическая проверка пока не выполнена.")
	} else if question.AnswerType == "multiple_choice" {
		text.WriteString("\n\nОтметьте все подходящие варианты и нажмите «Сохранить выбор».")
	} else {
		text.WriteString("\n\nВыберите один вариант.")
	}
	if nav.Progress.TotalQuestions > 0 {
		fmt.Fprintf(&text, "\n\nПрогресс: %d из %d вопросов с сохранёнными ответами.", nav.Progress.AnsweredQuestions, nav.Progress.TotalQuestions)
	}
	if notice != "" {
		text.WriteString("\n\n" + notice)
	}
	rows := make([][]InlineKeyboardButton, 0, len(question.Options)+4)
	if question.AnswerType == "text" {
		label := "✍️ Ответить текстом"
		if nav.SavedAnswer != nil {
			label = "✏️ Изменить ответ"
		}
		rows = append(rows, []InlineKeyboardButton{button(label, fmt.Sprintf("text:%d", question.ID))})
	} else if question.AnswerType == "single_choice" {
		for _, option := range question.Options {
			label := option.Text
			if nav.SavedAnswer != nil && includes(nav.SavedAnswer.SelectedOptionIDs, option.ID) {
				label = "✓ " + label
			}
			rows = append(rows, []InlineKeyboardButton{button(label, fmt.Sprintf("answer:%d:%d", question.ID, option.ID))})
		}
	} else if question.AnswerType == "multiple_choice" {
		for _, option := range question.Options {
			label := option.Text
			callback := fmt.Sprintf("toggle:%d:%d", question.ID, option.ID)
			if includes(state.SelectedOptionIDs, option.ID) {
				label = "☑ " + label
			} else {
				label = "☐ " + label
			}
			rows = append(rows, []InlineKeyboardButton{button(label, callback)})
		}
		rows = append(rows, []InlineKeyboardButton{button("💾 Сохранить выбор", fmt.Sprintf("submit:%d", question.ID))})
	}
	if nav.Progress.PreviousQuestionID != nil {
		rows = append(rows, []InlineKeyboardButton{button("⬅️ Назад", fmt.Sprintf("prev:%d", question.ID))})
	}
	if nav.Progress.NextQuestionID != nil {
		rows = append(rows, []InlineKeyboardButton{button("Далее ➡️", fmt.Sprintf("next:%d", question.ID))})
	} else if nav.Progress.AnsweredQuestions == nav.Progress.TotalQuestions && nav.Progress.TotalQuestions > 0 {
		rows = append(rows, []InlineKeyboardButton{button("✅ Завершить урок", fmt.Sprintf("finish:%d", question.ID))})
	}
	rows = append(rows, []InlineKeyboardButton{button("📚 К урокам", "lessons"), button("🏠 Меню", "home")})
	return h.render(ctx, state, text.String(), &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (h *Handler) submitChoice(ctx context.Context, studentID int64, state domain.TelegramDialogState, questionID, optionID, updateID int64) error {
	nav, err := h.currentQuestion(ctx, studentID, state)
	if err != nil {
		return h.showOperationError(ctx, state, "проверить вопрос с вариантом ответа", err)
	}
	if nav.Question.ID != questionID || nav.Question.AnswerType != "single_choice" || !questionHasOption(nav.Question, optionID) {
		return h.staleCallback(ctx, studentID, state)
	}
	_, err = h.learning.SubmitAnswer(ctx, domain.AnswerSubmission{
		StudentID: studentID, EnrollmentID: nav.Progress.Enrollment.ID, QuestionID: questionID,
		SelectedOptionIDs: []int64{optionID}, IdempotencyKey: idempotencyKey(updateID),
	})
	if err != nil {
		h.logger.Error("не удалось сохранить выбор", "student_id", studentID, "question_id", questionID, "error", err)
		return h.showNavigation(ctx, studentID, state, nav, "Не удалось сохранить выбор. Попробуйте ещё раз.")
	}
	nav, err = h.learning.OpenQuestion(ctx, studentID, nav.Progress.Enrollment.ID, questionID)
	if err != nil {
		return h.showOperationError(ctx, state, "обновить вопрос после сохранения", err)
	}
	return h.showNavigation(ctx, studentID, state, nav, "Ответ сохранён. Автоматическая проверка пока не выполнена.")
}

func (h *Handler) toggleChoice(ctx context.Context, studentID int64, state domain.TelegramDialogState, questionID, optionID int64) error {
	nav, err := h.currentQuestion(ctx, studentID, state)
	if err != nil {
		return h.showOperationError(ctx, state, "проверить множественный выбор", err)
	}
	if nav.Question.ID != questionID || nav.Question.AnswerType != "multiple_choice" || !questionHasOption(nav.Question, optionID) {
		return h.staleCallback(ctx, studentID, state)
	}
	if includes(state.SelectedOptionIDs, optionID) {
		state.SelectedOptionIDs = removeID(state.SelectedOptionIDs, optionID)
	} else {
		state.SelectedOptionIDs = append(state.SelectedOptionIDs, optionID)
	}
	state.SelectedOptionIDs = orderedOptions(nav.Question, state.SelectedOptionIDs)
	return h.showNavigation(ctx, studentID, state, nav, "")
}

func (h *Handler) submitMultipleChoice(ctx context.Context, studentID int64, state domain.TelegramDialogState, updateID int64) error {
	nav, err := h.currentQuestion(ctx, studentID, state)
	if err != nil {
		return h.showOperationError(ctx, state, "проверить множественный выбор", err)
	}
	if nav.Question.AnswerType != "multiple_choice" || len(state.SelectedOptionIDs) == 0 {
		return h.showNavigation(ctx, studentID, state, nav, "Отметьте хотя бы один вариант перед сохранением.")
	}
	if nav.SavedAnswer != nil && sameIDs(nav.SavedAnswer.SelectedOptionIDs, state.SelectedOptionIDs) {
		return h.showNavigation(ctx, studentID, state, nav, "Этот выбор уже сохранён. Измените отмеченные варианты, если хотите исправить ответ.")
	}
	_, err = h.learning.SubmitAnswer(ctx, domain.AnswerSubmission{
		StudentID: studentID, EnrollmentID: nav.Progress.Enrollment.ID, QuestionID: nav.Question.ID,
		SelectedOptionIDs: append([]int64{}, state.SelectedOptionIDs...), IdempotencyKey: idempotencyKey(updateID),
	})
	if err != nil {
		h.logger.Error("не удалось сохранить множественный выбор", "student_id", studentID, "question_id", nav.Question.ID, "error", err)
		return h.showNavigation(ctx, studentID, state, nav, "Не удалось сохранить выбор. Попробуйте ещё раз.")
	}
	nav, err = h.learning.OpenQuestion(ctx, studentID, nav.Progress.Enrollment.ID, nav.Question.ID)
	if err != nil {
		return h.showOperationError(ctx, state, "обновить вопрос после сохранения выбора", err)
	}
	state.SelectedOptionIDs = append([]int64{}, nav.SavedAnswer.SelectedOptionIDs...)
	return h.showNavigation(ctx, studentID, state, nav, "Ответ сохранён. Автоматическая проверка пока не выполнена.")
}

func (h *Handler) currentQuestion(ctx context.Context, studentID int64, state domain.TelegramDialogState) (service.NavigationState, error) {
	if state.Screen != "question" || state.EnrollmentID == nil || state.QuestionID == nil {
		return service.NavigationState{}, domain.ErrNotFound
	}
	return h.learning.OpenQuestion(ctx, studentID, *state.EnrollmentID, *state.QuestionID)
}

func (h *Handler) finishLesson(ctx context.Context, studentID int64, state domain.TelegramDialogState) error {
	if state.EnrollmentID == nil {
		return h.staleCallback(ctx, studentID, state)
	}
	progress, err := h.learning.GetProgress(ctx, studentID, *state.EnrollmentID)
	if err != nil {
		return h.showOperationError(ctx, state, "проверить прогресс перед завершением", err)
	}
	if progress.AnsweredQuestions != progress.TotalQuestions || progress.TotalQuestions == 0 {
		nav, navErr := h.currentQuestion(ctx, studentID, state)
		if navErr == nil {
			return h.showNavigation(ctx, studentID, state, nav, "Чтобы завершить урок, сохраните ответ на каждый вопрос.")
		}
		return h.staleCallback(ctx, studentID, state)
	}
	if _, err := h.learning.CompleteLesson(ctx, studentID, *state.EnrollmentID); err != nil {
		return h.showOperationError(ctx, state, "завершить урок", err)
	}
	state.Screen = "completed"
	state.AwaitingText = false
	state.SelectedOptionIDs = []int64{}
	return h.render(ctx, state, "Урок завершён. Все ответы сохранены, но автоматическая проверка пока не выполнена.", keyboard(
		[]InlineKeyboardButton{button("📚 К урокам", "lessons")},
		[]InlineKeyboardButton{button("📚 Другой курс", "courses"), button("🏠 Меню", "home")},
	))
}

func (h *Handler) staleCallback(ctx context.Context, studentID int64, state domain.TelegramDialogState) error {
	if state.Screen == "question" && state.EnrollmentID != nil {
		progress, err := h.learning.GetProgress(ctx, studentID, *state.EnrollmentID)
		if err == nil && progress.Enrollment.CurrentQuestionID != nil {
			nav, navErr := h.learning.OpenQuestion(ctx, studentID, *state.EnrollmentID, *progress.Enrollment.CurrentQuestionID)
			if navErr == nil {
				return h.showNavigation(ctx, studentID, state, nav, "Кнопка устарела. Показан текущий вопрос.")
			}
		}
	}
	state.Screen = "home"
	state.AwaitingText = false
	text, markup := h.homeScreen(ctx, studentID, state, "")
	return h.render(ctx, state, "Эта кнопка устарела или ресурс больше недоступен. Откройте нужный экран заново.\n\n"+text, markup)
}

func (h *Handler) showOperationError(ctx context.Context, state domain.TelegramDialogState, operation string, err error) error {
	h.logger.Error("ошибка операции Telegram-бота", "operation", operation, "student_id", state.StudentID, "error", err)
	return h.render(ctx, state, "Не удалось выполнить действие. Попробуйте ещё раз или откройте главное меню. Подробности записаны в журнал сервера.", h.questionOrHomeKeyboard(state))
}

func (h *Handler) apiErrorMessage(ctx context.Context, chatID, messageID int64, text string) error {
	if messageID > 0 {
		if err := h.api.EditMessageText(ctx, chatID, messageID, text, nil); err == nil || isMessageNotModified(err) {
			return nil
		}
	}
	_, err := h.api.SendMessage(ctx, chatID, text, nil)
	return err
}

func (h *Handler) questionOrHomeKeyboard(state domain.TelegramDialogState) *InlineKeyboardMarkup {
	if state.Screen == "question" && state.QuestionID != nil {
		return keyboard([]InlineKeyboardButton{button("↩️ К текущему вопросу", fmt.Sprintf("continue"))}, []InlineKeyboardButton{button("🏠 Главное меню", "home")})
	}
	return keyboard([]InlineKeyboardButton{button("📚 К курсам", "courses"), button("🏠 Меню", "home")})
}

func (h *Handler) render(ctx context.Context, state domain.TelegramDialogState, text string, markup *InlineKeyboardMarkup) error {
	text = truncateRunes(strings.TrimSpace(text), 3900)
	if text == "" {
		text = "Выберите действие в меню."
	}
	if state.ChatID <= 0 {
		return fmt.Errorf("не задан Telegram chat ID")
	}
	if state.MessageID != nil && *state.MessageID > 0 {
		err := h.api.EditMessageText(ctx, state.ChatID, *state.MessageID, text, markup)
		if err == nil || isMessageNotModified(err) {
			return h.states.Save(ctx, state)
		}
		if !isUneditableMessage(err) {
			return err
		}
	}
	message, err := h.api.SendMessage(ctx, state.ChatID, text, markup)
	if err != nil {
		return err
	}
	state.MessageID = int64Pointer(message.MessageID)
	return h.states.Save(ctx, state)
}

func parseID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, domain.ErrInvalidInput
	}
	return id, nil
}

func idempotencyKey(updateID int64) string {
	sum := sha256.Sum256([]byte("telegram-update:" + strconv.FormatInt(updateID, 10)))
	hexID := hex.EncodeToString(sum[:16])
	return hexID[:8] + "-" + hexID[8:12] + "-5" + hexID[13:16] + "-a" + hexID[17:20] + "-" + hexID[20:32]
}

func stateHasQuestion(state domain.TelegramDialogState, raw string) bool {
	id, err := parseID(raw)
	return err == nil && state.Screen == "question" && state.QuestionID != nil && *state.QuestionID == id
}

func int64Pointer(value int64) *int64 { return &value }

func keyboard(rows ...[]InlineKeyboardButton) *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: rows}
}

func button(label, callback string) InlineKeyboardButton {
	return InlineKeyboardButton{Text: truncateRunes(label, 60), CallbackData: callback}
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

func includes(values []int64, target int64) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func removeID(values []int64, target int64) []int64 {
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}

func orderedOptions(question domain.LessonQuestion, selected []int64) []int64 {
	result := make([]int64, 0, len(selected))
	for _, option := range question.Options {
		if includes(selected, option.ID) {
			result = append(result, option.ID)
		}
	}
	return result
}

func questionHasOption(question domain.LessonQuestion, optionID int64) bool {
	for _, option := range question.Options {
		if option.ID == optionID {
			return true
		}
	}
	return false
}

func sameIDs(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	for _, id := range left {
		if !includes(right, id) {
			return false
		}
	}
	return true
}

func isMessageNotModified(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && strings.Contains(strings.ToLower(apiErr.Description), "message is not modified")
}

func isUneditableMessage(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 400 {
		return false
	}
	description := strings.ToLower(apiErr.Description)
	return strings.Contains(description, "message to edit not found") ||
		strings.Contains(description, "message can't be edited") ||
		strings.Contains(description, "message identifier is not specified") ||
		strings.Contains(description, "message_id_invalid")
}
