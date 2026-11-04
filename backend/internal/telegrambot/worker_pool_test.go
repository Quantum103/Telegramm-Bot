package telegrambot

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func workerPoolMessageUpdate(updateID, chatID int64) Update {
	return Update{
		UpdateID: updateID,
		Message: &Message{
			Chat: Chat{ID: chatID, Type: "private"},
		},
	}
}

func awaitWorkerResult(t *testing.T, result <-chan bool) bool {
	t.Helper()
	select {
	case processed, ok := <-result:
		if !ok {
			t.Fatal("worker result channel closed without a result")
		}
		return processed
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for update processing")
		return false
	}
}

func newTestWorkerPool(t *testing.T, workers, queueCapacity int, handler func(context.Context, Update) bool) *UpdateWorkerPool {
	t.Helper()
	pool, err := NewUpdateWorkerPool(context.Background(), workers, queueCapacity, handler)
	if err != nil {
		t.Fatalf("NewUpdateWorkerPool() error = %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		pool.Wait()
	})
	return pool
}

func TestUpdateWorkerPoolProcessesSameChatInOrder(t *testing.T) {
	var (
		mu        sync.Mutex
		processed []int64
	)
	pool := newTestWorkerPool(t, 4, 4, func(_ context.Context, update Update) bool {
		mu.Lock()
		processed = append(processed, update.UpdateID)
		mu.Unlock()
		return true
	})

	results := make([]<-chan bool, 0, 3)
	for _, updateID := range []int64{101, 102, 103} {
		result, err := pool.Submit(context.Background(), workerPoolMessageUpdate(updateID, 7001))
		if err != nil {
			t.Fatalf("Submit(%d) error = %v", updateID, err)
		}
		results = append(results, result)
	}
	for _, result := range results {
		if !awaitWorkerResult(t, result) {
			t.Fatal("worker reported an unsuccessful update")
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if want := []int64{101, 102, 103}; !reflect.DeepEqual(processed, want) {
		t.Fatalf("processed updates = %v, want %v", processed, want)
	}
}

func TestUpdateWorkerPoolProcessesDifferentChatsConcurrently(t *testing.T) {
	started := make(chan int64, 2)
	release := make(chan struct{})
	pool := newTestWorkerPool(t, 2, 2, func(_ context.Context, update Update) bool {
		started <- update.UpdateID
		<-release
		return true
	})

	first, err := pool.Submit(context.Background(), workerPoolMessageUpdate(201, 100))
	if err != nil {
		t.Fatalf("Submit(first) error = %v", err)
	}
	second, err := pool.Submit(context.Background(), workerPoolMessageUpdate(202, 101))
	if err != nil {
		t.Fatalf("Submit(second) error = %v", err)
	}

	seen := make(map[int64]bool)
	for range 2 {
		select {
		case updateID := <-started:
			seen[updateID] = true
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("different chats were not processed concurrently")
		}
	}
	close(release)

	if !seen[201] || !seen[202] {
		t.Fatalf("started updates = %v, want both updates", seen)
	}
	if !awaitWorkerResult(t, first) || !awaitWorkerResult(t, second) {
		t.Fatal("worker reported an unsuccessful update")
	}
}

func TestUpdateWorkerPoolAppliesBackpressureWhenQueueIsFull(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	pool := newTestWorkerPool(t, 1, 1, func(_ context.Context, update Update) bool {
		if update.UpdateID == 301 {
			started <- struct{}{}
			<-release
		}
		return true
	})

	first, err := pool.Submit(context.Background(), workerPoolMessageUpdate(301, 9001))
	if err != nil {
		t.Fatalf("Submit(first) error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("first update did not start")
	}

	second, err := pool.Submit(context.Background(), workerPoolMessageUpdate(302, 9001))
	if err != nil {
		close(release)
		t.Fatalf("Submit(second) error = %v", err)
	}

	thirdSubmitted := make(chan struct {
		result <-chan bool
		err    error
	}, 1)
	go func() {
		result, submitErr := pool.Submit(context.Background(), workerPoolMessageUpdate(303, 9001))
		thirdSubmitted <- struct {
			result <-chan bool
			err    error
		}{result: result, err: submitErr}
	}()

	select {
	case <-thirdSubmitted:
		close(release)
		t.Fatal("Submit returned while the worker and its bounded queue were full")
	case <-time.After(30 * time.Millisecond):
	}

	close(release)
	third := <-thirdSubmitted
	if third.err != nil {
		t.Fatalf("Submit(third) error = %v", third.err)
	}
	if !awaitWorkerResult(t, first) || !awaitWorkerResult(t, second) || !awaitWorkerResult(t, third.result) {
		t.Fatal("worker reported an unsuccessful update")
	}
}

func TestUpdateWorkerPoolReturnsProcessingOutcome(t *testing.T) {
	pool := newTestWorkerPool(t, 1, 1, func(context.Context, Update) bool {
		return false
	})

	result, err := pool.Submit(context.Background(), workerPoolMessageUpdate(401, 1))
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if processed := awaitWorkerResult(t, result); processed {
		t.Fatal("worker result = true, want false")
	}
}

func TestUpdateWorkerPoolRejectsInvalidConfiguration(t *testing.T) {
	handler := func(context.Context, Update) bool { return true }
	tests := []struct {
		name          string
		ctx           context.Context
		workers       int
		queueCapacity int
		handler       func(context.Context, Update) bool
	}{
		{name: "nil context", workers: 1, queueCapacity: 1, handler: handler},
		{name: "zero workers", ctx: context.Background(), workers: 0, queueCapacity: 1, handler: handler},
		{name: "negative workers", ctx: context.Background(), workers: -1, queueCapacity: 1, handler: handler},
		{name: "zero queue", ctx: context.Background(), workers: 1, queueCapacity: 0, handler: handler},
		{name: "nil handler", ctx: context.Background(), workers: 1, queueCapacity: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewUpdateWorkerPool(test.ctx, test.workers, test.queueCapacity, test.handler)
			if err == nil {
				t.Fatal("NewUpdateWorkerPool() error = nil, want validation error")
			}
		})
	}
}

func TestUpdateWorkerPoolRejectsAlreadyCancelledSubmission(t *testing.T) {
	pool := newTestWorkerPool(t, 1, 1, func(context.Context, Update) bool {
		return true
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := pool.Submit(ctx, workerPoolMessageUpdate(451, 1))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Submit() error = %v, want %v", err, context.Canceled)
	}
}

func TestUpdateWorkerPoolRejectsSubmissionsAfterClose(t *testing.T) {
	pool, err := NewUpdateWorkerPool(context.Background(), 1, 1, func(context.Context, Update) bool {
		return true
	})
	if err != nil {
		t.Fatalf("NewUpdateWorkerPool() error = %v", err)
	}
	pool.Close()
	pool.Wait()

	_, err = pool.Submit(context.Background(), workerPoolMessageUpdate(501, 1))
	if !errors.Is(err, ErrUpdateWorkerPoolClosed) {
		t.Fatalf("Submit() error = %v, want %v", err, ErrUpdateWorkerPoolClosed)
	}
}

func TestUpdateChatKeyUsesCallbackChatOrActor(t *testing.T) {
	tests := []struct {
		name   string
		update Update
		want   int64
	}{
		{
			name:   "message chat",
			update: Update{Message: &Message{Chat: Chat{ID: -1001}}},
			want:   -1001,
		},
		{
			name: "callback message chat",
			update: Update{CallbackQuery: &CallbackQuery{
				Message: &Message{Chat: Chat{ID: -1002}},
				From:    &User{ID: 77},
			}},
			want: -1002,
		},
		{
			name: "inline callback actor",
			update: Update{CallbackQuery: &CallbackQuery{
				From: &User{ID: 78},
			}},
			want: 78,
		},
		{
			name:   "missing chat",
			update: Update{},
			want:   0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := updateChatKey(test.update); got != test.want {
				t.Fatalf("updateChatKey() = %d, want %d", got, test.want)
			}
		})
	}
}
