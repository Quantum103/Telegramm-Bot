package repository

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type bibleChapterDB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type BibleChapterRepository struct {
	db bibleChapterDB
}

func NewBibleChapterRepository(pool *pgxpool.Pool) *BibleChapterRepository {
	return &BibleChapterRepository{db: pool}
}

// GetChapter возвращает сохранённую главу по переводу, книге и номеру главы.
func (r *BibleChapterRepository) GetChapter(
	ctx context.Context,
	translationCode string,
	bookID, chapterNumber int,
) (domain.BibleChapter, error) {
	translationCode, err := normalizeBibleChapterKey(translationCode, bookID, chapterNumber)
	if err != nil {
		return domain.BibleChapter{}, err
	}

	const query = `
		SELECT translation_code, book_id, book_name, chapter_number, verses,
		       source_provider, source_version, source_copyright, content_sha256,
		       fetched_at, created_at, updated_at
		FROM bible_chapters
		WHERE translation_code = $1 AND book_id = $2 AND chapter_number = $3
	`

	chapter, err := scanBibleChapter(r.db.QueryRow(ctx, query, translationCode, bookID, chapterNumber))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.BibleChapter{}, ErrNotFound
		}
		return domain.BibleChapter{}, mapDatabaseError("получить главу Библии", err)
	}
	return chapter, nil
}

// SaveChapter вставляет главу или обновляет существующую запись.
// Повторная запись тех же данных не меняет updated_at и не создаёт дубликат.
func (r *BibleChapterRepository) SaveChapter(ctx context.Context, input domain.BibleChapter) error {
	chapter, err := normalizeBibleChapter(input)
	if err != nil {
		return err
	}

	versesJSON, err := json.Marshal(chapter.Verses)
	if err != nil {
		return fmt.Errorf("подготовить стихи главы: %w", err)
	}

	const query = `
		INSERT INTO bible_chapters (
			translation_code, book_id, book_name, chapter_number, verses,
			source_provider, source_version, source_copyright, content_sha256, fetched_at
		)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10)
		ON CONFLICT (translation_code, book_id, chapter_number)
		DO UPDATE SET
			book_name = EXCLUDED.book_name,
			verses = EXCLUDED.verses,
			source_provider = EXCLUDED.source_provider,
			source_version = EXCLUDED.source_version,
			source_copyright = EXCLUDED.source_copyright,
			content_sha256 = EXCLUDED.content_sha256,
			fetched_at = EXCLUDED.fetched_at,
			updated_at = now()
		WHERE (
			bible_chapters.book_name,
			bible_chapters.verses,
			bible_chapters.source_provider,
			bible_chapters.source_version,
			bible_chapters.source_copyright,
			bible_chapters.content_sha256,
			bible_chapters.fetched_at
		) IS DISTINCT FROM (
			EXCLUDED.book_name,
			EXCLUDED.verses,
			EXCLUDED.source_provider,
			EXCLUDED.source_version,
			EXCLUDED.source_copyright,
			EXCLUDED.content_sha256,
			EXCLUDED.fetched_at
		)
	`

	_, err = r.db.Exec(
		ctx,
		query,
		chapter.TranslationCode,
		chapter.BookID,
		chapter.BookName,
		chapter.ChapterNumber,
		string(versesJSON),
		chapter.SourceProvider,
		chapter.SourceVersion,
		optionalStringValue(chapter.SourceCopyright),
		chapter.ContentSHA256,
		chapter.FetchedAt,
	)
	if err != nil {
		return mapDatabaseError("сохранить главу Библии", err)
	}
	return nil
}

// GetTelegraphLink находит статью для конкретной версии содержимого главы.
func (r *BibleChapterRepository) GetTelegraphLink(
	ctx context.Context,
	translationCode string,
	bookID, chapterNumber int,
	contentSHA256 string,
) (domain.TelegraphChapterLink, error) {
	translationCode, contentSHA256, err := normalizeTelegraphLinkKey(
		translationCode, bookID, chapterNumber, contentSHA256,
	)
	if err != nil {
		return domain.TelegraphChapterLink{}, err
	}

	const query = `
		SELECT translation_code, book_id, chapter_number, content_sha256, page_url, created_at
		FROM bible_chapter_telegraph_links
		WHERE translation_code = $1
		  AND book_id = $2
		  AND chapter_number = $3
		  AND content_sha256 = $4
	`

	link, err := scanTelegraphChapterLink(
		r.db.QueryRow(ctx, query, translationCode, bookID, chapterNumber, contentSHA256),
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.TelegraphChapterLink{}, ErrNotFound
		}
		return domain.TelegraphChapterLink{}, mapDatabaseError("получить ссылку Telegraph для главы", err)
	}
	return link, nil
}

