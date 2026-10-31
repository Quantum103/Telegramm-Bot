package bibletext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	sourceRequestTimeout = 8 * time.Second
	maxSourceResponseSize = 2 << 20
)

var ErrSourceResponseTooLarge = errors.New("ответ источника Библии превышает допустимый размер")

// SourceChapter связывает общую модель главы с метаданными провайдера.
type SourceChapter struct {
	Chapter   Chapter
	Provider  string
	Version   string
	Copyright *string
}

// ChapterSource загружает полную главу для книги из стандартного набора 66 книг.
type ChapterSource interface {
	FetchChapter(ctx context.Context, bookID, chapterNumber int) (SourceChapter, error)
}

func newSourceHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: sourceRequestTimeout}
}

func fetchSourceJSON(ctx context.Context, client *http.Client, endpoint, provider string, destination any) error {
	requestCtx, cancel := context.WithTimeout(ctx, sourceRequestTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("%s: не удалось подготовить HTTP-запрос", provider)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Quantum103-Telegramm-Bot/1.0")

	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(requestCtx.Err(), context.Canceled) {
			return fmt.Errorf("%s: запрос отменён: %w", provider, context.Canceled)
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%s: превышено время ожидания HTTP-ответа", provider)
		}
		return fmt.Errorf("%s: HTTP-запрос не выполнен", provider)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("%s: API вернул HTTP %d", provider, response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxSourceResponseSize+1))
	if err != nil {
		return fmt.Errorf("%s: не удалось прочитать HTTP-ответ", provider)
	}
	if len(body) > maxSourceResponseSize {
		return fmt.Errorf("%s: %w", provider, ErrSourceResponseTooLarge)
	}
	if !utf8.Valid(body) {
		return fmt.Errorf("%s: HTTP-ответ содержит некорректный UTF-8", provider)
	}
	if err := json.Unmarshal(body, destination); err != nil {
		return fmt.Errorf("%s: API вернул некорректный JSON", provider)
	}
	return nil
}

func normalizeSourceCopyright(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}
