package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

func TestCatalogChoiceSubmissionProgressAndCompletion(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	student, err := NewStudentRepository(pool).EnsureByTelegramID(ctx, EnsureStudentParams{
		TelegramID: time.Now().UnixNano(), DisplayName: "catalog-test-" + suffix,
	})
	if err != nil {
		t.Fatalf("создать ученика: %v", err)
	}
	if again, err := NewStudentRepository(pool).EnsureByTelegramID(ctx, EnsureStudentParams{
		TelegramID: student.TelegramID, DisplayName: "обновлённое имя",
	}); err != nil {
		t.Fatalf("повторно найти ученика: %v", err)
	} else if again.ID != student.ID || again.DisplayName != "обновлённое имя" {
		t.Fatalf("идемпотентное создание изменило ID или профиль не обновился: %+v", again)
	}

	var courseID, firstLessonID, secondLessonID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO courses (slug, title, description, status, published_at)
		VALUES ($1, 'Курс для интеграционного теста', 'Описание теста', 'published', now())
		RETURNING id
	`, "catalog-test-"+suffix).Scan(&courseID); err != nil {
		t.Fatalf("создать курс: %v", err)
	}
	if err := pool.QueryRow(ctx, "INSERT INTO lessons DEFAULT VALUES RETURNING id").Scan(&firstLessonID); err != nil {
		t.Fatalf("создать первый урок: %v", err)
	}
	if err := pool.QueryRow(ctx, "INSERT INTO lessons DEFAULT VALUES RETURNING id").Scan(&secondLessonID); err != nil {
		t.Fatalf("создать второй урок: %v", err)
	}
	// Порядок курса намеренно отличается от порядка создания уроков.
	if _, err := pool.Exec(ctx, `
		INSERT INTO course_lessons (course_id, lesson_id, position)
		VALUES ($1, $2, 2), ($1, $3, 1)
	`, courseID, firstLessonID, secondLessonID); err != nil {
		t.Fatalf("привязать уроки к курсу: %v", err)
	}

	createVersion := func(lessonID int64, title string) int64 {
		t.Helper()
		var versionID int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO lesson_versions (lesson_id, version_number, title, status)
			VALUES ($1, 1, $2, 'draft')
			RETURNING id
		`, lessonID, title).Scan(&versionID); err != nil {
			t.Fatalf("создать версию урока %q: %v", title, err)
		}
		return versionID
	}
	firstVersionID := createVersion(firstLessonID, "Урок с выбором ответа")
	secondVersionID := createVersion(secondLessonID, "Первый по порядку урок")

	var firstQuestionID, secondQuestionID, otherQuestionID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO lesson_questions (lesson_version_id, question_number, prompt, expected_answer, answer_type)
		VALUES ($1, 1, 'Выберите верный вариант', 'Внутренний предполагаемый ответ', 'single_choice')
		RETURNING id
	`, firstVersionID).Scan(&firstQuestionID); err != nil {
		t.Fatalf("создать вопрос с выбором: %v", err)
	}
	var optionOneID, optionTwoID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO question_options (question_id, option_key, option_text, sort_order, is_correct)
		VALUES ($1, 'A', 'Первый ответ', 1, TRUE)
		RETURNING id
	`, firstQuestionID).Scan(&optionOneID); err != nil {
		t.Fatalf("создать первый вариант: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO question_options (question_id, option_key, option_text, sort_order, is_correct)
		VALUES ($1, 'B', 'Второй ответ', 2, FALSE)
		RETURNING id
	`, firstQuestionID).Scan(&optionTwoID); err != nil {
		t.Fatalf("создать второй вариант: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO lesson_questions (lesson_version_id, question_number, prompt, expected_answer, answer_type)
		VALUES ($1, 2, 'Напишите ответ', 'Внутренний ответ второго вопроса', 'text')
		RETURNING id
	`, firstVersionID).Scan(&secondQuestionID); err != nil {
		t.Fatalf("создать второй вопрос: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO lesson_questions (lesson_version_id, question_number, prompt, expected_answer)
		VALUES ($1, 1, 'Вопрос урока, стоящего первым', 'Ответ')
		RETURNING id
	`, secondVersionID).Scan(&otherQuestionID); err != nil {
		t.Fatalf("создать вопрос второго урока: %v", err)
	}
	for _, versionID := range []int64{firstVersionID, secondVersionID} {
		if _, err := pool.Exec(ctx, `
			UPDATE lesson_versions SET status = 'published', published_at = now() WHERE id = $1
		`, versionID); err != nil {
			t.Fatalf("опубликовать версию %d: %v", versionID, err)
		}
	}

	catalog := NewCatalogRepository(pool)
	course, err := catalog.GetPublishedCourseByID(ctx, courseID)
	if err != nil {
		t.Fatalf("получить опубликованный курс: %v", err)
	}
	if course.LessonCount != 2 {
		t.Fatalf("число уроков курса = %d, хотели 2", course.LessonCount)
	}
	lessons, err := catalog.ListPublishedLessonsByCourseID(ctx, courseID)
	if err != nil {
		t.Fatalf("получить уроки курса: %v", err)
	}
	if len(lessons) != 2 || lessons[0].LessonID != secondLessonID || lessons[0].Position != 1 ||
		lessons[1].LessonID != firstLessonID || lessons[1].Position != 2 {
		t.Fatalf("уроки возвращены не в заданном порядке: %+v", lessons)
	}
	detailLesson, err := catalog.GetPublishedLessonByID(ctx, firstLessonID)
	if err != nil || detailLesson.LessonVersionID != firstVersionID {
		t.Fatalf("получить опубликованный урок: lesson=%+v err=%v", detailLesson, err)
	}
	questions, err := NewContentRepository(pool).ListQuestionsForVersion(ctx, firstVersionID)
	if err != nil {
		t.Fatalf("получить вопросы урока: %v", err)
	}
	if len(questions) != 2 || questions[0].ID != firstQuestionID || questions[1].ID != secondQuestionID {
		t.Fatalf("вопросы возвращены не в заданном порядке: %+v", questions)
	}

	enrollments := NewEnrollmentRepository(pool)
	enrollment, err := enrollments.Start(ctx, StartEnrollmentParams{
		StudentID: student.ID, LessonID: firstLessonID, LessonVersionID: firstVersionID, Mode: "ai",
	})
	if err != nil {
		t.Fatalf("начать прохождение: %v", err)
	}
	if err := enrollments.SetCurrentQuestion(ctx, student.ID, enrollment.ID, firstQuestionID); err != nil {
		t.Fatalf("установить текущий вопрос: %v", err)
	}
	progress, err := enrollments.GetProgress(ctx, student.ID, enrollment.ID)
	if err != nil {
		t.Fatalf("получить начальный прогресс: %v", err)
	}
	if progress.TotalQuestions != 2 || progress.AnsweredQuestions != 0 ||
		progress.FirstQuestionID == nil || *progress.FirstQuestionID != firstQuestionID ||
		progress.NextQuestionID == nil || *progress.NextQuestionID != secondQuestionID ||
		progress.PreviousQuestionID != nil {
		t.Fatalf("неверный начальный прогресс/навигация: %+v", progress)
	}

	answers := NewAnswerRepository(pool)
	firstSubmission := AnswerSubmission{
		StudentID: student.ID, EnrollmentID: enrollment.ID, QuestionID: firstQuestionID,
		SelectedOptionIDs: []int64{optionOneID},
		IdempotencyKey:    "d7541e96-2f4c-407e-a8ef-241db4850001",
	}
	saved, err := answers.Submit(ctx, firstSubmission)
	if err != nil {
		t.Fatalf("отправить вариант ответа: %v", err)
	}
	if saved.ProcessingStatus != "pending" || saved.AttemptID <= 0 || len(saved.SelectedOptionIDs) != 1 {
		t.Fatalf("новая попытка не осталась непроверенной: %+v", saved)
	}
	replayed, err := answers.Submit(ctx, firstSubmission)
	if err != nil {
		t.Fatalf("повторить запрос с тем же ключом: %v", err)
	}
	if !replayed.Replayed || replayed.AttemptID != saved.AttemptID {
		t.Fatalf("повтор запроса создал новую попытку: first=%+v replay=%+v", saved, replayed)
	}

	stored, err := answers.GetSavedAnswer(ctx, student.ID, enrollment.ID, firstQuestionID)
	if err != nil {
		t.Fatalf("восстановить выбранный вариант: %v", err)
	}
	if len(stored.SelectedOptionIDs) != 1 || stored.SelectedOptionIDs[0] != optionOneID {
		t.Fatalf("выбранный вариант не восстановился из БД: %+v", stored)
	}
	history, err := answers.ListAttemptHistory(ctx, student.ID, enrollment.ID, firstQuestionID)
	if err != nil || len(history) != 1 {
		t.Fatalf("получить историю попыток: count=%d err=%v", len(history), err)
	}
	if history[0].ProcessingStatus != "pending" || history[0].EvaluationResult != nil {
		t.Fatalf("отправленный ответ получил фиктивный результат проверки: %+v", history[0])
	}

	_, err = answers.Submit(ctx, AnswerSubmission{
		StudentID: student.ID, EnrollmentID: enrollment.ID,
		QuestionID: firstQuestionID, SelectedOptionIDs: []int64{otherQuestionID},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("чужой вариант должен отклоняться, получено %v", err)
	}
	if _, err := enrollments.Complete(ctx, student.ID, enrollment.ID); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("нельзя завершить урок с неотвеченным вопросом, получено %v", err)
	}

	if _, err := answers.Submit(ctx, AnswerSubmission{
		StudentID: student.ID, EnrollmentID: enrollment.ID, QuestionID: secondQuestionID,
		AnswerText: "Ответ второго вопроса",
	}); err != nil {
		t.Fatalf("сохранить второй ответ: %v", err)
	}
	progress, err = enrollments.GetProgress(ctx, student.ID, enrollment.ID)
	if err != nil {
		t.Fatalf("пересчитать прогресс: %v", err)
	}
	if progress.AnsweredQuestions != 2 || progress.TotalQuestions != 2 || progress.IsComplete {
		t.Fatalf("отправка ответов не должна сама помечать урок завершённым: %+v", progress)
	}
	completed, err := enrollments.Complete(ctx, student.ID, enrollment.ID)
	if err != nil {
		t.Fatalf("завершить полностью отвеченный урок: %v", err)
	}
	if completed.Status != "completed" || completed.CompletedAt == nil {
		t.Fatalf("завершение не зафиксировано: %+v", completed)
	}

	if _, err := answers.Submit(ctx, AnswerSubmission{
		StudentID: student.ID, EnrollmentID: enrollment.ID, QuestionID: firstQuestionID,
		SelectedOptionIDs: []int64{optionTwoID},
	}); err != nil {
		t.Fatalf("изменить ранее отправленный ответ: %v", err)
	}
	progress, err = enrollments.GetProgress(ctx, student.ID, enrollment.ID)
	if err != nil {
		t.Fatalf("восстановить прогресс после изменения ответа: %v", err)
	}
	if progress.Enrollment.Status != "in_progress" || progress.Enrollment.CompletedAt != nil ||
		progress.AnsweredQuestions != 2 || progress.IsComplete {
		t.Fatalf("изменение ответа не вернуло прохождение в рабочее состояние: %+v", progress)
	}
	history, err = answers.ListAttemptHistory(ctx, student.ID, enrollment.ID, firstQuestionID)
	if err != nil || len(history) != 2 {
		t.Fatalf("история предыдущей попытки потеряна: count=%d err=%v", len(history), err)
	}
}
