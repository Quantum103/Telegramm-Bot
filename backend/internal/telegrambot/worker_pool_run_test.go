package telegrambot

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

func TestDispatchUpdateBatchAdvancesOffsetOnlyThroughSuccessfulPrefix(t *testing.T) {
	var (
		mu        sync.Mutex
		processed []int64
	)
	pool, err := NewUpdateWorkerPool(context.Background(), 3, 2, func(_ context.Context, update Update) bool {
		if update.UpdateID == 120 {
			time.Sleep(20 * time.Millisecond)
		}
		mu.Lock()
		processed = append(processed, update.UpdateID)
		mu.Unlock()
		return update.UpdateID != 120
	})
	if err != nil {
		t.Fatalf("NewUpdateWorkerPool() error = %v", err)
	}
	defer func() {
		pool.Close()
		pool.Wait()
	}()

	bot := &Bot{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	updates := []Update{
		workerPoolMessageUpdate(130, 3),
		workerPoolMessageUpdate(120, 2),
		workerPoolMessageUpdate(110, 1),
	}

	offset, retry := bot.dispatchUpdateBatch(context.Background(), pool, updates, 100)
	if offset != 111 {
		t.Fatalf("offset = %d, want 111 (must stop before failed update 120)", offset)
	}
	if !retry {
		t.Fatal("retry = false, want true after a failed update")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(processed) != 3 {
		t.Fatalf("processed updates = %v, want all submitted updates to finish", processed)
	}
}

func TestDispatchUpdateBatchAdvancesOffsetAcrossUpdateIDGaps(t *testing.T) {
	pool := newTestWorkerPool(t, 2, 2, func(context.Context, Update) bool {
		return true
	})
	bot := &Bot{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	offset, retry := bot.dispatchUpdateBatch(context.Background(), pool, []Update{
		workerPoolMessageUpdate(24, 4),
		workerPoolMessageUpdate(21, 1),
	}, 10)
	if retry {
		t.Fatal("retry = true, want false")
	}
	if offset != 25 {
		t.Fatalf("offset = %d, want 25", offset)
	}
}

func TestDispatchUpdateBatchAcknowledgesCallbackBeforeBusinessWork(t *testing.T) {
	api := &fakeAPI{}
	const callbackID = "callback-before-business"
	pool, err := NewUpdateWorkerPool(context.Background(), 1, 1, func(context.Context, Update) bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		for _, answered := range api.answered {
			if answered == callbackID {
				return true
			}
		}
		return false
	})
	if err != nil {
		t.Fatalf("NewUpdateWorkerPool() error = %v", err)
	}
	defer func() {
		pool.Close()
		pool.Wait()
	}()

	bot := &Bot{api: api, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	update := Update{
		UpdateID: 77,
		CallbackQuery: &CallbackQuery{
			ID:      callbackID,
			From:    &User{ID: 501},
			Message: &Message{Chat: Chat{ID: 501, Type: "private"}},
			Data:    "lesson:10:100",
		},
	}

	offset, retry := bot.dispatchUpdateBatch(context.Background(), pool, []Update{update}, 0)
	if retry || offset != 78 {
		t.Fatalf("dispatch result = (offset %d, retry %v), want (78, false)", offset, retry)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.answered) != 1 || api.answered[0] != callbackID {
		t.Fatalf("answered callbacks = %v, want exactly %q", api.answered, callbackID)
	}
}

type oneBatchTelegramAPI struct {
	*fakeAPI

	mu      sync.Mutex
	served  bool
	updates []Update
}

func (a *oneBatchTelegramAPI) GetUpdates(ctx context.Context, _ int64, _ int) ([]Update, error) {
	a.mu.Lock()
	if !a.served {
		a.served = true
		updates := append([]Update(nil), a.updates...)
		a.mu.Unlock()
		return updates, nil
	}
	a.mu.Unlock()

	<-ctx.Done()
	return nil, ctx.Err()
}

type blockingStudentRegistrar struct {
	next    StudentRegistrar
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingStudentRegistrar) EnsureByTelegramID(ctx context.Context, params domain.EnsureStudentParams) (domain.Student, error) {
	r.once.Do(func() { close(r.started) })
	select {
	case <-r.release:
		return r.next.EnsureByTelegramID(ctx, params)
	case <-ctx.Done():
		return domain.Student{}, ctx.Err()
	}
}

func TestBotRunDrainsAcceptedUpdateDuringGracefulShutdown(t *testing.T) {
	api := &oneBatchTelegramAPI{
		fakeAPI: &fakeAPI{},
		updates: []Update{testUpdate(1, "/start", testUser())},
	}
	catalog, learning := fixture()
	registrar := &blockingStudentRegistrar{
		next:    &fakeStudents{},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	bot := NewBot(api, registrar, catalog, learning, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bot.Run(ctx)
	}()

	select {
	case <-registrar.started:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("update handler did not start")
	}

	cancel()
	select {
	case err := <-done:
		t.Fatalf("Run returned before accepted update drained: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	close(registrar.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned an error during graceful shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not finish after accepted update was released")
	}
}
