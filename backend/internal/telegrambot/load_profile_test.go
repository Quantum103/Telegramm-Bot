package telegrambot

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTelegramAPILoadProfiles(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		time.Sleep(time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()

	transport := &http.Transport{MaxIdleConns: 32, MaxIdleConnsPerHost: 32}
	defer transport.CloseIdleConnections()
	client := newHTTPClient("load-test-token", server.URL, &http.Client{Transport: transport})

	const operationsPerStudent = 100
	for _, students := range []int{1, 5, 10} {
		results := make([][]time.Duration, students)
		errorsByStudent := make(chan error, students)
		var workers sync.WaitGroup
		workers.Add(students)
		startedAt := time.Now()
		for index := 0; index < students; index++ {
			index := index
			go func() {
				defer workers.Done()
				local := make([]time.Duration, 0, operationsPerStudent)
				for operation := 0; operation < operationsPerStudent; operation++ {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					requestStartedAt := time.Now()
					err := client.SendMessage(ctx, int64(1000+index), "load test")
					local = append(local, time.Since(requestStartedAt))
					cancel()
					if err != nil {
						errorsByStudent <- fmt.Errorf("ученик %d, запрос %d: %w", index+1, operation+1, err)
						break
					}
				}
				results[index] = local
			}()
		}
		workers.Wait()
		elapsed := time.Since(startedAt)

		select {
		case loadErr := <-errorsByStudent:
			t.Fatalf("Telegram API профиль для %d учеников завершился с ошибкой: %v", students, loadErr)
		default:
		}

		latencies := make([]time.Duration, 0, students*operationsPerStudent)
		for _, values := range results {
			latencies = append(latencies, values...)
		}
		if len(latencies) != students*operationsPerStudent {
			t.Fatalf("Telegram API профиль %d учеников: выполнено %d из %d запросов",
				students, len(latencies), students*operationsPerStudent)
		}
		report := map[string]any{
			"students":                       students,
			"requests":                       len(latencies),
			"elapsed_ms":                     float64(elapsed) / float64(time.Millisecond),
			"throughput_requests_per_second": float64(len(latencies)) / elapsed.Seconds(),
			"latency_ms":                     telegramLatencyPercentiles(latencies),
			"fake_api_requests_total":        requests.Load(),
		}
		encoded, err := json.Marshal(report)
		if err != nil {
			t.Fatalf("сериализовать Telegram API профиль: %v", err)
		}
		t.Logf("TELEGRAM_API_LOAD_PROFILE %s", encoded)
	}
}

func TestTelegramHTTPClientHandlesRateLimitServerFailureAndTimeout(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		delay    time.Duration
		timeout  time.Duration
		wantKind TelegramErrorKind
	}{
		{
			name: "rate limited", status: http.StatusTooManyRequests,
			body:     `{"ok":false,"error_code":429,"description":"Too Many Requests"}`,
			wantKind: TelegramErrorRateLimited,
		},
		{
			name: "server failure", status: http.StatusInternalServerError,
			body:     `{"ok":false,"error_code":500,"description":"Internal Server Error"}`,
			wantKind: TelegramErrorServerFailure,
		},
		{
			name: "request timeout", delay: 100 * time.Millisecond, timeout: 10 * time.Millisecond,
			wantKind: TelegramErrorTimeout,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.delay > 0 {
					select {
					case <-time.After(test.delay):
					case <-r.Context().Done():
						return
					}
				}
				w.Header().Set("Content-Type", "application/json")
				if test.status != 0 {
					w.WriteHeader(test.status)
				}
				if test.body != "" {
					_, _ = w.Write([]byte(test.body))
				}
			}))
			defer server.Close()

			client := newHTTPClient("failure-test-token", server.URL, server.Client())
			ctx := context.Background()
			cancel := func() {}
			if test.timeout > 0 {
				ctx, cancel = context.WithTimeout(ctx, test.timeout)
			}
			defer cancel()

			_, err := client.GetMe(ctx)
			if err == nil {
				t.Fatal("ожидалась ошибка Telegram API")
			}
			if got := classifyTelegramAPIError(err); got != test.wantKind {
				t.Fatalf("classifyTelegramAPIError() = %q, ожидалось %q; error=%v", got, test.wantKind, err)
			}
		})
	}
}

