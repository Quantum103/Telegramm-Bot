package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/bibletext"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"golang.org/x/text/unicode/norm"
)

const synodalTranslationCode = "synodal"

var ErrBibleChapterUnavailable = errors.New("полная глава Библии временно недоступна")

type BibleChapterStore interface {
	GetChapter(ctx context.Context, translationCode string, bookID, chapterNumber int) (domain.BibleChapter, error)
	SaveChapter(ctx context.Context, chapter domain.BibleChapter) error
}

type chapterKey struct {
	bookID        int
	chapterNumber int
}

type chapterFlight struct {
	done    chan struct{}
	chapter bibletext.Chapter
	err     error
}

type BibleChapterService struct {
	store           BibleChapterStore
	primarySource   bibletext.ChapterSource
	secondarySource bibletext.ChapterSource
	logger          *slog.Logger
	embeddedLoader  func(bookID, chapterNumber int, translation string) (bibletext.Chapter, error)

	flightMu sync.Mutex
	flights  map[chapterKey]*chapterFlight
}

// NewBibleChapterService создаёт сервис с фиксированным приоритетом Midvash, затем GetBible.
// Параллельный выбор намеренно не используется: идентичность редакций источников не подтверждена.
func NewBibleChapterService(
	store BibleChapterStore,
	primarySource bibletext.ChapterSource,
	secondarySource bibletext.ChapterSource,
	logger *slog.Logger,
) *BibleChapterService {
	if logger == nil {
		logger = slog.Default()
	}
	return &BibleChapterService{
		store:           store,
		primarySource:   primarySource,
		secondarySource: secondarySource,
		logger:          logger,
		embeddedLoader:  bibletext.Load,
		flights:         make(map[chapterKey]*chapterFlight),
	}
}

// GetChapter сначала читает PostgreSQL, а при отсутствии главы загружает её из источников.
func (s *BibleChapterService) GetChapter(
	ctx context.Context,
	bookID, chapterNumber int,
) (bibletext.Chapter, error) {
	if bookID < 1 || bookID > 66 || chapterNumber < 1 {
		return bibletext.Chapter{}, domain.ErrInvalidInput
	}

	key := chapterKey{bookID: bookID, chapterNumber: chapterNumber}
	chapter, err := s.loadStoredChapter(ctx, key)
	if err == nil {
		return cloneBibleChapter(chapter), nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return bibletext.Chapter{}, fmt.Errorf("прочитать главу из PostgreSQL: %w", err)
	}

	flight, leader := s.joinFlight(key)
	if !leader {
		select {
		case <-ctx.Done():
			return bibletext.Chapter{}, ctx.Err()
		case <-flight.done:
			return cloneBibleChapter(flight.chapter), flight.err
		}
	}

	chapter, err = s.loadOrFetchChapter(ctx, key)
	s.finishFlight(key, flight, chapter, err)
	return cloneBibleChapter(chapter), err
}

func (s *BibleChapterService) loadOrFetchChapter(ctx context.Context, key chapterKey) (bibletext.Chapter, error) {
	// Повторная проверка закрывает гонку между первоначальным SELECT и созданием flight.
	chapter, err := s.loadStoredChapter(ctx, key)
	if err == nil {
		return chapter, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return bibletext.Chapter{}, fmt.Errorf("повторно прочитать главу из PostgreSQL: %w", err)
	}

	// Источники вызываются последовательно в фиксированном порядке. Параллельный
	// выбор запрещён, пока текстовая идентичность их редакций не подтверждена.
	sources := []bibletext.ChapterSource{s.primarySource, s.secondarySource}
	providers := []string{"midvash", "getbible"}
	var lastSourceError error

	for index, source := range sources {
		if source == nil {
			continue
		}
		result, fetchErr := source.FetchChapter(ctx, key.bookID, key.chapterNumber)
		if fetchErr == nil {
			fetchErr = validateFetchedChapter(result, providers[index], key)
		}
		if fetchErr != nil {
			lastSourceError = fetchErr
			s.logger.Warn(
				"не удалось получить главу из внешнего источника",
				"provider", providers[index],
				"book_id", key.bookID,
				"chapter", key.chapterNumber,
				"reason", fetchErr.Error(),
			)
			if ctx.Err() != nil {
				return bibletext.Chapter{}, ctx.Err()
			}
			continue
		}

		if index > 0 {
			s.logger.Info(
				"глава получена из резервного источника после ошибки основного",
				"provider", providers[index],
				"book_id", key.bookID,
				"chapter", key.chapterNumber,
			)
		}
		return s.persistFetchedChapter(ctx, key, result)
	}

	// Офлайн-копия — последний резерв. Её не записываем в БД, поскольку у неё
	// нет подтверждённых метаданных внешнего источника и source_version.
	if ctx.Err() != nil {
		return bibletext.Chapter{}, ctx.Err()
	}
	localChapter, localErr := s.embeddedLoader(key.bookID, key.chapterNumber, synodalTranslationCode)
	if localErr == nil {
		if validationErr := validateChapter(localChapter, key); validationErr == nil {
			s.logger.Warn(
				"глава возвращена из встроенной офлайн-копии",
				"book_id", key.bookID,
				"chapter", key.chapterNumber,
			)
			return localChapter, nil
		} else {
			localErr = validationErr
		}
	}

	s.logger.Error(
		"глава недоступна из внешних источников и во встроенной копии",
		"book_id", key.bookID,
		"chapter", key.chapterNumber,
		"primary_reason", safeErrorText(lastSourceError),
		"local_reason", safeErrorText(localErr),
	)
	return bibletext.Chapter{}, fmt.Errorf("%w: книга %d, глава %d", ErrBibleChapterUnavailable, key.bookID, key.chapterNumber)
}

