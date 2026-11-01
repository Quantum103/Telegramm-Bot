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
	nav, ok := b.learning.(CourseEnrollmentNavigator)
	if !ok {
		b.showCourses(ctx, chatID, studentID, messageID, greeting)
		return
	}

	active, err := nav.ActiveCourse(ctx, studentID)
	if errors.Is(err, domain.ErrNotFound) {
		b.showCourses(ctx, chatID, studentID, messageID, greeting)
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
	mode := "с ИИ"
	if enrollment.Mode == "curator" {
		mode = "с куратором"
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
	}))
}

// selectCourse shows the one-time mode choice only when no course run exists.
func (b *Bot) selectCourse(ctx context.Context, chatID, studentID, courseID, messageID int64) {
	nav, ok := b.learning.(CourseEnrollmentNavigator)
	if !ok {
		b.showLessons(ctx, chatID, studentID, courseID, messageID)
		return
	}
	active, err := nav.ActiveCourse(ctx, studentID)
	if err == nil {
		if active.Enrollment.CourseID != courseID {
			b.renderActiveCourseBlocked(ctx, chatID, messageID, active)
			return
		}
		b.renderActiveCourse(ctx, chatID, messageID, active, "")
		return
	}
	if !errors.Is(err, domain.ErrNotFound) {
		b.logger.Error("не удалось проверить активный курс перед выбором режима", "student_id", studentID, "course_id", courseID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось проверить текущее прохождение. Попробуйте ещё раз.", keyboard(
			button("Повторить", fmt.Sprintf("course:%d", courseID)),
			button("К каталогу курсов", "courses"),
		))
		return
	}

	course, err := b.catalog.GetCourse(ctx, courseID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidInput) {
			b.render(ctx, chatID, messageID, "Этот курс больше недоступен. Откройте каталог курсов.", keyboard(button("К курсам", "courses")))
			return
		}
		b.logger.Error("не удалось получить курс перед выбором режима", "student_id", studentID, "course_id", courseID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось открыть курс. Попробуйте позже.", keyboard(button("К курсам", "courses")))
		return
	}
	lessons, err := b.catalog.ListCourseLessons(ctx, courseID)
	if err != nil {
		b.logger.Error("не удалось проверить уроки перед выбором режима", "student_id", studentID, "course_id", courseID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось загрузить уроки курса. Попробуйте позже.", keyboard(button("К курсам", "courses")))
		return
	}
	if len(lessons) == 0 {
		b.render(ctx, chatID, messageID,
			"Курс: "+truncate(course.Title, 180)+"\n\nВ этом курсе пока нет опубликованных уроков.",
			keyboard(button("К курсам", "courses")))
		return
	}

	message := fmt.Sprintf(
		"Курс: %s\n\nКак вы хотите проходить этот курс?\n\n⭐ С куратором — специалист будет проверять ваши ответы и помогать разобраться в сложных моментах.\n\nС ИИ — искусственный интеллект будет проверять ваши ответы.",
		truncate(course.Title, 180),
	)
	b.render(ctx, chatID, messageID, message, markup([][]InlineKeyboardButton{
		{button("⭐ С куратором", fmt.Sprintf("course:start:curator:%d", courseID))},
		{button("С ИИ", fmt.Sprintf("course:start:ai:%d", courseID))},
		{button("К курсам", "courses")},
	}))
}

