package bibletext

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func midvashTestCatalog() midvashBooksResponse {
	response := midvashBooksResponse{}
	response.Meta.Total = 66
	for id := 1; id <= 66; id++ {
		book := midvashBook{ID: id}
		book.Slugs.English = "book-" + strconv.Itoa(id)
		book.Names.English = "Book " + strconv.Itoa(id)
		book.Names.Russian = "Книга " + strconv.Itoa(id)
		if id == 40 {
			book.Slugs.English = "matthew"
			book.Names.English = "Matthew"
			book.Names.Russian = "Матфей"
		}
		response.Data = append(response.Data, book)
	}
	return response
}

func TestMidvashClientFetchChapter(t *testing.T) {
	catalog := midvashTestCatalog()
	requestedChapter := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/books":
			_ = json.NewEncoder(w).Encode(catalog)
		case "/v1/synodal/matthew/4":
			requestedChapter = true
			response := midvashChapterResponse{}
			response.Data.Version = "synodal"
			response.Data.Book = "matthew"
			response.Data.BookName = "Matthew"
			response.Data.Chapter = 4
			response.Data.Verse = 1
			response.Data.VerseEnd = 2
			response.Data.Verses = []string{"Первый стих.", "Второй стих."}
			response.Meta.Total = 2
			response.Meta.Copyright = "Public Domain"
			_ = json.NewEncoder(w).Encode(response)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewMidvashClientWithBaseURL(server.URL, server.Client())
	result, err := client.FetchChapter(context.Background(), 40, 4)
	if err != nil {
		t.Fatalf("FetchChapter вернул ошибку: %v", err)
	}
	if !requestedChapter {
		t.Fatal("клиент не запросил ожидаемую главу")
	}
	if result.Provider != "midvash" || result.Version != "synodal" {
		t.Fatalf("неожиданные метаданные источника: %+v", result)
	}
	if result.Chapter.Book != "Матфей" || result.Chapter.Chapter != 4 ||
		result.Chapter.Translation != "Синодальный перевод" {
		t.Fatalf("неверно преобразована глава: %+v", result.Chapter)
	}
	if len(result.Chapter.Verses) != 2 || result.Chapter.Verses[1].Number != 2 ||
		result.Chapter.Verses[1].Text != "Второй стих." {
		t.Fatalf("неверно преобразованы стихи: %+v", result.Chapter.Verses)
	}
	if result.Copyright == nil || *result.Copyright != "Public Domain" {
		t.Fatalf("не перенесены сведения об авторском статусе: %+v", result.Copyright)
	}
}

func TestMidvashClientRejectsDifferentTranslation(t *testing.T) {
	catalog := midvashTestCatalog()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/books" {
			_ = json.NewEncoder(w).Encode(catalog)
			return
		}
		response := midvashChapterResponse{}
		response.Data.Version = "kjv"
		response.Data.Book = "matthew"
		response.Data.BookName = "Matthew"
		response.Data.Chapter = 4
		response.Data.Verse = 1
		response.Data.VerseEnd = 1
		response.Data.Verses = []string{"Another translation."}
		response.Meta.Total = 1
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	_, err := NewMidvashClientWithBaseURL(server.URL, server.Client()).FetchChapter(context.Background(), 40, 4)
	if err == nil || !strings.Contains(err.Error(), "вместо synodal") {
		t.Fatalf("ожидался отказ от другого перевода, получено: %v", err)
	}
}

func TestMidvashClientRejectsIncompleteChapter(t *testing.T) {
	catalog := midvashTestCatalog()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/books" {
			_ = json.NewEncoder(w).Encode(catalog)
			return
		}
		response := midvashChapterResponse{}
		response.Data.Version = "synodal"
		response.Data.Book = "matthew"
		response.Data.BookName = "Matthew"
		response.Data.Chapter = 4
		response.Data.Verse = 1
		response.Data.VerseEnd = 2
		response.Data.Verses = []string{"Один стих."}
		response.Meta.Total = 1
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	_, err := NewMidvashClientWithBaseURL(server.URL, server.Client()).FetchChapter(context.Background(), 40, 4)
	if err == nil {
		t.Fatal("ожидалась ошибка для неполной главы")
	}
}
