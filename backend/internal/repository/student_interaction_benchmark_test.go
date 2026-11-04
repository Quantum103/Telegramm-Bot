package repository

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type studentInteractionSQLTracer struct {
	queries atomic.Int64
}

func (t *studentInteractionSQLTracer) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	_ pgx.TraceQueryStartData,
) context.Context {
	return ctx
}

func (t *studentInteractionSQLTracer) TraceQueryEnd(
	_ context.Context,
	_ *pgx.Conn,
	_ pgx.TraceQueryEndData,
) {
	t.queries.Add(1)
}

type studentInteractionBenchmarkFixture struct {
	tracer    *studentInteractionSQLTracer
	repo      *ConversationRepository
	studentID int64
}

func newStudentInteractionTracedPool(tb testing.TB) (*pgxpool.Pool, *studentInteractionSQLTracer) {
	tb.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		tb.Skip("TEST_DATABASE_URL не задан; SQL measurement требует PostgreSQL")
	}

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		tb.Fatalf("разобрать TEST_DATABASE_URL: %v", err)
	}
	tracer := &studentInteractionSQLTracer{}
	config.ConnConfig.Tracer = tracer
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		tb.Fatalf("создать traced PostgreSQL pool: %v", err)
	}
	tb.Cleanup(pool.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		tb.Fatalf("подключиться к PostgreSQL: %v", err)
	}
	return pool, tracer
}

func newStudentInteractionBenchmarkFixture(tb testing.TB) *studentInteractionBenchmarkFixture {
	tb.Helper()
	pool, tracer := newStudentInteractionTracedPool(tb)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var studentID int64
	telegramID := time.Now().UnixNano()
	if err := pool.QueryRow(ctx, `
		INSERT INTO students (telegram_id, display_name)
		VALUES ($1, $2)
		RETURNING id
	`, telegramID, fmt.Sprintf("student-interaction-benchmark-%d", telegramID)).Scan(&studentID); err != nil {
		tb.Fatalf("создать benchmark ученика: %v", err)
	}
	tracer.queries.Store(0)

	return &studentInteractionBenchmarkFixture{
		tracer: tracer, repo: NewConversationRepository(pool), studentID: studentID,
	}
}

func (f *studentInteractionBenchmarkFixture) leaveWithSeparateModeCalls(ctx context.Context) error {
	if err := f.repo.SetStudentChatMode(ctx, f.studentID, false); err != nil {
		return err
	}
	return f.repo.SetStudentLessonMode(ctx, f.studentID, false)
}

// TestLeaveStudentInteractionUsesOneSQLStatement compares the former two-setter
// exit path with the combined repository operation using pgx query tracing.
func TestLeaveStudentInteractionUsesOneSQLStatement(t *testing.T) {
	setupPool := integrationPool(t)
	pool, tracer := newStudentInteractionTracedPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var studentID int64
	telegramID := time.Now().UnixNano()
	if err := setupPool.QueryRow(ctx, `
		INSERT INTO students (telegram_id, display_name)
		VALUES ($1, $2)
		RETURNING id
	`, telegramID, fmt.Sprintf("student-interaction-sql-count-%d", telegramID)).Scan(&studentID); err != nil {
		t.Fatalf("создать ученика для SQL measurement: %v", err)
	}
	repo := NewConversationRepository(pool)

	if err := repo.SetStudentChatMode(ctx, studentID, true); err != nil {
		t.Fatalf("включить режим перед сравнением: %v", err)
	}
	tracer.queries.Store(0)
	if err := repo.SetStudentChatMode(ctx, studentID, false); err != nil {
		t.Fatalf("выключить chat mode отдельным вызовом: %v", err)
	}
	if err := repo.SetStudentLessonMode(ctx, studentID, false); err != nil {
		t.Fatalf("выключить lesson mode отдельным вызовом: %v", err)
	}
	legacyQueries := tracer.queries.Load()
	if legacyQueries != 3 {
		t.Fatalf("старый путь каталога выполнил %d SQL-команд, ожидалось 3 (2 переключения + пробуждение очереди)", legacyQueries)
	}

	if err := repo.SetStudentChatMode(ctx, studentID, true); err != nil {
		t.Fatalf("повторно включить режим перед объединённым выходом: %v", err)
	}
	tracer.queries.Store(0)
	if err := repo.LeaveStudentInteraction(ctx, studentID); err != nil {
		t.Fatalf("выполнить объединённый выход: %v", err)
	}
	combinedQueries := tracer.queries.Load()
	if combinedQueries != 1 {
		t.Fatalf("объединённый выход выполнил %d SQL-команд, ожидалась 1", combinedQueries)
	}

	t.Logf("SQL-команды на выход в каталог: до=%d, после=%d (сокращение на %.1f%%)",
		legacyQueries, combinedQueries,
		100*(1-float64(combinedQueries)/float64(legacyQueries)))
}

// BenchmarkStudentInteractionExitQueries measures the SQL command count and
// latency of the former two-setter flow versus LeaveStudentInteraction.
func BenchmarkStudentInteractionExitQueries(b *testing.B) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	b.Run("before_separate_setters", func(b *testing.B) {
		fixture := newStudentInteractionBenchmarkFixture(b)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := fixture.leaveWithSeparateModeCalls(ctx); err != nil {
				b.Fatalf("выход через два переключения режимов: %v", err)
			}
		}
		b.StopTimer()
		b.ReportMetric(float64(fixture.tracer.queries.Load())/float64(b.N), "sql/op")
	})

	b.Run("after_combined_operation", func(b *testing.B) {
		fixture := newStudentInteractionBenchmarkFixture(b)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := fixture.repo.LeaveStudentInteraction(ctx, fixture.studentID); err != nil {
				b.Fatalf("объединённый выход ученика: %v", err)
			}
		}
		b.StopTimer()
		b.ReportMetric(float64(fixture.tracer.queries.Load())/float64(b.N), "sql/op")
	})
}
