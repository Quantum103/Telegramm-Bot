package telegrambot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/bibletext"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/service"
)

const (
	telegraphAPIBaseURL = "https://api.telegra.ph"
	telegraphContentMax = 64 << 10
)

type ChapterReference struct {
	BookID      int
	BookName    string
	Chapter     int
	Translation string
}

type ChapterPagePublisher interface {
	PublishChapter(context.Context, ChapterReference) (string, error)
}

type BibleChapterReader interface {
	GetChapter(context.Context, int, int) (bibletext.Chapter, error)
}

type TelegraphChapterLinkStore interface {
	GetChapter(context.Context, string, int, int) (domain.BibleChapter, error)
	GetTelegraphLink(context.Context, string, int, int, string) (domain.TelegraphChapterLink, error)
	SaveTelegraphLink(context.Context, domain.TelegraphChapterLink) error
}

type TelegraphPublisher struct {
	accessToken string
	apiURL      string
	httpClient  *http.Client
	chapters    BibleChapterReader
	linkStore   TelegraphChapterLinkStore
}

type telegraphNode struct {
	Tag      string `json:"tag"`
	Children []any  `json:"children,omitempty"`
}

// NewTelegraphPublisher сохраняет совместимость со старым способом создания публикатора.
func NewTelegraphPublisher(accessToken string) *TelegraphPublisher {
	return NewTelegraphPublisherWithDependencies(accessToken, nil, nil)
}

// NewTelegraphPublisherWithDependencies подключает источник глав и PostgreSQL-кэш Telegraph.
func NewTelegraphPublisherWithDependencies(
	accessToken string,
	chapters BibleChapterReader,
	linkStore TelegraphChapterLinkStore,
) *TelegraphPublisher {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil
	}
	return &TelegraphPublisher{
		accessToken: accessToken,
		apiURL:      telegraphAPIBaseURL,
		httpClient:  &http.Client{Timeout: 20 * time.Second},
		chapters:    chapters,
		linkStore:   linkStore,
	}
}

func (p *TelegraphPublisher) PublishChapter(ctx context.Context, reference ChapterReference) (string, error) {
	if p == nil || p.accessToken == "" {
		return "", errors.New("TELEGRAPH_ACCESS_TOKEN не настроен")
	}
	if p.chapters == nil || p.linkStore == nil {
		return "", errors.New("сервис глав Библии или PostgreSQL-кэш Telegraph не настроен")
	}
	if err := validateChapterReference(reference); err != nil {
		return "", err
	}

	// PublishChapter вызывается только фоновым worker. Получение главы и HTTP
	// публикация выполняются вне транзакций PostgreSQL и без advisory lock.
	chapter, err := p.chapters.GetChapter(ctx, reference.BookID, reference.Chapter)
	if err != nil {
		return "", err
	}
	if chapter.Chapter != reference.Chapter || strings.TrimSpace(chapter.Book) == "" {
		return "", fmt.Errorf("%w: книга или номер главы не совпадает со ссылкой вопроса", bibletext.ErrInvalidChapter)
	}

	content, err := buildTelegraphChapterContent(chapter)
	if err != nil {
		return "", err
	}
	contentSHA256, err := telegraphContentSHA256(content)
	if err != nil {
		return "", err
	}

	existing, err := p.linkStore.GetTelegraphLink(
		ctx, "synodal", reference.BookID, reference.Chapter, contentSHA256,
	)
	if err == nil {
		if !isTelegraphPageURL(existing.PageURL) {
			return "", errors.New("PostgreSQL содержит некорректную ссылку на статью Telegraph")
		}
		return existing.PageURL, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return "", fmt.Errorf("проверить готовую ссылку Telegraph: %w", err)
	}

	title := fmt.Sprintf("%s — глава %d", chapter.Book, chapter.Chapter)
	pageURL, err := p.createPage(ctx, title, content)
	if err != nil {
		return "", err
	}
	link := domain.TelegraphChapterLink{
		TranslationCode: "synodal",
		BookID: reference.BookID,
		ChapterNumber: reference.Chapter,
		ContentSHA256: contentSHA256,
		PageURL: pageURL,
	}
	// UNIQUE (translation, book, chapter, content_sha256) оставляет одну
	// каноническую ссылку. Сохранение выполняется отдельным коротким SQL-запросом.
	if err := p.linkStore.SaveTelegraphLink(ctx, link); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return "", fmt.Errorf("%w: глава ещё не сохранена в PostgreSQL", service.ErrBibleChapterUnavailable)
		}
		return "", fmt.Errorf("сохранить готовую ссылку Telegraph: %w", err)
	}
	saved, err := p.linkStore.GetTelegraphLink(
		ctx, "synodal", reference.BookID, reference.Chapter, contentSHA256,
	)
	if err != nil {
		return "", fmt.Errorf("прочитать сохранённую ссылку Telegraph: %w", err)
	}
	if !isTelegraphPageURL(saved.PageURL) {
		return "", errors.New("PostgreSQL содержит некорректную ссылку на статью Telegraph")
	}
	return saved.PageURL, nil
}

