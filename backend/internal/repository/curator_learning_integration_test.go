package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

type curatorTestLessonRun struct {
	run        domain.CourseEnrollmentProgress
	lesson     domain.Lesson
	enrollment domain.Enrollment
	questionID int64
	answer     domain.SavedAnswer
}

func createCuratorTestStudent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string) domain.Student {
	t.Helper()
	telegramID := time.Now().UnixNano()
	student, err := NewStudentRepository(pool).Create(ctx, CreateStudentParams{
		TelegramID:  telegramID,
		DisplayName: fmt.Sprintf("%s-%d", label, telegramID),
	})
	if err != nil {
		t.Fatalf("создать ученика %s: %v", label, err)
	}
	return student
}

func startCuratorTestLesson(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	studentID int64,
	run domain.CourseEnrollmentProgress,
	lessonID int64,
	answerText string,
) curatorTestLessonRun {
	t.Helper()
	courses := NewCourseEnrollmentRepository(pool)
	lesson, err := courses.GetLessonForRun(ctx, studentID, run.Enrollment.ID, lessonID)
	if err != nil {
		t.Fatalf("получить урок запуска курса: %v", err)
	}
	enrollment, err := NewEnrollmentRepository(pool).Start(ctx, domain.StartEnrollmentParams{
		StudentID: studentID, LessonID: lesson.ID,
		LessonVersionID:    lesson.LessonVersionID,
		CourseEnrollmentID: run.Enrollment.ID,
		Mode:               run.Enrollment.Mode,
	})
	if err != nil {
		t.Fatalf("начать урок курса: %v", err)
	}
	var questionID int64
	if err := pool.QueryRow(ctx,
		"SELECT id FROM lesson_questions WHERE lesson_version_id = $1 ORDER BY question_number LIMIT 1",
		lesson.LessonVersionID,
	).Scan(&questionID); err != nil {
		t.Fatalf("получить вопрос урока: %v", err)
	}
	answer, err := NewAnswerRepository(pool).Submit(ctx, AnswerSubmission{
		StudentID: studentID, EnrollmentID: enrollment.ID,
		QuestionID: questionID, AnswerText: answerText,
	})
	if err != nil {
		t.Fatalf("отправить ответ: %v", err)
	}
	return curatorTestLessonRun{
		run: run, lesson: lesson, enrollment: enrollment,
		questionID: questionID, answer: answer,
	}
}

