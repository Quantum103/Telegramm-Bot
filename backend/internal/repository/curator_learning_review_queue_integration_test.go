package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func setCuratorQueueAttemptTime(t *testing.T, ctx context.Context, pool *pgxpool.Pool, attemptID int64, createdAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, "UPDATE answer_attempts SET created_at = $2 WHERE id = $1", attemptID, createdAt); err != nil {
		t.Fatalf("установить время попытки %d: %v", attemptID, err)
	}
}

func submitCuratorQueueAnswer(t *testing.T, ctx context.Context, pool *pgxpool.Pool, studentID, enrollmentID, questionID int64, text string) domain.SavedAnswer {
	t.Helper()
	answer, err := NewAnswerRepository(pool).Submit(ctx, AnswerSubmission{
		StudentID: studentID, EnrollmentID: enrollmentID,
		QuestionID: questionID, AnswerText: text,
	})
	if err != nil {
		t.Fatalf("отправить ответ на вопрос %d: %v", questionID, err)
	}
	return answer
}

func TestCuratorReviewQueueAggregatesCurrentAttemptsAndScopesAssignments(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	adminID := createStaffForCourseTest(t, ctx, pool, "administrator")
	firstCuratorID := createStaffForCourseTest(t, ctx, pool, "curator")
	secondCuratorID := createStaffForCourseTest(t, ctx, pool, "curator")
	studentA := createCuratorTestStudent(t, ctx, pool, "queue-student-a")
	studentB := createCuratorTestStudent(t, ctx, pool, "queue-student-b")
	studentC := createCuratorTestStudent(t, ctx, pool, "queue-student-c")
	studentAI := createCuratorTestStudent(t, ctx, pool, "queue-student-ai")
	staff := NewStaffManagementRepository(pool)
	for _, assignment := range []struct{ studentID, curatorID int64 }{
		{studentA.ID, firstCuratorID}, {studentB.ID, secondCuratorID},
		{studentC.ID, firstCuratorID}, {studentAI.ID, firstCuratorID},
	} {
		if err := staff.AssignStudentCurator(ctx, adminID, assignment.studentID, assignment.curatorID); err != nil {
			t.Fatalf("назначить куратора ученику %d: %v", assignment.studentID, err)
		}
	}

	courseOneID, lessonIDs, versionIDs := createCourseForEnrollmentTest(
		t, ctx, pool, fmt.Sprintf("%d", time.Now().UnixNano()), "Курс очереди", 2,
	)
	courseTwoID, courseTwoLessons, _ := createCourseForEnrollmentTest(
		t, ctx, pool, fmt.Sprintf("%d", time.Now().UnixNano()), "Другой курс очереди", 1,
	)
	for number := 2; number <= 3; number++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO lesson_questions (lesson_version_id, question_number, prompt, expected_answer, answer_type)
			VALUES ($1, $2, $3, 'Ожидаемый ответ', 'text')
		`, versionIDs[0], number, fmt.Sprintf("Вопрос %d", number)); err != nil {
			t.Fatalf("добавить вопрос %d: %v", number, err)
		}
	}

	courses := NewCourseEnrollmentRepository(pool)
	runA, err := courses.Start(ctx, studentA.ID, courseOneID, "curator")
	if err != nil {
		t.Fatalf("начать курс ученика A: %v", err)
	}
	runB, err := courses.Start(ctx, studentB.ID, courseOneID, "curator")
	if err != nil {
		t.Fatalf("начать курс ученика B: %v", err)
	}
	runC, err := courses.Start(ctx, studentC.ID, courseTwoID, "curator")
	if err != nil {
		t.Fatalf("начать курс ученика C: %v", err)
	}
	runAI, err := courses.Start(ctx, studentAI.ID, courseOneID, "ai")
	if err != nil {
		t.Fatalf("начать AI-курс: %v", err)
	}

	lessonA := startCuratorTestLesson(t, ctx, pool, studentA.ID, runA, lessonIDs[0], "Ответ A1")
	var questionTwoID, questionThreeID int64
	if err := pool.QueryRow(ctx, `
		SELECT id FROM lesson_questions WHERE lesson_version_id = $1 AND question_number = 2
	`, versionIDs[0]).Scan(&questionTwoID); err != nil {
		t.Fatalf("получить второй вопрос: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT id FROM lesson_questions WHERE lesson_version_id = $1 AND question_number = 3
	`, versionIDs[0]).Scan(&questionThreeID); err != nil {
		t.Fatalf("получить третий вопрос: %v", err)
	}
	answerTwo := submitCuratorQueueAnswer(t, ctx, pool, studentA.ID, lessonA.enrollment.ID, questionTwoID, "Ответ A2")
	answerThree := submitCuratorQueueAnswer(t, ctx, pool, studentA.ID, lessonA.enrollment.ID, questionThreeID, "Ответ A3")
	if _, err := NewCuratorLearningRepository(pool).ReviewAttempt(ctx, domain.ReviewAnswerAttemptParams{
		CuratorStaffID: firstCuratorID, AttemptID: lessonA.answer.AttemptID,
		Decision: "approved", Feedback: "Первый ответ принят.",
	}); err != nil {
		t.Fatalf("одобрить первый ответ ученика A: %v", err)
	}

	lessonB := startCuratorTestLesson(t, ctx, pool, studentB.ID, runB, lessonIDs[0], "Первоначальный ответ B")
	baseTime := time.Now().UTC().Truncate(time.Microsecond).Add(-24 * time.Hour)
	setCuratorQueueAttemptTime(t, ctx, pool, lessonB.answer.AttemptID, baseTime.Add(-time.Minute))
	if _, err := NewCuratorLearningRepository(pool).ReviewAttempt(ctx, domain.ReviewAnswerAttemptParams{
		CuratorStaffID: secondCuratorID, AttemptID: lessonB.answer.AttemptID,
		Decision: "needs_revision", Feedback: "Ответ нужно дополнить.",
	}); err != nil {
		t.Fatalf("вернуть ответ B на исправление: %v", err)
	}
	revisedB := submitCuratorQueueAnswer(t, ctx, pool, studentB.ID, lessonB.enrollment.ID, lessonB.questionID, "Исправленный ответ B")
	setCuratorQueueAttemptTime(t, ctx, pool, revisedB.AttemptID, baseTime)

	lessonC := startCuratorTestLesson(t, ctx, pool, studentC.ID, runC, courseTwoLessons[0], "Ответ C")
	setCuratorQueueAttemptTime(t, ctx, pool, lessonC.answer.AttemptID, baseTime.Add(3*time.Hour))

	// Завершённый урок не должен оставаться в очереди, даже если он входит
	// в тот же активный запуск курса, что и другой ожидающий проверки урок.
	completedLesson := startCuratorTestLesson(t, ctx, pool, studentA.ID, runA, lessonIDs[1], "Завершённый ответ")
	if _, err := NewCuratorLearningRepository(pool).ReviewAttempt(ctx, domain.ReviewAnswerAttemptParams{
		CuratorStaffID: firstCuratorID, AttemptID: completedLesson.answer.AttemptID,
		Decision: "approved", Feedback: "Урок принят.",
	}); err != nil {
		t.Fatalf("завершить второй урок ученика A: %v", err)
	}

	// Две актуальные попытки в одном уроке должны быть одной строкой очереди.
	setCuratorQueueAttemptTime(t, ctx, pool, answerTwo.AttemptID, baseTime.Add(time.Hour))
	setCuratorQueueAttemptTime(t, ctx, pool, answerThree.AttemptID, baseTime.Add(2*time.Hour))

	lessonAI := startCuratorTestLesson(t, ctx, pool, studentAI.ID, runAI, lessonIDs[0], "AI-ответ")
	_ = lessonAI

	queueRepo := NewCuratorLearningRepository(pool)
	firstQueue, err := queueRepo.ListReviewQueue(ctx, firstCuratorID)
	if err != nil {
		t.Fatalf("получить очередь первого куратора: %v", err)
	}
	if len(firstQueue) != 2 {
		t.Fatalf("первый куратор должен видеть две задачи, по одной на урок A и C: %+v", firstQueue)
	}
	if firstQueue[0].StudentID != studentA.ID || firstQueue[0].PendingAnswers != 2 ||
		firstQueue[0].ReviewedAnswers != 1 || firstQueue[0].TotalQuestions != 3 ||
		firstQueue[0].CourseID != courseOneID || firstQueue[0].CourseEnrollmentID != runA.Enrollment.ID ||
		firstQueue[0].LessonID != lessonIDs[0] || firstQueue[0].LessonStatus != "in_progress" ||
		firstQueue[0].StudentName != studentA.DisplayName ||
		firstQueue[0].CourseTitle != runA.Enrollment.CourseTitle ||
		firstQueue[0].LessonTitle != lessonA.lesson.Title ||
		!firstQueue[0].FirstSubmittedAt.Equal(baseTime.Add(time.Hour)) {
		t.Fatalf("неверная агрегация/DTO первой задачи очереди: %+v", firstQueue[0])
	}
	if firstQueue[1].StudentID != studentC.ID || firstQueue[1].PendingAnswers != 1 ||
		!firstQueue[1].FirstSubmittedAt.Equal(baseTime.Add(3*time.Hour)) {
		t.Fatalf("очередь первого куратора отсортирована неверно или содержит чужую задачу: %+v", firstQueue)
	}

	secondQueue, err := queueRepo.ListReviewQueue(ctx, secondCuratorID)
	if err != nil || len(secondQueue) != 1 || secondQueue[0].StudentID != studentB.ID ||
		secondQueue[0].PendingAnswers != 1 || secondQueue[0].ReviewedAnswers != 0 ||
		secondQueue[0].CourseEnrollmentID != runB.Enrollment.ID ||
		!secondQueue[0].FirstSubmittedAt.Equal(baseTime) {
		t.Fatalf("второй куратор получил неверную очередь после повторной отправки: queue=%+v err=%v", secondQueue, err)
	}

	adminQueue, err := queueRepo.ListReviewQueue(ctx, adminID)
	if err != nil || len(adminQueue) != 3 ||
		adminQueue[0].StudentID != studentB.ID ||
		adminQueue[1].StudentID != studentA.ID ||
		adminQueue[2].StudentID != studentC.ID {
		t.Fatalf("администратор должен видеть общую очередь по времени: queue=%+v err=%v", adminQueue, err)
	}

	// Детали урока по-прежнему загружаются существующим endpoint; подмена
	// studentID не даёт чужому куратору получить тексты ответов.
	answerDetails, err := queueRepo.ListLessonAnswers(ctx, firstCuratorID, studentA.ID, runA.Enrollment.ID, lessonIDs[0])
	if err != nil || len(answerDetails) != 3 {
		t.Fatalf("после открытия задачи должны быть доступны все три вопроса: answers=%+v err=%v", answerDetails, err)
	}
	if _, err := queueRepo.ListLessonAnswers(ctx, firstCuratorID, studentB.ID, runB.Enrollment.ID, lessonIDs[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("подмена studentID позволила получить чужие ответы: %v", err)
	}

	unauthorizedID := int64(0)
	if err := pool.QueryRow(ctx, `
		INSERT INTO staff_accounts (display_name, email, password_hash)
		VALUES ('Сотрудник без роли', $1, 'integration-test-only')
		RETURNING id
	`, fmt.Sprintf("queue-no-role-%d@example.test", time.Now().UnixNano())).Scan(&unauthorizedID); err != nil {
		t.Fatalf("создать сотрудника без роли: %v", err)
	}
	if _, err := queueRepo.ListReviewQueue(ctx, unauthorizedID); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("сотрудник без роли получил очередь: %v", err)
	}
}