// CachedChapterURL performs PostgreSQL reads only. It never fetches Bible content
// or contacts Telegraph, so it is safe to call while rendering a Telegram screen.
func (p *TelegraphPublisher) CachedChapterURL(ctx context.Context, reference ChapterReference) (string, error) {
	if p == nil || p.linkStore == nil {
		return "", domain.ErrInvalidState
	}
	if err := validateChapterReference(reference); err != nil {
		return "", err
	}
	chapter, err := p.linkStore.GetChapter(ctx, "synodal", reference.BookID, reference.Chapter)
	if err != nil {
		return "", err
	}
	content, err := buildTelegraphChapterContent(bibleChapterToTextChapter(chapter))
	if err != nil {
		return "", err
	}
	contentSHA256, err := telegraphContentSHA256(content)
	if err != nil {
		return "", err
	}
	link, err := p.linkStore.GetTelegraphLink(
		ctx, "synodal", reference.BookID, reference.Chapter, contentSHA256,
	)
	if err != nil {
		return "", err
	}
	if !isTelegraphPageURL(link.PageURL) {
		return "", errors.New("PostgreSQL содержит некорректную ссылку на статью Telegraph")
	}
	return link.PageURL, nil
}

func validateChapterReference(reference ChapterReference) error {
	if reference.BookID < 1 || reference.BookID > 66 || reference.Chapter < 1 {
		return bibletext.ErrInvalidChapter
	}
	translation := strings.ToLower(strings.TrimSpace(reference.Translation))
	switch translation {
	case "", "syn", "synodal", "синодальный", "синодальный перевод":
		return nil
	default:
		return fmt.Errorf("%w: поддерживается только Синодальный перевод", bibletext.ErrInvalidChapter)
	}
}

func bibleChapterToTextChapter(chapter domain.BibleChapter) bibletext.Chapter {
	verses := make([]bibletext.Verse, len(chapter.Verses))
	for index, verse := range chapter.Verses {
		verses[index] = bibletext.Verse{Number: verse.Number, Text: verse.Text}
	}
	return bibletext.Chapter{
		Book: chapter.BookName,
		Chapter: chapter.ChapterNumber,
		Translation: "Синодальный перевод",
		Verses: verses,
	}
}

func telegraphContentSHA256(content []telegraphNode) (string, error) {
	encodedContent, err := json.Marshal(content)
	if err != nil {
		return "", fmt.Errorf("подготовить содержимое Telegraph для вычисления SHA256: %w", err)
	}
	digest := sha256.Sum256(encodedContent)
	return hex.EncodeToString(digest[:]), nil
}

func buildTelegraphChapterContent(chapter bibletext.Chapter) ([]telegraphNode, error) {
	nodes := make([]telegraphNode, 0, len(chapter.Verses))
	for _, verse := range chapter.Verses {
		nodes = append(nodes, telegraphNode{
			Tag:      "p",
			Children: []any{strconv.Itoa(verse.Number) + ". " + verse.Text},
		})
	}

	encoded, err := json.Marshal(nodes)
	if err != nil {
		return nil, fmt.Errorf("подготовить содержимое статьи Telegraph: %w", err)
	}
	if len(encoded) > telegraphContentMax {
		return nil, fmt.Errorf("полный текст главы превышает лимит Telegraph API в 64 КБ")
	}
	return nodes, nil
}

