package performance

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Tracker собирает метрики одного Telegram update. Он не хранит SQL или аргументы запросов.
type Tracker struct {
	mu            sync.Mutex
	stages        map[string]*stageCounters
	sqlCount      int64
	sqlDuration   time.Duration
	sqlErrors     int64
	sqlByCommand  map[string]int64
}

type stageCounters struct {
	calls    int64
	errors   int64
	total    time.Duration
	max      time.Duration
	results  map[string]int64
}

type contextKey struct{}

type queryStartKey struct{}

type acquireStartKey struct{}

type queryStart struct {
	started time.Time
	command string
}

// StageSnapshot содержит агрегированные метрики одного этапа.
type StageSnapshot struct {
	Calls       int64            `json:"calls"`
	Successes   int64            `json:"successes"`
	Errors      int64            `json:"errors"`
	DurationMS  float64          `json:"duration_ms"`
	MaxMS       float64          `json:"max_ms"`
	Results     map[string]int64 `json:"results"`
}

// SQLSnapshot содержит только счётчики и суммарное время SQL, без текста запросов.
type SQLSnapshot struct {
	Count       int64            `json:"count"`
	Successes   int64            `json:"successes"`
	Errors      int64            `json:"errors"`
	DurationMS  float64          `json:"duration_ms"`
	ByCommand   map[string]int64 `json:"by_command"`
}

func NewTracker() *Tracker {
	return &Tracker{
		stages:       make(map[string]*stageCounters),
		sqlByCommand: make(map[string]int64),
	}
}

func WithTracker(ctx context.Context, tracker *Tracker) context.Context {
	if tracker == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, tracker)
}

func FromContext(ctx context.Context) *Tracker {
	tracker, _ := ctx.Value(contextKey{}).(*Tracker)
	return tracker
}

// Record добавляет длительность этапа. В логи попадает только имя операции и агрегаты.
func Record(ctx context.Context, operation string, duration time.Duration, err error) {
	tracker := FromContext(ctx)
	if tracker == nil || operation == "" {
		return
	}
	outcome := "success"
	if err != nil {
		outcome = "error"
	}
	RecordOutcome(ctx, operation, duration, outcome, err)
}

// RecordOutcome records a named non-error outcome such as a cache hit or miss.
func RecordOutcome(ctx context.Context, operation string, duration time.Duration, outcome string, err error) {
	tracker := FromContext(ctx)
	if tracker == nil || operation == "" {
		return
	}
	if outcome == "" {
		outcome = "unknown"
	}
	tracker.recordStage(operation, duration, outcome, err)
}

func (t *Tracker) recordStage(operation string, duration time.Duration, outcome string, err error) {
	if duration < 0 {
		duration = 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	counter := t.stages[operation]
	if counter == nil {
		counter = &stageCounters{results: make(map[string]int64)}
		t.stages[operation] = counter
	}
	counter.calls++
	counter.results[outcome]++
	counter.total += duration
	if duration > counter.max {
		counter.max = duration
	}
	if err != nil {
		counter.errors++
	}
}

// Measure выполняет операцию и записывает её длительность, не меняя возвращаемое значение.
func Measure[T any](ctx context.Context, operation string, fn func() (T, error)) (T, error) {
	started := time.Now()
	value, err := fn()
	Record(ctx, operation, time.Since(started), err)
	return value, err
}

// MeasureErr — вариант Measure для операций, возвращающих только ошибку.
func MeasureErr(ctx context.Context, operation string, fn func() error) error {
	started := time.Now()
	err := fn()
	Record(ctx, operation, time.Since(started), err)
	return err
}

func (t *Tracker) recordSQL(command string, duration time.Duration, err error) {
	if duration < 0 {
		duration = 0
	}
	if command == "" {
		command = "OTHER"
	}
	t.mu.Lock()
	t.sqlCount++
	t.sqlDuration += duration
	if err != nil {
		t.sqlErrors++
	}
	t.sqlByCommand[command]++
	t.mu.Unlock()
}

// Stages возвращает независимую копию метрик этапов для безопасного логирования.
func (t *Tracker) Stages() map[string]StageSnapshot {
	result := make(map[string]StageSnapshot)
	if t == nil {
		return result
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for name, counter := range t.stages {
		outcomes := make(map[string]int64, len(counter.results))
		for outcome, count := range counter.results {
			outcomes[outcome] = count
		}
		result[name] = StageSnapshot{
			Calls: counter.calls, Successes: counter.calls - counter.errors, Errors: counter.errors,
			DurationMS: float64(counter.total) / float64(time.Millisecond),
			MaxMS: float64(counter.max) / float64(time.Millisecond),
			Results: outcomes,
		}
	}
	return result
}

func (t *Tracker) SQL() SQLSnapshot {
	result := SQLSnapshot{ByCommand: make(map[string]int64)}
	if t == nil {
		return result
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	result.Count = t.sqlCount
	result.Errors = t.sqlErrors
	result.Successes = t.sqlCount - t.sqlErrors
	result.DurationMS = float64(t.sqlDuration) / float64(time.Millisecond)
	for command, count := range t.sqlByCommand {
		result.ByCommand[command] = count
	}
	return result
}

// QueryTracer считает запросы, выполняемые через pgx. Текст SQL и аргументы не сохраняются.
type QueryTracer struct{}

func (QueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if FromContext(ctx) == nil {
		return ctx
	}
	return context.WithValue(ctx, queryStartKey{}, queryStart{started: time.Now(), command: sqlCommand(data.SQL)})
}

func (QueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	tracker := FromContext(ctx)
	if tracker == nil {
		return
	}
	started, ok := ctx.Value(queryStartKey{}).(queryStart)
	if !ok {
		return
	}
	tracker.recordSQL(started.command, time.Since(started.started), data.Err)
}

// TraceAcquireStart/End measure waiting for a PostgreSQL connection separately from SQL execution.
func (QueryTracer) TraceAcquireStart(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireStartData) context.Context {
	if FromContext(ctx) == nil {
		return ctx
	}
	return context.WithValue(ctx, acquireStartKey{}, time.Now())
}

func (QueryTracer) TraceAcquireEnd(ctx context.Context, _ *pgxpool.Pool, data pgxpool.TraceAcquireEndData) {
	tracker := FromContext(ctx)
	if tracker == nil {
		return
	}
	started, ok := ctx.Value(acquireStartKey{}).(time.Time)
	if !ok {
		return
	}
	outcome := "success"
	if data.Err != nil {
		outcome = "error"
	}
	tracker.recordStage("postgres.pool.acquire", time.Since(started), outcome, data.Err)
}

func sqlCommand(sql string) string {
	fields := strings.Fields(sql)
	if len(fields) == 0 {
		return "OTHER"
	}
	command := strings.ToUpper(strings.Trim(fields[0], ";"))
	switch command {
	case "SELECT", "INSERT", "UPDATE", "DELETE", "WITH", "BEGIN", "COMMIT", "ROLLBACK":
		return command
	default:
		return "OTHER"
	}
}
