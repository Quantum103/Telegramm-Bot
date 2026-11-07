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

func (b *Bot) leaveStudentInteraction(ctx context.Context, studentID int64) error {
	if b.conversations == nil {
		return nil
	}
	if leaver, ok := b.conversations.(interface {
		LeaveStudentInteraction(context.Context, int64) error
	}); ok {
		return leaver.LeaveStudentInteraction(ctx, studentID)
	}

	// Compatibility fallback for conversation providers that do not yet expose
	// the combined operation. Production ConversationService uses one SQL call.
	var failures []error
	if err := b.conversations.SetStudentChatMode(ctx, studentID, false); err != nil {
		failures = append(failures, err)
	}
	if err := b.conversations.SetStudentLessonMode(ctx, studentID, false); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (b *Bot) showCourses(ctx context.Context, chatID, studentID, messageID int64, heading string) {
	if err := b.leaveStudentInteraction(ctx, studentID); err != nil {
		b.logger.Warn("не удалось завершить взаимодействие ученика при переходе к каталогу", "student_id", studentID, "error", err)
	}
	b.renderCourses(ctx, chatID, studentID, messageID, heading)
}

func (b *Bot) renderCourses(ctx context.Context, chatID, studentID, messageID int64, heading string) {
	courses, err := b.catalog.ListCourses(ctx)
	if err != nil {
		b.logger.Error("не удалось загрузить каталог курсов", "student_id", studentID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось загрузить каталог. Попробуйте позже.", keyboard(button("Назад", "home:main")))
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
	}
	rows = append(rows, []InlineKeyboardButton{button("Назад", "home:main")})
	b.render(ctx, chatID, messageID, text, markup(rows))
}

func (b *Bot) showLessons(ctx context.Context, chatID, studentID, courseID, messageID int64) {
	if err := b.leaveStudentInteraction(ctx, studentID); err != nil {
		b.logger.Warn("не удалось завершить взаимодействие ученика при открытии уроков", "student_id", studentID, "error", err)
	}
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

	if navigator, ok := b.learning.(CourseEnrollmentNavigator); ok {
		active, activeErr := navigator.ActiveCourse(ctx, studentID)
		if activeErr == nil && active.Enrollment.CourseID == courseID {
			runLessons, runErr := navigator.CourseLessons(ctx, studentID, active.Enrollment.ID)
			if runErr != nil {
				b.logger.Error("не удалось получить статусы уроков активного курса", "student_id", studentID, "course_enrollment_id", active.Enrollment.ID, "error", runErr)
				b.render(ctx, chatID, messageID, "Не удалось загрузить прогресс курса. Попробуйте позже.", keyboard(button("Продолжить курс", "course:continue")))
				return
			}
			submitted := make(map[int64]struct{}, len(runLessons))
			for _, runLesson := range runLessons {
				if runLesson.Status == "completed" {
					submitted[runLesson.LessonID] = struct{}{}
				}
			}
			available := lessons[:0]
			for _, lesson := range lessons {
				if _, sent := submitted[lesson.ID]; !sent {
					available = append(available, lesson)
				}
			}
			lessons = available
		} else if activeErr != nil && !errors.Is(activeErr, domain.ErrNotFound) {
			b.logger.Warn("не удалось проверить активный курс перед показом уроков", "student_id", studentID, "course_id", courseID, "error", activeErr)
			b.render(ctx, chatID, messageID, "Не удалось проверить прогресс курса. Попробуйте позже.", keyboard(button("К курсам", "courses")))
			return
		}
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
	var lesson domain.Lesson
	var err error
	if metadata, ok := b.catalog.(interface {
		GetLessonInCourse(context.Context, int64, int64) (domain.Lesson, error)
	}); ok {
		lesson, err = metadata.GetLessonInCourse(ctx, courseID, lessonID)
	} else {
		detail, detailErr := b.catalog.GetLessonDetailInCourse(ctx, courseID, lessonID)
		err = detailErr
		lesson = detail.Lesson
		if lesson.QuestionCount == 0 {
			lesson.QuestionCount = len(detail.Questions)
		}
	}
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
	if lesson.QuestionCount == 0 {
		b.render(ctx, chatID, messageID, "Урок «"+truncate(lesson.Title, 150)+"» пока не содержит вопросов.", keyboard(
			button("К урокам", fmt.Sprintf("lessons:%d", courseID)),
			button("К курсам", "courses"),
		))
		return
	}

	var progress domain.Progress
	if courseStarter, ok := b.learning.(interface {
		StartCourse(context.Context, int64, int64, string) (domain.CourseEnrollmentProgress, error)
		StartLessonForCourse(context.Context, int64, int64, int64, int64) (domain.Progress, error)
	}); ok {
		courseRun, startErr := courseStarter.StartCourse(ctx, studentID, courseID, "ai")
		if errors.Is(startErr, domain.ErrActiveCourse) {
			active := courseRun.Enrollment
			message := fmt.Sprintf(
				"У вас уже начат курс «%s».\n\nПрогресс: %d из %d уроков завершено.\n\nСначала продолжите текущий курс. После его завершения можно будет начать следующий.",
				truncate(active.CourseTitle, 180),
				courseRun.CompletedLessons,
				courseRun.TotalLessons,
			)
			b.render(ctx, chatID, messageID, message, keyboard(
				button("Продолжить текущий курс", fmt.Sprintf("course:%d", active.CourseID)),
				button("К курсам", "courses"),
			))
			return
		}
		if startErr != nil {
			err = startErr
		} else {
			progress, err = courseStarter.StartLessonForCourse(
				ctx, studentID, courseRun.Enrollment.ID, courseID, lesson.ID,
			)
		}
	} else {
		progress, err = b.learning.StartLesson(ctx, studentID, lesson.ID, "ai")
	}
	if err != nil {
		if errors.Is(err, domain.ErrNoCurator) {
			b.render(ctx, chatID, messageID, "Для прохождения с координатором сначала должно быть назначено ответственное лицо. Обратитесь к администратору платформы.", keyboard(
				button("К курсам", "courses"),
			))
			return
		}
		if errors.Is(err, domain.ErrLegacyProgress) {
			b.render(ctx, chatID, messageID, "У вас найдено старое незавершённое прохождение, которое нельзя безопасно связать с курсом автоматически. Обратитесь к администратору, чтобы сохранить и восстановить ваш прогресс.", keyboard(
				button("К курсам", "courses"),
			))
			return
		}
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
	b.renderQuestion(ctx, chatID, studentID, messageID, state, &lesson, courseID)
}

func (b *Bot) showQuestion(ctx context.Context, chatID, studentID, messageID, enrollmentID, courseID int64) {
	if current, ok := b.learning.(interface {
		OpenCurrentQuestion(context.Context, int64, int64, int64) (service.NavigationState, error)
	}); ok {
		nav, err := current.OpenCurrentQuestion(ctx, studentID, enrollmentID, 0)
		if err != nil {
			b.logger.Error("не удалось восстановить сохранённый вопрос", "student_id", studentID, "enrollment_id", enrollmentID, "error", err)
			b.render(ctx, chatID, messageID, "Сохранённый вопрос больше недоступен. Откройте каталог курсов.", keyboard(button("К курсам", "courses")))
			return
		}
		b.renderQuestion(ctx, chatID, studentID, messageID, nav, nil, courseID)
		return
	}

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
	b.renderQuestion(ctx, chatID, studentID, messageID, nav, nil, courseID)
}

func (b *Bot) renderQuestion(ctx context.Context, chatID, studentID, messageID int64, state service.NavigationState, lesson *domain.Lesson, contextCourseID int64) {
	b.renderQuestionWithNotice(ctx, chatID, studentID, messageID, state, lesson, contextCourseID, "")
}

func (b *Bot) renderQuestionWithNotice(ctx context.Context, chatID, studentID, messageID int64, state service.NavigationState, lesson *domain.Lesson, contextCourseID int64, notice string) {
	if b.conversations != nil {
		if err := b.conversations.SetStudentLessonMode(ctx, studentID, true); err != nil {
			b.logger.Error("не удалось сохранить экран прохождения урока", "student_id", studentID, "error", err)
			b.render(ctx, chatID, messageID, "Не удалось сохранить состояние прохождения. Попробуйте открыть урок ещё раз.", keyboard(button("К курсам", "courses")))
			return
		}
	}
	key := answerStateKey{studentID: studentID, enrollmentID: state.Progress.Enrollment.ID, questionID: state.Question.ID}
	b.renderQuestionMode(ctx, chatID, studentID, messageID, state, lesson, contextCourseID, b.isChoiceEditing(key), notice)
}

func (b *Bot) renderQuestionMode(ctx context.Context, chatID, studentID, messageID int64, state service.NavigationState, lesson *domain.Lesson, contextCourseID int64, choiceEditing bool, notice string) {
	if lesson == nil {
		if metadata, ok := b.catalog.(interface {
			GetLesson(context.Context, int64) (domain.Lesson, error)
		}); ok {
			value, err := metadata.GetLesson(ctx, state.Progress.Enrollment.LessonID)
			if err == nil {
				lesson = &value
			}
		} else if detail, err := b.catalog.GetLessonDetail(ctx, state.Progress.Enrollment.LessonID); err == nil {
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
	if contextCourseID > 0 {
		courseID = contextCourseID
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

	message := newTelegramHTMLBuilder(3800)
	if strings.TrimSpace(notice) != "" {
		message.text(notice + "\n\n")
	}
	message.text(fmt.Sprintf("📚 %s\n🟣 ВОПРОС %d / %d\n\n", truncate(lessonTitle, 180), number, total))
	message.raw("<b>ВОПРОС</b>\n")
	message.raw("<blockquote>")
	message.text(question.Prompt)
	message.raw("</blockquote>")

	for _, passage := range question.BiblePassages {
		message.text("\n\n📍 Откуда взято: " + biblePassageReference(passage))
		if passage.PassageText != nil && strings.TrimSpace(*passage.PassageText) != "" {
			message.text("\n\n📜 Библейский текст:\n")
			message.raw("<blockquote>")
			message.text(truncate(*passage.PassageText, 1400))
			message.raw("</blockquote>")
		}
	}

	chapterLinks, chapterNotice := b.chapterTelegraphLinks(ctx, question.BiblePassages)
	for _, chapterLink := range chapterLinks {
		message.raw("\n\n")
		message.raw(chapterLink)
	}
	if chapterNotice != "" {
		message.text("\n\n" + chapterNotice)
	}

	var rows [][]InlineKeyboardButton

	key := answerStateKey{studentID: studentID, enrollmentID: progress.Enrollment.ID, questionID: question.ID}
	hasSaved := state.SavedAnswer != nil
	isChoice := question.AnswerType == "single_choice" || question.AnswerType == "multiple_choice"
	showOptions := isChoice && (!hasSaved || choiceEditing)

	switch question.AnswerType {
	case "text":
		if hasSaved {
			message.text("\n\n💬 Ваш сохранённый ответ:\n" + truncate(state.SavedAnswer.AnswerText, 650))
			if progress.Enrollment.Mode == "curator" {
				message.text("\n\n✅ Ответ сохранён. После отправки урока координатор просмотрит все ваши ответы.")
			} else {
				message.text("\n\n✅ Ответ сохранён. Автоматическая проверка пока не выполнена. Отправьте новый текст, чтобы изменить ответ.")
			}
		} else {
			message.text("\n\n✍️ Отправьте ответ следующим сообщением.")
		}
	case "single_choice", "multiple_choice":
		if showOptions {
			if len(question.Options) == 0 {
				message.text("\n\nДля этого вопроса пока нет доступных вариантов ответа.")
			} else if question.AnswerType == "single_choice" {
				message.text("\n\nВыберите один вариант ответа:")
			} else {
				message.text("\n\nОтметьте подходящие варианты и нажмите «Подтвердить выбор»:")
			}
			selected := b.multiDraftSelection(key)
			if question.AnswerType == "single_choice" && hasSaved {
				selected = append([]int64(nil), state.SavedAnswer.SelectedOptionIDs...)
			}
			selectedSet := make(map[int64]struct{}, len(selected))
			for _, id := range selected {
				selectedSet[id] = struct{}{}
			}
			for _, option := range question.Options {
				label := option.Text
				if _, ok := selectedSet[option.ID]; ok {
					label = "✅ " + label
				} else if question.AnswerType == "multiple_choice" {
					label = "☐ " + label
				}
				if question.AnswerType == "single_choice" {
					rows = append(rows, []InlineKeyboardButton{button(label, fmt.Sprintf("p:%d:%d:%d", progress.Enrollment.ID, question.ID, option.ID))})
				} else {
					desiredSelected := "1"
					if _, isSelected := selectedSet[option.ID]; isSelected {
						desiredSelected = "0"
					}
					rows = append(rows, []InlineKeyboardButton{button(label, fmt.Sprintf("t:%d:%d:%d:%s", progress.Enrollment.ID, question.ID, option.ID, desiredSelected))})
				}
			}
			if question.AnswerType == "multiple_choice" {
				rows = append(rows, []InlineKeyboardButton{button("Подтвердить выбор", fmt.Sprintf("s:%d:%d", progress.Enrollment.ID, question.ID))})
			}
		} else if hasSaved {
			message.text("\n\nСохранённый выбор: " + truncate(optionSummary(question.Options, state.SavedAnswer.SelectedOptionIDs), 650))
			if progress.Enrollment.Mode == "curator" {
				message.text("\n\n✅ Ответ сохранён. После отправки урока координатор просмотрит все ваши ответы.")
			} else {
				message.text("\n\n✅ Ответ сохранён. Автоматическая проверка пока не выполнена.")
			}
			rows = append(rows, []InlineKeyboardButton{button("Изменить ответ", fmt.Sprintf("e:%d:%d", progress.Enrollment.ID, question.ID))})
		}
	default:
		message.text("\n\nЭтот тип вопроса пока не поддерживается ботом.")
	}

	if message.truncated {
		message.raw("\n\n")
		message.text("Сообщение сокращено из-за ограничения Telegram.")
	}
	if progress.PreviousQuestionID != nil {
		rows = append(rows, []InlineKeyboardButton{button("← Назад", navigationCallback("prev", progress.Enrollment.ID, question.ID, courseID))})
	}
	if progress.NextQuestionID != nil {
		next := button("Далее →", navigationCallback("next", progress.Enrollment.ID, question.ID, courseID))
		if len(rows) > 0 && progress.PreviousQuestionID != nil {
			rows[len(rows)-1] = append(rows[len(rows)-1], next)
		} else {
			rows = append(rows, []InlineKeyboardButton{next})
		}
	}
	if progress.Enrollment.Status == "in_progress" && progress.TotalQuestions > 0 &&
		progress.AnsweredQuestions >= progress.TotalQuestions {
		rows = append(rows, []InlineKeyboardButton{{
			Text: map[bool]string{true: "Отправить урок", false: "Завершить урок"}[progress.Enrollment.Mode == "curator"], CallbackData: fmt.Sprintf("lesson:finish:%d", progress.Enrollment.ID),
		}})
	}
	rows = append(rows, []InlineKeyboardButton{button("Связаться с координатором", "chat:start")})
	if courseID > 0 {
		rows = append(rows, []InlineKeyboardButton{button("К урокам", fmt.Sprintf("lessons:%d", courseID))})
	} else {
		rows = append(rows, []InlineKeyboardButton{button("К курсам", "courses")})
	}
	b.renderHTML(ctx, chatID, messageID, message.String(), markup(rows))
}
func optionSummary(options []domain.QuestionOption, selectedIDs []int64) string {
	selected := make(map[int64]struct{}, len(selectedIDs))
	for _, id := range selectedIDs {
		selected[id] = struct{}{}
	}
	labels := make([]string, 0, len(selectedIDs))
	for _, option := range options {
		if _, ok := selected[option.ID]; ok {
			labels = append(labels, option.Text)
		}
	}
	if len(labels) == 0 {
		return "варианты не найдены"
	}
	return strings.Join(labels, "; ")
}

func (b *Bot) handleCallback(ctx context.Context, callback CallbackQuery) {
	b.acknowledgeCallback(ctx, callback)
	b.handleCallbackWithUpdateID(ctx, 0, callback)
}

// handleCallbackWithUpdateID performs only the business part. Callback UI
// acknowledgement must happen at the update intake boundary, before queueing.
func (b *Bot) handleCallbackWithUpdateID(ctx context.Context, updateID int64, callback CallbackQuery) bool {
	if callback.From == nil || callback.From.ID <= 0 || callback.Message == nil ||
		callback.Message.Chat.ID <= 0 || callback.Message.Chat.ID != callback.From.ID ||
		(callback.Message.Chat.Type != "" && callback.Message.Chat.Type != "private") {
		return true
	}

	chatID, messageID := callback.Message.Chat.ID, callback.Message.MessageID
	student, err := b.ensureStudent(ctx, *callback.From)
	if err != nil {
		b.logger.Warn("не удалось проверить ученика для callback", "telegram_user_id", callback.From.ID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось проверить регистрацию. Отправьте /start и попробуйте снова.", keyboard(button("К курсам", "courses")))
		return !isRetryableUpdateError(err)
	}

	parts := strings.Split(callback.Data, ":")
	switch {
	case callback.Data == "home:refresh":
		b.showStudentHome(ctx, chatID, student.ID, messageID, "")
		return true
	case callback.Data == "home:main":
		return b.showMainMenu(ctx, chatID, student.ID, messageID, "")
	case callback.Data == "home:account":
		return b.showStudentCabinet(ctx, chatID, student.ID, messageID)
	case callback.Data == "home:lessons":
		b.showStudentHome(ctx, chatID, student.ID, messageID, "")
		return true
	case callback.Data == "course:continue":
		return b.continueCurrentCourse(ctx, chatID, student.ID, messageID)
	case len(parts) == 4 && parts[0] == "course" && parts[1] == "start":
		courseID, ok := parsePositiveID(parts[3])
		if !ok || (parts[2] != "ai" && parts[2] != "curator") {
			b.showStaleCallback(ctx, chatID, student.ID, messageID)
			return true
		}
		return b.startCourseWithMode(ctx, chatID, student.ID, courseID, messageID, parts[2])
	case callback.Data == "chat:history":
		return b.showStudentConversationHistory(ctx, chatID, student, messageID, 0)
	case len(parts) == 4 && parts[0] == "chat" && parts[1] == "history" && parts[2] == "older":
		beforeMessageID, ok := parsePositiveID(parts[3])
		if !ok {
			b.showStaleCallback(ctx, chatID, student.ID, messageID)
			return true
		}
		return b.showStudentConversationHistory(ctx, chatID, student, messageID, beforeMessageID)
	case callback.Data == "chat:start":
		return b.startStudentChat(ctx, chatID, messageID, student)
	case callback.Data == "chat:cancel":
		if err := b.leaveStudentInteraction(ctx, student.ID); err != nil {
			b.logger.Warn("не удалось завершить взаимодействие ученика из меню общения", "student_id", student.ID, "error", err)
			b.render(ctx, chatID, messageID, "Не удалось выключить режим общения. Попробуйте ещё раз.", keyboard(button("Повторить", "chat:cancel")))
			return !isRetryableUpdateError(err)
		}
		b.renderCourses(ctx, chatID, student.ID, messageID, "Режим общения выключен. Выберите курс, чтобы продолжить обучение.")
		return true
	case callback.Data == "courses":
		b.showCourses(ctx, chatID, student.ID, messageID, "Выберите курс для изучения Библии.")
		return true
	case len(parts) == 2 && parts[0] == "course":
		courseID, ok := parsePositiveID(parts[1])
		if !ok {
			b.showStaleCallback(ctx, chatID, student.ID, messageID)
			return true
		}
		return b.selectCourse(ctx, chatID, student.ID, courseID, messageID)
	case len(parts) == 2 && parts[0] == "lessons":
		courseID, ok := parsePositiveID(parts[1])
		if !ok {
			b.showStaleCallback(ctx, chatID, student.ID, messageID)
			return true
		}
		b.showLessons(ctx, chatID, student.ID, courseID, messageID)
		return true
	case len(parts) == 3 && parts[0] == "lesson" && parts[1] == "finish":
		enrollmentID, ok := parsePositiveID(parts[2])
		if !ok {
			b.showStaleCallback(ctx, chatID, student.ID, messageID)
			return true
		}
		return b.finishLesson(ctx, chatID, student.ID, messageID, enrollmentID)
	case len(parts) == 3 && parts[0] == "lesson":
		courseID, okCourse := parsePositiveID(parts[1])
		lessonID, okLesson := parsePositiveID(parts[2])
		if !okCourse || !okLesson {
			b.showStaleCallback(ctx, chatID, student.ID, messageID)
			return true
		}
		return b.openCourseLesson(ctx, chatID, student.ID, courseID, lessonID, messageID)
	case len(parts) > 0 && (parts[0] == "e" || parts[0] == "p" || parts[0] == "t" || parts[0] == "s" || parts[0] == "pick-edit" || parts[0] == "pick" || parts[0] == "multi-toggle" || parts[0] == "multi-submit"):
		return b.handleAnswerCallback(ctx, updateID, chatID, student.ID, messageID, parts, callback.Message.ReplyMarkup)
	case (len(parts) == 4 || len(parts) == 5) && parts[0] == "nav" && (parts[1] == "prev" || parts[1] == "next"):
		enrollmentID, okEnrollment := parsePositiveID(parts[2])
		expectedQuestionID, okQuestion := parsePositiveID(parts[3])
		courseID := int64(0)
		okCourse := true
		if len(parts) == 5 {
			courseID, okCourse = parsePositiveID(parts[4])
		}
		if !okEnrollment || !okQuestion || !okCourse {
			b.showStaleCallback(ctx, chatID, student.ID, messageID)
			return true
		}
		return b.navigate(ctx, chatID, student.ID, messageID, enrollmentID, expectedQuestionID, courseID, parts[1])
	default:
		b.showStaleCallback(ctx, chatID, student.ID, messageID)
		return true
	}
}

func (b *Bot) finishLesson(ctx context.Context, chatID, studentID, messageID, enrollmentID int64) bool {
	progress, err := b.learning.GetProgress(ctx, studentID, enrollmentID)
	if err != nil {
		b.logger.Warn("не удалось проверить прогресс перед завершением урока", "student_id", studentID, "enrollment_id", enrollmentID, "error", err)
		b.showStaleCallback(ctx, chatID, studentID, messageID)
		return !isRetryableUpdateError(err)
	}
	if progress.Enrollment.Status != "in_progress" {
		if progress.Enrollment.Status == "completed" && b.conversations != nil {
			if err := b.conversations.SetStudentLessonMode(ctx, studentID, false); err != nil {
				b.logger.Warn("не удалось закрыть режим прохождения после повторного завершения урока", "student_id", studentID, "error", err)
				return !isRetryableUpdateError(err)
			}
		}
		b.showCourses(ctx, chatID, studentID, messageID, "Это прохождение уже не активно. Выберите следующий урок.")
		return true
	}
	if progress.TotalQuestions == 0 || progress.AnsweredQuestions < progress.TotalQuestions {
		if progress.Enrollment.CurrentQuestionID != nil {
			b.showQuestion(ctx, chatID, studentID, messageID, enrollmentID, 0)
			return true
		}
		b.render(ctx, chatID, messageID, "Сначала сохраните ответы на все вопросы урока.", keyboard(button("К курсам", "courses")))
		return true
	}
	completed, err := b.learning.CompleteLesson(ctx, studentID, enrollmentID)
	if err != nil {
		b.logger.Warn("не удалось завершить урок", "student_id", studentID, "enrollment_id", enrollmentID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось завершить урок. Попробуйте ещё раз.", keyboard(button("Повторить завершение", fmt.Sprintf("lesson:finish:%d", enrollmentID)), button("К курсам", "courses")))
		return !isRetryableUpdateError(err)
	}
	if b.conversations != nil {
		if err := b.conversations.SetStudentLessonMode(ctx, studentID, false); err != nil {
			b.logger.Warn("не удалось закрыть режим прохождения после завершения урока", "student_id", studentID, "error", err)
			// Completion is already durable. A redelivery will retry this cleanup
			// through the completed-status branch above.
			return !isRetryableUpdateError(err)
		}
	}
	text := "✅ Урок завершён!\n\nСпасибо за прохождение. Теперь можно выбрать следующий урок или связаться с координатором."
	rows := [][]InlineKeyboardButton{{button("К курсам", "courses")}, {button("Связаться с координатором", "chat:start")}}
	if completed.Mode == "curator" {
		text = "✅ Урок отправлен координатору!\n\nОн больше не отображается среди доступных уроков. Координатор самостоятельно проверит ваши ответы и при необходимости свяжется с вами лично."
		if completed.CourseEnrollmentID != nil {
			if navigator, ok := b.learning.(CourseEnrollmentNavigator); ok {
				lessons, lessonsErr := navigator.CourseLessons(ctx, studentID, *completed.CourseEnrollmentID)
				if lessonsErr == nil {
					for _, lesson := range lessons {
						if lesson.Status != "completed" {
							rows = append([][]InlineKeyboardButton{{button("Продолжить курс", "course:continue")}}, rows...)
							break
						}
					}
				}
			}
		}
	} else {
		if metadata, ok := b.catalog.(interface {
			GetLesson(context.Context, int64) (domain.Lesson, error)
		}); ok {
			if lesson, lessonErr := metadata.GetLesson(ctx, completed.LessonID); lessonErr == nil && lesson.CourseID > 0 {
				rows = append([][]InlineKeyboardButton{{button("К урокам", fmt.Sprintf("lessons:%d", lesson.CourseID))}}, rows...)
			}
		} else if detail, detailErr := b.catalog.GetLessonDetail(ctx, completed.LessonID); detailErr == nil && detail.Lesson.CourseID > 0 {
			rows = append([][]InlineKeyboardButton{{button("К урокам", fmt.Sprintf("lessons:%d", detail.Lesson.CourseID))}}, rows...)
		}
	}
	b.render(ctx, chatID, messageID, text, markup(rows))
	return true
}

func (b *Bot) navigate(ctx context.Context, chatID, studentID, messageID, enrollmentID, expectedQuestionID, courseID int64, direction string) bool {
	step := 1
	if direction == "prev" {
		step = -1
	}
	if navigator, ok := b.learning.(interface {
		NavigateQuestion(context.Context, int64, int64, int64, int) (service.NavigationState, error)
	}); ok {
		state, err := navigator.NavigateQuestion(ctx, studentID, enrollmentID, expectedQuestionID, step)
		if state.StaleCallback {
			b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, courseID)
			return true
		}
		b.clearChoiceState(answerStateKey{studentID: studentID, enrollmentID: enrollmentID, questionID: expectedQuestionID})
		if err != nil {
			if errors.Is(err, domain.ErrNoPreviousQuestion) || errors.Is(err, domain.ErrNoNextQuestion) {
				if state.Question.ID > 0 {
					b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, courseID)
				} else {
					b.showQuestion(ctx, chatID, studentID, messageID, enrollmentID, courseID)
				}
				return true
			}
			b.logger.Warn("не удалось переместиться между вопросами", "student_id", studentID, "enrollment_id", enrollmentID, "error", err)
			b.render(ctx, chatID, messageID, "Не удалось открыть вопрос. Попробуйте снова или вернитесь к каталогу.", keyboard(button("К курсам", "courses")))
			return !isRetryableUpdateError(err)
		}
		b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, courseID)
		return true
	}

	progress, err := b.learning.GetProgress(ctx, studentID, enrollmentID)
	if err != nil {
		b.logger.Warn("callback с недоступным прохождением", "student_id", studentID, "enrollment_id", enrollmentID, "error", err)
		b.showStaleCallback(ctx, chatID, studentID, messageID)
		return !isRetryableUpdateError(err)
	}
	if progress.Enrollment.CurrentQuestionID == nil {
		b.showStaleCallback(ctx, chatID, studentID, messageID)
		return true
	}
	if *progress.Enrollment.CurrentQuestionID != expectedQuestionID {
		b.showQuestion(ctx, chatID, studentID, messageID, enrollmentID, courseID)
		return true
	}
	b.clearChoiceState(answerStateKey{studentID: studentID, enrollmentID: enrollmentID, questionID: expectedQuestionID})

	var state service.NavigationState
	if direction == "prev" {
		state, err = b.learning.PreviousQuestion(ctx, studentID, enrollmentID)
	} else {
		state, err = b.learning.NextQuestion(ctx, studentID, enrollmentID)
	}
	if err != nil {
		if errors.Is(err, domain.ErrNoPreviousQuestion) || errors.Is(err, domain.ErrNoNextQuestion) {
			b.showQuestion(ctx, chatID, studentID, messageID, enrollmentID, courseID)
			return true
		}
		b.logger.Warn("не удалось переместиться между вопросами", "student_id", studentID, "enrollment_id", enrollmentID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось открыть вопрос. Попробуйте снова или вернитесь к каталогу.", keyboard(button("К курсам", "courses")))
		return !isRetryableUpdateError(err)
	}
	b.renderQuestion(ctx, chatID, studentID, messageID, state, nil, courseID)
	return true
}
func (b *Bot) showStaleCallback(ctx context.Context, chatID, studentID, messageID int64) {
	b.showCourses(ctx, chatID, studentID, messageID, "Эта кнопка устарела. Выберите курс из актуального каталога.")
}

func (b *Bot) render(ctx context.Context, chatID, messageID int64, text string, replyMarkup *InlineKeyboardMarkup) {
	if messageID <= 0 {
		sentID, err := b.api.SendMessageWithKeyboard(ctx, chatID, text, replyMarkup)
		if err != nil && ctx.Err() == nil {
			b.logger.Warn("не удалось отправить экран Telegram", "chat_id", chatID, "error", err)
		} else if err == nil {
			b.rememberMainMessage(chatID, sentID)
		}
		return
	}
	if err := b.api.EditMessageText(ctx, chatID, messageID, text, replyMarkup); err == nil || isMessageNotModified(err) {
		b.rememberMainMessage(chatID, messageID)
		return
	} else if ctx.Err() == nil {
		if !isMessageEditUnavailable(err) {
			b.logger.Warn("результат редактирования основного сообщения неоднозначен; повторная отправка подавлена",
				"chat_id", chatID, "message_id", messageID,
				"error_kind", classifyTelegramAPIError(err), "error", err)
			return
		}
		b.logger.Warn("сообщение Telegram нельзя отредактировать; отправляем замену",
			"chat_id", chatID, "message_id", messageID,
			"error_kind", classifyTelegramAPIError(err), "error", err)
		sentID, sendErr := b.api.SendMessageWithKeyboard(ctx, chatID, text, replyMarkup)
		if sendErr != nil && ctx.Err() == nil {
			b.logger.Warn("не удалось отправить новое основное сообщение Telegram",
				"chat_id", chatID, "error_kind", classifyTelegramAPIError(sendErr), "error", sendErr)
		} else if sendErr == nil {
			b.rememberMainMessage(chatID, sentID)
		}
	}
}

// renderHTML keeps formatting confined to question screens. Other bot messages remain plain text.
func (b *Bot) renderHTML(ctx context.Context, chatID, messageID int64, text string, replyMarkup *InlineKeyboardMarkup) {
	api, ok := b.api.(htmlTelegramAPI)
	if !ok {
		b.logger.Error("Telegram API client does not support HTML messages", "chat_id", chatID)
		return
	}
	if messageID <= 0 {
		sentID, err := api.SendHTMLMessageWithKeyboard(ctx, chatID, text, replyMarkup)
		if err != nil && ctx.Err() == nil {
			b.logger.Warn("не удалось отправить HTML-экран Telegram", "chat_id", chatID, "error", err)
		} else if err == nil {
			b.rememberMainMessage(chatID, sentID)
		}
		return
	}
	if err := api.EditHTMLMessageText(ctx, chatID, messageID, text, replyMarkup); err == nil || isMessageNotModified(err) {
		b.rememberMainMessage(chatID, messageID)
		return
	} else if ctx.Err() == nil {
		if !isMessageEditUnavailable(err) {
			b.logger.Warn("результат редактирования HTML-сообщения неоднозначен; повторная отправка подавлена",
				"chat_id", chatID, "message_id", messageID,
				"error_kind", classifyTelegramAPIError(err), "error", err)
			return
		}
		b.logger.Warn("HTML-сообщение Telegram нельзя отредактировать; отправляем замену",
			"chat_id", chatID, "message_id", messageID,
			"error_kind", classifyTelegramAPIError(err), "error", err)
		sentID, sendErr := api.SendHTMLMessageWithKeyboard(ctx, chatID, text, replyMarkup)
		if sendErr != nil && ctx.Err() == nil {
			b.logger.Warn("не удалось отправить новое HTML-сообщение Telegram",
				"chat_id", chatID, "error_kind", classifyTelegramAPIError(sendErr), "error", sendErr)
		} else if sendErr == nil {
			b.rememberMainMessage(chatID, sentID)
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

func navigationCallback(direction string, enrollmentID, questionID, courseID int64) string {
	callback := fmt.Sprintf("nav:%s:%d:%d:%d", direction, enrollmentID, questionID, courseID)
	if len(callback) <= 64 {
		return callback
	}
	return fmt.Sprintf("nav:%s:%d:%d", direction, enrollmentID, questionID)
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
	return classifyTelegramAPIError(err) == TelegramErrorMessageUnchanged
}
