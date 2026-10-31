package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/bibletext"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"log/slog"
)

type mockBibleChapterStore struct {
	mu        sync.Mutex
	record    *domain.BibleChapter
	getErr    error
	saveErr   error
	getCalls  int
	saveCalls int
	getCalled chan struct{}
}

func (store *mockBibleChapterStore) GetChapter(
	_ context.Context,
	translationCode string,
	bookID, chapterNumber int,
) (domain.BibleChapter, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.getCalls++
	if store.getCalled != nil {
		select {
		case store.getCalled <- struct{}{}:
		default:
		}
	}
	if store.getErr != nil {
		return domain.BibleChapter{}, store.getErr
	}
	if store.record == nil {
		return domain.BibleChapter{}, domain.ErrNotFound
	}
	if store.record.TranslationCode != translationCode ||
		store.record.BookID != bookID ||
		store.record.ChapterNumber != chapterNumber {
		return domain.BibleChapter{}, domain.ErrNotFound
	}
	copy := *store.record
	copy.Verses = append([]domain.BibleVerse(nil), store.record.Verses...)
	return copy, nil
}

func (store *mockBibleChapterStore) SaveChapter(_ context.Context, chapter domain.BibleChapter) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.saveCalls++
	if store.saveErr != nil {
		return store.saveErr
	}
	copy := chapter
	copy.Verses = append([]domain.BibleVerse(nil), chapter.Verses...)
	store.record = &copy
	return nil
}

func (store *mockBibleChapterStore) snapshot() (*domain.BibleChapter, int, int) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.record == nil {
		return nil, store.getCalls, store.saveCalls
	}
	copy := *store.record
	copy.Verses = append([]domain.BibleVerse(nil), store.record.Verses...)
	return &copy, store.getCalls, store.saveCalls
}

type mockBibleChapterSource struct {
	mu     sync.Mutex
	result bibletext.SourceChapter
	err    error
	calls  int
	fetch  func(context.Context, int, int) (bibletext.SourceChapter, error)
}

func (source *mockBibleChapterSource) FetchChapter(
	ctx context.Context,
	bookID, chapterNumber int,
) (bibletext.SourceChapter, error) {
	source.mu.Lock()
	source.calls++
	fetch := source.fetch
	result := source.result
	err := source.err
	source.mu.Unlock()
	if fetch != nil {
		return fetch(ctx, bookID, chapterNumber)
	}
	return result, err
}

func (source *mockBibleChapterSource) callCount() int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.calls
}

func testChapterResult(provider string) bibletext.SourceChapter {
	return bibletext.SourceChapter{
		Chapter: bibletext.Chapter{
			Book:        "Матфей",
			Chapter:     4,
			Translation: "Синодальный перевод",
			Verses: []bibletext.Verse{
				{Number: 1, Text: "Тогда Иисус возведен был Духом в пустыню."},
				{Number: 2, Text: "И, постившись сорок дней и сорок ночей, напоследок взалкал."},
			},
		},
		Provider: provider,
		Version: func() string {
			if provider == "midvash" {
				return "synodal"
			}
			return "Synodal Translation (1876)"
		}(),
	}
}

func testStoredChapter() *domain.BibleChapter {
	result := testChapterResult("midvash")
	verses := make([]domain.BibleVerse, len(result.Chapter.Verses))
	for index, verse := range result.Chapter.Verses {
		verses[index] = domain.BibleVerse{Number: verse.Number, Text: verse.Text}
	}
	return &domain.BibleChapter{
		TranslationCode: "synodal",
		BookID:          40,
		BookName:        "Матфей",
		ChapterNumber:   4,
		Verses:          verses,
		SourceProvider:  "midvash",
		SourceVersion:   "synodal",
		ContentSHA256:   strings.Repeat("a", 64),
		FetchedAt:       time.Now().UTC(),
	}
}

func newTestBibleChapterService(
	store *mockBibleChapterStore,
	primary, secondary bibletext.ChapterSource,
) *BibleChapterService {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewBibleChapterService(store, primary, secondary, logger)
}

