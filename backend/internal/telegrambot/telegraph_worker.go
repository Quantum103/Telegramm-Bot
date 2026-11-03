package telegrambot

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

const (
	telegraphJobLeaseDuration = 2 * time.Minute
	telegraphJobTimeout       = 60 * time.Second
	telegraphWorkerPollDelay  = time.Second
	telegraphBackfillInterval = 5 * time.Minute
	telegraphRetryBaseDelay   = 5 * time.Second
	telegraphRetryMaxDelay    = 30 * time.Minute
)

type telegraphPreparationQueue interface {
	ClaimTelegraphChapterJob(context.Context, time.Duration) (domain.TelegraphChapterJob, error)
	CompleteTelegraphChapterJob(context.Context, domain.TelegraphChapterJob) error
	RetryTelegraphChapterJob(context.Context, domain.TelegraphChapterJob, time.Duration, error) error
}

type publishedTelegraphChapterEnqueuer interface {
	EnqueuePublishedTelegraphChapters(context.Context) error
}

// TelegraphPreparationWorker prepares full chapter pages outside Telegram update
// handling. PostgreSQL leases allow multiple backend instances to share the queue.
type TelegraphPreparationWorker struct {
	queue       telegraphPreparationQueue
	publisher   ChapterPagePublisher
	logger      *slog.Logger
	concurrency int
}

func NewTelegraphPreparationWorker(
	queue telegraphPreparationQueue,
	publisher ChapterPagePublisher,
	logger *slog.Logger,
	concurrency int,
) *TelegraphPreparationWorker {
	if logger == nil {
		logger = slog.Default()
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 4 {
		concurrency = 4
	}
	return &TelegraphPreparationWorker{
		queue: queue, publisher: publisher, logger: logger, concurrency: concurrency,
	}
}

func (w *TelegraphPreparationWorker) Run(ctx context.Context) error {
	if w == nil || w.queue == nil || w.publisher == nil {
		return errors.New("очередь или публикатор Telegraph не настроены")
	}

	if enqueuer, ok := w.queue.(publishedTelegraphChapterEnqueuer); ok {
		w.enqueuePublished(ctx, enqueuer)
		go w.backfillLoop(ctx, enqueuer)
	}

	w.logger.Info("worker подготовки полных глав запущен",
		"worker_count", w.concurrency,
		"lease_seconds", int(telegraphJobLeaseDuration.Seconds()),
		"job_timeout_seconds", int(telegraphJobTimeout.Seconds()),
	)
	for range w.concurrency {
		go w.runWorker(ctx)
	}

	<-ctx.Done()
	return nil
}

func (w *TelegraphPreparationWorker) backfillLoop(
	ctx context.Context,
	enqueuer publishedTelegraphChapterEnqueuer,
) {
	ticker := time.NewTicker(telegraphBackfillInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.enqueuePublished(ctx, enqueuer)
		}
	}
}

func (w *TelegraphPreparationWorker) enqueuePublished(
	ctx context.Context,
	enqueuer publishedTelegraphChapterEnqueuer,
) {
	enqueueCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := enqueuer.EnqueuePublishedTelegraphChapters(enqueueCtx); err != nil && ctx.Err() == nil {
		w.logger.Warn("не удалось добавить ранее опубликованные главы в очередь Telegraph", "error", err)
	}
}

func (w *TelegraphPreparationWorker) runWorker(ctx context.Context) {
	for ctx.Err() == nil {
		job, err := w.queue.ClaimTelegraphChapterJob(ctx, telegraphJobLeaseDuration)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !errors.Is(err, domain.ErrNotFound) {
				w.logger.Warn("не удалось получить задание из очереди Telegraph", "error", err)
				if !wait(ctx, 3*time.Second) {
					return
				}
				continue
			}
			if !wait(ctx, telegraphWorkerPollDelay) {
				return
			}
			continue
		}
		w.processJob(ctx, job)
	}
}

func (w *TelegraphPreparationWorker) processJob(ctx context.Context, job domain.TelegraphChapterJob) {
	jobCtx, cancel := context.WithTimeout(ctx, telegraphJobTimeout)
	_, publishErr := w.publisher.PublishChapter(jobCtx, ChapterReference{
		BookID: job.BookID,
		Chapter: job.ChapterNumber,
		Translation: job.TranslationCode,
	})
	cancel()

	if ctx.Err() != nil {
		// The lease will become available after expiry when another instance starts.
		return
	}
	if publishErr != nil {
		delay := telegraphRetryDelay(job.Attempts)
		retryCtx, retryCancel := context.WithTimeout(context.Background(), 5*time.Second)
		retryErr := w.queue.RetryTelegraphChapterJob(retryCtx, job, delay, publishErr)
		retryCancel()
		if retryErr != nil {
			w.logger.Error("не удалось вернуть задание Telegraph в очередь",
				"job_id", job.ID, "book_id", job.BookID, "chapter", job.ChapterNumber,
				"publish_error", publishErr, "queue_error", retryErr)
			return
		}
		w.logger.Warn("подготовка полной главы не удалась; назначена повторная попытка",
			"job_id", job.ID, "book_id", job.BookID, "chapter", job.ChapterNumber,
			"attempt", job.Attempts, "retry_after_seconds", int(delay.Seconds()),
			"error", publishErr)
		return
	}

	completeCtx, completeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	completeErr := w.queue.CompleteTelegraphChapterJob(completeCtx, job)
	completeCancel()
	if completeErr != nil {
		w.logger.Error("не удалось подтвердить готовность ссылки Telegraph",
			"job_id", job.ID, "book_id", job.BookID, "chapter", job.ChapterNumber,
			"error", completeErr)
		return
	}
	w.logger.Info("ссылка на полную главу подготовлена",
		"job_id", job.ID, "book_id", job.BookID, "chapter", job.ChapterNumber,
		"attempt", job.Attempts)
}

func telegraphRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := telegraphRetryBaseDelay
	for current := 1; current < attempt && delay < telegraphRetryMaxDelay; current++ {
		delay *= 2
	}
	if delay > telegraphRetryMaxDelay {
		return telegraphRetryMaxDelay
	}
	return delay
}
