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

func createCourseForEnrollmentTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix string, title string, lessonCount int) (int64, []int64, []int64) {
	t.Helper()
	var courseID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO courses (slug, title, status, published_at)
		VALUES ($1, $2, 'published', now()) RETURNING id
	`, "course-enrollment-"+suffix, title).Scan(&courseID); err != nil {
		t.Fatalf("создать тестовый курс: %v", err)
	}
	lessonIDs := make([]int64, 0, lessonCount)
	versionIDs := make([]int64, 0, lessonCount)
	for i := 0; i < lessonCount; i++ {
		var lessonID, versionID int64
		if err := pool.QueryRow(ctx, "INSERT INTO lessons DEFAULT VALUES RETURNING id").Scan(&lessonID); err != nil {
			t.Fatalf("создать урок: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO course_lessons (course_id, lesson_id, position) VALUES ($1, $2, $3)
		`, courseID, lessonID, i+1); err != nil {
			t.Fatalf("добавить урок в курс: %v", err)
		}
		if err := pool.QueryRow(ctx, `
			INSERT INTO lesson_versions (lesson_id, version_number, title, status)
			VALUES ($1, 1, $2, 'draft') RETURNING id
		`, lessonID, fmt.Sprintf("%s · урок %d", title, i+1)).Scan(&versionID); err != nil {
			t.Fatalf("создать версию урока: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO lesson_questions (lesson_version_id, question_number, prompt, expected_answer, answer_type)
			VALUES ($1, 1, 'Напишите ответ', 'Ожидаемый ответ', 'text')
		`, versionID); err != nil {
			t.Fatalf("создать вопрос: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE lesson_versions SET status = 'published', published_at = now() WHERE id = $1
		`, versionID); err != nil {
			t.Fatalf("опубликовать версию урока: %v", err)
		}
		lessonIDs = append(lessonIDs, lessonID)
		versionIDs = append(versionIDs, versionID)
	}
	return courseID, lessonIDs, versionIDs
}

func createStaffForCourseTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, roleCode string) int64 {
	t.Helper()
	var staffID int64
	email := fmt.Sprintf("%s-%d@example.test", roleCode, time.Now().UnixNano())
	if err := pool.QueryRow(ctx, `
		INSERT INTO staff_accounts (display_name, email, password_hash)
		VALUES ($1, $2, 'integration-test-only') RETURNING id
	`, "Тестовый "+roleCode, email).Scan(&staffID); err != nil {
		t.Fatalf("создать сотрудника: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO staff_role_assignments (staff_id, role_id)
		SELECT $1, id FROM staff_roles WHERE role_code = $2
	`, staffID, roleCode); err != nil {
		t.Fatalf("назначить роль сотруднику: %v", err)
	}
	return staffID
}

func TestCourseEnrollmentPersistsModeAndRejectsSecondActiveCourse(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	student, err := NewStudentRepository(pool).Create(ctx, CreateStudentParams{
		TelegramID: time.Now().UnixNano(), DisplayName: "course-mode-test",
	})
	if err != nil {
		t.Fatalf("создать ученика: %v", err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	firstCourse, _, _ := createCourseForEnrollmentTest(t, ctx, pool, suffix+"-a", "Первый курс", 1)
	secondCourse, _, _ := createCourseForEnrollmentTest(t, ctx, pool, suffix+"-b", "Второй курс", 1)
	courses := NewCourseEnrollmentRepository(pool)

	withoutCurator, err := courses.Start(ctx, student.ID, firstCourse, "curator")
	if !errors.Is(err, domain.ErrNoCurator) || withoutCurator.Enrollment.ID != 0 {
		t.Fatalf("режим curator без назначения должен быть отклонён без записи: progress=%+v err=%v", withoutCurator, err)
	}
	var createdBeforeAssignment int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM course_enrollments WHERE student_id = $1", student.ID).Scan(&createdBeforeAssignment); err != nil {
		t.Fatalf("проверить отсутствие незавершённой записи: %v", err)
	}
	if createdBeforeAssignment != 0 {
		t.Fatalf("при отсутствии куратора создалось %d прохождений", createdBeforeAssignment)
	}

	adminID := createStaffForCourseTest(t, ctx, pool, "administrator")
	curatorID := createStaffForCourseTest(t, ctx, pool, "curator")
	if err := NewStaffManagementRepository(pool).AssignStudentCurator(ctx, adminID, student.ID, curatorID); err != nil {
		t.Fatalf("назначить куратора: %v", err)
	}

	started, err := courses.Start(ctx, student.ID, firstCourse, "curator")
	if err != nil {
		t.Fatalf("начать курс с куратором: %v", err)
	}
	if started.Enrollment.Mode != "curator" || started.Enrollment.CuratorStaffID == nil ||
		*started.Enrollment.CuratorStaffID != curatorID || started.TotalLessons != 1 {
		t.Fatalf("режим/ответственный не сохранены: %+v", started)
	}

	reopened, err := NewCourseEnrollmentRepository(pool).Start(ctx, student.ID, firstCourse, "ai")
	if err != nil {
		t.Fatalf("повторно открыть тот же курс с устаревшим режимом: %v", err)
	}
	if reopened.Enrollment.ID != started.Enrollment.ID || reopened.Enrollment.Mode != "curator" {
		t.Fatalf("повторное открытие изменило режим или создало запуск: started=%+v reopened=%+v", started.Enrollment, reopened.Enrollment)
	}

	blocked, err := courses.Start(ctx, student.ID, secondCourse, "ai")
	if !errors.Is(err, domain.ErrActiveCourse) || blocked.Enrollment.ID != started.Enrollment.ID ||
		blocked.Enrollment.CourseID != firstCourse || blocked.TotalLessons != 1 {
		t.Fatalf("второй курс должен вернуть прогресс первого: progress=%+v err=%v", blocked, err)
	}

	if _, err := pool.Exec(ctx, "UPDATE course_enrollments SET mode = 'ai' WHERE id = $1", started.Enrollment.ID); err == nil {
		t.Fatal("обычный UPDATE не должен менять сохранённый режим")
	}
	var activeCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM course_enrollments
		WHERE student_id = $1 AND status IN ('awaiting_curator', 'in_progress')
	`, student.ID).Scan(&activeCount); err != nil {
		t.Fatalf("посчитать активные курсы: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("активных курсов = %d, ожидался 1", activeCount)
	}
}

func TestCourseEnrollmentSerializesConcurrentStarts(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	student, err := NewStudentRepository(pool).Create(ctx, CreateStudentParams{
		TelegramID: time.Now().UnixNano(), DisplayName: "course-concurrency-test",
	})
	if err != nil {
		t.Fatalf("создать ученика: %v", err)
	}
	courseID, _, _ := createCourseForEnrollmentTest(t, ctx, pool,
		fmt.Sprintf("%d", time.Now().UnixNano()), "Курс для конкурентного теста", 1)
	const workers = 12
	results := make(chan domain.CourseEnrollmentProgress, workers)
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			progress, startErr := NewCourseEnrollmentRepository(pool).Start(ctx, student.ID, courseID, "ai")
			results <- progress
			errs <- startErr
		}()
	}
	wait.Wait()
	close(results)
	close(errs)

	var enrollmentID int64
	for progress := range results {
		if progress.Enrollment.ID <= 0 {
			t.Fatalf("параллельный запрос не вернул прохождение: %+v", progress)
		}
		if enrollmentID == 0 {
			enrollmentID = progress.Enrollment.ID
		} else if progress.Enrollment.ID != enrollmentID {
			t.Fatalf("созданы разные запуски курса: %d и %d", enrollmentID, progress.Enrollment.ID)
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("конкурентный запуск завершился ошибкой: %v", err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM course_enrollments WHERE student_id = $1 AND course_id = $2
	`, student.ID, courseID).Scan(&count); err != nil {
		t.Fatalf("посчитать прохождения: %v", err)
	}
	if count != 1 {
		t.Fatalf("создано запусков = %d, ожидался 1", count)
	}
}

func TestCuratorTransferReviewAndCourseCompletion(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	student, err := NewStudentRepository(pool).Create(ctx, CreateStudentParams{
		TelegramID: time.Now().UnixNano(), DisplayName: "curator-course-test",
	})
	if err != nil {
		t.Fatalf("создать ученика: %v", err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	courseID, lessonIDs, versionIDs := createCourseForEnrollmentTest(t, ctx, pool, suffix, "Курс с ручной проверкой", 2)
	adminID := createStaffForCourseTest(t, ctx, pool, "administrator")
	firstCuratorID := createStaffForCourseTest(t, ctx, pool, "curator")
	secondCuratorID := createStaffForCourseTest(t, ctx, pool, "curator")
	staffRepository := NewStaffManagementRepository(pool)
	if err := staffRepository.AssignStudentCurator(ctx, adminID, student.ID, firstCuratorID); err != nil {
		t.Fatalf("назначить первого куратора: %v", err)
	}
	courses := NewCourseEnrollmentRepository(pool)
	run, err := courses.Start(ctx, student.ID, courseID, "curator")
	if err != nil {
		t.Fatalf("создать курс с первым куратором: %v", err)
	}

	// Передача ученика обновляет ответственного уже созданного запуска и не
	// создаёт вторую запись назначения.
	if err := staffRepository.AssignStudentCurator(ctx, adminID, student.ID, secondCuratorID); err != nil {
		t.Fatalf("передать ученика второму куратору: %v", err)
	}
	run, err = courses.GetForStudent(ctx, student.ID, run.Enrollment.ID)
	if err != nil || run.Enrollment.CuratorStaffID == nil || *run.Enrollment.CuratorStaffID != secondCuratorID {
		t.Fatalf("курс не передан новому куратору: progress=%+v err=%v", run, err)
	}
	var activeAssignments, endedAssignments int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status = 'active'),
		       count(*) FILTER (WHERE status = 'ended')
		FROM student_curator_assignments WHERE student_id = $1
	`, student.ID).Scan(&activeAssignments, &endedAssignments); err != nil {
		t.Fatalf("проверить историю назначений: %v", err)
	}
	if activeAssignments != 1 || endedAssignments != 1 {
		t.Fatalf("история назначения потеряна: active=%d ended=%d", activeAssignments, endedAssignments)
	}

	curatorRepository := NewCuratorLearningRepository(pool)
	if _, err := curatorRepository.ListCourseLessons(ctx, firstCuratorID, student.ID, run.Enrollment.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("старый куратор не должен видеть курс после передачи: %v", err)
	}
	access, err := curatorRepository.ListCourseLessons(ctx, secondCuratorID, student.ID, run.Enrollment.ID)
	if err != nil || len(access.Lessons) != 2 {
		t.Fatalf("новый куратор не получил доступ к обязательным урокам: access=%+v err=%v", access, err)
	}

	enrollments := NewEnrollmentRepository(pool)
	answers := NewAnswerRepository(pool)
	for index, lessonID := range lessonIDs {
		lesson, err := courses.GetLessonForRun(ctx, student.ID, run.Enrollment.ID, lessonID)
		if err != nil || lesson.LessonVersionID != versionIDs[index] {
			t.Fatalf("получить фиксированную версию урока: lesson=%+v err=%v", lesson, err)
		}
		enrollment, err := enrollments.Start(ctx, StartEnrollmentParams{
			StudentID: student.ID, LessonID: lessonID, LessonVersionID: lesson.LessonVersionID,
			CourseEnrollmentID: run.Enrollment.ID, Mode: "curator",
		})
		if err != nil {
			t.Fatalf("начать урок курса: %v", err)
		}
		var questionID int64
		if err := pool.QueryRow(ctx, "SELECT id FROM lesson_questions WHERE lesson_version_id = $1", lesson.LessonVersionID).Scan(&questionID); err != nil {
			t.Fatalf("получить вопрос: %v", err)
		}
		saved, err := answers.Submit(ctx, AnswerSubmission{
			StudentID: student.ID, EnrollmentID: enrollment.ID, QuestionID: questionID,
			AnswerText: fmt.Sprintf("Ответ ученика на урок %d", index+1),
		})
		if err != nil {
			t.Fatalf("отправить ответ: %v", err)
		}
		curatorAnswers, err := curatorRepository.ListLessonAnswers(
			ctx, secondCuratorID, student.ID, run.Enrollment.ID, lessonID,
		)
		if err != nil || len(curatorAnswers) != 1 || curatorAnswers[0].AttemptID == nil ||
			*curatorAnswers[0].AttemptID != saved.AttemptID {
			t.Fatalf("текущий куратор не получил ответ ученика: answers=%+v err=%v", curatorAnswers, err)
		}
		if _, err := curatorRepository.ListLessonAnswers(
			ctx, firstCuratorID, student.ID, run.Enrollment.ID, lessonID,
		); !errors.Is(err, ErrNotFound) {
			t.Fatalf("прежний куратор не должен видеть ответы после передачи: %v", err)
		}

		// Отправленный, но не проверенный ответ не разрешает завершить урок.
		if _, err := enrollments.Complete(ctx, student.ID, enrollment.ID); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("непроверенный ответ завершил урок: %v", err)
		}
		if index == 0 {
			if _, err := curatorRepository.ReviewAttempt(ctx, domain.ReviewAnswerAttemptParams{
				CuratorStaffID: secondCuratorID, AttemptID: saved.AttemptID,
				Decision: "needs_revision", Feedback: "Добавьте пояснение к ответу.",
			}); err != nil {
				t.Fatalf("вернуть ответ на исправление: %v", err)
			}
			history, err := answers.ListAttemptHistory(ctx, student.ID, enrollment.ID, questionID)
			if err != nil || len(history) != 1 || history[0].ReviewDecision == nil ||
				*history[0].ReviewDecision != "needs_revision" ||
				history[0].ReviewFeedback != nil && *history[0].ReviewFeedback != "Добавьте пояснение к ответу." {
				t.Fatalf("ученик не получил решение и комментарий куратора: history=%+v err=%v", history, err)
			}
			revised, err := answers.Submit(ctx, AnswerSubmission{
				StudentID: student.ID, EnrollmentID: enrollment.ID, QuestionID: questionID,
				AnswerText: "Исправленный ответ с пояснением",
			})
			if err != nil {
				t.Fatalf("повторно отправить исправленный ответ: %v", err)
			}
			if _, err := enrollments.Complete(ctx, student.ID, enrollment.ID); !errors.Is(err, ErrInvalidState) {
				t.Fatalf("непроверенная повторная попытка завершила урок: %v", err)
			}
			saved = revised
		}
		if _, err := curatorRepository.ReviewAttempt(ctx, domain.ReviewAnswerAttemptParams{
			CuratorStaffID: secondCuratorID, AttemptID: saved.AttemptID,
			Decision: "approved", Feedback: "Ответ принят.",
		}); err != nil {
			t.Fatalf("одобрить последнюю попытку: %v", err)
		}
		if _, err := enrollments.Complete(ctx, student.ID, enrollment.ID); err != nil {
			t.Fatalf("завершить урок после решения куратора: %v", err)
		}
		if _, err := answers.Submit(ctx, AnswerSubmission{
			StudentID: student.ID, EnrollmentID: enrollment.ID, QuestionID: questionID,
			AnswerText: "Попытка изменить уже завершённый урок",
		}); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("завершённый урок не должен принимать новый ответ, получено %v", err)
		}
		if index == 0 {
			afterFirst, err := courses.GetForStudent(ctx, student.ID, run.Enrollment.ID)
			if err != nil {
				t.Fatalf("получить прогресс после первого урока: %v", err)
			}
			if afterFirst.Enrollment.Status != "in_progress" || afterFirst.CompletedLessons != 1 {
				t.Fatalf("курс завершился раньше второго урока: %+v", afterFirst)
			}
		}
	}

	completed, err := courses.GetForStudent(ctx, student.ID, run.Enrollment.ID)
	if err != nil || completed.Enrollment.Status != "completed" ||
		completed.CompletedLessons != 2 || completed.RemainingLessons != 0 || !completed.IsComplete {
		t.Fatalf("курс не завершился после проверки всех уроков: progress=%+v err=%v", completed, err)
	}
	nextRun, err := courses.Start(ctx, student.ID, courseID, "ai")
	if err != nil || nextRun.Enrollment.ID == run.Enrollment.ID ||
		nextRun.Enrollment.RunNumber != run.Enrollment.RunNumber+1 || nextRun.Enrollment.Mode != "ai" {
		t.Fatalf("после завершения нельзя начать новый запуск курса: progress=%+v err=%v", nextRun, err)
	}

	// Режим ai использует уже существующее назначение, но не переназначает его.
	var assignedCuratorID int64
	if err := pool.QueryRow(ctx, `
		SELECT curator_staff_id FROM student_curator_assignments
		WHERE student_id = $1 AND status = 'active'
	`, student.ID).Scan(&assignedCuratorID); err != nil {
		t.Fatalf("проверить сохранённое назначение после старта ai: %v", err)
	}
	if assignedCuratorID != secondCuratorID {
		t.Fatalf("старт ai изменил административное назначение: got=%d want=%d", assignedCuratorID, secondCuratorID)
	}

	aiLesson, err := courses.GetLessonForRun(ctx, student.ID, nextRun.Enrollment.ID, lessonIDs[0])
	if err != nil {
		t.Fatalf("получить урок для AI-прохождения: %v", err)
	}
	aiEnrollment, err := NewEnrollmentRepository(pool).Start(ctx, StartEnrollmentParams{
		StudentID:          student.ID,
		LessonID:           lessonIDs[0],
		LessonVersionID:    aiLesson.LessonVersionID,
		CourseEnrollmentID: nextRun.Enrollment.ID,
		Mode:               "ai",
	})
	if err != nil {
		t.Fatalf("начать урок в режиме ai: %v", err)
	}
	var aiQuestionID int64
	if err := pool.QueryRow(ctx, "SELECT id FROM lesson_questions WHERE lesson_version_id = $1", aiLesson.LessonVersionID).Scan(&aiQuestionID); err != nil {
		t.Fatalf("получить вопрос AI-прохождения: %v", err)
	}
	aiAnswer, err := answers.Submit(ctx, AnswerSubmission{
		StudentID:    student.ID,
		EnrollmentID: aiEnrollment.ID,
		QuestionID:   aiQuestionID,
		AnswerText:   "Ответ для самостоятельного прохождения",
	})
	if err != nil {
		t.Fatalf("отправить ответ в режиме ai: %v", err)
	}
	if _, err := curatorRepository.ReviewAttempt(ctx, domain.ReviewAnswerAttemptParams{
		CuratorStaffID: secondCuratorID,
		AttemptID:      aiAnswer.AttemptID,
		Decision:       "approved",
		Feedback:       "Не должно приниматься в режиме ai.",
	}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("режим ai не должен запускать ручную проверку куратора, получено %v", err)
	}
	if _, err := enrollments.Complete(ctx, student.ID, aiEnrollment.ID); err != nil {
		t.Fatalf("AI-урок должен завершаться без ручного решения куратора: %v", err)
	}
	var aiReviewCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM answer_attempt_reviews WHERE answer_attempt_id = $1", aiAnswer.AttemptID).Scan(&aiReviewCount); err != nil {
		t.Fatalf("проверить отсутствие ручной проверки AI-ответа: %v", err)
	}
	if aiReviewCount != 0 {
		t.Fatalf("AI-ответ получил %d ручных решений куратора", aiReviewCount)
	}
}

