package bibletext

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const getBibleBaseURL = "https://api.getbible.net/v2"

type GetBibleClient struct {
	baseURL    string
	httpClient *http.Client
}

type getBibleChapterResponse struct {
	Translation  string          `json:"translation"`
	Abbreviation string          `json:"abbreviation"`
	LanguageCode string          `json:"lang"`
	BookNumber   int             `json:"book_nr"`
	BookName     string          `json:"book_name"`
	Chapter      int             `json:"chapter"`
	Verses       []getBibleVerse `json:"verses"`
}

type getBibleVerse struct {
	Chapter int    `json:"chapter"`
	Number  int    `json:"verse"`
	Text    string `json:"text"`
}

func NewGetBibleClient(client *http.Client) *GetBibleClient {
	return NewGetBibleClientWithBaseURL(getBibleBaseURL, client)
}

// NewGetBibleClientWithBaseURL позволяет тестировать адаптер на httptest.Server.
func NewGetBibleClientWithBaseURL(baseURL string, client *http.Client) *GetBibleClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = getBibleBaseURL
	}
	return &GetBibleClient{
		baseURL:    baseURL,
		httpClient: newSourceHTTPClient(client),
	}
}

func (c *GetBibleClient) FetchChapter(
	ctx context.Context,
	bookID, chapterNumber int,
) (SourceChapter, error) {
	if bookID < 1 || bookID > 66 || chapterNumber < 1 {
		return SourceChapter{}, fmt.Errorf("GetBible: некорректная ссылка на главу")
	}

	endpoint, err := url.JoinPath(
		c.baseURL,
		"synodal",
		strconv.Itoa(bookID),
		strconv.Itoa(chapterNumber)+".json",
	)
	if err != nil {
		return SourceChapter{}, fmt.Errorf("GetBible: не удалось сформировать URL главы")
	}

	var response getBibleChapterResponse
	if err := fetchSourceJSON(ctx, c.httpClient, endpoint, "GetBible", &response); err != nil {
		return SourceChapter{}, err
	}

	if response.Translation != "Synodal Translation (1876)" ||
		response.Abbreviation != "synodal" ||
		response.LanguageCode != "ru" {
		return SourceChapter{}, fmt.Errorf("GetBible: API вернул не Синодальный перевод 1876 года")
	}
	if response.BookNumber != bookID || strings.TrimSpace(response.BookName) == "" {
		return SourceChapter{}, fmt.Errorf("GetBible: API вернул другую книгу")
	}
	if response.Chapter != chapterNumber {
		return SourceChapter{}, fmt.Errorf("GetBible: API вернул другую главу")
	}
	if len(response.Verses) == 0 || len(response.Verses) > 200 {
		return SourceChapter{}, fmt.Errorf("GetBible: глава неполная или содержит некорректное количество стихов")
	}

	verses := make([]Verse, len(response.Verses))
	for index, verse := range response.Verses {
		if verse.Chapter != chapterNumber || verse.Number != index+1 {
			return SourceChapter{}, fmt.Errorf("GetBible: нумерация стихов не совпадает с запросом")
		}
		verses[index] = Verse{Number: verse.Number, Text: verse.Text}
	}

	return SourceChapter{
		Chapter: Chapter{
			Book:        strings.TrimSpace(response.BookName),
			Chapter:     chapterNumber,
			Translation: "Синодальный перевод",
			Verses:      verses,
		},
		Provider: "getbible",
		Version:  response.Translation,
	}, nil
}
