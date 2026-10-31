package telegrambot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/bibletext"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
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

type TelegraphPublisher struct {
	accessToken string
	apiURL      string
	httpClient  *http.Client

	mu    sync.Mutex
	cache map[string]string
}

type telegraphNode struct {
	Tag      string `json:"tag"`
	Children []any  `json:"children,omitempty"`
}

func NewTelegraphPublisher(accessToken string) *TelegraphPublisher {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil
	}
	return &TelegraphPublisher{
		accessToken: accessToken,
		apiURL:      telegraphAPIBaseURL,
		httpClient:  &http.Client{Timeout: 20 * time.Second},
		cache:       make(map[string]string),
	}
}

func (p *TelegraphPublisher) PublishChapter(ctx context.Context, reference ChapterReference) (string, error) {
	if p == nil || p.accessToken == "" {
		return "", errors.New("TELEGRAPH_ACCESS_TOKEN не настроен")
	}

	chapter, err := bibletext.Load(reference.BookID, reference.Chapter, reference.Translation)
	if err != nil {
		return "", err
	}
	if chapter.Chapter != reference.Chapter || bibleBookIDs[normalizeBibleBookName(chapter.Book)] != reference.BookID {
		return "", fmt.Errorf("%w: книга или номер главы не совпадает со ссылкой вопроса", bibletext.ErrInvalidChapter)
	}

	content, err := buildTelegraphChapterContent(chapter)
	if err != nil {
		return "", err
	}
	chapterBytes, err := json.Marshal(chapter)
	if err != nil {
		return "", fmt.Errorf("подготовить главу для кэширования: %w", err)
	}
	digest := sha256.Sum256(chapterBytes)
	cacheKey := fmt.Sprintf("%d:%d:%x", reference.BookID, reference.Chapter, digest)

	// Hold the lock while creating a page so concurrent requests do not publish duplicates.
	p.mu.Lock()
	defer p.mu.Unlock()
	if pageURL, ok := p.cache[cacheKey]; ok {
		return pageURL, nil
	}

	title := fmt.Sprintf("%s — глава %d", chapter.Book, chapter.Chapter)
	pageURL, err := p.createPage(ctx, title, content)
	if err != nil {
		return "", err
	}
	p.cache[cacheKey] = pageURL
	return pageURL, nil
}

func buildTelegraphChapterContent(chapter bibletext.Chapter) ([]telegraphNode, error) {
	translation := strings.TrimSpace(chapter.Translation)
	if translation == "" {
		translation = "Синодальный перевод"
	}
	nodes := make([]telegraphNode, 0, len(chapter.Verses)+1)
	nodes = append(nodes, telegraphNode{Tag: "p", Children: []any{translation}})
	for _, verse := range chapter.Verses {
		nodes = append(nodes, telegraphNode{
			Tag: "p",
			Children: []any{
				telegraphNode{Tag: "strong", Children: []any{strconv.Itoa(verse.Number) + ". "}},
				verse.Text,
			},
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

func (b *Bot) chapterTelegraphButtons(ctx context.Context, passages []domain.BiblePassage) ([]InlineKeyboardButton, string) {
	if len(passages) == 0 {
		return nil, ""
	}
	if b.chapterPublisher == nil {
		return nil, "⚙️ Ссылка на полную главу появится после настройки TELEGRAPH_ACCESS_TOKEN."
	}

	targets := bibleChapterTargets(passages)
	if len(targets) == 0 {
		return nil, "Полная глава для указанного перевода пока не подключена."
	}

	buttons := make([]InlineKeyboardButton, 0, len(targets))
	missingChapter := false
	publishFailed := false
	for _, target := range targets {
		reference := ChapterReference{
			BookID: target.bookID, BookName: target.bookName,
			Chapter: target.chapter, Translation: "synodal",
		}
		pageURL, err := b.chapterPublisher.PublishChapter(ctx, reference)
		if err != nil {
			if errors.Is(err, bibletext.ErrChapterNotFound) {
				missingChapter = true
			} else {
				publishFailed = true
				b.logger.Warn("не удалось создать статью полной главы в Telegraph",
					"book", target.bookName, "chapter", target.chapter, "error", err)
			}
			continue
		}
		if !isTelegraphPageURL(pageURL) {
			publishFailed = true
			b.logger.Warn("публикатор вернул ссылку не на статью Telegraph",
				"book", target.bookName, "chapter", target.chapter)
			continue
		}

		label := "📖 Прочитать всю главу"
		if len(targets) > 1 {
			label = fmt.Sprintf("📖 %s %d", target.bookName, target.chapter)
		}
		buttons = append(buttons, InlineKeyboardButton{Text: label, URL: pageURL})
	}

	notice := ""
	if missingChapter {
		notice = "Полный текст одной или нескольких глав ещё не добавлен в файлы проекта."
	}
	if publishFailed {
		if notice != "" {
			notice += " "
		}
		notice += "Не удалось создать статью Telegraph сейчас. Попробуйте открыть вопрос позже."
	}
	if len(buttons) == 0 && notice == "" {
		notice = "Не удалось подготовить ссылку на полную главу."
	}
	return buttons, notice
}
