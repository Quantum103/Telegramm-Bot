package telegrambot

import (
	"context"
	"errors"
	"fmt"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

// CourseEnrollmentNavigator exposes the PostgreSQL-backed course run workflow.
// The bot deliberately treats it as an optional extension to keep the older
// constructor and test doubles compatible.
type CourseEnrollmentNavigator interface {
	StartCourse(context.Context, int64, int64, string) (domain.CourseEnrollmentProgress, error)
	ActiveCourse(context.Context, int64) (domain.CourseEnrollmentProgress, error)
	CourseLessons(context.Context, int64, int64) ([]domain.CourseEnrollmentLesson, error)
	StartLessonForCourse(context.Context, int64, int64, int64, int64) (domain.Progress, error)
}

// showStudentHome restores the student's active course from PostgreSQL. The
// Telegram process never keeps the learning mode or course progress in memory.
func (b *Bot) showStudentHome(ctx context.Context, chatID, studentID, messageID int64, greeting string) {
	if err := b.leaveStudentInteraction(ctx, studentID); err != nil {
		b.logger.Warn("не удалось завершить взаимодействие ученика при открытии главного экрана", "student_id", studentID, "error", err)
	}
	nav, ok := b.learning.(CourseEnrollmentNavigator)
	if !ok {
		b.renderCourses(ctx, chatID, studentID, messageID, greeting)
		return
	}

	active, err := nav.ActiveCourse(ctx, studentID)
	if errors.Is(err, domain.ErrNotFound) {
		b.renderCourses(ctx, chatID, studentID, messageID, greeting)
		return
	}
	if err != nil {
		b.logger.Error("не удалось восстановить активный курс ученика", "student_id", studentID, "error", err)
		b.render(ctx, chatID, messageID,
			"Не удалось проверить ваше текущее прохождение курса. Попробуйте обновить экран.",
			keyboard(button("Повторить", "home:refresh"), button("Каталог курсов", "courses")))
		return
	}
	b.renderActiveCourse(ctx, chatID, messageID, active, greeting)
}

func (b *Bot) renderActiveCourse(
	ctx context.Context,
	chatID, messageID int64,
	progress domain.CourseEnrollmentProgress,
	prefix string,
) {
	enrollment := progress.Enrollment
	mode := "самостоятельно"
	if enrollment.Mode == "curator" {
		mode = "с координатором"
	}
	text := ""
	if prefix != "" {
		text = prefix + "\n\n"
	}
	text += fmt.Sprintf(
		"Ваш текущий курс: „%s“\n\nПрогресс: %d из %d уроков завершено.\nСпособ обучения: %s.\n\nПродолжайте с сохранённого места.",
		truncate(enrollment.CourseTitle, 180),
		progress.CompletedLessons,
		progress.TotalLessons,
		mode,
	)
	b.render(ctx, chatID, messageID, text, markup([][]InlineKeyboardButton{
		{button("Продолжить курс", "course:continue")},
		{button("Каталог курсов", "courses")},
		{button("Главное меню", "home:main")},
	}))
}

func (b *Bot) renderActiveCourseBlocked(
	ctx context.Context,
	chatID, messageID int64,
	progress domain.CourseEnrollmentProgress,
) {
	enrollment := progress.Enrollment
	message := fmt.Sprintf(
		"У вас уже есть незавершённый курс „%s“. Сначала завершите его, чтобы перейти к следующему.\n\nПрогресс: %d из %d уроков завершено.",
		truncate(enrollment.CourseTitle, 180),
		progress.CompletedLessons,
		progress.TotalLessons,
	)
	b.render(ctx, chatID, messageID, message, markup([][]InlineKeyboardButton{
		{button("Продолжить текущий курс", "course:continue")},
		{button("К каталогу курсов", "courses")},
		{button("Главное меню", "home:main")},
	}))
}

// selectCourse shows the one-time mode choice only when no course run exists.

func (b *Bot) selectCourse(ctx context.Context, chatID, studentID, courseID, messageID int64) bool {
	nav, ok := b.learning.(CourseEnrollmentNavigator)
	if !ok {
		b.showLessons(ctx, chatID, studentID, courseID, messageID)
		return true
	}
	active, err := nav.ActiveCourse(ctx, studentID)
	if err == nil {
		if active.Enrollment.CourseID != courseID {
			b.renderActiveCourseBlocked(ctx, chatID, messageID, active)
			return true
		}
		b.renderActiveCourse(ctx, chatID, messageID, active, "")
		return true
	}
	if !errors.Is(err, domain.ErrNotFound) {
		b.logger.Error("не удалось проверить активный курс перед выбором режима", "student_id", studentID, "course_id", courseID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось проверить текущее прохождение. Попробуйте ещё раз.", keyboard(
			button("Повторить", fmt.Sprintf("course:%d", courseID)),
			button("К каталогу курсов", "courses"),
		))
		return !isRetryableUpdateError(err)
	}

	course, err := b.catalog.GetCourse(ctx, courseID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidInput) {
			b.render(ctx, chatID, messageID, "Этот курс больше недоступен. Откройте каталог курсов.", keyboard(button("К курсам", "courses")))
			return true
		}
		b.logger.Error("не удалось получить курс перед выбором режима", "student_id", studentID, "course_id", courseID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось открыть курс. Попробуйте позже.", keyboard(button("К курсам", "courses")))
		return !isRetryableUpdateError(err)
	}
	lessons, err := b.catalog.ListCourseLessons(ctx, courseID)
	if err != nil {
		b.logger.Error("не удалось проверить уроки перед выбором режима", "student_id", studentID, "course_id", courseID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось загрузить уроки курса. Попробуйте позже.", keyboard(button("К курсам", "courses")))
		return !isRetryableUpdateError(err)
	}
	if len(lessons) == 0 {
		b.render(ctx, chatID, messageID,
			"Курс: "+truncate(course.Title, 180)+"\n\nВ этом курсе пока нет опубликованных уроков.",
			keyboard(button("К курсам", "courses")))
		return true
	}

	message := fmt.Sprintf(
		"Курс: %s\n\nКак вы хотите проходить этот курс?\n\n⭐ С координатором — человек будет помогать вам и отвечать на вопросы.\n\nСамостоятельно — проходите уроки в своём темпе. Ответы сохраняются автоматически; при сложном вопросе можно связаться с координатором.",
		truncate(course.Title, 180),
	)
	b.render(ctx, chatID, messageID, message, markup([][]InlineKeyboardButton{
		{button("⭐ С координатором", fmt.Sprintf("course:start:curator:%d", courseID))},
		{button("Самостоятельно", fmt.Sprintf("course:start:ai:%d", courseID))},
		{button("К курсам", "courses")},
	}))
	return true
}

func (b *Bot) startCourseWithMode(
	ctx context.Context,
	chatID, studentID, courseID, messageID int64,
	requestedMode string,
) bool {
	nav, ok := b.learning.(CourseEnrollmentNavigator)
	if !ok {
		b.showStaleCallback(ctx, chatID, studentID, messageID)
		return true
	}
	if requestedMode != "ai" && requestedMode != "curator" {
		b.showStaleCallback(ctx, chatID, studentID, messageID)
		return true
	}

	run, err := nav.StartCourse(ctx, studentID, courseID, requestedMode)
	if errors.Is(err, domain.ErrNoCurator) {
		// A concurrent AI selection may have won the race. In that case the
		// persisted mode is authoritative and must not be overwritten.
		current, currentErr := nav.ActiveCourse(ctx, studentID)
		if currentErr == nil && current.Enrollment.CourseID == courseID &&
			current.Enrollment.Status == "in_progress" {
			run, err = current, nil
		} else {
			if currentErr != nil && !errors.Is(currentErr, domain.ErrNotFound) {
				return !isRetryableUpdateError(currentErr)
			}
			b.render(ctx, chatID, messageID,
				"Сейчас нет доступного координатора с подключённым Telegram. Курс не начат. Ваш выбор не изменён. Можно повторить попытку позже или выбрать самостоятельное прохождение.",
				markup([][]InlineKeyboardButton{
					{button("Повторить выбор способа", fmt.Sprintf("course:%d", courseID))},
					{button("К курсам", "courses")},
				}))
			return true
		}
	}
	if errors.Is(err, domain.ErrActiveCourse) {
		b.renderActiveCourseBlocked(ctx, chatID, messageID, run)
		return true
	}
	if errors.Is(err, domain.ErrLegacyProgress) {
		b.render(ctx, chatID, messageID,
			"Найдено старое незавершённое прохождение, которое нельзя безопасно связать с курсом автоматически. Обратитесь к администратору, чтобы сохранить и восстановить ваш прогресс.",
			keyboard(button("К курсам", "courses")))
		return true
	}
	if err != nil {
		b.logger.Error("не удалось сохранить способ прохождения курса", "student_id", studentID, "course_id", courseID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось начать курс. Выбор способа обучения не сохранён. Попробуйте ещё раз.", keyboard(
			button("Повторить выбор", fmt.Sprintf("course:%d", courseID)),
			button("К курсам", "courses"),
		))
		return !isRetryableUpdateError(err)
	}

	notice := "✅ Вы выбрали самостоятельное прохождение. Ответы будут сохраняться автоматически, а при сложном вопросе вы сможете связаться с координатором."
	if run.Enrollment.Mode == "curator" {
		notice = "✅ Вы будете проходить этот курс с координатором. Он поможет вам разобраться в сложных моментах и ответит на вопросы."
	}
	return b.openFirstIncompleteLesson(ctx, nav, chatID, studentID, messageID, run, notice)
}

