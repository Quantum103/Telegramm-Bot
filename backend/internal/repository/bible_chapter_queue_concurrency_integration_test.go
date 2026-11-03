package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

func TestTelegraphChapterQueueEnforcesGlobalActiveJobLimit(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := NewBibleChapterRepository(pool)
	const translation = "synodal"
	const bookID = 65
	baseChapter := 20000 + int(time.Now().UnixNano()%1000)
	for chapter := baseChapter; chapter < baseChapter+3; chapter++ {
		if err := repo.EnqueueTelegraphChapter(ctx, translation, bookID, chapter); err != nil {
			t.Fatalf("enqueue chapter %d: %v", chapter, err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, `
			DELETE FROM bible_chapter_telegraph_jobs
			WHERE translation_code = $1 AND book_id = $2 AND chapter_number BETWEEN $3 AND $4
		`, translation, bookID, baseChapter, baseChapter+2); err != nil {
			t.Errorf("cleanup active Telegraph jobs: %v", err)
		}
	})

	first, err := repo.ClaimTelegraphChapterJob(ctx, time.Minute)
	if err != nil {
		t.Fatalf("claim first job: %v", err)
	}
	second, err := repo.ClaimTelegraphChapterJob(ctx, time.Minute)
	if err != nil {
		t.Fatalf("claim second job: %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("two workers leased the same job %d", first.ID)
	}
	_, err = repo.ClaimTelegraphChapterJob(ctx, time.Minute)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("third claim error = %v, want ErrNotFound at global concurrency limit", err)
	}

	if err := repo.CompleteTelegraphChapterJob(ctx, first); err != nil {
		t.Fatalf("complete first job: %v", err)
	}
	third, err := repo.ClaimTelegraphChapterJob(ctx, time.Minute)
	if err != nil {
		t.Fatalf("claim third job after freeing one slot: %v", err)
	}
	if third.ID == first.ID || third.ID == second.ID {
		t.Fatalf("third job id %d duplicates an already leased job", third.ID)
	}
}
