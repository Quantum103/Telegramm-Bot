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
			"students": students,
			"requests": len(latencies),
			"elapsed_ms": float64(elapsed) / float64(time.Millisecond),
			"throughput_requests_per_second": float64(len(latencies)) / elapsed.Seconds(),
			"latency_ms": telegramLatencyPercentiles(latencies),
			"fake_api_requests_total": requests.Load(),
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
		name       string
		status     int
		body       string
		delay      time.Duration
		timeout    time.Duration
		wantKind   TelegramErrorKind
	}{
		{
			name: "rate limited", status: http.StatusTooManyRequests,
			body: `{"ok":false,"error_code":429,"description":"Too Many Requests"}`,
			wantKind: TelegramErrorRateLimited,
		},
		{
			name: "server failure", status: http.StatusInternalServerError,
			body: `{"ok":false,"error_code":500,"description":"Internal Server Error"}`,
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
