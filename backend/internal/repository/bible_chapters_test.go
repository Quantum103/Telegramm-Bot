package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type bibleChapterTestDB struct {
	execFn     func(context.Context, string, ...any) (pgconn.CommandTag, error)
	queryRowFn func(context.Context, string, ...any) pgx.Row
	execCalls  int
	queryCalls int
	lastSQL    string
	lastArgs   []any
}

func (db *bibleChapterTestDB) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	db.execCalls++
	db.lastSQL = query
	db.lastArgs = args
	if db.execFn != nil {
		return db.execFn(ctx, query, args...)
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (db *bibleChapterTestDB) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	db.queryCalls++
	db.lastSQL = query
	db.lastArgs = args
	if db.queryRowFn != nil {
		return db.queryRowFn(ctx, query, args...)
	}
	return bibleChapterTestRow{err: pgx.ErrNoRows}
}

type bibleChapterTestRow struct {
	scanFn func(...any) error
	err    error
}

func (row bibleChapterTestRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	if row.scanFn == nil {
		return errors.New("unexpected Scan call")
	}
	return row.scanFn(dest...)
}

func TestBibleChapterRepositoryGetChapter(t *testing.T) {
	fetchedAt := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	createdAt := fetchedAt.Add(-time.Hour)
	updatedAt := fetchedAt.Add(-time.Minute)
	rawVerses := []byte(`[{"number":1,"text":"Первый стих."},{"number":2,"text":"Второй стих."}]`)

	db := &bibleChapterTestDB{
		queryRowFn: func(_ context.Context, query string, args ...any) pgx.Row {
			if !strings.Contains(query, "FROM bible_chapters") {
				t.Fatalf("неожиданный SQL запроса главы: %s", query)
			}
			if len(args) != 3 || args[0] != "synodal" || args[1] != 40 || args[2] != 4 {
				t.Fatalf("неожиданные параметры запроса: %#v", args)
			}
			return bibleChapterTestRow{scanFn: func(dest ...any) error {
				*dest[0].(*string) = "synodal"
				*dest[1].(*int16) = 40
				*dest[2].(*string) = "Матфея"
				*dest[3].(*int16) = 4
				*dest[4].(*[]byte) = rawVerses
				*dest[5].(*string) = "midvash"
				*dest[6].(*string) = "2026.1"
				*dest[7].(*pgtype.Text) = pgtype.Text{String: "Public Domain", Valid: true}
				*dest[8].(*string) = strings.Repeat("a", 64)
				*dest[9].(*time.Time) = fetchedAt
				*dest[10].(*time.Time) = createdAt
				*dest[11].(*time.Time) = updatedAt
				return nil
			}}
		},
	}

	got, err := (&BibleChapterRepository{db: db}).GetChapter(context.Background(), " SYNODAL ", 40, 4)
	if err != nil {
		t.Fatalf("GetChapter вернул ошибку: %v", err)
	}
	if got.TranslationCode != "synodal" || got.BookID != 40 || got.BookName != "Матфея" || got.ChapterNumber != 4 {
		t.Fatalf("неожиданные данные главы: %+v", got)
	}
	if len(got.Verses) != 2 || got.Verses[0].Number != 1 || got.Verses[1].Text != "Второй стих." {
		t.Fatalf("стихи главы разобраны неверно: %+v", got.Verses)
	}
	if got.SourceCopyright == nil || *got.SourceCopyright != "Public Domain" {
		t.Fatalf("не восстановлены сведения об авторском статусе: %+v", got.SourceCopyright)
	}
	if !got.FetchedAt.Equal(fetchedAt) || !got.CreatedAt.Equal(createdAt) || !got.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("не восстановлены даты главы: %+v", got)
	}
}

func TestBibleChapterRepositoryGetChapterNotFound(t *testing.T) {
	db := &bibleChapterTestDB{}
	_, err := (&BibleChapterRepository{db: db}).GetChapter(context.Background(), "synodal", 1, 999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ожидалась ErrNotFound, получено: %v", err)
	}
}