// startCourseWithMode sends the selection to the backend. The mode displayed
// afterwards always comes from the saved enrollment, never from callback data.
func (b *Bot) startCourseWithMode(
	ctx context.Context,
	chatID, studentID, courseID, messageID int64,
	requestedMode string,
) {
	nav, ok := b.learning.(CourseEnrollmentNavigator)
	if !ok {
		b.showStaleCallback(ctx, chatID, studentID, messageID)
		return
	}
	if requestedMode != "ai" && requestedMode != "curator" {
		b.showStaleCallback(ctx, chatID, studentID, messageID)
		return
	}

	run, err := nav.StartCourse(ctx, studentID, courseID, requestedMode)
	if errors.Is(err, domain.ErrNoCurator) {
		// A concurrent AI selection may have won the race. In that case the
		// persisted mode is authoritative and must not be overwritten.
		if current, currentErr := nav.ActiveCourse(ctx, studentID); currentErr == nil &&
			current.Enrollment.CourseID == courseID {
			run, err = current, nil
		} else {
			b.render(ctx, chatID, messageID,
				"Пока не удалось начать курс с куратором: вам ещё не назначен активный куратор. Администратор платформы должен назначить его. Ваш выбор не изменён — после назначения можно будет повторить попытку.",
				markup([][]InlineKeyboardButton{
					{button("Повторить выбор способа", fmt.Sprintf("course:%d", courseID))},
					{button("К курсам", "courses")},
				}))
			return
		}
	}
	if errors.Is(err, domain.ErrActiveCourse) {
		b.renderActiveCourseBlocked(ctx, chatID, messageID, run)
		return
	}
	if errors.Is(err, domain.ErrLegacyProgress) {
		b.render(ctx, chatID, messageID,
			"Найдено старое незавершённое прохождение, которое нельзя безопасно связать с курсом автоматически. Обратитесь к администратору, чтобы сохранить и восстановить ваш прогресс.",
			keyboard(button("К курсам", "courses")))
		return
	}
	if err != nil {
		b.logger.Error("не удалось сохранить способ прохождения курса", "student_id", studentID, "course_id", courseID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось начать курс. Выбор способа обучения не сохранён. Попробуйте ещё раз.", keyboard(
			button("Повторить выбор", fmt.Sprintf("course:%d", courseID)),
			button("К курсам", "courses"),
		))
		return
	}

	notice := "✅ Вы выбрали прохождение курса с ИИ. Искусственный интеллект будет проверять ваши ответы."
	if run.Enrollment.Mode == "curator" {
		notice = "✅ Вы будете проходить этот курс с куратором. Назначенный специалист будет проверять ваши ответы и помогать разобраться в сложных моментах."
	}
	b.openFirstIncompleteLesson(ctx, nav, chatID, studentID, messageID, run, notice)
}

