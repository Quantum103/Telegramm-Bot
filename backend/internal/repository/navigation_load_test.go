package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type navigationLoadQueryStartKey struct{}

type navigationLoadTracer struct {
	mu        sync.Mutex
	durations []time.Duration
}

func (t *navigationLoadTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, navigationLoadQueryStartKey{}, time.Now())
}

func (t *navigationLoadTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	startedAt, ok := ctx.Value(navigationLoadQueryStartKey{}).(time.Time)
	if !ok {
		return
	}
	t.mu.Lock()
	t.durations = append(t.durations, time.Since(startedAt))
	t.mu.Unlock()
}

func (t *navigationLoadTracer) takeDurations() []time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := append([]time.Duration(nil), t.durations...)
	t.durations = nil
	return result
}

type navigationLoadLearner struct {
	studentID        int64
	enrollmentID     int64
	firstQuestionID  int64
	secondQuestionID int64
}

type navigationPoolPeaks struct {
	mu          sync.Mutex
	samples     int
	maxTotal    int32
	maxAcquired int32
	maxIdle     int32
}

func (p *navigationPoolPeaks) observe(stat *pgxpool.Stat) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.samples++
	if stat.TotalConns() > p.maxTotal {
		p.maxTotal = stat.TotalConns()
	}
	if stat.AcquiredConns() > p.maxAcquired {
		p.maxAcquired = stat.AcquiredConns()
	}
	if stat.IdleConns() > p.maxIdle {
		p.maxIdle = stat.IdleConns()
	}
}

func (p *navigationPoolPeaks) snapshot() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return map[string]any{
		"samples": p.samples, "peak_total_connections": p.maxTotal,
		"peak_acquired_connections": p.maxAcquired, "peak_idle_connections": p.maxIdle,
	}
}