func telegramLatencyPercentiles(samples []time.Duration) map[string]float64 {
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
	return map[string]float64{"p50": quantile(0.50), "p95": quantile(0.95), "p99": quantile(0.99)}
}

func TestUpdateWorkerPoolLoadProfiles(t *testing.T) {
	const operationsPerStudent = 100
	for _, students := range []int{1, 5, 10} {
		pool, err := NewUpdateWorkerPool(context.Background(), 4, 16, func(ctx context.Context, _ Update) bool {
			timer := time.NewTimer(time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return false
			case <-timer.C:
				return true
			}
		})
		if err != nil {
			t.Fatalf("создать worker pool для %d учеников: %v", students, err)
		}

		startGate := make(chan struct{})
		results := make([][]time.Duration, students)
		errorsByStudent := make(chan error, students)
		var workers sync.WaitGroup
		workers.Add(students)
		startedAt := time.Now()
		for index := 0; index < students; index++ {
			index := index
			go func() {
				defer workers.Done()
				local := make([]time.Duration, 0, operationsPerStudent)
				chatID := int64(2000 + index)
				<-startGate
				for operation := 0; operation < operationsPerStudent; operation++ {
					update := Update{
						UpdateID: int64(100000 + index*operationsPerStudent + operation),
						Message:  &Message{Chat: Chat{ID: chatID, Type: "private"}},
					}
					requestStartedAt := time.Now()
					result, submitErr := pool.Submit(context.Background(), update)
					if submitErr != nil {
						errorsByStudent <- fmt.Errorf("ученик %d, update %d: Submit: %w", index+1, operation+1, submitErr)
						break
					}
					select {
					case processed, ok := <-result:
						local = append(local, time.Since(requestStartedAt))
						if !ok || !processed {
							errorsByStudent <- fmt.Errorf("ученик %d, update %d: обработчик не подтвердил update", index+1, operation+1)
							results[index] = local
							return
						}
					case <-time.After(5 * time.Second):
						errorsByStudent <- fmt.Errorf("ученик %d, update %d: timeout ожидания worker pool", index+1, operation+1)
						results[index] = local
						return
					}
				}
				results[index] = local
			}()
		}
		close(startGate)
		workers.Wait()
		elapsed := time.Since(startedAt)
		pool.Close()
		pool.Wait()

		select {
		case loadErr := <-errorsByStudent:
			t.Fatalf("worker pool профиль %d учеников завершился с ошибкой: %v", students, loadErr)
		default:
		}

		latencies := make([]time.Duration, 0, students*operationsPerStudent)
		for _, values := range results {
			latencies = append(latencies, values...)
		}
		if len(latencies) != students*operationsPerStudent {
			t.Fatalf("worker pool профиль %d учеников: завершено %d из %d updates",
				students, len(latencies), students*operationsPerStudent)
		}
		report := map[string]any{
			"students":                      students,
			"workers":                       4,
			"queue_capacity_per_worker":     16,
			"updates":                       len(latencies),
			"simulated_handler_work_ms":     1,
			"elapsed_ms":                    float64(elapsed) / float64(time.Millisecond),
			"throughput_updates_per_second": float64(len(latencies)) / elapsed.Seconds(),
			"queue_and_handler_latency_ms":  telegramLatencyPercentiles(latencies),
		}
		encoded, err := json.Marshal(report)
		if err != nil {
			t.Fatalf("сериализовать worker pool профиль: %v", err)
		}
		t.Logf("TELEGRAM_WORKER_POOL_LOAD_PROFILE %s", encoded)
	}
}