func TestCuratorReviewQueueEmptyCorrectionResubmissionAndCompletion(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	student := createCuratorTestStudent(t, ctx, pool, "queue-lifecycle")
	adminID := createStaffForCourseTest(t, ctx, pool, "administrator")
	curatorID := createStaffForCourseTest(t, ctx, pool, "curator")
	courseID, lessonIDs, _ := createCourseForEnrollmentTest(
		t, ctx, pool, fmt.Sprintf("%d", time.Now().UnixNano()), "Курс жизненного цикла очереди", 1,
	)
	if err := NewStaffManagementRepository(pool).AssignStudentCurator(ctx, adminID, student.ID, curatorID); err != nil {
		t.Fatalf("назначить куратора: %v", err)
	}
	run, err := NewCourseEnrollmentRepository(pool).Start(ctx, student.ID, courseID, "curator")
	if err != nil {
		t.Fatalf("начать курс: %v", err)
	}
	queueRepo := NewCuratorLearningRepository(pool)

	empty, err := queueRepo.ListReviewQueue(ctx, curatorID)
	if err != nil || len(empty) != 0 {
		t.Fatalf("очередь без отправленных ответов должна быть пустой: queue=%+v err=%v", empty, err)
	}

	lesson := startCuratorTestLesson(t, ctx, pool, student.ID, run, lessonIDs[0], "Первый ответ")
	pending, err := queueRepo.ListReviewQueue(ctx, curatorID)
	if err != nil || len(pending) != 1 || pending[0].PendingAnswers != 1 {
		t.Fatalf("новая отправка не появилась в очереди: queue=%+v err=%v", pending, err)
	}

	if _, err := queueRepo.ReviewAttempt(ctx, domain.ReviewAnswerAttemptParams{
		CuratorStaffID: curatorID, AttemptID: lesson.answer.AttemptID,
		Decision: "needs_revision", Feedback: "Исправьте ответ.",
	}); err != nil {
		t.Fatalf("отправить ответ на исправление: %v", err)
	}
	afterRevision, err := queueRepo.ListReviewQueue(ctx, curatorID)
	if err != nil || len(afterRevision) != 0 {
		t.Fatalf("проверенный ответ на исправление не должен ожидать проверки повторно: queue=%+v err=%v", afterRevision, err)
	}

	revised := submitCuratorQueueAnswer(t, ctx, pool, student.ID, lesson.enrollment.ID, lesson.questionID, "Исправленный ответ")
	afterResubmission, err := queueRepo.ListReviewQueue(ctx, curatorID)
	if err != nil || len(afterResubmission) != 1 ||
		afterResubmission[0].PendingAnswers != 1 ||
		afterResubmission[0].ReviewedAnswers != 0 {
		t.Fatalf("после исправления новая попытка должна вернуться в очередь без старого решения: queue=%+v err=%v", afterResubmission, err)
	}

	if _, err := queueRepo.ReviewAttempt(ctx, domain.ReviewAnswerAttemptParams{
		CuratorStaffID: curatorID, AttemptID: revised.AttemptID,
		Decision: "approved", Feedback: "Принято.",
	}); err != nil {
		t.Fatalf("принять исправленный ответ: %v", err)
	}
	afterApproval, err := queueRepo.ListReviewQueue(ctx, curatorID)
	if err != nil || len(afterApproval) != 0 {
		t.Fatalf("принятый и завершённый урок остался в очереди: queue=%+v err=%v", afterApproval, err)
	}
	progress, err := NewCourseEnrollmentRepository(pool).GetForStudent(ctx, student.ID, run.Enrollment.ID)
	if err != nil || progress.Enrollment.Status != "completed" || !progress.IsComplete {
		t.Fatalf("принятие последней актуальной попытки должно завершить курс: progress=%+v err=%v", progress, err)
	}
}
