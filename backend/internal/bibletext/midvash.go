package bibletext

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

const (
	midvashBaseURL  = "https://api.midvash.com"
	midvashBooksURL = "/v1/books"
)

type MidvashClient struct {
	baseURL    string
	httpClient *http.Client
	mu         sync.Mutex
	books      map[int]midvashBook
}

type midvashBooksResponse struct {
	Data []midvashBook `json:"data"`
	Meta struct {
		Total int `json:"total"`
	} `json:"meta"`
}

type midvashBook struct {
	ID    int `json:"id"`
	Slugs struct {
		English string `json:"en"`
	} `json:"slug"`
	Names struct {
		English string `json:"en"`
		Russian string `json:"ru"`
	} `json:"name"`
}

type midvashChapterResponse struct {
	Data struct {
		Version  string   `json:"version"`
		Book     string   `json:"book"`
		BookName string   `json:"bookName"`
		Chapter  int      `json:"chapter"`
		Verse    int      `json:"verse"`
		VerseEnd int      `json:"verseEnd"`
		Verses   []string `json:"verses"`
	} `json:"data"`
	Meta struct {
		Total     int    `json:"total"`
		Copyright string `json:"copyright"`
	} `json:"meta"`
}

func NewMidvashClient(client *http.Client) *MidvashClient {
	return NewMidvashClientWithBaseURL(midvashBaseURL, client)
}

// NewMidvashClientWithBaseURL позволяет тестировать адаптер на httptest.Server.
func NewMidvashClientWithBaseURL(baseURL string, client *http.Client) *MidvashClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = midvashBaseURL
	}
	return &MidvashClient{
		baseURL:    baseURL,
		httpClient: newSourceHTTPClient(client),
	}
}

func (c *MidvashClient) FetchChapter(
	ctx context.Context,
	bookID, chapterNumber int,
) (SourceChapter, error) {
	if bookID < 1 || bookID > 66 || chapterNumber < 1 {
		return SourceChapter{}, fmt.Errorf("Midvash: некорректная ссылка на главу")
	}

	book, err := c.getBook(ctx, bookID)
	if err != nil {
		return SourceChapter{}, err
	}

	endpoint, err := url.JoinPath(c.baseURL, "v1", "synodal", book.Slugs.English, strconv.Itoa(chapterNumber))
	if err != nil {
		return SourceChapter{}, fmt.Errorf("Midvash: не удалось сформировать URL главы")
	}

	var response midvashChapterResponse
	if err := fetchSourceJSON(ctx, c.httpClient, endpoint, "Midvash", &response); err != nil {
		return SourceChapter{}, err
	}

	data := response.Data
	if data.Version != "synodal" {
		return SourceChapter{}, fmt.Errorf("Midvash: API вернул перевод %q вместо synodal", data.Version)
	}
	if data.Book != book.Slugs.English || !strings.EqualFold(data.BookName, book.Names.English) {
		return SourceChapter{}, fmt.Errorf("Midvash: API вернул другую книгу")
	}
	if data.Chapter != chapterNumber || data.Verse != 1 || data.VerseEnd != len(data.Verses) {
		return SourceChapter{}, fmt.Errorf("Midvash: номер главы или диапазон стихов не совпадает с запросом")
	}
	if len(data.Verses) == 0 || len(data.Verses) > 200 || response.Meta.Total != len(data.Verses) {
		return SourceChapter{}, fmt.Errorf("Midvash: глава неполная или содержит некорректное количество стихов")
	}

	verses := make([]Verse, len(data.Verses))
	for index, text := range data.Verses {
		verses[index] = Verse{Number: index + 1, Text: text}
	}

	return SourceChapter{
		Chapter: Chapter{
			Book:        book.Names.Russian,
			Chapter:     chapterNumber,
			Translation: "Синодальный перевод",
			Verses:      verses,
		},
		Provider:  "midvash",
		Version:   data.Version,
		Copyright: normalizeSourceCopyright(response.Meta.Copyright),
	}, nil
}

func (c *MidvashClient) getBook(ctx context.Context, bookID int) (midvashBook, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if book, ok := c.books[bookID]; ok {
		return book, nil
	}

	var response midvashBooksResponse
	endpoint := c.baseURL + midvashBooksURL
	if err := fetchSourceJSON(ctx, c.httpClient, endpoint, "Midvash", &response); err != nil {
		return midvashBook{}, fmt.Errorf("Midvash: не удалось получить каталог книг: %w", err)
	}
	if len(response.Data) != 66 || (response.Meta.Total != 0 && response.Meta.Total != 66) {
		return midvashBook{}, fmt.Errorf("Midvash: каталог не содержит ровно 66 канонических книг")
	}

	books := make(map[int]midvashBook, 66)
	for _, book := range response.Data {
		if book.ID < 1 || book.ID > 66 || book.Slugs.English == "" ||
			book.Names.English == "" || book.Names.Russian == "" {
			return midvashBook{}, fmt.Errorf("Midvash: каталог содержит некорректные данные книги")
		}
		if _, exists := books[book.ID]; exists {
			return midvashBook{}, fmt.Errorf("Midvash: каталог содержит повторяющийся ID книги")
		}
		books[book.ID] = book
	}
	if len(books) != 66 {
		return midvashBook{}, fmt.Errorf("Midvash: каталог книг неполный")
	}

	c.books = books
	book, ok := c.books[bookID]
	if !ok {
		return midvashBook{}, fmt.Errorf("Midvash: книга с ID %d отсутствует в каталоге", bookID)
	}
	return book, nil
}
