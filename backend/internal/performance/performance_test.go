package performance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMeasureAndSQLTracerAggregateWithoutRetainingSQL(t *testing.T) {
	tracker := NewTracker()
	ctx := WithTracker(context.Background(), tracker)

	_, err := Measure(ctx, "learning.question.fetch", func() (int, error) {
		return 0, errors.New("test failure")
	})
	if err == nil {
		t.Fatal("Measure() error = nil, want failure")
	}
	Record(ctx, "learning.question.fetch", 5*time.Millisecond, nil)

	tracer := QueryTracer{}
	queryCtx := tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{
		SQL:  "SELECT password FROM users WHERE id = $1",
		Args: []any{"must-not-be-retained"},
	})
	tracer.TraceQueryEnd(queryCtx, nil, pgx.TraceQueryEndData{})

	failedQueryCtx := tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{
		SQL: "UPDATE accounts SET password = $1 WHERE id = $2",
	})
	tracer.TraceQueryEnd(failedQueryCtx, nil, pgx.TraceQueryEndData{Err: errors.New("database failure")})

	acquireCtx := tracer.TraceAcquireStart(ctx, nil, pgxpool.TraceAcquireStartData{})
	tracer.TraceAcquireEnd(acquireCtx, nil, pgxpool.TraceAcquireEndData{})

	stages := tracker.Stages()
	stage, ok := stages["learning.question.fetch"]
	if !ok {
		t.Fatal("tracked stage is missing")
	}
	if stage.Calls != 2 || stage.Errors != 1 || stage.Successes != 1 {
		t.Fatalf("stage counters = %+v, want 2 calls, 1 success and 1 error", stage)
	}
	acquire := stages["postgres.pool.acquire"]
	if acquire.Calls != 1 || acquire.Errors != 0 {
		t.Fatalf("pool acquire counters = %+v, want one success", acquire)
	}

	sql := tracker.SQL()
	if sql.Count != 2 || sql.Successes != 1 || sql.Errors != 1 ||
		sql.ByCommand["SELECT"] != 1 || sql.ByCommand["UPDATE"] != 1 {
		t.Fatalf("SQL counters = %+v, want one successful SELECT and one failed UPDATE", sql)
	}
	if len(sql.ByCommand) != 2 {
		t.Fatalf("unexpected SQL command labels: %+v", sql.ByCommand)
	}
}

func TestRecordWithoutTrackerIsNoop(t *testing.T) {
	Record(context.Background(), "ignored", time.Second, nil)
}