func TestCuratorLearningReadScopeActiveCourseAndAdminAIRead(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	firstStudent := createCuratorTestStudent(t, ctx, pool, "curator-read-first")
	secondStudent := createCuratorTestStudent(t, ctx, pool, "curator-read-second")
	adminID := createStaffForCourseTest(t, ctx, pool, "administrator")
	firstCuratorID := createStaffForCourseTest(t, ctx, pool, "curator")
	secondCuratorID := createStaffForCourseTest(t, ctx, pool, "curator")

	courseID, lessonIDs, _ := createCourseForEnrollmentTest(
		t, ctx, pool, fmt.Sprintf("%d", time.Now().UnixNano()), "Курс для проверки доступа", 1,
	)
	staff := NewStaffManagementRepository(pool)
	if err := staff.AssignStudentCurator(ctx, adminID, firstStudent.ID, firstCuratorID); err != nil {
		t.Fatalf("назначить первого куратора: %v", err)
	}
	if err := staff.AssignStudentCurator(ctx, adminID, secondStudent.ID, secondCuratorID); err != nil {
		t.Fatalf("назначить второго куратора: %v", err)
	}

	courses := NewCourseEnrollmentRepository(pool)
	firstRun, err := courses.Start(ctx, firstStudent.ID, courseID, "curator")
	if err != nil {
		t.Fatalf("начать курс в режиме curator: %v", err)
	}
	secondRun, err := courses.Start(ctx, secondStudent.ID, courseID, "ai")
	if err != nil {
		t.Fatalf("начать курс в режиме ai: %v", err)
	}
	firstLesson := startCuratorTestLesson(t, ctx, pool, firstStudent.ID, firstRun, lessonIDs[0], "Ответ первого ученика")
	secondLesson := startCuratorTestLesson(t, ctx, pool, secondStudent.ID, secondRun, lessonIDs[0], "Ответ второго ученика")
	if _, err := NewEnrollmentRepository(pool).Complete(ctx, firstStudent.ID, firstLesson.enrollment.ID); err != nil {
		t.Fatalf("отправить урок первого ученика до получения списка: %v", err)
	}

	// Список, используемый новым API, должен ограничивать куратора текущим
	// назначением и сохранять доступ к завершённому, но ожидающему проверки уроку.
	firstCuratorStudents, err := staff.ListStudentsForStaff(ctx, firstCuratorID)
	if err != nil || len(firstCuratorStudents) != 1 || firstCuratorStudents[0].ID != firstStudent.ID {
		t.Fatalf("куратор получил чужих учеников: students=%+v err=%v", firstCuratorStudents, err)
	}
	if active := firstCuratorStudents[0].ActiveCourse; active == nil ||
		active.CourseEnrollmentID != firstRun.Enrollment.ID ||
		active.CourseTitle != firstRun.Enrollment.CourseTitle ||
		active.Mode != "curator" || active.Status != "completed" || active.TotalLessons != 1 || active.CompletedLessons != 1 ||
		active.PendingReviews != 1 || active.NeedsRevision != 0 {
		t.Fatalf("активный курс или счётчики проверки не включены в список закреплённых учеников: %+v", firstCuratorStudents[0])
	}
	adminStudents, err := staff.ListStudentsForStaff(ctx, adminID)
	if err != nil || len(adminStudents) != 2 {
		t.Fatalf("администратор не получил всех учеников: students=%+v err=%v", adminStudents, err)
	}
	activeModes := map[int64]string{}
	for _, student := range adminStudents {
		if student.ActiveCourse != nil {
			activeModes[student.ID] = student.ActiveCourse.Mode
			if student.ID == secondStudent.ID &&
				(student.ActiveCourse.PendingReviews != 0 || student.ActiveCourse.NeedsRevision != 0) {
				t.Fatalf("AI-прохождение получило счётчики ручной проверки: %+v", student.ActiveCourse)
			}
		}
	}
	if activeModes[firstStudent.ID] != "curator" || activeModes[secondStudent.ID] != "ai" {
		t.Fatalf("администратор не видит режимы активных курсов: %+v", activeModes)
	}

	curatorLearning := NewCuratorLearningRepository(pool)
	access, err := curatorLearning.ListCourseLessons(ctx, firstCuratorID, firstStudent.ID, firstRun.Enrollment.ID)
	if err != nil || access.Enrollment.Mode != "curator" || len(access.Lessons) != 1 {
		t.Fatalf("назначенный куратор не видит уроки своего ученика: access=%+v err=%v", access, err)
	}
	firstAnswers, err := curatorLearning.ListLessonAnswers(
		ctx, firstCuratorID, firstStudent.ID, firstRun.Enrollment.ID, lessonIDs[0],
	)
	if err != nil || len(firstAnswers) != 1 || len(firstAnswers[0].Attempts) != 1 ||
		firstAnswers[0].Attempts[0].ID != firstLesson.answer.AttemptID {
		t.Fatalf("куратор не видит ответ и историю попыток: answers=%+v err=%v", firstAnswers, err)
	}
	if _, err := curatorLearning.ListCourseLessons(ctx, firstCuratorID, secondStudent.ID, secondRun.Enrollment.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("куратор получил курс чужого ученика через подменённый ID: %v", err)
	}
	if _, err := curatorLearning.ListLessonAnswers(ctx, firstCuratorID, secondStudent.ID, secondRun.Enrollment.ID, lessonIDs[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("куратор получил ответы чужого ученика через подменённый ID: %v", err)
	}
	if _, err := curatorLearning.ReviewAttempt(ctx, domain.ReviewAnswerAttemptParams{
		CuratorStaffID: firstCuratorID, AttemptID: secondLesson.answer.AttemptID,
		Decision: "approved", Feedback: "Попытка обойти назначение.",
	}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("куратор смог проверить ответ другого ученика: %v", err)
	}
	if _, err := curatorLearning.ReviewAttempt(ctx, domain.ReviewAnswerAttemptParams{
		CuratorStaffID: secondCuratorID, AttemptID: secondLesson.answer.AttemptID,
		Decision: "approved", Feedback: "AI не должен попадать в ручную проверку.",
	}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("ответ AI-курса попал в ручную проверку: %v", err)
	}

	// Администратор читает также AI-прохождение, но это не даёт права
	// создавать curator review для AI-ответа.
	adminAccess, err := curatorLearning.ListCourseLessons(ctx, adminID, secondStudent.ID, secondRun.Enrollment.ID)
	if err != nil || adminAccess.Enrollment.Mode != "ai" || len(adminAccess.Lessons) != 1 {
		t.Fatalf("администратор не видит курс в режиме ai: access=%+v err=%v", adminAccess, err)
	}
	adminAnswers, err := curatorLearning.ListLessonAnswers(ctx, adminID, secondStudent.ID, secondRun.Enrollment.ID, lessonIDs[0])
	if err != nil || len(adminAnswers) != 1 || len(adminAnswers[0].Attempts) != 1 {
		t.Fatalf("администратор не видит ответы и историю AI-курса: answers=%+v err=%v", adminAnswers, err)
	}

	// После передачи курса старый куратор теряет доступ сразу, новый получает его.
	if err := staff.AssignStudentCurator(ctx, adminID, firstStudent.ID, secondCuratorID); err != nil {
		t.Fatalf("передать ученика новому куратору: %v", err)
	}
	if _, err := curatorLearning.ListCourseLessons(ctx, firstCuratorID, firstStudent.ID, firstRun.Enrollment.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("старый куратор сохранил доступ после передачи: %v", err)
	}
	if _, err := curatorLearning.ListLessonAnswers(ctx, firstCuratorID, firstStudent.ID, firstRun.Enrollment.ID, lessonIDs[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("старый куратор сохранил доступ к ответам после передачи: %v", err)
	}
	newAccess, err := curatorLearning.ListCourseLessons(ctx, secondCuratorID, firstStudent.ID, firstRun.Enrollment.ID)
	if err != nil || newAccess.Enrollment.CuratorStaffID == nil || *newAccess.Enrollment.CuratorStaffID != secondCuratorID {
		t.Fatalf("новый куратор не получил текущий курс: access=%+v err=%v", newAccess, err)
	}
	transferredStudents, err := staff.ListStudentsForStaff(ctx, firstCuratorID)
	if err != nil || len(transferredStudents) != 0 {
		t.Fatalf("старый куратор видит ученика после передачи: %+v err=%v", transferredStudents, err)
	}
	newCuratorStudents, err := staff.ListStudentsForStaff(ctx, secondCuratorID)
	if err != nil || len(newCuratorStudents) != 2 {
		t.Fatalf("новый куратор не видит обоих закреплённых учеников: %+v err=%v", newCuratorStudents, err)
	}
}

func TestCuratorReviewHistoryRevisionApprovalAndIdempotency(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	student := createCuratorTestStudent(t, ctx, pool, "curator-review-after-submit")
	adminID := createStaffForCourseTest(t, ctx, pool, "administrator")
	oldCuratorID := createStaffForCourseTest(t, ctx, pool, "curator")
	currentCuratorID := createStaffForCourseTest(t, ctx, pool, "curator")
	courseID, lessonIDs, _ := createCourseForEnrollmentTest(
		t, ctx, pool, fmt.Sprintf("%d", time.Now().UnixNano()), "Курс истории проверок", 1,
	)
	staff := NewStaffManagementRepository(pool)
	if err := staff.AssignStudentCurator(ctx, adminID, student.ID, oldCuratorID); err != nil {
		t.Fatalf("назначить старого куратора: %v", err)
	}
	run, err := NewCourseEnrollmentRepository(pool).Start(ctx, student.ID, courseID, "curator")
	if err != nil {
		t.Fatalf("начать курс с куратором: %v", err)
	}
	lessonRun := startCuratorTestLesson(t, ctx, pool, student.ID, run, lessonIDs[0], "Ответ ученика")
	answersRepo := NewAnswerRepository(pool)
	enrollments := NewEnrollmentRepository(pool)
	if _, err := enrollments.Complete(ctx, student.ID, lessonRun.enrollment.ID); err != nil {
		t.Fatalf("ученик не смог отправить урок до проверки: %v", err)
	}

	// Смена куратора после отправки передаёт новому специалисту право проверки.
	if err := staff.AssignStudentCurator(ctx, adminID, student.ID, currentCuratorID); err != nil {
		t.Fatalf("передать ученика новому куратору: %v", err)
	}
	curatorLearning := NewCuratorLearningRepository(pool)
	if _, err := curatorLearning.ReviewAttempt(ctx, domain.ReviewAnswerAttemptParams{
		CuratorStaffID: oldCuratorID, AttemptID: lessonRun.answer.AttemptID,
		Decision: "approved", Feedback: "Старый куратор.",
	}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("старый куратор смог проверить отправленный ответ: %v", err)
	}
	queue, err := curatorLearning.ListReviewQueue(ctx, currentCuratorID)
	if err != nil || len(queue) != 1 || queue[0].LessonID != lessonIDs[0] {
		t.Fatalf("отправленный урок отсутствует в очереди нового куратора: queue=%+v err=%v", queue, err)
	}

	revision, err := curatorLearning.ReviewAttempt(ctx, domain.ReviewAnswerAttemptParams{
		CuratorStaffID: currentCuratorID, AttemptID: lessonRun.answer.AttemptID,
		Decision: "needs_revision", Feedback: "Добавьте пояснение к ответу.",
	})
	if err != nil || revision.Decision != "needs_revision" || revision.CuratorStaffID != currentCuratorID {
		t.Fatalf("не удалось сохранить замечание к ответу: review=%+v err=%v", revision, err)
	}
	replayedRevision, err := curatorLearning.ReviewAttempt(ctx, domain.ReviewAnswerAttemptParams{
		CuratorStaffID: currentCuratorID, AttemptID: lessonRun.answer.AttemptID,
		Decision: "needs_revision", Feedback: "Добавьте пояснение к ответу.",
	})
	if err != nil || !replayedRevision.Replayed || replayedRevision.ID != revision.ID {
		t.Fatalf("повтор одного решения не идемпотентен: review=%+v err=%v", replayedRevision, err)
	}
	if _, err := curatorLearning.ReviewAttempt(ctx, domain.ReviewAnswerAttemptParams{
		CuratorStaffID: currentCuratorID, AttemptID: lessonRun.answer.AttemptID,
		Decision: "approved", Feedback: "Изменить прошлое решение нельзя.",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("повторное решение перезаписало историю: %v", err)
	}
	afterReview, err := curatorLearning.ListReviewQueue(ctx, currentCuratorID)
	if err != nil || len(afterReview) != 0 {
		t.Fatalf("уже рассмотренный ответ остался в очереди: queue=%+v err=%v", afterReview, err)
	}

	history, err := answersRepo.ListAttemptHistory(ctx, student.ID, lessonRun.enrollment.ID, lessonRun.questionID)
	if err != nil || len(history) != 1 || history[0].ReviewDecision == nil ||
		*history[0].ReviewDecision != "needs_revision" ||
		history[0].ReviewFeedback == nil || *history[0].ReviewFeedback != "Добавьте пояснение к ответу." {
		t.Fatalf("история ученика не сохранила решение и комментарий куратора: history=%+v err=%v", history, err)
	}
	courseAnswers, err := curatorLearning.ListLessonAnswers(ctx, currentCuratorID, student.ID, run.Enrollment.ID, lessonIDs[0])
	if err != nil || len(courseAnswers) != 1 || courseAnswers[0].ReviewStatus != "needs_revision" ||
		len(courseAnswers[0].Attempts) != 1 ||
		courseAnswers[0].Attempts[0].ReviewerStaffID == nil ||
		*courseAnswers[0].Attempts[0].ReviewerStaffID != currentCuratorID ||
		courseAnswers[0].Attempts[0].ReviewFeedback == nil ||
		*courseAnswers[0].Attempts[0].ReviewFeedback != "Добавьте пояснение к ответу." {
		t.Fatalf("API не сохранил автора, комментарий или историю проверки: answers=%+v err=%v", courseAnswers, err)
	}

	// После отправки ученик не может открыть новую попытку: замечание остаётся
	// в истории и разбирается куратором в личной переписке.
	if _, err := answersRepo.Submit(ctx, AnswerSubmission{
		StudentID: student.ID, EnrollmentID: lessonRun.enrollment.ID,
		QuestionID: lessonRun.questionID, AnswerText: "Попытка повторно отправить урок",
	}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("ученик смог изменить уже отправленный урок: %v", err)
	}
	completed, err := NewEnrollmentRepository(pool).Complete(ctx, student.ID, lessonRun.enrollment.ID)
	if err != nil || completed.Status != "completed" {
		t.Fatalf("повторное подтверждение отправленного урока не идемпотентно: enrollment=%+v err=%v", completed, err)
	}
	progress, err := NewCourseEnrollmentRepository(pool).GetForStudent(ctx, student.ID, run.Enrollment.ID)
	if err != nil || progress.Enrollment.Status != "completed" || !progress.IsComplete {
		t.Fatalf("замечание к ответу не должно возвращать курс в активное состояние: progress=%+v err=%v", progress, err)
	}
}

func TestCuratorRejectsSupersededAttemptsAndFailedAttemptsAreNotPending(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	student := createCuratorTestStudent(t, ctx, pool, "curator-stale-attempt")
	adminID := createStaffForCourseTest(t, ctx, pool, "administrator")
	curatorID := createStaffForCourseTest(t, ctx, pool, "curator")
	courseID, lessonIDs, _ := createCourseForEnrollmentTest(
		t, ctx, pool, fmt.Sprintf("%d", time.Now().UnixNano()), "Курс устаревших попыток", 1,
	)
	staff := NewStaffManagementRepository(pool)
	if err := staff.AssignStudentCurator(ctx, adminID, student.ID, curatorID); err != nil {
		t.Fatalf("назначить куратора: %v", err)
	}
	run, err := NewCourseEnrollmentRepository(pool).Start(ctx, student.ID, courseID, "curator")
	if err != nil {
		t.Fatalf("начать курс: %v", err)
	}
	lesson := startCuratorTestLesson(t, ctx, pool, student.ID, run, lessonIDs[0], "Первая попытка")
	answers := NewAnswerRepository(pool)
	second, err := answers.Submit(ctx, AnswerSubmission{
		StudentID: student.ID, EnrollmentID: lesson.enrollment.ID,
		QuestionID: lesson.questionID, AnswerText: "Вторая попытка",
	})
	if err != nil {
		t.Fatalf("создать более новую попытку: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE answer_attempts
		SET processing_status = 'failed', completed_at = now(), error_code = 'test_processing_failure'
		WHERE id = $1
	`, second.AttemptID); err != nil {
		t.Fatalf("смоделировать ошибку обработки последней попытки: %v", err)
	}

	curator := NewCuratorLearningRepository(pool)
	lessonAnswers, err := curator.ListLessonAnswers(
		ctx, curatorID, student.ID, run.Enrollment.ID, lessonIDs[0],
	)
	if err != nil || len(lessonAnswers) != 1 ||
		lessonAnswers[0].ProcessingStatus != "failed" ||
		lessonAnswers[0].ReviewStatus != "failed" {
		t.Fatalf("ошибка обработки должна иметь отдельный статус: answers=%+v err=%v", lessonAnswers, err)
	}
	beforeSubmission, err := curator.ListReviewQueue(ctx, curatorID)
	if err != nil || len(beforeSubmission) != 0 {
		t.Fatalf("неотправленный урок появился в очереди: queue=%+v err=%v", beforeSubmission, err)
	}

	third, err := answers.Submit(ctx, AnswerSubmission{
		StudentID: student.ID, EnrollmentID: lesson.enrollment.ID,
		QuestionID: lesson.questionID, AnswerText: "Последний ответ перед отправкой",
	})
	if err != nil {
		t.Fatalf("ученик должен иметь возможность исправить ответ до отправки: %v", err)
	}
	if _, err := NewEnrollmentRepository(pool).Complete(ctx, student.ID, lesson.enrollment.ID); err != nil {
		t.Fatalf("отправить урок с последним сохранённым ответом: %v", err)
	}
	if _, err := curator.ReviewAttempt(ctx, domain.ReviewAnswerAttemptParams{
		CuratorStaffID: curatorID, AttemptID: lesson.answer.AttemptID,
		Decision: "approved", Feedback: "Устаревшую попытку принимать нельзя.",
	}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("устаревшую попытку нельзя принимать после отправки новой: %v", err)
	}
	queue, err := curator.ListReviewQueue(ctx, curatorID)
	if err != nil || len(queue) != 1 || queue[0].PendingAnswers != 1 {
		t.Fatalf("последняя pending-попытка не появилась после отправки урока: queue=%+v err=%v", queue, err)
	}
	review, err := curator.ReviewAttempt(ctx, domain.ReviewAnswerAttemptParams{
		CuratorStaffID: curatorID, AttemptID: third.AttemptID,
		Decision: "approved", Feedback: "",
	})
	if err != nil || review.Decision != "approved" {
		t.Fatalf("куратор не смог принять последнюю попытку отправленного урока: review=%+v err=%v", review, err)
	}
	afterReview, err := curator.ListReviewQueue(ctx, curatorID)
	if err != nil || len(afterReview) != 0 {
		t.Fatalf("проверенный урок остался в очереди: queue=%+v err=%v", afterReview, err)
	}
	progress, err := NewCourseEnrollmentRepository(pool).GetForStudent(ctx, student.ID, run.Enrollment.ID)
	if err != nil || progress.Enrollment.Status != "completed" ||
		progress.CompletedLessons != 1 || !progress.IsComplete {
		t.Fatalf("курс должен завершиться при отправке единственного урока, а не при проверке: progress=%+v err=%v", progress, err)
	}
	if _, err := answers.Submit(ctx, AnswerSubmission{
		StudentID: student.ID, EnrollmentID: lesson.enrollment.ID,
		QuestionID: lesson.questionID, AnswerText: "Ответ после отправки",
	}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("после отправки урок продолжает принимать ответы: %v", err)
	}
}

func TestConcurrentCuratorReviewIsAtomicAndIdempotent(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	student := createCuratorTestStudent(t, ctx, pool, "curator-review-concurrent")
	adminID := createStaffForCourseTest(t, ctx, pool, "administrator")
	curatorID := createStaffForCourseTest(t, ctx, pool, "curator")
	otherCuratorID := createStaffForCourseTest(t, ctx, pool, "curator")
	courseID, lessonIDs, _ := createCourseForEnrollmentTest(
		t, ctx, pool, fmt.Sprintf("%d", time.Now().UnixNano()), "Курс конкурентной проверки", 1,
	)
	if err := NewStaffManagementRepository(pool).AssignStudentCurator(ctx, adminID, student.ID, curatorID); err != nil {
		t.Fatalf("назначить куратора: %v", err)
	}
	run, err := NewCourseEnrollmentRepository(pool).Start(ctx, student.ID, courseID, "curator")
	if err != nil {
		t.Fatalf("начать курс: %v", err)
	}
	lessonRun := startCuratorTestLesson(t, ctx, pool, student.ID, run, lessonIDs[0], "Ответ для конкурентной проверки")
	if _, err := NewEnrollmentRepository(pool).Complete(ctx, student.ID, lessonRun.enrollment.ID); err != nil {
		t.Fatalf("отправить урок перед конкурентной проверкой: %v", err)
	}

	const workers = 12
	type result struct {
		staffID int64
		review  domain.AnswerAttemptReview
		err     error
	}
	results := make(chan result, workers+1)
	var wait sync.WaitGroup
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			review, reviewErr := NewCuratorLearningRepository(pool).ReviewAttempt(ctx, domain.ReviewAnswerAttemptParams{
				CuratorStaffID: curatorID, AttemptID: lessonRun.answer.AttemptID,
				Decision: "approved", Feedback: "Ответ принят.",
			})
			results <- result{staffID: curatorID, review: review, err: reviewErr}
		}()
	}
	// Второй сотрудник пытается проверить ту же попытку одновременно, но
	// не назначен учеником и не должен получить право на решение.
	wait.Add(1)
	go func() {
		defer wait.Done()
		review, reviewErr := NewCuratorLearningRepository(pool).ReviewAttempt(ctx, domain.ReviewAnswerAttemptParams{
			CuratorStaffID: otherCuratorID, AttemptID: lessonRun.answer.AttemptID,
			Decision: "approved", Feedback: "Проверка не назначенным куратором.",
		})
		results <- result{staffID: otherCuratorID, review: review, err: reviewErr}
	}()
	wait.Wait()
	close(results)

	var originalCount, replayCount, unauthorizedCount int
	var reviewID int64
	for item := range results {
		if item.staffID == otherCuratorID {
			if !errors.Is(item.err, ErrInvalidState) {
				t.Fatalf("не назначенный куратор получил право параллельной проверки: %v", item.err)
			}
			unauthorizedCount++
			continue
		}
		if item.err != nil {
			t.Fatalf("конкурентная проверка назначенным куратором завершилась ошибкой: %v", item.err)
		}
		if item.review.ID == 0 {
			t.Fatal("пустой ID решения куратора")
		}
		if reviewID == 0 {
			reviewID = item.review.ID
		} else if item.review.ID != reviewID {
			t.Fatalf("повторная проверка создала другой ID: got=%d want=%d", item.review.ID, reviewID)
		}
		if item.review.Replayed {
			replayCount++
		} else {
			originalCount++
		}
	}
	if originalCount != 1 || replayCount != workers-1 || unauthorizedCount != 1 {
		t.Fatalf("ожидалось одно решение, %d безопасных повторов и отказ чужому сотруднику; first=%d replay=%d unauthorized=%d",
			workers-1, originalCount, replayCount, unauthorizedCount)
	}

	var storedReviews int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM answer_attempt_reviews WHERE answer_attempt_id = $1",
		lessonRun.answer.AttemptID,
	).Scan(&storedReviews); err != nil {
		t.Fatalf("проверить количество решений: %v", err)
	}
	if storedReviews != 1 {
		t.Fatalf("для одной попытки сохранено %d решений вместо одного", storedReviews)
	}
}