func TestBibleChapterServiceReturnsStoredChapterWithoutCallingSources(t *testing.T) {
	store := &mockBibleChapterStore{record: testStoredChapter()}
	primary := &mockBibleChapterSource{err: errors.New("must not be called")}
	secondary := &mockBibleChapterSource{err: errors.New("must not be called")}
	service := newTestBibleChapterService(store, primary, secondary)

	chapter, err := service.GetChapter(context.Background(), 40, 4)
	if err != nil {
		t.Fatalf("GetChapter вернул ошибку: %v", err)
	}
	if chapter.Book != "Матфей" || chapter.Chapter != 4 || len(chapter.Verses) != 2 {
		t.Fatalf("получена неверная глава: %+v", chapter)
	}
	if primary.callCount() != 0 || secondary.callCount() != 0 {
		t.Fatalf("внешние источники вызваны при попадании в PostgreSQL: primary=%d secondary=%d",
			primary.callCount(), secondary.callCount())
	}
}

func TestBibleChapterServiceFetchesPrimaryAndPersists(t *testing.T) {
	store := &mockBibleChapterStore{}
	primary := &mockBibleChapterSource{result: testChapterResult("midvash")}
	secondary := &mockBibleChapterSource{err: errors.New("резервный источник не должен вызываться")}
	service := newTestBibleChapterService(store, primary, secondary)

	chapter, err := service.GetChapter(context.Background(), 40, 4)
	if err != nil {
		t.Fatalf("GetChapter вернул ошибку: %v", err)
	}
	if len(chapter.Verses) != 2 || chapter.Translation != "Синодальный перевод" {
		t.Fatalf("получена неверная глава: %+v", chapter)
	}
	record, _, saveCalls := store.snapshot()
	if saveCalls != 1 || record == nil {
		t.Fatalf("глава не была сохранена: record=%+v saveCalls=%d", record, saveCalls)
	}
	if record.SourceProvider != "midvash" || record.TranslationCode != "synodal" ||
		record.ContentSHA256 != chapterContentSHA256(chapter) || len(record.ContentSHA256) != 64 {
		t.Fatalf("неверные метаданные сохранённой главы: %+v", record)
	}
	if primary.callCount() != 1 || secondary.callCount() != 0 {
		t.Fatalf("нарушен фиксированный порядок источников: primary=%d secondary=%d",
			primary.callCount(), secondary.callCount())
	}
}

func TestBibleChapterServiceUsesGetBibleAfterMidvashFailure(t *testing.T) {
	store := &mockBibleChapterStore{}
	primary := &mockBibleChapterSource{err: errors.New("Midvash недоступен")}
	secondary := &mockBibleChapterSource{result: testChapterResult("getbible")}
	service := newTestBibleChapterService(store, primary, secondary)

	_, err := service.GetChapter(context.Background(), 40, 4)
	if err != nil {
		t.Fatalf("GetChapter не использовал резервный источник: %v", err)
	}
	record, _, saveCalls := store.snapshot()
	if saveCalls != 1 || record == nil || record.SourceProvider != "getbible" {
		t.Fatalf("резервная глава сохранена некорректно: record=%+v saveCalls=%d", record, saveCalls)
	}
	if primary.callCount() != 1 || secondary.callCount() != 1 {
		t.Fatalf("ожидался последовательный запрос двух источников: primary=%d secondary=%d",
			primary.callCount(), secondary.callCount())
	}
}

func TestBibleChapterServiceRejectsAnotherTranslation(t *testing.T) {
	store := &mockBibleChapterStore{}
	primaryResult := testChapterResult("midvash")
	primaryResult.Chapter.Translation = "Другой перевод"
	primary := &mockBibleChapterSource{result: primaryResult}
	secondary := &mockBibleChapterSource{err: errors.New("резервный источник недоступен")}
	service := newTestBibleChapterService(store, primary, secondary)
	service.embeddedLoader = func(int, int, string) (bibletext.Chapter, error) {
		return bibletext.Chapter{}, bibletext.ErrChapterNotFound
	}

	_, err := service.GetChapter(context.Background(), 40, 4)
	if !errors.Is(err, ErrBibleChapterUnavailable) {
		t.Fatalf("ожидалась ErrBibleChapterUnavailable, получено: %v", err)
	}
	_, _, saveCalls := store.snapshot()
	if saveCalls != 0 {
		t.Fatalf("данные другого перевода были сохранены: %d", saveCalls)
	}
}

func TestBibleChapterServiceRejectsUnexpectedSourceVersion(t *testing.T) {
	store := &mockBibleChapterStore{}
	result := testChapterResult("midvash")
	result.Version = "kjv"
	primary := &mockBibleChapterSource{result: result}
	secondary := &mockBibleChapterSource{err: errors.New("GetBible недоступен")}
	service := newTestBibleChapterService(store, primary, secondary)
	service.embeddedLoader = func(int, int, string) (bibletext.Chapter, error) {
		return bibletext.Chapter{}, bibletext.ErrChapterNotFound
	}

	_, err := service.GetChapter(context.Background(), 40, 4)
	if !errors.Is(err, ErrBibleChapterUnavailable) {
		t.Fatalf("ожидалась ошибка для неподходящей версии источника, получено: %v", err)
	}
	_, _, saveCalls := store.snapshot()
	if saveCalls != 0 {
		t.Fatalf("глава с неподходящей версией была сохранена: %d", saveCalls)
	}
}