func TestBibleChapterRepositorySaveChapterIsIdempotentUpsert(t *testing.T) {
	fetchedAt := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	copyright := " Public Domain "
	input := domain.BibleChapter{
		TranslationCode: " SYNODAL ",
		BookID:          40,
		BookName:        " Матфея ",
		ChapterNumber:   4,
		Verses: []domain.BibleVerse{
			{Number: 1, Text: "Первый стих."},
			{Number: 2, Text: "Второй стих."},
		},
		SourceProvider:  " MIDVASH ",
		SourceVersion:   " 2026.1 ",
		SourceCopyright: &copyright,
		ContentSHA256:   strings.Repeat("A", 64),
		FetchedAt:       fetchedAt,
	}
	db := &bibleChapterTestDB{}
	err := (&BibleChapterRepository{db: db}).SaveChapter(context.Background(), input)
	if err != nil {
		t.Fatalf("SaveChapter вернул ошибку: %v", err)
	}
	if db.execCalls != 1 {
		t.Fatalf("количество SQL-запросов сохранения = %d, ожидался 1", db.execCalls)
	}
	if !strings.Contains(db.lastSQL, "ON CONFLICT (translation_code, book_id, chapter_number)") ||
		!strings.Contains(db.lastSQL, "IS DISTINCT FROM") {
		t.Fatalf("SQL не обеспечивает идемпотентный upsert: %s", db.lastSQL)
	}
	if db.lastArgs[0] != "synodal" || db.lastArgs[2] != "Матфея" ||
		db.lastArgs[5] != "midvash" || db.lastArgs[6] != "2026.1" ||
		db.lastArgs[7] != "Public Domain" || db.lastArgs[8] != strings.Repeat("a", 64) {
		t.Fatalf("параметры не нормализованы: %#v", db.lastArgs)
	}
	var verses []domain.BibleVerse
	if err := json.Unmarshal([]byte(db.lastArgs[4].(string)), &verses); err != nil {
		t.Fatalf("передано некорректное JSONB содержимое: %v", err)
	}
	if len(verses) != 2 || verses[1].Text != "Второй стих." {
		t.Fatalf("в SQL переданы неверные стихи: %+v", verses)
	}
	if !db.lastArgs[9].(time.Time).Equal(fetchedAt) {
		t.Fatalf("не передан fetched_at: %#v", db.lastArgs[9])
	}
}

func TestBibleChapterRepositorySaveChapterRejectsInvalidInput(t *testing.T) {
	valid := domain.BibleChapter{
		TranslationCode: "synodal",
		BookID:          1,
		BookName:        "Бытие",
		ChapterNumber:   1,
		Verses:          []domain.BibleVerse{{Number: 2, Text: "Стих с пропущенным номером."}},
		SourceProvider:  "midvash",
		SourceVersion:   "2026.1",
		ContentSHA256:   strings.Repeat("a", 64),
		FetchedAt:       time.Now(),
	}
	db := &bibleChapterTestDB{}
	err := (&BibleChapterRepository{db: db}).SaveChapter(context.Background(), valid)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ожидалась ErrInvalidInput, получено: %v", err)
	}
	if db.execCalls != 0 {
		t.Fatalf("SQL выполнен для некорректной главы: %d вызовов", db.execCalls)
	}
}