func (b *Bot) continueCurrentCourse(ctx context.Context, chatID, studentID, messageID int64) {
	nav, ok := b.learning.(CourseEnrollmentNavigator)
	if !ok {
		b.showCourses(ctx, chatID, studentID, messageID, "Выберите курс для продолжения.")
		return
	}
	active, err := nav.ActiveCourse(ctx, studentID)
	if errors.Is(err, domain.ErrNotFound) {
		b.showCourses(ctx, chatID, studentID, messageID, "У вас пока нет незавершённого курса. Выберите курс для изучения.")
		return
	}
	if err != nil {
		b.logger.Error("не удалось восстановить курс для продолжения", "student_id", studentID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось восстановить курс. Попробуйте ещё раз.", keyboard(button("Повторить", "course:continue")))
		return
	}

	// StartCourse resumes a paused run, and for an active run returns its
	// existing database record without changing the originally chosen mode.
	run, err := nav.StartCourse(ctx, studentID, active.Enrollment.CourseID, active.Enrollment.Mode)
	if errors.Is(err, domain.ErrNoCurator) {
		b.render(ctx, chatID, messageID,
			"Не удалось продолжить курс с куратором: назначение куратора сейчас неактивно. Администратор платформы должен восстановить назначение. Режим обучения не изменён.",
			keyboard(button("Повторить", "course:continue"), button("К курсам", "courses")))
		return
	}
	if err != nil {
		b.logger.Error("не удалось продолжить сохранённый курс", "student_id", studentID, "course_id", active.Enrollment.CourseID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось продолжить курс. Попробуйте ещё раз.", keyboard(button("Повторить", "course:continue")))
		return
	}
	b.openFirstIncompleteLesson(ctx, nav, chatID, studentID, messageID, run, "")
}

func (b *Bot) openFirstIncompleteLesson(
	ctx context.Context,
	nav CourseEnrollmentNavigator,
	chatID, studentID, messageID int64,
	run domain.CourseEnrollmentProgress,
	notice string,
) {
	lessons, err := nav.CourseLessons(ctx, studentID, run.Enrollment.ID)
	if err != nil {
		b.logger.Error("не удалось получить сохранённые уроки курса", "student_id", studentID, "course_enrollment_id", run.Enrollment.ID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось загрузить прогресс уроков курса. Попробуйте ещё раз.", keyboard(button("Продолжить курс", "course:continue")))
		return
	}
	for _, lesson := range lessons {
		if lesson.Status == "completed" {
			continue
		}
		b.openCourseRunLesson(ctx, nav, chatID, studentID, messageID, run, lesson, notice)
		return
	}
	b.render(ctx, chatID, messageID,
		fmt.Sprintf("Вы завершили все уроки курса „%s“. Теперь можно вернуться в каталог и выбрать следующий курс.", truncate(run.Enrollment.CourseTitle, 180)),
		keyboard(button("К курсам", "courses")))
}

func (b *Bot) openCourseRunLesson(
	ctx context.Context,
	nav CourseEnrollmentNavigator,
	chatID, studentID, messageID int64,
	run domain.CourseEnrollmentProgress,
	lesson domain.CourseEnrollmentLesson,
	notice string,
) {
	progress, err := nav.StartLessonForCourse(ctx, studentID, run.Enrollment.ID, run.Enrollment.CourseID, lesson.LessonID)
	if errors.Is(err, domain.ErrNoCurator) {
		b.render(ctx, chatID, messageID,
			"Не удалось открыть урок с куратором: активное назначение куратора недоступно. Администратор платформы должен проверить назначение. Режим обучения не изменён.",
			keyboard(button("Повторить", "course:continue")))
		return
	}
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidState) || errors.Is(err, domain.ErrInvalidInput) {
		b.render(ctx, chatID, messageID, "Этот урок больше недоступен в сохранённом прохождении. Обратитесь к администратору платформы.", keyboard(
			button("Продолжить курс", "course:continue"),
			button("К курсам", "courses"),
		))
		return
	}
	if err != nil {
		b.logger.Error("не удалось восстановить урок курса", "student_id", studentID, "course_enrollment_id", run.Enrollment.ID, "lesson_id", lesson.LessonID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось открыть урок. Попробуйте ещё раз.", keyboard(button("Повторить", "course:continue")))
		return
	}
	questionID := int64(0)
	if progress.Enrollment.CurrentQuestionID != nil {
		questionID = *progress.Enrollment.CurrentQuestionID
	} else if progress.FirstQuestionID != nil {
		questionID = *progress.FirstQuestionID
	}
	if questionID <= 0 {
		b.render(ctx, chatID, messageID, "В этом уроке пока нет доступных вопросов.", keyboard(button("Продолжить курс", "course:continue")))
		return
	}
	state, err := b.learning.OpenQuestion(ctx, studentID, progress.Enrollment.ID, questionID)
	if err != nil {
		b.logger.Error("не удалось восстановить вопрос курса", "student_id", studentID, "course_enrollment_id", run.Enrollment.ID, "lesson_id", lesson.LessonID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось открыть сохранённый вопрос. Попробуйте продолжить курс ещё раз.", keyboard(button("Продолжить курс", "course:continue")))
		return
	}
	lessonForScreen := domain.Lesson{
		ID: lesson.LessonID, CourseID: run.Enrollment.CourseID,
		CourseTitle: run.Enrollment.CourseTitle, Title: lesson.Title,
	}
	b.renderQuestionWithNotice(ctx, chatID, studentID, messageID, state, &lessonForScreen, run.Enrollment.CourseID, notice)
}

func (b *Bot) openCourseLesson(ctx context.Context, chatID, studentID, courseID, lessonID, messageID int64) {
	nav, ok := b.learning.(CourseEnrollmentNavigator)
	if !ok {
		b.openLesson(ctx, chatID, studentID, courseID, lessonID, messageID)
		return
	}
	active, err := nav.ActiveCourse(ctx, studentID)
	if errors.Is(err, domain.ErrNotFound) {
		b.selectCourse(ctx, chatID, studentID, courseID, messageID)
		return
	}
	if err != nil {
		b.logger.Error("не удалось проверить активный курс перед открытием урока", "student_id", studentID, "course_id", courseID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось проверить текущее прохождение. Попробуйте ещё раз.", keyboard(button("Продолжить курс", "course:continue")))
		return
	}
	if active.Enrollment.CourseID != courseID {
		b.renderActiveCourseBlocked(ctx, chatID, messageID, active)
		return
	}
	lessons, err := nav.CourseLessons(ctx, studentID, active.Enrollment.ID)
	if err != nil {
		b.logger.Error("не удалось проверить состав активного курса", "student_id", studentID, "course_enrollment_id", active.Enrollment.ID, "error", err)
		b.render(ctx, chatID, messageID, "Не удалось загрузить уроки текущего курса. Попробуйте ещё раз.", keyboard(button("Продолжить курс", "course:continue")))
		return
	}
	for _, lesson := range lessons {
		if lesson.LessonID == lessonID {
			if lesson.Status == "completed" {
				b.render(ctx, chatID, messageID, "Этот урок уже завершён. Продолжите курс со следующего доступного урока.", keyboard(button("Продолжить курс", "course:continue")))
				return
			}
			b.openCourseRunLesson(ctx, nav, chatID, studentID, messageID, active, lesson, "")
			return
		}
	}
	b.render(ctx, chatID, messageID,
		"Этот урок не входит в сохранённый состав вашего курса. Откройте урок из текущего прохождения.",
		keyboard(button("Продолжить курс", "course:continue")))
}