func TestBibleChapterServiceUsesEmbeddedChapterWhenBothSourcesFail(t *testing.T) {
	store := &mockBibleChapterStore{}
	primary := &mockBibleChapterSource{err: errors.New("Midvash недоступен")}
	secondary := &mockBibleChapterSource{err: errors.New("GetBible недоступен")}
	service := newTestBibleChapterService(store, primary, secondary)
	expected := testChapterResult("midvash").Chapter
	service.embeddedLoader = func(bookID, chapterNumber int, translation string) (bibletext.Chapter, error) {
		if bookID != 40 || chapterNumber != 4 || translation != "synodal" {
			return bibletext.Chapter{}, fmt.Errorf("неожиданные параметры локальной загрузки")
		}
		return expected, nil
	}

	got, err := service.GetChapter(context.Background(), 40, 4)
	if err != nil {
		t.Fatalf("не использована встроенная глава: %v", err)
	}
	if len(got.Verses) != len(expected.Verses) || got.Verses[1].Text != expected.Verses[1].Text {
		t.Fatalf("возвращена неверная встроенная глава: %+v", got)
	}
	_, _, saveCalls := store.snapshot()
	if saveCalls != 0 {
		t.Fatalf("встроенная глава без метаданных источника записана в PostgreSQL: %d", saveCalls)
	}
}

func TestBibleChapterServiceReturnsClearErrorWhenAllSourcesUnavailable(t *testing.T) {
	store := &mockBibleChapterStore{}
	primary := &mockBibleChapterSource{err: errors.New("Midvash недоступен")}
	secondary := &mockBibleChapterSource{err: errors.New("GetBible недоступен")}
	service := newTestBibleChapterService(store, primary, secondary)
	service.embeddedLoader = func(int, int, string) (bibletext.Chapter, error) {
		return bibletext.Chapter{}, bibletext.ErrChapterNotFound
	}

	_, err := service.GetChapter(context.Background(), 40, 4)
	if !errors.Is(err, ErrBibleChapterUnavailable) {
		t.Fatalf("ожидалась понятная ошибка недоступности главы, получено: %v", err)
	}
}

func TestBibleChapterServiceDoesNotFetchWhenPostgresFails(t *testing.T) {
	store := &mockBibleChapterStore{getErr: errors.New("database unavailable")}
	primary := &mockBibleChapterSource{err: errors.New("must not be called")}
	secondary := &mockBibleChapterSource{err: errors.New("must not be called")}
	service := newTestBibleChapterService(store, primary, secondary)

	_, err := service.GetChapter(context.Background(), 40, 4)
	if err == nil || primary.callCount() != 0 || secondary.callCount() != 0 {
		t.Fatalf("при ошибке PostgreSQL сервис обратился к внешним API: err=%v primary=%d secondary=%d",
			err, primary.callCount(), secondary.callCount())
	}
}

func TestBibleChapterServiceDeduplicatesConcurrentLoads(t *testing.T) {
	store := &mockBibleChapterStore{getCalled: make(chan struct{}, 8)}
	started := make(chan struct{})
	release := make(chan struct{})
	primary := &mockBibleChapterSource{
		fetch: func(ctx context.Context, bookID, chapterNumber int) (bibletext.SourceChapter, error) {
			close(started)
			select {
			case <-ctx.Done():
				return bibletext.SourceChapter{}, ctx.Err()
			case <-release:
				return testChapterResult("midvash"), nil
			}
		},
	}
	service := newTestBibleChapterService(store, primary, nil)

	type result struct {
		chapter bibletext.Chapter
		err     error
	}
	results := make(chan result, 2)
	go func() {
		chapter, err := service.GetChapter(context.Background(), 40, 4)
		results <- result{chapter: chapter, err: err}
	}()

	// Две проверки БД завершились до начала запроса Midvash:
	// первоначальный SELECT и повторный SELECT лидера.
	<-store.getCalled
	<-store.getCalled
	<-started

	go func() {
		chapter, err := service.GetChapter(context.Background(), 40, 4)
		results <- result{chapter: chapter, err: err}
	}()
	// Второй вызов также выполнил первоначальный SELECT и увидел отсутствие записи.
	<-store.getCalled
	close(release)

	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("параллельные запросы завершились ошибкой: first=%v second=%v", first.err, second.err)
	}
	if len(first.chapter.Verses) != 2 || len(second.chapter.Verses) != 2 {
		t.Fatalf("один из вызовов получил неполную главу")
	}
	if primary.callCount() != 1 {
		t.Fatalf("одновременные запросы вызвали API %d раз, ожидался 1", primary.callCount())
	}
	_, _, saveCalls := store.snapshot()
	if saveCalls != 1 {
		t.Fatalf("одновременная загрузка сохранила главу %d раз, ожидался 1", saveCalls)
	}
}