func TestQuestionNavigationLoadProfiles(t *testing.T) {
	if os.Getenv("RUN_LOAD_PROFILE") != "1" {
		t.Skip("нагрузочный профиль запускается отдельно: RUN_LOAD_PROFILE=1")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL не задан; нагрузочный профиль требует изолированную PostgreSQL с миграциями")
	}

	iterations := 1000
	if configured := os.Getenv("LOAD_PROFILE_ITERATIONS"); configured != "" {
		parsed, err := strconv.Atoi(configured)
		if err != nil || parsed < 1 {
			t.Fatalf("LOAD_PROFILE_ITERATIONS должен быть положительным целым числом, получено %q", configured)
		}
		iterations = parsed
	}

	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("разобрать TEST_DATABASE_URL: %v", err)
	}
	tracer := &navigationLoadTracer{}
	poolConfig.ConnConfig.Tracer = tracer
	poolConfig.MaxConns = 10
	poolConfig.MinConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		t.Fatalf("создать нагрузочный пул PostgreSQL: %v", err)
	}
	t.Cleanup(pool.Close)

	setupCtx, cancelSetup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelSetup()
	if err := pool.Ping(setupCtx); err != nil {
		t.Fatalf("подключиться к PostgreSQL: %v", err)
	}

	learners := createNavigationLoadLearners(t, setupCtx, pool)
	enrollments := NewEnrollmentRepository(pool)

	// Проверяем, что tracer фиксирует искусственно добавленную задержку SQL.
	tracer.takeDurations()
	if _, err := pool.Exec(setupCtx, "SELECT pg_sleep(0.03)"); err != nil {
		t.Fatalf("проверить измерение искусственной задержки PostgreSQL: %v", err)
	}
	delaySamples := tracer.takeDurations()
	if len(delaySamples) != 1 || delaySamples[0] < 20*time.Millisecond {
		t.Fatalf("pgx tracer не зафиксировал pg_sleep(30ms): samples=%v", delaySamples)
	}
	t.Logf("POSTGRES_INJECTED_DELAY %s", delaySamples[0])

	for _, profile := range []struct {
		name     string
		students int
	}{
		{name: "one_student", students: 1},
		{name: "five_students", students: 5},
		{name: "ten_students", students: 10},
	} {
		selected := learners[:profile.students]
		for _, learner := range selected {
			if _, err := enrollments.OpenQuestionPosition(
				setupCtx, learner.studentID, learner.enrollmentID, learner.firstQuestionID,
			); err != nil {
				t.Fatalf("сбросить позицию перед профилем %s: %v", profile.name, err)
			}
		}
		tracer.takeDurations()

		beforePool := pool.Stat()
		peaks := &navigationPoolPeaks{}
		peaks.observe(beforePool)
		stopSampling := make(chan struct{})
		samplingDone := make(chan struct{})
		go func() {
			defer close(samplingDone)
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stopSampling:
					return
				case <-ticker.C:
					peaks.observe(pool.Stat())
				}
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		startedAt := time.Now()
		results := make([][]time.Duration, profile.students)
		errorsByStudent := make(chan error, profile.students)
		var workers sync.WaitGroup
		workers.Add(profile.students)
		for index, learner := range selected {
			index, learner := index, learner
			go func() {
				defer workers.Done()
				local := make([]time.Duration, 0, iterations)
				currentQuestionID := learner.firstQuestionID
				for operation := 0; operation < iterations; operation++ {
					step := 1
					if currentQuestionID == learner.secondQuestionID {
						step = -1
					}
					operationStartedAt := time.Now()
					progress, stale, err := enrollments.NavigateQuestion(
						ctx, learner.studentID, learner.enrollmentID, currentQuestionID, step,
					)
					local = append(local, time.Since(operationStartedAt))
					if err != nil {
						errorsByStudent <- fmt.Errorf("ученик %d, операция %d: %w", index+1, operation+1, err)
						break
					}
					if stale || progress.Enrollment.CurrentQuestionID == nil {
						errorsByStudent <- fmt.Errorf("ученик %d, операция %d: получена stale/пустая позиция", index+1, operation+1)
						break
					}
					currentQuestionID = *progress.Enrollment.CurrentQuestionID
					if currentQuestionID != learner.firstQuestionID && currentQuestionID != learner.secondQuestionID {
						errorsByStudent <- fmt.Errorf("ученик %d, операция %d: неожиданный question_id=%d", index+1, operation+1, currentQuestionID)
						break
					}
				}
				results[index] = local
			}()
		}
		workers.Wait()
		elapsed := time.Since(startedAt)
		cancel()
		close(stopSampling)
		<-samplingDone
		peaks.observe(pool.Stat())

		select {
		case loadErr := <-errorsByStudent:
			t.Fatalf("нагрузочный профиль %s завершился с ошибкой: %v", profile.name, loadErr)
		default:
		}

		operationDurations := make([]time.Duration, 0, profile.students*iterations)
		for _, values := range results {
			operationDurations = append(operationDurations, values...)
		}
		if len(operationDurations) != profile.students*iterations {
			t.Fatalf("профиль %s: завершено %d из %d операций", profile.name, len(operationDurations), profile.students*iterations)
		}
		sqlDurations := tracer.takeDurations()
		if len(sqlDurations) == 0 {
			t.Fatalf("профиль %s не собрал SQL latency", profile.name)
		}

		afterPool := pool.Stat()
		report := map[string]any{
			"profile":                   profile.name,
			"students":                  profile.students,
			"operations_per_student":    iterations,
			"operation_count":           len(operationDurations),
			"elapsed_ms":                float64(elapsed) / float64(time.Millisecond),
			"throughput_ops_per_second": float64(len(operationDurations)) / elapsed.Seconds(),
			"operation_latency_ms":      navigationLatencyPercentiles(operationDurations),
			"sql_query_count":           len(sqlDurations),
			"sql_queries_per_operation": float64(len(sqlDurations)) / float64(len(operationDurations)),
			"sql_latency_ms":            navigationLatencyPercentiles(sqlDurations),
			"pool": map[string]any{
				"max_connections":              afterPool.MaxConns(),
				"acquire_count_delta":          afterPool.AcquireCount() - beforePool.AcquireCount(),
				"acquire_wait_ms_delta":        float64(afterPool.AcquireDuration()-beforePool.AcquireDuration()) / float64(time.Millisecond),
				"empty_acquire_count_delta":    afterPool.EmptyAcquireCount() - beforePool.EmptyAcquireCount(),
				"canceled_acquire_count_delta": afterPool.CanceledAcquireCount() - beforePool.CanceledAcquireCount(),
				"peaks":                        peaks.snapshot(),
			},
		}
		encoded, err := json.Marshal(report)
		if err != nil {
			t.Fatalf("сериализовать профиль %s: %v", profile.name, err)
		}
		t.Logf("NAVIGATION_LOAD_PROFILE %s", encoded)
	}
}