func (s *BibleChapterService) loadStoredChapter(ctx context.Context, key chapterKey) (bibletext.Chapter, error) {
	stored, err := s.store.GetChapter(ctx, synodalTranslationCode, key.bookID, key.chapterNumber)
	if err != nil {
		return bibletext.Chapter{}, err
	}
	if stored.TranslationCode != synodalTranslationCode ||
		stored.BookID != key.bookID ||
		stored.ChapterNumber != key.chapterNumber {
		return bibletext.Chapter{}, fmt.Errorf("сохранённая глава не соответствует запрошенной ссылке")
	}

	chapter := bibletext.Chapter{
		Book:        stored.BookName,
		Chapter:     stored.ChapterNumber,
		Translation: "Синодальный перевод",
		Verses:      make([]bibletext.Verse, len(stored.Verses)),
	}
	for index, verse := range stored.Verses {
		chapter.Verses[index] = bibletext.Verse{Number: verse.Number, Text: verse.Text}
	}
	if err := validateChapter(chapter, key); err != nil {
		return bibletext.Chapter{}, fmt.Errorf("сохранённая глава повреждена: %w", err)
	}
	return chapter, nil
}

func (s *BibleChapterService) persistFetchedChapter(
	ctx context.Context,
	key chapterKey,
	result bibletext.SourceChapter,
) (bibletext.Chapter, error) {
	verses := make([]domain.BibleVerse, len(result.Chapter.Verses))
	for index, verse := range result.Chapter.Verses {
		verses[index] = domain.BibleVerse{Number: verse.Number, Text: verse.Text}
	}

	record := domain.BibleChapter{
		TranslationCode: synodalTranslationCode,
		BookID:          key.bookID,
		BookName:        strings.TrimSpace(result.Chapter.Book),
		ChapterNumber:   key.chapterNumber,
		Verses:          verses,
		SourceProvider:  result.Provider,
		SourceVersion:   strings.TrimSpace(result.Version),
		SourceCopyright: normalizeCopyright(result.Copyright),
		ContentSHA256:   chapterContentSHA256(result.Chapter),
		FetchedAt:       time.Now().UTC(),
	}
	if err := s.store.SaveChapter(ctx, record); err != nil {
		return bibletext.Chapter{}, fmt.Errorf("сохранить загруженную главу в PostgreSQL: %w", err)
	}
	return result.Chapter, nil
}

func validateFetchedChapter(result bibletext.SourceChapter, expectedProvider string, key chapterKey) error {
	if result.Provider != expectedProvider {
		return fmt.Errorf("источник вернул некорректную метку провайдера")
	}
	if strings.TrimSpace(result.Version) == "" {
		return fmt.Errorf("источник не указал версию перевода")
	}
	return validateChapter(result.Chapter, key)
}

func validateChapter(chapter bibletext.Chapter, key chapterKey) error {
	if strings.TrimSpace(chapter.Book) == "" || chapter.Chapter != key.chapterNumber {
		return fmt.Errorf("название книги или номер главы не совпадает с запросом")
	}
	translation := strings.ToLower(strings.TrimSpace(chapter.Translation))
	switch translation {
	case "synodal", "syn", "синодальный", "синодальный перевод":
	default:
		return fmt.Errorf("источник вернул перевод, отличный от synodal")
	}
	if len(chapter.Verses) == 0 || len(chapter.Verses) > 200 {
		return fmt.Errorf("глава не содержит полного допустимого массива стихов")
	}
	for index, verse := range chapter.Verses {
		if verse.Number != index+1 {
			return fmt.Errorf("нумерация стихов должна быть последовательной")
		}
		if strings.TrimSpace(verse.Text) == "" || !utf8.ValidString(verse.Text) {
			return fmt.Errorf("текст стиха пуст или содержит некорректный UTF-8")
		}
	}
	return nil
}

func chapterContentSHA256(chapter bibletext.Chapter) string {
	var normalized strings.Builder
	for _, verse := range chapter.Verses {
		text := norm.NFC.String(verse.Text)
		text = strings.Join(strings.Fields(text), " ")
		if normalized.Len() > 0 {
			normalized.WriteByte(' ')
		}
		normalized.WriteString(text)
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(normalized.String())))
	return hex.EncodeToString(sum[:])
}

func normalizeCopyright(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func safeErrorText(err error) string {
	if err == nil {
		return "нет данных"
	}
	return err.Error()
}

func cloneBibleChapter(chapter bibletext.Chapter) bibletext.Chapter {
	chapter.Verses = append([]bibletext.Verse(nil), chapter.Verses...)
	return chapter
}

func (s *BibleChapterService) joinFlight(key chapterKey) (*chapterFlight, bool) {
	s.flightMu.Lock()
	defer s.flightMu.Unlock()

	if flight, exists := s.flights[key]; exists {
		return flight, false
	}
	flight := &chapterFlight{done: make(chan struct{})}
	s.flights[key] = flight
	return flight, true
}

func (s *BibleChapterService) finishFlight(
	key chapterKey,
	flight *chapterFlight,
	chapter bibletext.Chapter,
	err error,
) {
	s.flightMu.Lock()
	flight.chapter = cloneBibleChapter(chapter)
	flight.err = err
	delete(s.flights, key)
	close(flight.done)
	s.flightMu.Unlock()
}
