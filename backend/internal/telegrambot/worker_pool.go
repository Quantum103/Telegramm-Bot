package telegrambot

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var ErrUpdateWorkerPoolClosed = errors.New("telegram update worker pool is closed")

// UpdateWorkerPool processes updates on a fixed number of workers. Updates
// that belong to the same chat are always sent to the same FIFO queue.
type UpdateWorkerPool struct {
	ctx    context.Context
	cancel context.CancelFunc

	queues  []chan updateTask
	handler func(context.Context, Update) bool
	workers sync.WaitGroup

	submitMu sync.RWMutex
	closed   bool
}

type updateTask struct {
	update Update
	result chan bool
}

// NewUpdateWorkerPool starts workers with a bounded queue for each worker.
// The total number of queued updates cannot exceed workers*queueCapacity.
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

	ctx, cancel := context.WithCancel(parent)
	pool := &UpdateWorkerPool{
		ctx:     ctx,
		cancel:  cancel,
		queues:  make([]chan updateTask, workerCount),
		handler: handler,
	}
	for i := range pool.queues {
		pool.queues[i] = make(chan updateTask, queueCapacity)
		pool.workers.Add(1)
		go pool.run(pool.queues[i])
	}
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
	case <-p.ctx.Done():
		return nil, ErrUpdateWorkerPoolClosed
	case queue <- task:
		return result, nil
	}
}

// Close stops accepting submissions and cancels worker contexts. It does not
// wait for handlers; callers can use Wait when they need to join the workers.
func (p *UpdateWorkerPool) Close() {
	p.cancel()
	p.submitMu.Lock()
	p.closed = true
	p.submitMu.Unlock()
}

// Wait blocks until every worker has exited.
func (p *UpdateWorkerPool) Wait() {
	p.workers.Wait()
}

func (p *UpdateWorkerPool) run(queue <-chan updateTask) {
	defer p.workers.Done()
	for {
		if p.ctx.Err() != nil {
			return
		}
		select {
		case <-p.ctx.Done():
			return
		case task := <-queue:
			if p.ctx.Err() != nil {
				return
			}
			task.result <- p.handler(p.ctx, task.update)
			close(task.result)
		}
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
