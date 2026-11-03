package repository

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type navigationBenchmarkTracer struct {
	queries atomic.Int64
}

func (t *navigationBenchmarkTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}

func (t *navigationBenchmarkTracer) TraceQueryEnd(_ context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	t.queries.Add(1)
}

type navigationBenchmarkFixture struct {
	pool         *pgxpool.Pool
	tracer       *navigationBenchmarkTracer
	students     *StudentRepository
	enrollments  *EnrollmentRepository
	content      *ContentRepository
	catalog      *CatalogRepository
	answers      *AnswerRepository
	studentID    int64
	enrollmentID int64
	lessonID     int64
	versionID    int64
	firstID      int64
	secondID     int64
}

func newNavigationBenchmarkFixture(b *testing.B) *navigationBenchmarkFixture {
	b.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		b.Skip("TEST_DATABASE_URL не задан; benchmark навигации требует PostgreSQL")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		b.Fatalf("разобрать TEST_DATABASE_URL: %v", err)
	}
	tracer := &navigationBenchmarkTracer{}
	config.ConnConfig.Tracer = tracer
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		b.Fatalf("создать пул benchmark: %v", err)
	}
	b.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		b.Fatalf("подключиться к PostgreSQL: %v", err)
	}

	suffix := fmt.Sprintf("navigation-bench-%d", time.Now().UnixNano())
	students := NewStudentRepository(pool)
	student, err := students.Create(ctx, CreateStudentParams{
		TelegramID: time.Now().UnixNano(), DisplayName: suffix,
	})
	if err != nil {
		b.Fatalf("создать benchmark ученика: %v", err)
	}
	var courseID, lessonID, versionID, firstID, secondID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO courses (slug, title, status, published_at)
		VALUES ($1, 'Benchmark курс', 'published', now())
		RETURNING id
	`, suffix).Scan(&courseID); err != nil {
		b.Fatalf("создать benchmark курс: %v", err)
	}
	if err := pool.QueryRow(ctx, "INSERT INTO lessons DEFAULT VALUES RETURNING id").Scan(&lessonID); err != nil {
		b.Fatalf("создать benchmark урок: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO course_lessons (course_id, lesson_id, position)
		VALUES ($1, $2, 1)
	`, courseID, lessonID); err != nil {
		b.Fatalf("добавить benchmark урок в курс: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO lesson_versions (lesson_id, version_number, title, status)
		VALUES ($1, 1, 'Benchmark навигации', 'draft')
		RETURNING id
	`, lessonID).Scan(&versionID); err != nil {
		b.Fatalf("создать benchmark версию: %v", err)
	}
	for _, item := range []struct {
		number int
		prompt string
		target *int64
	}{
		{number: 1, prompt: "Первый benchmark вопрос", target: &firstID},
		{number: 2, prompt: "Второй benchmark вопрос", target: &secondID},
	} {
		if err := pool.QueryRow(ctx, `
			INSERT INTO lesson_questions (lesson_version_id, question_number, prompt, expected_answer)
			VALUES ($1, $2, $3, 'Ответ')
			RETURNING id
		`, versionID, item.number, item.prompt).Scan(item.target); err != nil {
			b.Fatalf("создать benchmark вопрос: %v", err)
		}
	}
	if _, err := pool.Exec(ctx,
		"UPDATE lesson_versions SET status = 'published', published_at = now() WHERE id = $1",
		versionID,
	); err != nil {
		b.Fatalf("опубликовать benchmark версию: %v", err)
	}

	enrollments := NewEnrollmentRepository(pool)
	enrollment, err := enrollments.Start(ctx, StartEnrollmentParams{
		StudentID: student.ID, LessonID: lessonID, LessonVersionID: versionID, Mode: "ai",
	})
	if err != nil {
		b.Fatalf("создать benchmark прохождение: %v", err)
	}
	for _, questionID := range []int64{firstID, secondID} {
		var answerID int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO student_answers (enrollment_id, lesson_version_id, question_id, answer_text)
			VALUES ($1, $2, $3, 'Ответ')
			RETURNING id
		`, enrollment.ID, versionID, questionID).Scan(&answerID); err != nil {
			b.Fatalf("создать сохранённый benchmark ответ: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO answer_attempts (
				answer_id, enrollment_id, lesson_version_id, question_id,
				submitted_answer_text, processing_status, evaluation_result,
				completed_at
			) VALUES ($1, $2, $3, $4, 'Ответ', 'completed', 'correct', now())
		`, answerID, enrollment.ID, versionID, questionID); err != nil {
			b.Fatalf("создать benchmark попытку: %v", err)
		}
	}
	if _, err := enrollments.OpenQuestionPosition(ctx, student.ID, enrollment.ID, firstID); err != nil {
		b.Fatalf("установить начальный benchmark вопрос: %v", err)
	}

	content := NewContentRepository(pool)
	if _, err := content.ListQuestionsForVersion(ctx, versionID); err != nil {
		b.Fatalf("прогреть кэш опубликованного контента: %v", err)
	}
	tracer.queries.Store(0)

	return &navigationBenchmarkFixture{
		pool: pool, tracer: tracer, students: students, enrollments: enrollments,
		content: content, catalog: NewCatalogRepository(pool), answers: NewAnswerRepository(pool),
		studentID: student.ID, enrollmentID: enrollment.ID, lessonID: lessonID, versionID: versionID,
		firstID: firstID, secondID: secondID,
	}
}

func (f *navigationBenchmarkFixture) legacyStep(ctx context.Context, currentID int64, step int) (int64, error) {
	// Callback validation read.
	if _, err := f.enrollments.GetProgress(ctx, f.studentID, f.enrollmentID); err != nil {
		return 0, err
	}
	// LearningService.navigate: active-student check, progress and full question list.
	if _, err := f.students.GetByID(ctx, f.studentID); err != nil {
		return 0, err
	}
	if _, err := f.enrollments.GetProgress(ctx, f.studentID, f.enrollmentID); err != nil {
		return 0, err
	}
	firstRead, err := f.content.listQuestionsForVersion(ctx, f.versionID, false)
	if err != nil {
		return 0, err
	}
	firstIndex := -1
	for index := range firstRead {
		if firstRead[index].ID == currentID {
			firstIndex = index
			break
		}
	}
	targetIndex := firstIndex + step
	if firstIndex < 0 || targetIndex < 0 || targetIndex >= len(firstRead) {
		return 0, domain.ErrInvalidState
	}
	targetID := firstRead[targetIndex].ID

	// OpenQuestion repeated student/enrollment checks and loaded the full version again.
	if _, err := f.students.GetByID(ctx, f.studentID); err != nil {
		return 0, err
	}
	if _, err := f.enrollments.GetByIDForStudent(ctx, f.studentID, f.enrollmentID); err != nil {
		return 0, err
	}
	secondRead, err := f.content.listQuestionsForVersion(ctx, f.versionID, false)
	if err != nil {
		return 0, err
	}
	foundTarget := false
	for _, question := range secondRead {
		if question.ID == targetID {
			foundTarget = true
			break
		}
	}
	if !foundTarget {
		return 0, domain.ErrNotFound
	}
	if err := f.enrollments.SetCurrentQuestion(ctx, f.studentID, f.enrollmentID, targetID); err != nil {
		return 0, err
	}
	if _, err := f.enrollments.GetProgress(ctx, f.studentID, f.enrollmentID); err != nil {
		return 0, err
	}
	if _, err := f.answers.GetSavedAnswer(ctx, f.studentID, f.enrollmentID, targetID); err != nil {
		return 0, err
	}
	if _, err := f.answers.ListAttemptHistory(ctx, f.studentID, f.enrollmentID, targetID); err != nil {
		return 0, err
	}

	// renderQuestionMode previously loaded LessonDetail including all questions
	// just to display the lesson title.
	lesson, err := f.catalog.GetPublishedLessonByID(ctx, f.lessonID)
	if err != nil {
		return 0, err
	}
	if _, err := f.content.listQuestionsForVersion(ctx, lesson.LessonVersionID, false); err != nil {
		return 0, err
	}
	return targetID, nil
}

func (f *navigationBenchmarkFixture) optimizedStep(
	ctx context.Context, currentID int64, step int,
) (int64, error) {
	progress, stale, err := f.enrollments.NavigateQuestion(
		ctx, f.studentID, f.enrollmentID, currentID, step,
	)
	if err != nil {
		return 0, err
	}
	if stale || progress.Enrollment.CurrentQuestionID == nil {
		return 0, domain.ErrConflict
	}
	targetID := *progress.Enrollment.CurrentQuestionID
	questions, err := f.content.ListQuestionsForVersion(ctx, f.versionID)
	if err != nil {
		return 0, err
	}
	if len(questions) == 0 {
		return 0, domain.ErrNotFound
	}
	foundTarget := false
	for _, question := range questions {
		if question.ID == targetID {
			foundTarget = true
			break
		}
	}
	if !foundTarget {
		return 0, domain.ErrNotFound
	}
	if _, _, err := f.answers.GetNavigationAnswerState(ctx, f.studentID, f.enrollmentID, targetID); err != nil {
		return 0, err
	}
	if _, err := f.catalog.GetPublishedLessonByID(ctx, f.lessonID); err != nil {
		return 0, err
	}
	return targetID, nil
}

// BenchmarkQuestionNavigationPaths compares the previous multi-call navigation path
// with the transactional path using a warm immutable-content cache. Both paths use
// the same local PostgreSQL database, published content, and saved answer/attempt rows.
func BenchmarkQuestionNavigationPaths(b *testing.B) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	b.Run("before", func(b *testing.B) {
		fixture := newNavigationBenchmarkFixture(b)
		currentID := fixture.firstID
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			step := 1
			if currentID == fixture.secondID {
				step = -1
			}
			nextID, err := fixture.legacyStep(ctx, currentID, step)
			if err != nil {
				b.Fatalf("legacy navigation: %v", err)
			}
			currentID = nextID
		}
		b.StopTimer()
		b.ReportMetric(float64(fixture.tracer.queries.Load())/float64(b.N), "sql/op")
	})

	b.Run("after_warm_cache", func(b *testing.B) {
		fixture := newNavigationBenchmarkFixture(b)
		currentID := fixture.firstID
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			step := 1
			if currentID == fixture.secondID {
				step = -1
			}
			nextID, err := fixture.optimizedStep(ctx, currentID, step)
			if err != nil {
				b.Fatalf("optimized navigation: %v", err)
			}
			currentID = nextID
		}
		b.StopTimer()
		b.ReportMetric(float64(fixture.tracer.queries.Load())/float64(b.N), "sql/op")
	})
}