func (b *Bot) continueCurrentCourse(ctx context.Context, chatID, studentID, messageID int64) bool {
	nav, ok := b.learning.(CourseEnrollmentNavigator)
	if !ok {
		b.showCourses(ctx, chatID, studentID, messageID, "Выберите курс для продолжения.")
		return true
	}
	active, err := nav.ActiveCourse(ctx, studentID)
	if errors.Is(err, domain.ErrNotFound) {
		b.showCourses(ctx, chatID, studentID, messageID, "У вас пока нет незавершённого курса. Выберите курс для изучения.")
		return true
	}
	if err != nil {
		b.logger.Error("не удалось восстановить курс для продолжения", "student_id", studentID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось восстановить курс. Попробуйте ещё раз.", keyboard(button("Повторить", "course:continue")))
		return !isRetryableUpdateError(err)
	}

	run, err := nav.StartCourse(ctx, studentID, active.Enrollment.CourseID, active.Enrollment.Mode)
	if errors.Is(err, domain.ErrNoCurator) {
		b.render(ctx, chatID, messageID,
			"Сейчас нет доступного координатора для продолжения курса. Ваш прогресс и выбранный способ обучения сохранены. Попробуйте позже или обратитесь к администратору платформы.",
			keyboard(button("Повторить", "course:continue"), button("К курсам", "courses")))
		return true
	}
	if err != nil {
		b.logger.Error("не удалось продолжить сохранённый курс", "student_id", studentID, "course_id", active.Enrollment.CourseID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось продолжить курс. Попробуйте ещё раз.", keyboard(button("Повторить", "course:continue")))
		return !isRetryableUpdateError(err)
	}
	return b.openFirstIncompleteLesson(ctx, nav, chatID, studentID, messageID, run, "")
}

func (b *Bot) openFirstIncompleteLesson(
	ctx context.Context,
	nav CourseEnrollmentNavigator,
	chatID, studentID, messageID int64,
	run domain.CourseEnrollmentProgress,
	notice string,
) bool {
	lessons, err := nav.CourseLessons(ctx, studentID, run.Enrollment.ID)
	if err != nil {
		b.logger.Error("не удалось получить сохранённые уроки курса", "student_id", studentID, "course_enrollment_id", run.Enrollment.ID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось загрузить прогресс уроков курса. Попробуйте ещё раз.", keyboard(button("Продолжить курс", "course:continue")))
		return !isRetryableUpdateError(err)
	}
	for _, lesson := range lessons {
		if lesson.Status == "completed" {
			continue
		}
		return b.openCourseRunLesson(ctx, nav, chatID, studentID, messageID, run, lesson, notice)
	}
	message := fmt.Sprintf("Вы завершили все уроки курса „%s“. Теперь можно вернуться в каталог и выбрать следующий курс.", truncate(run.Enrollment.CourseTitle, 180))
	rows := [][]InlineKeyboardButton{
		{button("К курсам", "courses")},
		{button("Главное меню", "home:main")},
	}
	if run.Enrollment.Mode == "curator" {
		message = fmt.Sprintf("✅ Вы отправили все уроки курса „%s“ координатору. Он проверит ответы и при необходимости свяжется с вами лично.", truncate(run.Enrollment.CourseTitle, 180))
		rows = append(rows, []InlineKeyboardButton{button("Связаться с координатором", "chat:start")})
	}
	b.render(ctx, chatID, messageID, message, markup(rows))
	return true
}

func (b *Bot) openCourseRunLesson(
	ctx context.Context,
	nav CourseEnrollmentNavigator,
	chatID, studentID, messageID int64,
	run domain.CourseEnrollmentProgress,
	lesson domain.CourseEnrollmentLesson,
	notice string,
) bool {
	progress, err := nav.StartLessonForCourse(ctx, studentID, run.Enrollment.ID, run.Enrollment.CourseID, lesson.LessonID)
	if errors.Is(err, domain.ErrNoCurator) {
		b.render(ctx, chatID, messageID,
			"Не удалось открыть урок с координатором: активное назначение недоступно. Администратор платформы должен проверить назначение. Режим обучения не изменён.",
			keyboard(button("Повторить", "course:continue")))
		return true
	}
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidState) || errors.Is(err, domain.ErrInvalidInput) {
		b.render(ctx, chatID, messageID, "Этот урок больше недоступен в сохранённом прохождении. Обратитесь к администратору платформы.", keyboard(
			button("Продолжить курс", "course:continue"),
			button("К курсам", "courses"),
		))
		return true
	}
	if err != nil {
		b.logger.Error("не удалось восстановить урок курса", "student_id", studentID, "course_enrollment_id", run.Enrollment.ID, "lesson_id", lesson.LessonID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось открыть урок. Попробуйте ещё раз.", keyboard(button("Повторить", "course:continue")))
		return !isRetryableUpdateError(err)
	}
	questionID := int64(0)
	if progress.Enrollment.CurrentQuestionID != nil {
		questionID = *progress.Enrollment.CurrentQuestionID
	} else if progress.FirstQuestionID != nil {
		questionID = *progress.FirstQuestionID
	}
	if questionID <= 0 {
		b.render(ctx, chatID, messageID, "В этом уроке пока нет доступных вопросов.", keyboard(button("Продолжить курс", "course:continue")))
		return true
	}
	state, err := b.learning.OpenQuestion(ctx, studentID, progress.Enrollment.ID, questionID)
	if err != nil {
		b.logger.Error("не удалось восстановить вопрос курса", "student_id", studentID, "course_enrollment_id", run.Enrollment.ID, "lesson_id", lesson.LessonID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось открыть сохранённый вопрос. Попробуйте продолжить курс ещё раз.", keyboard(button("Продолжить курс", "course:continue")))
		return !isRetryableUpdateError(err)
	}
	lessonForScreen := domain.Lesson{
		ID: lesson.LessonID, CourseID: run.Enrollment.CourseID,
		CourseTitle: run.Enrollment.CourseTitle, Title: lesson.Title,
	}
	b.renderQuestionWithNotice(ctx, chatID, studentID, messageID, state, &lessonForScreen, run.Enrollment.CourseID, notice)
	return true
}

func (b *Bot) openCourseLesson(ctx context.Context, chatID, studentID, courseID, lessonID, messageID int64) bool {
	nav, ok := b.learning.(CourseEnrollmentNavigator)
	if !ok {
		b.openLesson(ctx, chatID, studentID, courseID, lessonID, messageID)
		return true
	}
	active, err := nav.ActiveCourse(ctx, studentID)
	if errors.Is(err, domain.ErrNotFound) {
		return b.selectCourse(ctx, chatID, studentID, courseID, messageID)
	}
	if err != nil {
		b.logger.Error("не удалось проверить активный курс перед открытием урока", "student_id", studentID, "course_id", courseID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось проверить текущее прохождение. Попробуйте ещё раз.", keyboard(button("Продолжить курс", "course:continue")))
		return !isRetryableUpdateError(err)
	}
	if active.Enrollment.CourseID != courseID {
		b.renderActiveCourseBlocked(ctx, chatID, messageID, active)
		return true
	}
	lessons, err := nav.CourseLessons(ctx, studentID, active.Enrollment.ID)
	if err != nil {
		b.logger.Error("не удалось проверить состав активного курса", "student_id", studentID, "course_enrollment_id", active.Enrollment.ID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось загрузить уроки текущего курса. Попробуйте ещё раз.", keyboard(button("Продолжить курс", "course:continue")))
		return !isRetryableUpdateError(err)
	}
	for _, lesson := range lessons {
		if lesson.LessonID == lessonID {
			if lesson.Status == "completed" {
				b.render(ctx, chatID, messageID, "Этот урок уже завершён. Продолжите курс со следующего доступного урока.", keyboard(button("Продолжить курс", "course:continue")))
				return true
			}
			return b.openCourseRunLesson(ctx, nav, chatID, studentID, messageID, active, lesson, "")
		}
	}
	b.render(ctx, chatID, messageID,
		"Этот урок не входит в сохранённый состав вашего курса. Откройте урок из текущего прохождения.",
		keyboard(button("Продолжить курс", "course:continue")))
	return true
}
