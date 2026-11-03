package telegrambot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type telegraphQueueTestJob struct {
	job       domain.TelegraphChapterJob
	status    string
	available time.Time
}

type fakeTelegraphPreparationQueue struct {
	mu            sync.Mutex
	jobs          map[string]*telegraphQueueTestJob
	nextID        int64
	tokenSequence int64
	retryDelays   []time.Duration
	backfillCalls int
}

func newFakeTelegraphPreparationQueue() *fakeTelegraphPreparationQueue {
	return &fakeTelegraphPreparationQueue{jobs: make(map[string]*telegraphQueueTestJob)}
}

func telegraphQueueTestKey(translation string, bookID, chapter int) string {
	return fmt.Sprintf("%s:%d:%d", translation, bookID, chapter)
}

func (q *fakeTelegraphPreparationQueue) enqueue(translation string, bookID, chapter int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	key := telegraphQueueTestKey(translation, bookID, chapter)
	if q.jobs[key] != nil {
		return
	}
	q.nextID++
	q.jobs[key] = &telegraphQueueTestJob{
		job: domain.TelegraphChapterJob{
			ID: q.nextID, TranslationCode: translation, BookID: bookID, ChapterNumber: chapter,
		},
		status: "queued", available: time.Now(),
	}
}

func (q *fakeTelegraphPreparationQueue) ClaimTelegraphChapterJob(
	_ context.Context,
	leaseDuration time.Duration,
) (domain.TelegraphChapterJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	var selected *telegraphQueueTestJob
	for _, candidate := range q.jobs {
		available := candidate.status == "queued" && !candidate.available.After(now)
		expired := candidate.status == "processing" && !candidate.available.Add(leaseDuration).After(now)
		if available || expired {
			if selected == nil || candidate.job.ID < selected.job.ID {
				selected = candidate
			}
		}
	}
	if selected == nil {
		return domain.TelegraphChapterJob{}, domain.ErrNotFound
	}
	q.tokenSequence++
	selected.job.Attempts++
	selected.job.LeaseToken = fmt.Sprintf("lease-%d", q.tokenSequence)
	selected.status = "processing"
	selected.available = now
	return selected.job, nil
}

func (q *fakeTelegraphPreparationQueue) CompleteTelegraphChapterJob(
	_ context.Context,
	job domain.TelegraphChapterJob,
) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, stored := range q.jobs {
		if stored.job.ID != job.ID {
			continue
		}
		if stored.status != "processing" || stored.job.LeaseToken != job.LeaseToken {
			return domain.ErrConflict
		}
		stored.status = "completed"
		return nil
	}
	return domain.ErrNotFound
}

func (q *fakeTelegraphPreparationQueue) RetryTelegraphChapterJob(
	_ context.Context,
	job domain.TelegraphChapterJob,
	delay time.Duration,
	cause error,
) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, stored := range q.jobs {
		if stored.job.ID != job.ID {
			continue
		}
		if stored.status != "processing" || stored.job.LeaseToken != job.LeaseToken {
			return domain.ErrConflict
		}
		stored.status = "queued"
		// The delay is asserted separately; make the job due immediately in this fake.
		stored.available = time.Time{}
		q.retryDelays = append(q.retryDelays, delay)
		return nil
	}
	return domain.ErrNotFound
}

func (q *fakeTelegraphPreparationQueue) EnqueuePublishedTelegraphChapters(context.Context) error {
	q.mu.Lock()
	q.backfillCalls++
	q.mu.Unlock()
	return nil
}

func (q *fakeTelegraphPreparationQueue) snapshot() (total, completed, queued int, attemptsByChapter map[int]int, retries []time.Duration, backfills int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	attemptsByChapter = make(map[int]int)
	retries = append([]time.Duration(nil), q.retryDelays...)
	for _, stored := range q.jobs {
		total++
		attemptsByChapter[stored.job.ChapterNumber] = stored.job.Attempts
		switch stored.status {
		case "completed":
			completed++
		case "queued":
			queued++
		}
	}
	return total, completed, queued, attemptsByChapter, retries, q.backfillCalls
}

type fakeTelegraphJobPublisher struct {
	active       atomic.Int32
	maxActive    atomic.Int32
	calls        atomic.Int32
	firstTwo     atomic.Int32
	ready        chan struct{}
	readyOnce    sync.Once
	mu           sync.Mutex
	attempts     map[int]int
	failFirstFor map[int]bool
}