func TestBibleChapterRepositoryGetTelegraphLink(t *testing.T) {
	createdAt := time.Date(2026, 10, 31, 13, 0, 0, 0, time.UTC)
	db := &bibleChapterTestDB{
		queryRowFn: func(_ context.Context, query string, args ...any) pgx.Row {
			if !strings.Contains(query, "FROM bible_chapter_telegraph_links") {
				t.Fatalf("неожиданный SQL ссылки Telegraph: %s", query)
			}
			return bibleChapterTestRow{scanFn: func(dest ...any) error {
				*dest[0].(*string) = "synodal"
				*dest[1].(*int16) = 40
				*dest[2].(*int16) = 4
				*dest[3].(*string) = strings.Repeat("b", 64)
				*dest[4].(*string) = "https://telegra.ph/matfeya-04-31"
				*dest[5].(*time.Time) = createdAt
				return nil
			}}
		},
	}

	got, err := (&BibleChapterRepository{db: db}).GetTelegraphLink(
		context.Background(), "synodal", 40, 4, strings.Repeat("b", 64),
	)
	if err != nil {
		t.Fatalf("GetTelegraphLink вернул ошибку: %v", err)
	}
	if got.PageURL != "https://telegra.ph/matfeya-04-31" || got.ContentSHA256 != strings.Repeat("b", 64) {
		t.Fatalf("неожиданная сохранённая ссылка: %+v", got)
	}
	if !got.CreatedAt.Equal(createdAt) {
		t.Fatalf("не восстановлена дата создания ссылки: %v", got.CreatedAt)
	}
}

func TestBibleChapterRepositorySaveTelegraphLinkIsIdempotent(t *testing.T) {
	createdAt := time.Date(2026, 10, 31, 13, 0, 0, 0, time.UTC)
	existing := bibleChapterTestRow{scanFn: func(dest ...any) error {
		*dest[0].(*string) = "synodal"
		*dest[1].(*int16) = 40
		*dest[2].(*int16) = 4
		*dest[3].(*string) = strings.Repeat("c", 64)
		*dest[4].(*string) = "https://telegra.ph/matfeya-04-31"
		*dest[5].(*time.Time) = createdAt
		return nil
	}}
	db := &bibleChapterTestDB{
		execFn: func(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
			if !strings.Contains(query, "ON CONFLICT (translation_code, book_id, chapter_number, content_sha256)") ||
				!strings.Contains(query, "DO NOTHING") {
				t.Fatalf("SQL не гарантирует уникальность версии ссылки: %s", query)
			}
			return pgconn.NewCommandTag("INSERT 0 0"), nil
		},
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return existing
		},
	}
	link := domain.TelegraphChapterLink{
		TranslationCode: "synodal",
		BookID:          40,
		ChapterNumber:   4,
		ContentSHA256:   strings.Repeat("c", 64),
		PageURL:         "https://telegra.ph/matfeya-04-31",
	}
	if err := (&BibleChapterRepository{db: db}).SaveTelegraphLink(context.Background(), link); err != nil {
		t.Fatalf("повторное сохранение ссылки должно быть успешным: %v", err)
	}
	if db.execCalls != 1 || db.queryCalls != 1 {
		t.Fatalf("ожидались INSERT и проверка ранее сохранённой ссылки, Exec=%d QueryRow=%d", db.execCalls, db.queryCalls)
	}
}

func TestBibleChapterRepositorySaveTelegraphLinkRequiresChapter(t *testing.T) {
	db := &bibleChapterTestDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("INSERT 0 0"), nil
		},
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return bibleChapterTestRow{err: pgx.ErrNoRows}
		},
	}
	link := domain.TelegraphChapterLink{
		TranslationCode: "synodal",
		BookID:          40,
		ChapterNumber:   4,
		ContentSHA256:   strings.Repeat("d", 64),
		PageURL:         "https://telegra.ph/matfeya-04-31",
	}
	err := (&BibleChapterRepository{db: db}).SaveTelegraphLink(context.Background(), link)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ожидалась ErrNotFound для отсутствующей главы, получено: %v", err)
	}
}

func TestBibleChapterRepositoryRejectsInvalidTelegraphURL(t *testing.T) {
	db := &bibleChapterTestDB{}
	link := domain.TelegraphChapterLink{
		TranslationCode: "synodal",
		BookID:          40,
		ChapterNumber:   4,
		ContentSHA256:   strings.Repeat("e", 64),
		PageURL:         "https://example.com/chapter",
	}
	err := (&BibleChapterRepository{db: db}).SaveTelegraphLink(context.Background(), link)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ожидалась ErrInvalidInput для постороннего URL, получено: %v", err)
	}
	if db.execCalls != 0 {
		t.Fatalf("SQL выполнен для некорректного URL: %d вызовов", db.execCalls)
	}
}
