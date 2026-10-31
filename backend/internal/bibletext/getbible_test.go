package bibletext

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func validGetBibleTestResponse() getBibleChapterResponse {
	return getBibleChapterResponse{
		Translation:  "Synodal Translation (1876)",
		Abbreviation: "synodal",
		LanguageCode: "ru",
		BookNumber:   40,
		BookName:     "Матфей",
		Chapter:      4,
		Verses: []getBibleVerse{
			{Chapter: 4, Number: 1, Text: "Первый стих."},
			{Chapter: 4, Number: 2, Text: "Второй стих."},
		},
	}
}

func TestGetBibleClientFetchChapter(t *testing.T) {
	requestedPath := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(validGetBibleTestResponse())
	}))
	defer server.Close()

	result, err := NewGetBibleClientWithBaseURL(server.URL, server.Client()).FetchChapter(context.Background(), 40, 4)
	if err != nil {
		t.Fatalf("FetchChapter вернул ошибку: %v", err)
	}
	if requestedPath != "/synodal/40/4.json" {
		t.Fatalf("запрошен URL %q, ожидался /synodal/40/4.json", requestedPath)
	}
	if result.Provider != "getbible" || result.Version != "Synodal Translation (1876)" {
		t.Fatalf("неожиданные метаданные: %+v", result)
	}
	if result.Chapter.Book != "Матфей" || result.Chapter.Chapter != 4 ||
		result.Chapter.Translation != "Синодальный перевод" || len(result.Chapter.Verses) != 2 {
		t.Fatalf("неверно преобразована глава: %+v", result.Chapter)
	}
	if result.Chapter.Verses[1].Number != 2 || result.Chapter.Verses[1].Text != "Второй стих." {
		t.Fatalf("неверно преобразованы стихи: %+v", result.Chapter.Verses)
	}
}

func TestGetBibleClientRejectsDifferentTranslation(t *testing.T) {
	response := validGetBibleTestResponse()
	response.Translation = "Russian Synodal Revision"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	_, err := NewGetBibleClientWithBaseURL(server.URL, server.Client()).FetchChapter(context.Background(), 40, 4)
	if err == nil || !strings.Contains(err.Error(), "не Синодальный перевод 1876 года") {
		t.Fatalf("ожидался отказ от другого перевода, получено: %v", err)
	}
}

func TestGetBibleClientRejectsDifferentBookOrVerseSequence(t *testing.T) {
	tests := []struct {
		name   string
		change func(*getBibleChapterResponse)
	}{
		{
			name: "другая книга",
			change: func(response *getBibleChapterResponse) {
				response.BookNumber = 19
			},
		},
		{
			name: "непоследовательные стихи",
			change: func(response *getBibleChapterResponse) {
				response.Verses[1].Number = 3
			},
		},
		{
			name: "стих из другой главы",
			change: func(response *getBibleChapterResponse) {
				response.Verses[0].Chapter = 3
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := validGetBibleTestResponse()
			test.change(&response)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()

			_, err := NewGetBibleClientWithBaseURL(server.URL, server.Client()).FetchChapter(context.Background(), 40, 4)
			if err == nil {
				t.Fatal("ожидалась ошибка валидации")
			}
		})
	}
}

func TestGetBibleClientRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxSourceResponseSize+1)))
	}))
	defer server.Close()

	_, err := NewGetBibleClientWithBaseURL(server.URL, server.Client()).FetchChapter(context.Background(), 40, 4)
	if err == nil || !strings.Contains(err.Error(), ErrSourceResponseTooLarge.Error()) {
		t.Fatalf("ожидался отказ от слишком большого ответа, получено: %v", err)
	}
}

func TestGetBibleClientDoesNotExposeErrorResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "секретный текст ответа", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := NewGetBibleClientWithBaseURL(server.URL, server.Client()).FetchChapter(context.Background(), 40, 4)
	if err == nil || strings.Contains(err.Error(), "секретный текст") {
		t.Fatalf("ошибка должна содержать только безопасный статус HTTP: %v", err)
	}
}