func newFakeTelegraphJobPublisher(failFirstFor ...int) *fakeTelegraphJobPublisher {
	failures := make(map[int]bool, len(failFirstFor))
	for _, chapter := range failFirstFor {
		failures[chapter] = true
	}
	return &fakeTelegraphJobPublisher{
		attempts: make(map[int]int), failFirstFor: failures, ready: make(chan struct{}),
	}
}

func (p *fakeTelegraphJobPublisher) PublishChapter(_ context.Context, reference ChapterReference) (string, error) {
	active := p.active.Add(1)
	defer p.active.Add(-1)
	p.calls.Add(1)
	for {
		maximum := p.maxActive.Load()
		if active <= maximum || p.maxActive.CompareAndSwap(maximum, active) {
			break
		}
	}
	if p.firstTwo.Add(1) == 2 {
		p.readyOnce.Do(func() { close(p.ready) })
	}
	select {
	case <-p.ready:
	case <-time.After(250 * time.Millisecond):
	}

	p.mu.Lock()
	p.attempts[reference.Chapter]++
	attempt := p.attempts[reference.Chapter]
	shouldFail := p.failFirstFor[reference.Chapter] && attempt == 1
	p.mu.Unlock()

	time.Sleep(25 * time.Millisecond)
	if shouldFail {
		return "", errors.New("внешний источник временно недоступен")
	}
	return fmt.Sprintf("https://telegra.ph/chapter-%d", reference.Chapter), nil
}

func TestTelegraphPreparationWorkerDeduplicatesRetriesAndLimitsConcurrency(t *testing.T) {
	queue := newFakeTelegraphPreparationQueue()
	for chapter := 1; chapter <= 6; chapter++ {
		queue.enqueue("synodal", 40, chapter)
	}
	queue.enqueue("synodal", 40, 3)
	queue.enqueue("synodal", 40, 3)
	if total, _, _, _, _, _ := queue.snapshot(); total != 6 {
		t.Fatalf("unique queued jobs = %d, want 6 after duplicate enqueues", total)
	}

	publisher := newFakeTelegraphJobPublisher(2)
	worker := NewTelegraphPreparationWorker(queue, publisher, nil, 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()

	deadline := time.Now().Add(4 * time.Second)
	for {
		_, completed, _, _, _, _ := queue.snapshot()
		if completed == 6 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			total, completed, queued, attempts, retries, backfills := queue.snapshot()
			t.Fatalf("worker did not finish: total=%d completed=%d queued=%d attempts=%v retries=%v backfills=%d",
				total, completed, queued, attempts, retries, backfills)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("worker.Run() error = %v", err)
	}

	total, completed, queued, attempts, retries, backfills := queue.snapshot()
	if total != 6 || completed != 6 || queued != 0 {
		t.Fatalf("queue state: total=%d completed=%d queued=%d; want 6, 6, 0", total, completed, queued)
	}
	if attempts[2] != 2 || publisher.calls.Load() != 7 {
		t.Fatalf("failed job was not retried once: attempts=%v publish calls=%d", attempts, publisher.calls.Load())
	}
	if len(retries) != 1 || retries[0] != telegraphRetryBaseDelay {
		t.Fatalf("retry delays = %v, want one delay of %s", retries, telegraphRetryBaseDelay)
	}
	if publisher.maxActive.Load() > 2 || publisher.maxActive.Load() < 2 {
		t.Fatalf("maximum concurrent publishers = %d, want exactly 2", publisher.maxActive.Load())
	}
	if backfills == 0 {
		t.Fatal("worker did not run the published-lesson queue backfill")
	}
}

func TestTelegraphRetryDelayUsesCappedExponentialBackoff(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 0, want: 5 * time.Second},
		{attempt: 1, want: 5 * time.Second},
		{attempt: 2, want: 10 * time.Second},
		{attempt: 3, want: 20 * time.Second},
		{attempt: 4, want: 40 * time.Second},
		{attempt: 20, want: telegraphRetryMaxDelay},
	}
	for _, test := range tests {
		if got := telegraphRetryDelay(test.attempt); got != test.want {
			t.Errorf("telegraphRetryDelay(%d) = %s, want %s", test.attempt, got, test.want)
		}
	}
}