func TestLegacyLessonProgressRemainsAccessibleAndBlocksUnsafeCourseGuess(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	student, err := NewStudentRepository(pool).Create(ctx, CreateStudentParams{
		TelegramID: time.Now().UnixNano(), DisplayName: "legacy-course-progress-test",
	})
	if err != nil {
		t.Fatalf("создать ученика: %v", err)
	}
	courseID, lessonIDs, versionIDs := createCourseForEnrollmentTest(
		t, ctx, pool, fmt.Sprintf("%d", time.Now().UnixNano()), "Старый курс", 1,
	)

	// This models a lesson enrollment created by the pre-course-enrollment API.
	legacy, err := NewEnrollmentRepository(pool).Start(ctx, StartEnrollmentParams{
		StudentID:       student.ID,
		LessonID:        lessonIDs[0],
		LessonVersionID: versionIDs[0],
		Mode:            "ai",
	})
	if err != nil {
		t.Fatalf("создать старое прохождение урока: %v", err)
	}
	var questionID int64
	if err := pool.QueryRow(ctx, "SELECT id FROM lesson_questions WHERE lesson_version_id = $1", versionIDs[0]).Scan(&questionID); err != nil {
		t.Fatalf("получить вопрос старого урока: %v", err)
	}
	if _, err := NewAnswerRepository(pool).Submit(ctx, AnswerSubmission{
		StudentID:    student.ID,
		EnrollmentID: legacy.ID,
		QuestionID:   questionID,
		AnswerText:   "Сохранённый ответ старого ученика",
	}); err != nil {
		t.Fatalf("сохранить старый ответ: %v", err)
	}

	progress, err := NewEnrollmentRepository(pool).GetProgress(ctx, student.ID, legacy.ID)
	if err != nil || progress.AnsweredQuestions != 1 ||
		progress.Enrollment.CourseEnrollmentID != nil {
		t.Fatalf("старый прогресс потерян или автоматически привязан: progress=%+v err=%v", progress, err)
	}
	saved, err := NewAnswerRepository(pool).GetSavedAnswer(ctx, student.ID, legacy.ID, questionID)
	if err != nil || saved.AnswerText != "Сохранённый ответ старого ученика" {
		t.Fatalf("ответ старого ученика не сохранился: answer=%+v err=%v", saved, err)
	}

	// If the relation cannot be proven from legacy rows, a new parent run must
	// not be guessed or created alongside that unlinked progress.
	if _, err := NewCourseEnrollmentRepository(pool).Start(ctx, student.ID, courseID, "ai"); !errors.Is(err, domain.ErrLegacyProgress) {
		t.Fatalf("неоднозначное старое прохождение должно блокировать догадку о курсе, получено %v", err)
	}
	var courseRuns int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM course_enrollments WHERE student_id = $1", student.ID).Scan(&courseRuns); err != nil {
		t.Fatalf("проверить отсутствие нового запуска курса: %v", err)
	}
	if courseRuns != 0 {
		t.Fatalf("при неоднозначном legacy-прогрессе создалось %d запусков курса", courseRuns)
	}
}

