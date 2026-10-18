package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

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
	// Интеграционные тесты запускаются только на отдельной тестовой БД.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, "TRUNCATE TABLE students, courses, lessons RESTART IDENTITY CASCADE"); err != nil {
			t.Errorf("очистить тестовые данные: %v", err)
			return
		}
		// Отрывки не удаляются каскадно после удаления связей, поэтому убираем сиротские строки в изолированной БД тестов.
		if _, err := pool.Exec(cleanupCtx, `
			DELETE FROM bible_passages passage
			WHERE NOT EXISTS (
				SELECT 1 FROM question_bible_passages link WHERE link.passage_id = passage.id
			)
		`); err != nil {
			t.Errorf("очистить библейские отрывки без связей: %v", err)
		}
	})
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
		TelegramID:  telegramID,
		DisplayName: displayName,
	})
	if err != nil {
		t.Fatalf("создать ученика: %v", err)
	}

	got, err := repo.GetByTelegramID(ctx, telegramID)
	if err != nil {
		t.Fatalf("найти ученика: %v", err)
	}
	if got.ID != student.ID || got.TelegramID != telegramID || got.DisplayName != displayName {
		t.Fatalf("неожиданные данные ученика: %+v", got)
	}

	_, err = repo.Create(ctx, CreateStudentParams{
		TelegramID:  telegramID,
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
		TelegramID:  time.Now().UnixNano(),
		DisplayName: fmt.Sprintf("test-answer-%d", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatalf("создать ученика: %v", err)
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var courseID, lessonID, versionID, questionID, secondQuestionID, enrollmentID int64
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
		INSERT INTO lesson_versions (lesson_id, version_number, title, status)
		VALUES ($1, 1, 'Тестовая версия', 'draft')
		RETURNING id
	`, lessonID).Scan(&versionID); err != nil {
		t.Fatalf("создать черновик версии урока: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO lesson_questions (lesson_version_id, question_number, prompt, expected_answer)
		VALUES ($1, 1, 'Тестовый вопрос?', 'Тестовый предполагаемый ответ.')
		RETURNING id
	`, versionID).Scan(&questionID); err != nil {
		t.Fatalf("создать вопрос: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO lesson_questions (lesson_version_id, question_number, prompt, expected_answer)
		VALUES ($1, 2, 'Второй тестовый вопрос?', 'Предполагаемый ответ на второй вопрос.')
		RETURNING id
	`, versionID).Scan(&secondQuestionID); err != nil {
		t.Fatalf("создать второй вопрос: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE lesson_versions SET status = 'published', published_at = now() WHERE id = $1
	`, versionID); err != nil {
		t.Fatalf("опубликовать тестовую версию урока: %v", err)
	}
	enrollment, err := NewEnrollmentRepository(pool).Start(ctx, StartEnrollmentParams{
		StudentID: student.ID, LessonID: lessonID, LessonVersionID: versionID, Mode: "ai",
	})
	if err != nil {
		t.Fatalf("создать прохождение: %v", err)
	}
	enrollmentID = enrollment.ID

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

	progress, err := NewEnrollmentRepository(pool).GetByStudentAndVersion(ctx, student.ID, versionID, "ai")
	if err != nil {
		t.Fatalf("получить сохранённый прогресс: %v", err)
	}
	if progress.ID != enrollmentID || progress.Status != "in_progress" ||
		progress.CurrentQuestionID == nil || *progress.CurrentQuestionID != questionID {
		t.Fatalf("прогресс прохождения не сохранён: %+v", progress)
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

	// Смоделированная ошибка в конце транзакции проверяет, что текущий ответ и прогресс откатываются вместе.
	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION test_reject_answer_attempt() RETURNS TRIGGER
		LANGUAGE plpgsql AS $test$
		BEGIN
			RAISE EXCEPTION 'ошибка, созданная integration test';
		END;
		$test$
	`); err != nil {
		t.Fatalf("создать тестовую функцию отказа: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DROP TRIGGER IF EXISTS test_reject_answer_attempt_trg ON answer_attempts")
		_, _ = pool.Exec(cleanupCtx, "DROP FUNCTION IF EXISTS test_reject_answer_attempt()")
	}()
	if _, err := pool.Exec(ctx, `
		CREATE TRIGGER test_reject_answer_attempt_trg
		BEFORE INSERT ON answer_attempts
		FOR EACH ROW EXECUTE FUNCTION test_reject_answer_attempt()
	`); err != nil {
		t.Fatalf("создать тестовый триггер отказа: %v", err)
	}

	_, err = answers.SaveTextAnswer(ctx, SaveTextAnswerParams{
		StudentID: student.ID, EnrollmentID: enrollmentID, QuestionID: secondQuestionID,
		AnswerText:     "Этот ответ не должен сохраниться",
		IdempotencyKey: "902b71df-0e63-4ed4-b1c2-b8bb5b38e004",
	})
	if err == nil {
		t.Fatal("ожидалась ошибка вставки попытки, но транзакция прошла")
	}
	savedAfterRollback, err := answers.GetSavedTextAnswer(ctx, student.ID, enrollmentID, questionID)
	if err != nil {
		t.Fatalf("получить текущий ответ после отката: %v", err)
	}
	if savedAfterRollback.AnswerText != "Ответ сохранён до вызова ИИ" {
		t.Fatalf("транзакция не откатила изменение текущего ответа: %q", savedAfterRollback.AnswerText)
	}
	progressAfterRollback, err := NewEnrollmentRepository(pool).GetByStudentAndVersion(ctx, student.ID, versionID, "ai")
	if err != nil {
		t.Fatalf("получить прогресс после отката: %v", err)
	}
	if progressAfterRollback.CurrentQuestionID == nil || *progressAfterRollback.CurrentQuestionID != questionID {
		t.Fatalf("неожиданный прогресс после отката: %+v", progressAfterRollback)
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

	if _, err := pool.Exec(ctx, `
		UPDATE lesson_versions SET status = 'archived' WHERE id = $1
	`, versionID); err != nil {
		t.Fatalf("архивировать версию для теста истории: %v", err)
	}
	questions, err := NewContentRepository(pool).ListQuestionsForVersion(ctx, versionID)
	if err != nil {
		t.Fatalf("прочитать вопросы архивной версии: %v", err)
	}
	if len(questions) != 2 || questions[0].ID != questionID || questions[1].ID != secondQuestionID {
		t.Fatalf("архивная версия недоступна для восстановления: %+v", questions)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE lesson_questions SET prompt = 'Подменённый вопрос' WHERE id = $1
	`, questionID); err == nil {
		t.Fatal("опубликованную/архивную версию удалось изменить")
	}
	questions, err = NewContentRepository(pool).ListQuestionsForVersion(ctx, versionID)
	if err != nil {
		t.Fatalf("повторно прочитать архивную версию: %v", err)
	}
	if questions[0].Prompt != "Тестовый вопрос?" {
		t.Fatalf("триггер не сохранил содержимое архивного вопроса: %q", questions[0].Prompt)
	}
}
