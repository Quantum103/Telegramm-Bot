package repository

import (
	"sync"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

func TestQuestionContentCacheLoadsOnceAndReturnsCopies(t *testing.T) {
	cache := newQuestionContentCacheWithLimits(4, 1<<20, time.Minute)
	loads := 0
	passageText := "Исходный текст"
	questions, err := cache.getOrLoad(42, func() ([]domain.LessonQuestion, error) {
		loads++
		return []domain.LessonQuestion{{
			ID: 7, Prompt: "Вопрос",
			Options: []domain.QuestionOption{{ID: 1, Text: "Вариант"}},
			BiblePassages: []domain.BiblePassage{{PassageText: &passageText}},
		}}, nil
	})
	if err != nil {
		t.Fatalf("первая загрузка: %v", err)
	}
	questions[0].Prompt = "изменено вызывающим кодом"
	questions[0].Options[0].Text = "изменён вариант"
	*questions[0].BiblePassages[0].PassageText = "изменён отрывок"

	cached, err := cache.getOrLoad(42, func() ([]domain.LessonQuestion, error) {
		loads++
		return nil, nil
	})
	if err != nil {
		t.Fatalf("чтение кэша: %v", err)
	}
	if loads != 1 {
		t.Fatalf("loader вызван %d раз, ожидался 1", loads)
	}
	if cached[0].Prompt != "Вопрос" || cached[0].Options[0].Text != "Вариант" ||
		*cached[0].BiblePassages[0].PassageText != "Исходный текст" {
		t.Fatalf("изменение возвращённой копии повредило кэш: %+v", cached[0])
	}
}

func TestQuestionContentCacheExpiresAndEvictsLeastRecentlyUsed(t *testing.T) {
	cache := newQuestionContentCacheWithLimits(2, 1<<20, time.Minute)
	now := time.Now()
	cache.now = func() time.Time { return now }

	cache.put(1, []domain.LessonQuestion{{ID: 1}})
	cache.put(2, []domain.LessonQuestion{{ID: 2}})
	if _, ok := cache.get(1); !ok {
		t.Fatal("ожидалась первая версия в кэше")
	}
	cache.put(3, []domain.LessonQuestion{{ID: 3}})
	if _, ok := cache.get(2); ok {
		t.Fatal("наименее недавно использованная версия должна быть вытеснена")
	}
	if _, ok := cache.get(1); !ok {
		t.Fatal("недавно использованная версия должна остаться")
	}

	now = now.Add(2 * time.Minute)
	if _, ok := cache.get(1); ok {
		t.Fatal("просроченная запись не должна возвращаться")
	}
	if cache.order.Len() != 1 {
		t.Fatalf("просроченная запись не удалена, элементов=%d", cache.order.Len())
	}
}

func TestQuestionContentCacheDoesNotStoreOversizedContent(t *testing.T) {
	cache := newQuestionContentCacheWithLimits(2, 200, time.Minute)
	cache.put(1, []domain.LessonQuestion{{ID: 1, Prompt: string(make([]byte, 500))}})
	if _, ok := cache.get(1); ok {
		t.Fatal("контент больше лимита памяти не должен сохраняться")
	}
	if cache.usedBytes != 0 {
		t.Fatalf("кэш учитывает память для несохранённой записи: %d", cache.usedBytes)
	}
}

func TestQuestionContentCacheConcurrentAccess(t *testing.T) {
	cache := newQuestionContentCacheWithLimits(8, 1<<20, time.Minute)
	var wg sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for iteration := 0; iteration < 50; iteration++ {
				versionID := int64(id%8 + 1)
				_, _ = cache.getOrLoad(versionID, func() ([]domain.LessonQuestion, error) {
					return []domain.LessonQuestion{{ID: versionID, Prompt: "question"}}, nil
				})
				cache.put(versionID, []domain.LessonQuestion{{ID: versionID, Prompt: "updated"}})
			}
		}(worker)
	}
	wg.Wait()
}