func TestBibleChapterServiceRejectsInvalidReference(t *testing.T) {
	store := &mockBibleChapterStore{}
	service := newTestBibleChapterService(store, nil, nil)

	_, err := service.GetChapter(context.Background(), 67, 1)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("ожидалась ErrInvalidInput для книги вне 66 канонических книг, получено: %v", err)
	}
	_, err = service.GetChapter(context.Background(), 1, 0)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("ожидалась ErrInvalidInput для номера главы 0, получено: %v", err)
	}
}


func TestBibleChapterServiceFallsBackAfterMidvashReturnsMalformedJSON(t *testing.T) {
	var midvashCatalogCalls, midvashChapterCalls, getBibleCalls int

	midvashServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/books":
			midvashCatalogCalls++
			books := make([]map[string]any, 0, 66)
			for id := 1; id <= 66; id++ {
				slug := fmt.Sprintf("book-%d", id)
				englishName := fmt.Sprintf("Book %d", id)
				russianName := fmt.Sprintf("Книга %d", id)
				if id == 40 {
					slug, englishName, russianName = "matthew", "Matthew", "Матфей"
				}
				books = append(books, map[string]any{
					"id": id,
					"slug": map[string]string{"en": slug},
					"name": map[string]string{"en": englishName, "ru": russianName},
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": books,
				"meta": map[string]int{"total": 66},
			})
		case "/v1/synodal/matthew/4":
			midvashChapterCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"data":`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer midvashServer.Close()

	getBibleServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		getBibleCalls++
		if r.URL.Path != "/synodal/40/4.json" {
			t.Errorf("GetBible path = %q, want /synodal/40/4.json", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"translation":  "Synodal Translation (1876)",
			"abbreviation": "synodal",
			"lang":         "ru",
			"book_nr":      40,
			"book_name":    "Матфей",
			"chapter":      4,
			"verses": []map[string]any{
				{"chapter": 4, "verse": 1, "text": "Тогда Иисус возведен был Духом в пустыню."},
				{"chapter": 4, "verse": 2, "text": "И, постившись сорок дней и сорок ночей, напоследок взалкал."},
			},
		})
	}))
	defer getBibleServer.Close()

	store := &mockBibleChapterStore{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	chapterService := NewBibleChapterService(
		store,
		bibletext.NewMidvashClientWithBaseURL(midvashServer.URL, midvashServer.Client()),
		bibletext.NewGetBibleClientWithBaseURL(getBibleServer.URL, getBibleServer.Client()),
		logger,
	)

	chapter, err := chapterService.GetChapter(context.Background(), 40, 4)
	if err != nil {
		t.Fatalf("GetChapter() после повреждённого JSON основного API: %v", err)
	}
	if chapter.Book != "Матфей" || chapter.Chapter != 4 || len(chapter.Verses) != 2 {
		t.Fatalf("резервный источник вернул некорректную главу: %+v", chapter)
	}
	if midvashCatalogCalls != 1 || midvashChapterCalls != 1 || getBibleCalls != 1 {
		t.Fatalf("вызовы API: Midvash catalog=%d chapter=%d, GetBible=%d; ожидалось 1/1/1",
			midvashCatalogCalls, midvashChapterCalls, getBibleCalls)
	}

	record, _, saveCalls := store.snapshot()
	if saveCalls != 1 || record == nil {
		t.Fatalf("глава должна быть сохранена ровно один раз после валидации fallback: record=%+v saves=%d",
			record, saveCalls)
	}
	if record.SourceProvider != "getbible" || record.SourceVersion != "Synodal Translation (1876)" ||
		record.Verses[0].Text != chapter.Verses[0].Text {
		t.Fatalf("в PostgreSQL-модель попал некорректный результат: %+v", record)
	}
}
