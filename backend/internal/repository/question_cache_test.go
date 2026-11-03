package repository

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

func TestPublishedQuestionCacheExpiresAndReturnsDeepCopies(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	cache := newPublishedQuestionCacheWithConfig(4, 1<<20, time.Minute, func() time.Time { return now })
	explanation := "Пояснение"
	note := "Примечание"
	translation := "Синодальный"
	passageText := "Библейский текст"
	sourceNote := "Источник"
	cache.Set(7, []domain.LessonQuestion{{
		ID: 1, LessonVersionID: 7, Prompt: "Вопрос", ExpectedExplanation: &explanation, Note: &note,
		Criteria: []domain.QuestionCriterion{{Key: "main", Description: "Основное", Weight: 1}},
		Options: []domain.QuestionOption{{ID: 10, Key: "a", Text: "Ответ"}},
		CommonMistakes: []domain.CommonMistake{{Text: "Ошибка", Explanation: "Причина"}},
		BiblePassages: []domain.BiblePassage{{
			BookName: "Бытие", TranslationName: &translation, PassageText: &passageText, SourceNote: &sourceNote,
		}},
	}})

	first, ok := cache.Get(7)
	if !ok || len(first) != 1 {
		t.Fatalf("cache hit = %v, questions = %d", ok, len(first))
	}
	*first[0].ExpectedExplanation = "Изменено извне"
	*first[0].Note = "Изменено извне"
	first[0].Criteria[0].Description = "Изменено извне"
	first[0].Options[0].Text = "Изменено извне"
	first[0].CommonMistakes[0].Explanation = "Изменено извне"
	*first[0].BiblePassages[0].TranslationName = "Изменено извне"
	*first[0].BiblePassages[0].PassageText = "Изменено извне"
	*first[0].BiblePassages[0].SourceNote = "Изменено извне"

	second, ok := cache.Get(7)
	if !ok {
		t.Fatal("expected cached question after caller mutation")
	}
	if *second[0].ExpectedExplanation != explanation || *second[0].Note != note ||
		second[0].Criteria[0].Description != "Основное" ||
		second[0].Options[0].Text != "Ответ" ||
		second[0].CommonMistakes[0].Explanation != "Причина" ||
		*second[0].BiblePassages[0].TranslationName != translation ||
		*second[0].BiblePassages[0].PassageText != passageText ||
		*second[0].BiblePassages[0].SourceNote != sourceNote {
		t.Fatalf("cache returned mutable shared content: %+v", second[0])
	}

	now = now.Add(61 * time.Second)
	if _, ok := cache.Get(7); ok {
		t.Fatal("expired cache entry was returned")
	}
}

func TestPublishedQuestionCacheEvictsLeastRecentlyUsedAndRejectsOversized(t *testing.T) {
	cache := newPublishedQuestionCacheWithConfig(2, 1<<20, time.Hour, time.Now)
	cache.Set(1, []domain.LessonQuestion{{ID: 1, Prompt: "one"}})
	cache.Set(2, []domain.LessonQuestion{{ID: 2, Prompt: "two"}})
	if _, ok := cache.Get(1); !ok {
		t.Fatal("expected first entry before eviction")
	}
	cache.Set(3, []domain.LessonQuestion{{ID: 3, Prompt: "three"}})
	if _, ok := cache.Get(1); !ok {
		t.Fatal("recently used entry was evicted")
	}
	if _, ok := cache.Get(2); ok {
		t.Fatal("least recently used entry was not evicted")
	}
	if _, ok := cache.Get(3); !ok {
		t.Fatal("new entry was not cached")
	}

	smallCache := newPublishedQuestionCacheWithConfig(4, 512, time.Hour, time.Now)
	smallCache.Set(9, []domain.LessonQuestion{{ID: 9, Prompt: string(make([]byte, 2048))}})
	if _, ok := smallCache.Get(9); ok {
		t.Fatal("oversized lesson version should not be cached")
	}
}

func TestPublishedQuestionCacheDeduplicatesConcurrentLoads(t *testing.T) {
	cache := newPublishedQuestionCacheWithConfig(4, 1<<20, time.Hour, time.Now)
	started := make(chan struct{})
	release := make(chan struct{})
	var loads atomic.Int32
	const callers = 8

	var wait sync.WaitGroup
	wait.Add(callers)
	errors := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wait.Done()
			questions, err := cache.GetOrLoad(context.Background(), 42, func() ([]domain.LessonQuestion, error) {
				if loads.Add(1) == 1 {
					close(started)
				}
				<-release
				return []domain.LessonQuestion{{ID: 420, LessonVersionID: 42, Prompt: "cached"}}, nil
			})
			if err == nil && (len(questions) != 1 || questions[0].ID != 420) {
				errors <- context.Canceled
				return
			}
			errors <- err
		}()
	}
	<-started
	// Give all goroutines a chance to join the same in-flight load before releasing it.
	time.Sleep(20 * time.Millisecond)
	close(release)
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent cache load: %v", err)
		}
	}
	if got := loads.Load(); got != 1 {
		t.Fatalf("loader calls = %d, want 1", got)
	}
}
