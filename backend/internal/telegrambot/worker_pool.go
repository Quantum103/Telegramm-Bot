package telegrambot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	defaultUpdateWorkerCount    = 4
	defaultUpdateQueueCapacity  = 16
	updateWorkerShutdownTimeout = 30 * time.Second
)

var ErrUpdateWorkerPoolClosed = errors.New("telegram update worker pool is closed")

// UpdateWorkerPool processes updates on a fixed number of workers. Updates
// that belong to the same chat are always sent to the same FIFO queue.
type UpdateWorkerPool struct {
	submitContext context.Context
	submitStop    context.Context
	stopSubmit    context.CancelFunc

	workerContext context.Context
	cancelWorkers context.CancelFunc

	queues  []chan updateTask
	handler func(context.Context, Update) bool
	done    chan struct{}

	submitMu sync.RWMutex
	closed   bool
}

type updateTask struct {
	update Update
	result chan bool
}

// NewUpdateWorkerPool starts workers with a bounded queue for each worker.
// The total number of queued updates cannot exceed workers*queueCapacity.
// Parent cancellation stops submissions; accepted work is drained by Close.
func NewUpdateWorkerPool(
	parent context.Context,
	workerCount int,
	queueCapacity int,
	handler func(context.Context, Update) bool,
) (*UpdateWorkerPool, error) {
	if parent == nil {
		return nil, errors.New("worker pool context must not be nil")
	}
	if workerCount < 1 {
		return nil, fmt.Errorf("worker count must be positive")
	}
	if queueCapacity < 1 {
		return nil, fmt.Errorf("queue capacity must be positive")
	}
	if handler == nil {
		return nil, errors.New("worker pool handler must not be nil")
	}

	submitStop, stopSubmit := context.WithCancel(context.Background())
	workerContext, cancelWorkers := context.WithCancel(context.Background())
	pool := &UpdateWorkerPool{
		submitContext: parent,
		submitStop:    submitStop,
		stopSubmit:    stopSubmit,
		workerContext: workerContext,
		cancelWorkers: cancelWorkers,
		queues:        make([]chan updateTask, workerCount),
		handler:       handler,
		done:          make(chan struct{}),
	}

	var workers sync.WaitGroup
	for i := range pool.queues {
		pool.queues[i] = make(chan updateTask, queueCapacity)
		workers.Add(1)
		go func(queue <-chan updateTask) {
			defer workers.Done()
			pool.run(queue)
		}(pool.queues[i])
	}
	go func() {
		workers.Wait()
		close(pool.done)
	}()

	return pool, nil
}

// Submit enqueues an update and returns a channel that receives its processing
// outcome. It blocks when the selected worker queue is full, applying
// backpressure to the Telegram polling loop.
func (p *UpdateWorkerPool) Submit(ctx context.Context, update Update) (<-chan bool, error) {
	if ctx == nil {
		return nil, errors.New("update submission context must not be nil")
	}

	p.submitMu.RLock()
	defer p.submitMu.RUnlock()
	if p.closed {
		return nil, ErrUpdateWorkerPoolClosed
	}

	result := make(chan bool, 1)
	task := updateTask{update: update, result: result}
	queue := p.queues[p.workerIndex(update)]
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.submitContext.Done():
		return nil, p.submitContext.Err()
	case <-p.submitStop.Done():
		return nil, ErrUpdateWorkerPoolClosed
	case queue <- task:
		return result, nil
	}
}

// Close stops accepting new updates and gracefully drains all accepted work.
// Use Shutdown when a bounded wait and forced cancellation are required.
func (p *UpdateWorkerPool) Close() {
	p.stopSubmit()
	p.submitMu.Lock()
	defer p.submitMu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	for _, queue := range p.queues {
		close(queue)
	}
}

// Shutdown closes the pool and waits for accepted work to finish. If the
// shutdown context expires, running handlers receive cancellation.
func (p *UpdateWorkerPool) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("worker pool shutdown context must not be nil")
	}
	p.Close()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		p.cancelWorkers()
		<-p.done
		return ctx.Err()
	}
}

// Wait blocks until every accepted update has been processed or cancelled.
func (p *UpdateWorkerPool) Wait() {
	<-p.done
}

func (p *UpdateWorkerPool) run(queue <-chan updateTask) {
	for task := range queue {
		processed := false
		if p.workerContext.Err() == nil {
			processed = p.handler(p.workerContext, task.update)
		}
		task.result <- processed
		close(task.result)
	}
}

func (p *UpdateWorkerPool) workerIndex(update Update) int {
	return int(uint64(updateChatKey(update)) % uint64(len(p.queues)))
}

func updateChatKey(update Update) int64 {
	if update.Message != nil && update.Message.Chat.ID != 0 {
		return update.Message.Chat.ID
	}
	if update.CallbackQuery != nil {
		if message := update.CallbackQuery.Message; message != nil && message.Chat.ID != 0 {
			return message.Chat.ID
		}
		if user := update.CallbackQuery.From; user != nil && user.ID != 0 {
			// Inline callbacks have no chat message; the actor ID is the
			// most stable available key for serializing that user's updates.
			return user.ID
		}
	}
	// Malformed or unsupported updates are serialized on one shard.
	return 0
}