func TestCourseEnrollmentSerializesDifferentConcurrentStarts(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	student, err := NewStudentRepository(pool).Create(ctx, CreateStudentParams{
		TelegramID: time.Now().UnixNano(), DisplayName: "course-cross-concurrency-test",
	})
	if err != nil {
		t.Fatalf("создать ученика: %v", err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	firstCourse, _, _ := createCourseForEnrollmentTest(t, ctx, pool, suffix+"-first", "Первый конкурентный курс", 1)
	secondCourse, _, _ := createCourseForEnrollmentTest(t, ctx, pool, suffix+"-second", "Второй конкурентный курс", 1)

	const workers = 12
	type result struct {
		requestedCourseID int64
		progress          domain.CourseEnrollmentProgress
		err               error
	}
	results := make(chan result, workers)
	var wait sync.WaitGroup
	for i := 0; i < workers; i++ {
		courseID := firstCourse
		if i%2 == 1 {
			courseID = secondCourse
		}
		wait.Add(1)
		go func(requestedCourseID int64) {
			defer wait.Done()
			progress, startErr := NewCourseEnrollmentRepository(pool).Start(
				ctx, student.ID, requestedCourseID, "ai",
			)
			results <- result{
				requestedCourseID: requestedCourseID,
				progress:          progress,
				err:               startErr,
			}
		}(courseID)
	}
	wait.Wait()
	close(results)

	var winningCourseID, winningEnrollmentID int64
	successfulStarts, conflicts := 0, 0
	for item := range results {
		if item.progress.Enrollment.ID <= 0 {
			t.Fatalf("конкурентный запрос не вернул активное прохождение: %+v", item)
		}
		if item.err == nil {
			successfulStarts++
			if item.progress.Enrollment.CourseID != item.requestedCourseID {
				t.Fatalf("успешный запрос получил чужой курс: %+v", item)
			}
			if winningCourseID == 0 {
				winningCourseID = item.progress.Enrollment.CourseID
				winningEnrollmentID = item.progress.Enrollment.ID
			} else if item.progress.Enrollment.ID != winningEnrollmentID {
				t.Fatalf("созданы два разных незавершённых запуска: %d и %d", winningEnrollmentID, item.progress.Enrollment.ID)
			}
			continue
		}
		if !errors.Is(item.err, domain.ErrActiveCourse) {
			t.Fatalf("ожидался конфликт активного курса, получено %v", item.err)
		}
		conflicts++
		if item.progress.Enrollment.CourseID != winningCourseID && winningCourseID != 0 {
			t.Fatalf("проигравший запрос получил неверный текущий курс: %+v", item.progress.Enrollment)
		}
	}
	if successfulStarts == 0 || conflicts == 0 || winningCourseID == 0 {
		t.Fatalf("ожидался один победивший курс и конфликты второго: success=%d conflicts=%d", successfulStarts, conflicts)
	}

	var activeCount int
	var activeCourseID, activeEnrollmentID int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*), max(course_id), max(id)
		FROM course_enrollments
		WHERE student_id = $1 AND status IN ('awaiting_curator', 'in_progress', 'paused')
	`, student.ID).Scan(&activeCount, &activeCourseID, &activeEnrollmentID); err != nil {
		t.Fatalf("проверить единственный активный курс: %v", err)
	}
	if activeCount != 1 || activeCourseID != winningCourseID || activeEnrollmentID != winningEnrollmentID {
		t.Fatalf("конкурентные запросы создали несколько активных курсов: count=%d course=%d enrollment=%d",
			activeCount, activeCourseID, activeEnrollmentID)
	}
}

func TestSupersededLegacyCourseRunResumesWithoutLosingItsMode(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	student, err := NewStudentRepository(pool).Create(ctx, CreateStudentParams{
		TelegramID: time.Now().UnixNano(), DisplayName: "superseded-legacy-course-test",
	})
	if err != nil {
		t.Fatalf("создать ученика: %v", err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	courseID, lessonIDs, versionIDs := createCourseForEnrollmentTest(
		t, ctx, pool, suffix+"-old", "Старый сохранённый курс", 1,
	)
	otherCourseID, _, _ := createCourseForEnrollmentTest(
		t, ctx, pool, suffix+"-other", "Другой курс", 1,
	)

	var oldRunID, questionID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO course_enrollments (
		    student_id, course_id, run_number, mode, status, started_at
		)
		VALUES ($1, $2, 1, 'ai', 'superseded', now())
		RETURNING id
	`, student.ID, courseID).Scan(&oldRunID); err != nil {
		t.Fatalf("создать сохранённый старый запуск: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO course_enrollment_lessons (
		    course_enrollment_id, lesson_id, lesson_version_id, position
		)
		VALUES ($1, $2, $3, 1)
	`, oldRunID, lessonIDs[0], versionIDs[0]); err != nil {
		t.Fatalf("сохранить обязательный урок старого запуска: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT id FROM lesson_questions WHERE lesson_version_id = $1
	`, versionIDs[0]).Scan(&questionID); err != nil {
		t.Fatalf("получить вопрос старого запуска: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO lesson_enrollments (
		    student_id, lesson_id, lesson_version_id, course_enrollment_id, mode,
		    current_question_id, status, started_at, last_activity_at
		)
		VALUES ($1, $2, $3, $4, 'ai', $5, 'in_progress', now(), now())
	`, student.ID, lessonIDs[0], versionIDs[0], oldRunID, questionID); err != nil {
		t.Fatalf("сохранить старое прохождение урока: %v", err)
	}

	resumed, err := NewCourseEnrollmentRepository(pool).Start(ctx, student.ID, courseID, "curator")
	if err != nil {
		t.Fatalf("возобновить старый запуск: %v", err)
	}
	if resumed.Enrollment.ID != oldRunID ||
		resumed.Enrollment.Status != "in_progress" ||
		resumed.Enrollment.Mode != "ai" ||
		resumed.TotalLessons != 1 {
		t.Fatalf("старый запуск/режим/состав курса не восстановлены: %+v", resumed)
	}

	blocked, err := NewCourseEnrollmentRepository(pool).Start(ctx, student.ID, otherCourseID, "ai")
	if !errors.Is(err, domain.ErrActiveCourse) ||
		blocked.Enrollment.ID != oldRunID ||
		blocked.Enrollment.CourseID != courseID {
		t.Fatalf("другой курс не должен вытеснять возобновлённый старый: progress=%+v err=%v", blocked, err)
	}
}
