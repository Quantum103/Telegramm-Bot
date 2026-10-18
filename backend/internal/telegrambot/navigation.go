package telegrambot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/service"
)

func (b *Bot) showCourses(ctx context.Context, chatID, studentID, messageID int64, heading string) {
	courses, err := b.catalog.ListCourses(ctx)
	if err != nil {
		b.logger.Error("не удалось загрузить каталог курсов", "student_id", studentID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось загрузить каталог. Попробуйте обновить список позже.", keyboard(button("Обновить список", "courses")))
		return
	}

	var rows [][]InlineKeyboardButton
	for _, course := range courses {
		rows = append(rows, []InlineKeyboardButton{button(truncate(course.Title, 58), "course:"+strconv.FormatInt(course.ID, 10))})
	}
	text := heading
	if text == "" {
		text = "Выберите курс для изучения Библии."
	}
	if len(courses) == 0 {
		text += "\n\nПока нет опубликованных курсов. Попробуйте позже."
		rows = append(rows, []InlineKeyboardButton{button("Обновить список", "courses")})
	}
	b.render(ctx, chatID, messageID, text, markup(rows))
}

func (b *Bot) showLessons(ctx context.Context, chatID, studentID, courseID, messageID int64) {
	course, err := b.catalog.GetCourse(ctx, courseID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidInput) {
			b.render(ctx, chatID, messageID, "Этот курс больше недоступен. Откройте каталог курсов.", keyboard(button("К курсам", "courses")))
			return
		}
		b.logger.Error("не удалось получить курс", "student_id", studentID, "course_id", courseID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось открыть курс. Попробуйте позже.", keyboard(button("К курсам", "courses")))
		return
	}

	lessons, err := b.catalog.ListCourseLessons(ctx, courseID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidInput) {
			b.render(ctx, chatID, messageID, "Этот курс больше недоступен. Откройте каталог курсов.", keyboard(button("К курсам", "courses")))
			return
		}
		b.logger.Error("не удалось загрузить уроки курса", "student_id", studentID, "course_id", courseID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось загрузить уроки. Попробуйте позже.", keyboard(button("К курсам", "courses")))
		return
	}

	var rows [][]InlineKeyboardButton
	for _, lesson := range lessons {
		title := fmt.Sprintf("%s · %d вопросов", lesson.Title, lesson.QuestionCount)
		rows = append(rows, []InlineKeyboardButton{button(truncate(title, 58), fmt.Sprintf("lesson:%d:%d", courseID, lesson.ID))})
	}
	if len(lessons) == 0 {
		rows = append(rows, []InlineKeyboardButton{button("Обновить уроки", "course:"+strconv.FormatInt(courseID, 10))})
	}
	rows = append(rows, []InlineKeyboardButton{button("К курсам", "courses")})
	text := "Курс: " + truncate(course.Title, 180) + "\n\nВыберите урок."
	if len(lessons) == 0 {
		text = "Курс: " + truncate(course.Title, 180) + "\n\nВ этом курсе пока нет опубликованных уроков."
	}
	b.render(ctx, chatID, messageID, text, markup(rows))
}

func (b *Bot) openLesson(ctx context.Context, chatID, studentID, courseID, lessonID, messageID int64) {
	detail, err := b.catalog.GetLessonDetailInCourse(ctx, courseID, lessonID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidInput) {
			b.render(ctx, chatID, messageID, "Этот урок недоступен в выбранном курсе. Откройте список уроков.", keyboard(
				button("К урокам", fmt.Sprintf("lessons:%d", courseID)),
				button("К курсам", "courses"),
			))
			return
		}
		b.logger.Error("не удалось открыть урок из курса", "student_id", studentID, "course_id", courseID, "lesson_id", lessonID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось открыть урок. Попробуйте позже.", keyboard(button("К урокам", fmt.Sprintf("lessons:%d", courseID))))
		return
	}
	if len(detail.Questions) == 0 {
		b.render(ctx, chatID, messageID, "Урок «"+truncate(detail.Lesson.Title, 150)+"» пока не содержит вопросов.", keyboard(
			button("К урокам", fmt.Sprintf("lessons:%d", courseID)),
			button("К курсам", "courses"),
		))
		return
	}

	progress, err := b.learning.StartLesson(ctx, studentID, detail.Lesson.ID, "ai")
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidState) || errors.Is(err, domain.ErrInvalidInput) {
			b.render(ctx, chatID, messageID, "Не удалось начать этот урок: он мог стать недоступен. Вернитесь к списку уроков.", keyboard(button("К урокам", fmt.Sprintf("lessons:%d", courseID))))
			return
		}
		b.logger.Error("не удалось начать прохождение урока", "student_id", studentID, "course_id", courseID, "lesson_id", lessonID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось загрузить состояние урока. Попробуйте ещё раз.", keyboard(button("К урокам", fmt.Sprintf("lessons:%d", courseID))))
		return
	}

	questionID := int64(0)
	if progress.Enrollment.CurrentQuestionID != nil {
		questionID = *progress.Enrollment.CurrentQuestionID
	} else if progress.FirstQuestionID != nil {
		questionID = *progress.FirstQuestionID
	}
	if questionID <= 0 {
		b.render(ctx, chatID, messageID, "В этом уроке пока нет доступных вопросов.", keyboard(
			button("К урокам", fmt.Sprintf("lessons:%d", courseID)),
			button("К курсам", "courses"),
		))
		return
	}
	state, err := b.learning.OpenQuestion(ctx, studentID, progress.Enrollment.ID, questionID)
	if err != nil {
		b.logger.Error("не удалось открыть текущий вопрос", "student_id", studentID, "enrollment_id", progress.Enrollment.ID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось открыть вопрос. Вернитесь к списку уроков и попробуйте снова.", keyboard(button("К урокам", fmt.Sprintf("lessons:%d", courseID))))
		return
	}
	b.renderQuestion(ctx, chatID, studentID, messageID, state, &detail.Lesson)
}

func (b *Bot) showQuestion(ctx context.Context, chatID, studentID, messageID int64, enrollmentID int64) {
	progress, err := b.learning.GetProgress(ctx, studentID, enrollmentID)
	if err != nil {
		b.logger.Error("не удалось восстановить прогресс вопроса", "student_id", studentID, "enrollment_id", enrollmentID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось восстановить прогресс. Откройте урок из каталога.", keyboard(button("К курсам", "courses")))
		return
	}
	if progress.Enrollment.CurrentQuestionID == nil {
		b.render(ctx, chatID, messageID, "У текущего прохождения нет сохранённой позиции. Откройте урок из списка.", keyboard(button("К курсам", "courses")))
		return
	}
	nav, err := b.learning.OpenQuestion(ctx, studentID, enrollmentID, *progress.Enrollment.CurrentQuestionID)
	if err != nil {
		b.logger.Error("не удалось загрузить сохранённый вопрос", "student_id", studentID, "enrollment_id", enrollmentID, "error", err)
		b.render(ctx, chatID, messageID, "Сохранённый вопрос больше недоступен. Откройте каталог курсов.", keyboard(button("К курсам", "courses")))
		return
	}
	b.renderQuestion(ctx, chatID, studentID, messageID, nav, nil)
}

func (b *Bot) renderQuestion(ctx context.Context, chatID, studentID, messageID int64, state service.NavigationState, lesson *domain.Lesson) {
	if lesson == nil {
		detail, err := b.catalog.GetLessonDetail(ctx, state.Progress.Enrollment.LessonID)
		if err == nil {
			lesson = &detail.Lesson
		}
	}
	lessonTitle := "Урок"
	courseID := int64(0)
	if lesson != nil {
		if strings.TrimSpace(lesson.Title) != "" {
			lessonTitle = lesson.Title
		}
		courseID = lesson.CourseID
	}

	progress := state.Progress
	question := state.Question
	number := question.QuestionNumber
	if number <= 0 {
		number = 1
	}
	total := progress.TotalQuestions
	if total <= 0 {
		total = number
	}
	text := fmt.Sprintf("📖 %s\nВопрос %d из %d\n\n%s\n\nОтправка учебных ответов будет подключена следующим подэтапом.",
		truncate(lessonTitle, 180), number, total, question.Prompt)
	text = truncate(text, 3900)

	var rows [][]InlineKeyboardButton
	currentQuestionID := question.ID
	enrollmentID := progress.Enrollment.ID
	if progress.PreviousQuestionID != nil {
		rows = append(rows, []InlineKeyboardButton{button("← Назад", fmt.Sprintf("nav:prev:%d:%d", enrollmentID, currentQuestionID))})
	}
	if progress.NextQuestionID != nil {
		next := button("Далее →", fmt.Sprintf("nav:next:%d:%d", enrollmentID, currentQuestionID))
		if len(rows) > 0 && progress.PreviousQuestionID != nil {
			rows[len(rows)-1] = append(rows[len(rows)-1], next)
		} else {
			rows = append(rows, []InlineKeyboardButton{next})
		}
	}
	if courseID > 0 {
		rows = append(rows, []InlineKeyboardButton{button("К урокам", fmt.Sprintf("lessons:%d", courseID))})
	} else {
		rows = append(rows, []InlineKeyboardButton{button("К курсам", "courses")})
	}
	b.render(ctx, chatID, messageID, text, markup(rows))
}

func (b *Bot) handleCallback(ctx context.Context, callback CallbackQuery) {
	if callback.ID != "" {
		if err := b.api.AnswerCallbackQuery(ctx, callback.ID, ""); err != nil && ctx.Err() == nil {
			b.logger.Warn("не удалось подтвердить callback Telegram", "error", err)
		}
	}
	if callback.From == nil || callback.From.ID <= 0 || callback.Message == nil ||
		callback.Message.Chat.ID <= 0 || callback.Message.Chat.ID != callback.From.ID ||
		(callback.Message.Chat.Type != "" && callback.Message.Chat.Type != "private") {
		return
	}

	chatID, messageID := callback.Message.Chat.ID, callback.Message.MessageID
	student, err := b.ensureStudent(ctx, *callback.From)
	if err != nil {
		b.logger.Warn("не удалось проверить ученика для callback", "telegram_user_id", callback.From.ID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось проверить регистрацию. Отправьте /start и попробуйте снова.", keyboard(button("К курсам", "courses")))
		return
	}

	parts := strings.Split(callback.Data, ":")
	switch {
	case callback.Data == "courses":
		b.showCourses(ctx, chatID, student.ID, messageID, "Выберите курс для изучения Библии.")
	case len(parts) == 2 && parts[0] == "course":
		courseID, ok := parsePositiveID(parts[1])
		if !ok {
			b.showStaleCallback(ctx, chatID, student.ID, messageID)
			return
		}
		b.showLessons(ctx, chatID, student.ID, courseID, messageID)
	case len(parts) == 2 && parts[0] == "lessons":
		courseID, ok := parsePositiveID(parts[1])
		if !ok {
			b.showStaleCallback(ctx, chatID, student.ID, messageID)
			return
		}
		b.showLessons(ctx, chatID, student.ID, courseID, messageID)
	case len(parts) == 3 && parts[0] == "lesson":
		courseID, okCourse := parsePositiveID(parts[1])
		lessonID, okLesson := parsePositiveID(parts[2])
		if !okCourse || !okLesson {
			b.showStaleCallback(ctx, chatID, student.ID, messageID)
			return
		}
		b.openLesson(ctx, chatID, student.ID, courseID, lessonID, messageID)
	case len(parts) == 4 && parts[0] == "nav" && (parts[1] == "prev" || parts[1] == "next"):
		enrollmentID, okEnrollment := parsePositiveID(parts[2])
		expectedQuestionID, okQuestion := parsePositiveID(parts[3])
		if !okEnrollment || !okQuestion {
			b.showStaleCallback(ctx, chatID, student.ID, messageID)
			return
		}
		b.navigate(ctx, chatID, student.ID, messageID, enrollmentID, expectedQuestionID, parts[1])
	default:
		b.showStaleCallback(ctx, chatID, student.ID, messageID)
	}
}

func (b *Bot) navigate(ctx context.Context, chatID, studentID, messageID, enrollmentID, expectedQuestionID int64, direction string) {
	progress, err := b.learning.GetProgress(ctx, studentID, enrollmentID)
	if err != nil {
		b.logger.Warn("callback с недоступным прохождением", "student_id", studentID, "enrollment_id", enrollmentID, "error", err)
		b.showStaleCallback(ctx, chatID, studentID, messageID)
		return
	}
	if progress.Enrollment.CurrentQuestionID == nil {
		b.showStaleCallback(ctx, chatID, studentID, messageID)
		return
	}
	if *progress.Enrollment.CurrentQuestionID != expectedQuestionID {
		b.showQuestion(ctx, chatID, studentID, messageID, enrollmentID)
		return
	}

	var state service.NavigationState
	if direction == "prev" {
		state, err = b.learning.PreviousQuestion(ctx, studentID, enrollmentID)
	} else {
		state, err = b.learning.NextQuestion(ctx, studentID, enrollmentID)
	}
	if err != nil {
		if errors.Is(err, domain.ErrNoPreviousQuestion) || errors.Is(err, domain.ErrNoNextQuestion) {
			b.showQuestion(ctx, chatID, studentID, messageID, enrollmentID)
			return
		}
		b.logger.Warn("не удалось переместиться между вопросами", "student_id", studentID, "enrollment_id", enrollmentID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось открыть вопрос. Попробуйте снова или вернитесь к каталогу.", keyboard(button("К курсам", "courses")))
		return
	}
	b.renderQuestion(ctx, chatID, studentID, messageID, state, nil)
}

func (b *Bot) showStaleCallback(ctx context.Context, chatID, studentID, messageID int64) {
	b.showCourses(ctx, chatID, studentID, messageID, "Эта кнопка устарела. Выберите курс из актуального каталога.")
}

func (b *Bot) render(ctx context.Context, chatID, messageID int64, text string, replyMarkup *InlineKeyboardMarkup) {
	if messageID <= 0 {
		if _, err := b.api.SendMessageWithKeyboard(ctx, chatID, text, replyMarkup); err != nil && ctx.Err() == nil {
			b.logger.Warn("не удалось отправить экран Telegram", "chat_id", chatID, "error", err)
		}
		return
	}
	if err := b.api.EditMessageText(ctx, chatID, messageID, text, replyMarkup); err == nil {
		return
	} else if isMessageNotModified(err) {
		return
	} else if ctx.Err() == nil {
		b.logger.Warn("не удалось изменить основное сообщение Telegram; создаём новое", "chat_id", chatID, "message_id", messageID, "error", err)
		if _, sendErr := b.api.SendMessageWithKeyboard(ctx, chatID, text, replyMarkup); sendErr != nil && ctx.Err() == nil {
			b.logger.Warn("не удалось отправить новое основное сообщение Telegram", "chat_id", chatID, "error", sendErr)
		}
	}
}

func keyboard(buttons ...InlineKeyboardButton) *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{buttons}}
}

func markup(rows [][]InlineKeyboardButton) *InlineKeyboardMarkup {
	if len(rows) == 0 {
		return nil
	}
	return &InlineKeyboardMarkup{InlineKeyboard: rows}
}

func button(text, callbackData string) InlineKeyboardButton {
	return InlineKeyboardButton{Text: truncate(text, 60), CallbackData: callbackData}
}

func parsePositiveID(value string) (int64, bool) {
	id, err := strconv.ParseInt(value, 10, 64)
	return id, err == nil && id > 0
}

func truncate(value string, maxRunes int) string {
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	if maxRunes <= 1 {
		return "…"
	}
	return string(runes[:maxRunes-1]) + "…"
}

func isMessageNotModified(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && strings.Contains(strings.ToLower(apiErr.Description), "message is not modified")
}
