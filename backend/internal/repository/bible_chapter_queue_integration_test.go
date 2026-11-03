package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

func TestTelegraphChapterQueueDeduplicatesJobsAndUsesLeaseOwnership(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := NewBibleChapterRepository(pool)
	bookID := 66
	chapterNumber := 10000 + int(time.Now().UnixNano()%20000)
	translation := "synodal"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, `
			DELETE FROM bible_chapter_telegraph_jobs
			WHERE translation_code = $1 AND book_id = $2 AND chapter_number = $3
		`, translation, bookID, chapterNumber); err != nil {
			t.Errorf("cleanup Telegraph chapter job: %v", err)
		}
	})

	if err := repo.EnqueueTelegraphChapter(ctx, translation, bookID, chapterNumber); err != nil {
		t.Fatalf("enqueue chapter: %v", err)
	}
	if err := repo.EnqueueTelegraphChapter(ctx, translation, bookID, chapterNumber); err != nil {
		t.Fatalf("enqueue duplicate chapter: %v", err)
	}

	assertJobCount := func(want int) {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, `
			SELECT count(*)::INTEGER
			FROM bible_chapter_telegraph_jobs
			WHERE translation_code = $1 AND book_id = $2 AND chapter_number = $3
		`, translation, bookID, chapterNumber).Scan(&count); err != nil {
			t.Fatalf("read job count: %v", err)
		}
		if count != want {
			t.Fatalf("stored jobs = %d, want %d", count, want)
		}
	}
	assertJobCount(1)

	// Competing workers must not lease the same ready row.
	start := make(chan struct{})
	type claimResult struct {
		job domain.TelegraphChapterJob
		err error
	}
	results := make(chan claimResult, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			job, err := repo.ClaimTelegraphChapterJob(ctx, time.Minute)
			results <- claimResult{job: job, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	var first domain.TelegraphChapterJob
	successes, empty := 0, 0
	for result := range results {
		switch {
		case result.err == nil:
			successes++
			first = result.job
		case errors.Is(result.err, domain.ErrNotFound):
			empty++
		default:
			t.Fatalf("concurrent claim failed unexpectedly: %v", result.err)
		}
	}
	if successes != 1 || empty != 1 {
		t.Fatalf("claim results: successes=%d empty=%d; want one lease and one empty result", successes, empty)
	}
	if first.Attempts != 1 || first.LeaseToken == "" || first.BookID != bookID || first.ChapterNumber != chapterNumber {
		t.Fatalf("unexpected first lease: %+v", first)
	}

	cause := errors.New("Telegraph temporarily unavailable")
	if err := repo.RetryTelegraphChapterJob(ctx, first, 0, cause); err != nil {
		t.Fatalf("retry first lease: %v", err)
	}
	second, err := repo.ClaimTelegraphChapterJob(ctx, time.Minute)
	if err != nil {
		t.Fatalf("claim retried job: %v", err)
	}
	if second.Attempts != 2 || second.LeaseToken == first.LeaseToken {
		t.Fatalf("retry did not create a new lease: first=%+v second=%+v", first, second)
	}
	if err := repo.CompleteTelegraphChapterJob(ctx, first); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale lease completion error = %v, want ErrConflict", err)
	}
	if err := repo.CompleteTelegraphChapterJob(ctx, second); err != nil {
		t.Fatalf("complete current lease: %v", err)
	}
	assertJobCount(1)

	// A lesson published after completion may schedule the same chapter again,
	// but it reuses the unique row rather than inserting a second job.
	if err := repo.EnqueueTelegraphChapter(ctx, translation, bookID, chapterNumber); err != nil {
		t.Fatalf("re-enqueue completed chapter: %v", err)
	}
	third, err := repo.ClaimTelegraphChapterJob(ctx, time.Minute)
	if err != nil {
		t.Fatalf("claim re-enqueued completed job: %v", err)
	}
	if third.Attempts != 1 {
		t.Fatalf("re-enqueued job attempts = %d, want reset to 1 after the new claim", third.Attempts)
	}
	if err := repo.CompleteTelegraphChapterJob(ctx, third); err != nil {
		t.Fatalf("complete re-enqueued job: %v", err)
	}
	assertJobCount(1)

	_ = fmt.Sprintf("%s", translation) // keep test key explicit in failure traces above
}