func createNavigationLoadLearners(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []navigationLoadLearner {
	t.Helper()

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	var courseID, lessonID, versionID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO courses (slug, title, status, published_at)
		VALUES ($1, 'Нагрузочный тест курса', 'published', now())
		RETURNING id
	`, "navigation-load-"+suffix).Scan(&courseID); err != nil {
		t.Fatalf("создать нагрузочный курс: %v", err)
	}
	if err := pool.QueryRow(ctx, "INSERT INTO lessons DEFAULT VALUES RETURNING id").Scan(&lessonID); err != nil {
		t.Fatalf("создать нагрузочный урок: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO course_lessons (course_id, lesson_id, position)
		VALUES ($1, $2, 1)
	`, courseID, lessonID); err != nil {
		t.Fatalf("добавить урок в нагрузочный курс: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO lesson_versions (lesson_id, version_number, title, status)
		VALUES ($1, 1, 'Нагрузочная навигация', 'draft')
		RETURNING id
	`, lessonID).Scan(&versionID); err != nil {
		t.Fatalf("создать нагрузочную версию: %v", err)
	}
	questionIDs := make([]int64, 0, 2)
	for number, prompt := range []string{"Нагрузочный вопрос 1", "Нагрузочный вопрос 2"} {
		var questionID int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO lesson_questions (lesson_version_id, question_number, prompt, expected_answer)
			VALUES ($1, $2, $3, 'Ответ')
			RETURNING id
		`, versionID, number+1, prompt).Scan(&questionID); err != nil {
			t.Fatalf("создать нагрузочный вопрос: %v", err)
		}
		questionIDs = append(questionIDs, questionID)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE lesson_versions SET status = 'published', published_at = now() WHERE id = $1
	`, versionID); err != nil {
		t.Fatalf("опубликовать нагрузочную версию: %v", err)
	}

	learners := make([]navigationLoadLearner, 0, 10)
	students := NewStudentRepository(pool)
	enrollments := NewEnrollmentRepository(pool)
	for index := 0; index < 10; index++ {
		student, err := students.Create(ctx, CreateStudentParams{
			TelegramID: time.Now().UnixNano() + int64(index),
			DisplayName: fmt.Sprintf("navigation-load-%s-%d", suffix, index+1),
		})
		if err != nil {
			t.Fatalf("создать нагрузочного ученика %d: %v", index+1, err)
		}
		enrollment, err := enrollments.Start(ctx, StartEnrollmentParams{
			StudentID: student.ID, LessonID: lessonID, LessonVersionID: versionID, Mode: "ai",
		})
		if err != nil {
			t.Fatalf("создать прохождение нагрузочного ученика %d: %v", index+1, err)
		}
		if _, err := enrollments.OpenQuestionPosition(ctx, student.ID, enrollment.ID, questionIDs[0]); err != nil {
			t.Fatalf("установить начальный вопрос ученика %d: %v", index+1, err)
		}
		learners = append(learners, navigationLoadLearner{
			studentID: student.ID, enrollmentID: enrollment.ID,
			firstQuestionID: questionIDs[0], secondQuestionID: questionIDs[1],
		})
	}
	return learners
}

func navigationLatencyPercentiles(samples []time.Duration) map[string]float64 {
	if len(samples) == 0 {
		return map[string]float64{}
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	quantile := func(fraction float64) float64 {
		index := int(math.Ceil(fraction*float64(len(sorted)))) - 1
		if index < 0 {
			index = 0
		}
		if index >= len(sorted) {
			index = len(sorted) - 1
		}
		return float64(sorted[index]) / float64(time.Millisecond)
	}
	return map[string]float64{
		"p50": quantile(0.50), "p95": quantile(0.95), "p99": quantile(0.99),
	}
}