// SaveTelegraphLink сохраняет ссылку один раз для конкретного хэша главы.
// Ссылка допускается только для существующей главы; конфликт ключа идемпотентен.
func (r *BibleChapterRepository) SaveTelegraphLink(
	ctx context.Context,
	input domain.TelegraphChapterLink,
) error {
	translationCode, contentSHA256, err := normalizeTelegraphLinkKey(
		input.TranslationCode, input.BookID, input.ChapterNumber, input.ContentSHA256,
	)
	if err != nil {
		return err
	}
	pageURL, err := normalizeTelegraphPageURL(input.PageURL)
	if err != nil {
		return err
	}

	const query = `
		INSERT INTO bible_chapter_telegraph_links (
			translation_code, book_id, chapter_number, content_sha256, page_url
		)
		SELECT $1, $2, $3, $4, $5
		WHERE EXISTS (
			SELECT 1
			FROM bible_chapters
			WHERE translation_code = $1 AND book_id = $2 AND chapter_number = $3
		)
		ON CONFLICT (translation_code, book_id, chapter_number, content_sha256)
		DO NOTHING
	`

	tag, err := r.db.Exec(
		ctx, query, translationCode, input.BookID, input.ChapterNumber, contentSHA256, pageURL,
	)
	if err != nil {
		return mapDatabaseError("сохранить ссылку Telegraph для главы", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}

	// INSERT ... DO NOTHING возвращает 0 как при повторе, так и при отсутствии главы.
	// Проверка существующей ссылки отличает идемпотентный повтор от несуществующей главы.
	_, err = r.GetTelegraphLink(ctx, translationCode, input.BookID, input.ChapterNumber, contentSHA256)
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("глава не найдена для сохранения ссылки Telegraph: %w", ErrNotFound)
	}
	return err
}

func scanBibleChapter(row pgx.Row) (domain.BibleChapter, error) {
	var chapter domain.BibleChapter
	var bookID, chapterNumber int16
	var versesJSON []byte
	var sourceCopyright pgtype.Text

	err := row.Scan(
		&chapter.TranslationCode,
		&bookID,
		&chapter.BookName,
		&chapterNumber,
		&versesJSON,
		&chapter.SourceProvider,
		&chapter.SourceVersion,
		&sourceCopyright,
		&chapter.ContentSHA256,
		&chapter.FetchedAt,
		&chapter.CreatedAt,
		&chapter.UpdatedAt,
	)
	if err != nil {
		return domain.BibleChapter{}, err
	}
	if err := json.Unmarshal(versesJSON, &chapter.Verses); err != nil {
		return domain.BibleChapter{}, fmt.Errorf("разобрать JSONB стихов главы: %w", err)
	}
	chapter.BookID = int(bookID)
	chapter.ChapterNumber = int(chapterNumber)
	if sourceCopyright.Valid {
		value := sourceCopyright.String
		chapter.SourceCopyright = &value
	}
	return chapter, nil
}

func scanTelegraphChapterLink(row pgx.Row) (domain.TelegraphChapterLink, error) {
	var link domain.TelegraphChapterLink
	var bookID, chapterNumber int16

	err := row.Scan(
		&link.TranslationCode,
		&bookID,
		&chapterNumber,
		&link.ContentSHA256,
		&link.PageURL,
		&link.CreatedAt,
	)
	if err != nil {
		return domain.TelegraphChapterLink{}, err
	}
	link.BookID = int(bookID)
	link.ChapterNumber = int(chapterNumber)
	return link, nil
}

func normalizeBibleChapter(input domain.BibleChapter) (domain.BibleChapter, error) {
	input.TranslationCode = strings.ToLower(strings.TrimSpace(input.TranslationCode))
	input.BookName = strings.TrimSpace(input.BookName)
	input.SourceProvider = strings.ToLower(strings.TrimSpace(input.SourceProvider))
	input.SourceVersion = strings.TrimSpace(input.SourceVersion)
	input.ContentSHA256 = strings.ToLower(strings.TrimSpace(input.ContentSHA256))
	input.SourceCopyright = normalizeOptionalString(input.SourceCopyright)

	if _, err := normalizeBibleChapterKey(input.TranslationCode, input.BookID, input.ChapterNumber); err != nil {
		return domain.BibleChapter{}, err
	}
	if input.BookName == "" || input.SourceVersion == "" || input.FetchedAt.IsZero() {
		return domain.BibleChapter{}, ErrInvalidInput
	}
	switch input.SourceProvider {
	case "midvash", "getbible":
	default:
		return domain.BibleChapter{}, ErrInvalidInput
	}
	if !isSHA256(input.ContentSHA256) || len(input.Verses) == 0 || len(input.Verses) > 200 {
		return domain.BibleChapter{}, ErrInvalidInput
	}
	for index, verse := range input.Verses {
		if verse.Number != index+1 || strings.TrimSpace(verse.Text) == "" || !utf8.ValidString(verse.Text) {
			return domain.BibleChapter{}, ErrInvalidInput
		}
	}
	input.FetchedAt = input.FetchedAt.UTC()
	return input, nil
}

func normalizeBibleChapterKey(translationCode string, bookID, chapterNumber int) (string, error) {
	translationCode = strings.ToLower(strings.TrimSpace(translationCode))
	if translationCode == "" || bookID < 1 || bookID > 66 || chapterNumber < 1 {
		return "", ErrInvalidInput
	}
	return translationCode, nil
}

func normalizeTelegraphLinkKey(
	translationCode string,
	bookID, chapterNumber int,
	contentSHA256 string,
) (string, string, error) {
	translationCode, err := normalizeBibleChapterKey(translationCode, bookID, chapterNumber)
	if err != nil {
		return "", "", err
	}
	contentSHA256 = strings.ToLower(strings.TrimSpace(contentSHA256))
	if !isSHA256(contentSHA256) {
		return "", "", ErrInvalidInput
	}
	return translationCode, contentSHA256, nil
}

func normalizeTelegraphPageURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, " \t\r\n") {
		return "", ErrInvalidInput
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
		strings.Trim(parsed.Path, "/") == "" {
		return "", ErrInvalidInput
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "telegra.ph" && host != "graph.org" {
		return "", ErrInvalidInput
	}
	parsed.Host = host
	return parsed.String(), nil
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func normalizeOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func optionalStringValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
