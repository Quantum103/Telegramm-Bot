package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/service"
)

func TestPublishingLessonVersionEnqueuesSynodalBibleChapters(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repository := NewCatalogRepository(pool)
	authoring := service.NewAuthoringService(repository)
	prefix := fmt.Sprintf("telegraph-queue-%d", time.Now().UnixNano())
	course, err := authoring.CreateDraftCourse(ctx, domain.CreateCourseParams{
		Slug: prefix, Title: "Тест очереди Telegraph",
	})
	if err != nil {
		t.Fatalf("create draft course: %v", err)
	}
	lesson, err := authoring.CreateDraftLesson(ctx, domain.CreateLessonParams{
		CourseID: course.ID, Title: "Урок со ссылкой на главу",
	})
	if err != nil {
		t.Fatalf("create draft lesson: %v", err)
	}
	chapterNumber := 10000 + int(time.Now().UnixNano()%20000)
	if _, err := authoring.CreateQuestion(ctx, domain.CreateQuestionParams{
		LessonVersionID: lesson.LessonVersionID,
		QuestionNumber:  1,
		Prompt:          "Почему это важно?",
		ExpectedAnswer:  "Потому что так написано.",
		AnswerType:      "text",
		BiblePassages: []domain.BiblePassageInput{{
			BookName: "Матфея", Chapter: chapterNumber, StartVerse: 1, EndVerse: 2,
			TranslationName: stringPtr("Синодальный перевод"),
			PassageText:     stringPtr("Тестовый отрывок."),
		}},
	}); err != nil {
		t.Fatalf("create question with Bible passage: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, `
			DELETE FROM bible_chapter_telegraph_jobs
			WHERE translation_code = 'synodal' AND book_id = 40 AND chapter_number = $1
		`, chapterNumber); err != nil {
			t.Errorf("cleanup Telegraph job: %v", err)
		}
	})

	if err := authoring.PublishLessonVersion(ctx, lesson.LessonVersionID, 0); err != nil {
		t.Fatalf("publish lesson version: %v", err)
	}

	var status string
	var attempts int
	if err := pool.QueryRow(ctx, `
		SELECT status, attempts
		FROM bible_chapter_telegraph_jobs
		WHERE translation_code = 'synodal' AND book_id = 40 AND chapter_number = $1
	`, chapterNumber).Scan(&status, &attempts); err != nil {
		t.Fatalf("read queued Telegraph chapter: %v", err)
	}
	if status != "queued" || attempts != 0 {
		t.Fatalf("new job state = %q attempts=%d, want queued and 0", status, attempts)
	}

	// Simulate a pre-existing published lesson whose queue row was not present
	// when the async preparation worker was introduced.
	if _, err := pool.Exec(ctx, `
		DELETE FROM bible_chapter_telegraph_jobs
		WHERE translation_code = 'synodal' AND book_id = 40 AND chapter_number = $1
	`, chapterNumber); err != nil {
		t.Fatalf("remove job before backfill test: %v", err)
	}
	if err := repository.EnqueuePublishedTelegraphChapters(ctx); err != nil {
		t.Fatalf("backfill published lesson chapters: %v", err)
	}
	if err := repository.EnqueuePublishedTelegraphChapters(ctx); err != nil {
		t.Fatalf("repeat backfill of published lesson chapters: %v", err)
	}
	var queuedJobs int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::INTEGER
		FROM bible_chapter_telegraph_jobs
		WHERE translation_code = 'synodal' AND book_id = 40 AND chapter_number = $1
	`, chapterNumber).Scan(&queuedJobs); err != nil {
		t.Fatalf("count backfilled Telegraph jobs: %v", err)
	}
	if queuedJobs != 1 {
		t.Fatalf("backfill jobs = %d, want exactly 1 after repeated runs", queuedJobs)
	}
}