func (p *TelegraphPublisher) createPage(ctx context.Context, title string, content []telegraphNode) (string, error) {
	encodedContent, err := json.Marshal(content)
	if err != nil {
		return "", fmt.Errorf("подготовить запрос Telegraph: %w", err)
	}
	if len(encodedContent) > telegraphContentMax {
		return "", fmt.Errorf("содержимое статьи Telegraph превышает лимит в 64 КБ")
	}

	form := url.Values{}
	form.Set("access_token", p.accessToken)
	form.Set("title", title)
	form.Set("content", string(encodedContent))

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL+"/createPage", strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("не удалось создать запрос к Telegraph API")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")

	response, err := p.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("сетевой запрос к Telegraph API завершился ошибкой")
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", errors.New("не удалось прочитать ответ Telegraph API")
	}
	var envelope struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Result struct {
			URL string `json:"url"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", fmt.Errorf("Telegraph API вернул некорректный ответ (HTTP %d)", response.StatusCode)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices || !envelope.OK {
		if envelope.Error != "" {
			return "", fmt.Errorf("Telegraph API отклонил публикацию: %s", envelope.Error)
		}
		return "", fmt.Errorf("Telegraph API отклонил публикацию (HTTP %d)", response.StatusCode)
	}
	if !isTelegraphPageURL(envelope.Result.URL) {
		return "", errors.New("Telegraph API вернул некорректную ссылку на статью")
	}
	return envelope.Result.URL, nil
}

func isTelegraphPageURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || strings.Trim(parsed.Path, "/") == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "telegra.ph" || host == "graph.org"
}

type cachedChapterLinkReader interface {
	CachedChapterURL(context.Context, ChapterReference) (string, error)
}

func (b *Bot) chapterTelegraphLinks(ctx context.Context, passages []domain.BiblePassage) ([]string, string) {
	if len(passages) == 0 {
		return nil, ""
	}
	if b.chapterPublisher == nil {
		return nil, "Ссылка на полную главу пока не настроена."
	}
	targets := bibleChapterTargets(passages)
	if len(targets) == 0 {
		return nil, "Полная глава для указанного перевода пока не подключена."
	}
	cachedReader, ok := b.chapterPublisher.(cachedChapterLinkReader)
	if !ok {
		b.logger.Error("публикатор Telegraph не поддерживает чтение готовых ссылок")
		return nil, "Ссылка на полную главу готовится. Она появится после подготовки."
	}

	links := make([]string, 0, len(targets))
	pending := false
	unavailable := false
	for _, target := range targets {
		reference := ChapterReference{
			BookID: target.bookID, BookName: target.bookName,
			Chapter: target.chapter, Translation: "synodal",
		}
		pageURL, err := cachedReader.CachedChapterURL(ctx, reference)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				pending = true
			} else {
				unavailable = true
				b.logger.Warn("не удалось прочитать готовую ссылку полной главы из PostgreSQL",
					"book", target.bookName, "chapter", target.chapter, "error", err)
			}
			continue
		}
		if !isTelegraphPageURL(pageURL) {
			unavailable = true
			b.logger.Warn("кэш вернул некорректную ссылку на Telegraph",
				"book", target.bookName, "chapter", target.chapter)
			continue
		}
		label := "📖 Прочитать всю главу"
		if len(targets) > 1 {
			label = fmt.Sprintf("📖 %s %d", target.bookName, target.chapter)
		}
		links = append(links, fmt.Sprintf(
			`<a href="%s"><u>%s</u></a>`,
			html.EscapeString(pageURL),
			html.EscapeString(label),
		))
	}

	notice := ""
	if pending {
		notice = "Ссылка на полную главу готовится. Она появится автоматически после подготовки."
	}
	if unavailable {
		if notice != "" {
			notice += " "
		}
		notice += "Сейчас не удалось получить готовую ссылку на полную главу. Попробуйте открыть вопрос позже."
	}
	if len(links) == 0 && notice == "" {
		notice = "Ссылка на полную главу готовится. Она появится автоматически после подготовки."
	}
	return links, notice
}
