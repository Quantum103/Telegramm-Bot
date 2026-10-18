package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL не задан; интеграционный тест требует отдельную БД с применёнными миграциями")
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("разобрать TEST_DATABASE_URL: %v", err)
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("создать тестовый пул: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("подключиться к тестовой БД: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestStudentCreateLookupAndUniqueTelegramID(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	repo := NewStudentRepository(pool)
	telegramID := time.Now().UnixNano()
	displayName := fmt.Sprintf("test-student-%d", telegramID)
	student, err := repo.Create(ctx, CreateStudentParams{
		TelegramID: telegramID,
		DisplayName: displayName,
	})
	if err != nil {
		t.Fatalf("создать ученика: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM students WHERE id = $1", student.ID)
	})

	got, err := repo.GetByTelegramID(ctx, telegramID)
	if err != nil {
		t.Fatalf("найти ученика: %v", err)
	}
	if got.ID != student.ID || got.TelegramID != telegramID || got.DisplayName != displayName {
		t.Fatalf("неожиданные данные ученика: %+v", got)
	}

	_, err = repo.Create(ctx, CreateStudentParams{
		TelegramID: telegramID,
		DisplayName: "duplicate",
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("дублирующий Telegram ID должен вернуть ErrConflict, получено: %v", err)
	}
}

func TestAnswerSavedIdempotentlyAndAttemptFailureIsNotIncorrect(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	student, err := NewStudentRepository(pool).Create(ctx, CreateStudentParams{
		TelegramID: time.Now().UnixNano(),
		DisplayName: fmt.Sprintf("test-answer-%d", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatalf("создать ученика: %v", err)
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var courseID, lessonID, versionID, questionID, enrollmentID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO courses (slug, title, status, published_at)
		VALUES ($1, 'Тестовый курс', 'published', now())
		RETURNING id
	`, "integration-"+suffix).Scan(&courseID); err != nil {
		t.Fatalf("создать курс: %v", err)
	}
	if err := pool.QueryRow(ctx, "INSERT INTO lessons DEFAULT VALUES RETURNING id").Scan(&lessonID); err != nil {
		t.Fatalf("создать урок: %v", err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO course_lessons (course_id, lesson_id, position) VALUES ($1, $2, 1)", courseID, lessonID); err != nil {
		t.Fatalf("добавить урок в курс: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO lesson_versions (lesson_id, version_number, title, status, published_at)
		VALUES ($1, 1, 'Тестовая версия', 'published', now())
		RETURNING id
	`, lessonID).Scan(&versionID); err != nil {
		t.Fatalf("создать версию урока: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO lesson_questions (lesson_version_id, question_number, prompt, expected_answer)
		VALUES ($1, 1, 'Тестовый вопрос?', 'Тестовый предполагаемый ответ.')
		RETURNING id
	`, versionID).Scan(&questionID); err != nil {
		t.Fatalf("создать вопрос: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO lesson_enrollments (student_id, lesson_id, lesson_version_id, mode)
		VALUES ($1, $2, $3, 'ai')
		RETURNING id
	`, student.ID, lessonID, versionID).Scan(&enrollmentID); err != nil {
		t.Fatalf("создать прохождение: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM answer_attempts WHERE enrollment_id = $1", enrollmentID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM student_answers WHERE enrollment_id = $1", enrollmentID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM lesson_enrollments WHERE id = $1", enrollmentID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM students WHERE id = $1", student.ID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM question_criteria WHERE question_id = $1", questionID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM question_options WHERE question_id = $1", questionID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM question_common_mistakes WHERE question_id = $1", questionID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM question_bible_passages WHERE question_id = $1", questionID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM lesson_questions WHERE id = $1", questionID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM lesson_versions WHERE id = $1", versionID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM course_lessons WHERE course_id = $1 AND lesson_id = $2", courseID, lessonID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM lessons WHERE id = $1", lessonID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM courses WHERE id = $1", courseID)
	})

	answers := NewAnswerRepository(pool)
	idempotencyKey := "7ad6a3e1-4c40-4ea9-810f-8e5e27ab0001"
	first, err := answers.SaveTextAnswer(ctx, SaveTextAnswerParams{
		StudentID: student.ID, EnrollmentID: enrollmentID, QuestionID: questionID,
		AnswerText: "Первый сохранённый ответ", IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		t.Fatalf("сохранить ответ: %v", err)
	}
	if first.ProcessingStatus != "pending" || first.Replayed {
		t.Fatalf("неожиданный результат первого сохранения: %+v", first)
	}

	replay, err := answers.SaveTextAnswer(ctx, SaveTextAnswerParams{
		StudentID: student.ID, EnrollmentID: enrollmentID, QuestionID: questionID,
		AnswerText: "Изменённое тело повторного запроса", IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		t.Fatalf("повторить идемпотентный запрос: %v", err)
	}
	if !replay.Replayed || replay.AttemptID != first.AttemptID {
		t.Fatalf("повторный запрос создал новую попытку: first=%+v replay=%+v", first, replay)
	}

	saved, err := answers.GetSavedTextAnswer(ctx, student.ID, enrollmentID, questionID)
	if err != nil {
		t.Fatalf("получить текущий ответ: %v", err)
	}
	if saved.AnswerText != "Первый сохранённый ответ" {
		t.Fatalf("повтор запроса изменил сохранённый ответ: %q", saved.AnswerText)
	}

	explanation := "Смысл ответа верен."
	if err := answers.CompleteEvaluation(ctx, EvaluationParams{
		AttemptID: first.AttemptID, Result: "correct", Explanation: &explanation,
		MissingPoints: []string{}, Provider: "test-provider", Model: "test-model",
		Duration: 25 * time.Millisecond,
	}); err != nil {
		t.Fatalf("сохранить результат проверки: %v", err)
	}

	second, err := answers.SaveTextAnswer(ctx, SaveTextAnswerParams{
		StudentID: student.ID, EnrollmentID: enrollmentID, QuestionID: questionID,
		AnswerText: "Ответ сохранён до вызова ИИ", IdempotencyKey: "6c695f28-4a15-46ef-8d40-e7e57b5e0002",
	})
	if err != nil {
		t.Fatalf("сохранить следующую попытку: %v", err)
	}
	if err := answers.FailAttempt(ctx, AttemptFailureParams{
		AttemptID: second.AttemptID, Provider: "test-provider", Model: "test-model",
		ErrorCode: "provider_unavailable", Duration: 40 * time.Millisecond,
	}); err != nil {
		t.Fatalf("сохранить техническую ошибку ИИ: %v", err)
	}

	history, err := answers.ListAttemptHistory(ctx, student.ID, enrollmentID, questionID)
	if err != nil {
		t.Fatalf("получить историю попыток: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("число попыток = %d, хотели 2", len(history))
	}
	var failedFound, completedFound bool
	for _, attempt := range history {
		if attempt.ID == second.AttemptID {
			failedFound = attempt.ProcessingStatus == "failed" &&
				attempt.EvaluationResult == nil && attempt.ErrorCode != nil &&
				*attempt.ErrorCode == "provider_unavailable"
		}
		if attempt.ID == first.AttemptID {
			completedFound = attempt.ProcessingStatus == "completed" &&
				attempt.EvaluationResult != nil && *attempt.EvaluationResult == "correct"
		}
	}
	if !failedFound || !completedFound {
		t.Fatalf("история не различает результат и ошибку провайдера: %+v", history)
	}
}

var _ = pgx.ErrNoRows
