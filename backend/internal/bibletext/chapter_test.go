package bibletext

import (
	"errors"
	"testing"
)

func TestDecodeChapterAcceptsCompleteSequentialVerses(t *testing.T) {
	content := []byte(`{
		"book":"Матфея",
		"chapter":4,
		"translation":"Синодальный перевод",
		"verses":[
			{"number":1,"text":"Первый стих."},
			{"number":2,"text":"Второй стих."}
		]
	}`)

	chapter, err := decodeChapter(content, 4)
	if err != nil {
		t.Fatalf("decodeChapter() error = %v", err)
	}
	if chapter.Book != "Матфея" || chapter.Chapter != 4 || len(chapter.Verses) != 2 {
		t.Fatalf("unexpected decoded chapter: %+v", chapter)
	}
}

func TestDecodeChapterRejectsMissingVerseNumber(t *testing.T) {
	content := []byte(`{
		"book":"Матфея",
		"chapter":4,
		"verses":[
			{"number":1,"text":"Первый стих."},
			{"number":3,"text":"Третий стих."}
		]
	}`)

	_, err := decodeChapter(content, 4)
	if !errors.Is(err, ErrInvalidChapter) {
		t.Fatalf("decodeChapter() error = %v, want ErrInvalidChapter", err)
	}
}

func TestDecodeChapterRejectsDifferentChapterNumber(t *testing.T) {
	content := []byte(`{
		"book":"Матфея",
		"chapter":5,
		"verses":[{"number":1,"text":"Стих."}]
	}`)

	_, err := decodeChapter(content, 4)
	if !errors.Is(err, ErrInvalidChapter) {
		t.Fatalf("decodeChapter() error = %v, want ErrInvalidChapter", err)
	}
}

func TestDecodeChapterRejectsEmptyVerseText(t *testing.T) {
	content := []byte(`{
		"book":"Матфея",
		"chapter":4,
		"verses":[{"number":1,"text":"  "}]
	}`)

	_, err := decodeChapter(content, 4)
	if !errors.Is(err, ErrInvalidChapter) {
		t.Fatalf("decodeChapter() error = %v, want ErrInvalidChapter", err)
	}
}
