package bibletext

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

var (
	ErrChapterNotFound = errors.New("полная глава не найдена")
	ErrInvalidChapter  = errors.New("некорректный файл главы")
)

//go:embed chapters
var chapterFiles embed.FS

type Verse struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
}

type Chapter struct {
	Book        string  `json:"book"`
	Chapter     int     `json:"chapter"`
	Translation string  `json:"translation"`
	Verses      []Verse `json:"verses"`
}

// Load reads a complete embedded chapter. At present only the Synodal translation is supported.
func Load(bookID, chapter int, translation string) (Chapter, error) {
	if bookID < 1 || bookID > 66 || chapter < 1 {
		return Chapter{}, fmt.Errorf("%w: неверный номер книги или главы", ErrInvalidChapter)
	}

	switch strings.ToLower(strings.TrimSpace(translation)) {
	case "", "syn", "synodal", "синодальный", "синодальный перевод":
	default:
		return Chapter{}, fmt.Errorf("%w: неподдерживаемый перевод %q", ErrInvalidChapter, translation)
	}

	filePath := path.Join("chapters", "synodal", strconv.Itoa(bookID), strconv.Itoa(chapter)+".json")
	data, err := chapterFiles.ReadFile(filePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Chapter{}, fmt.Errorf("%w: %s", ErrChapterNotFound, filePath)
		}
		return Chapter{}, fmt.Errorf("прочитать файл главы: %w", err)
	}
	return decodeChapter(data, chapter)
}

func decodeChapter(data []byte, expectedChapter int) (Chapter, error) {
	var chapter Chapter
	if err := json.Unmarshal(data, &chapter); err != nil {
		return Chapter{}, fmt.Errorf("%w: JSON: %v", ErrInvalidChapter, err)
	}
	chapter.Book = strings.TrimSpace(chapter.Book)
	chapter.Translation = strings.TrimSpace(chapter.Translation)
	if chapter.Book == "" || chapter.Chapter != expectedChapter || expectedChapter < 1 {
		return Chapter{}, fmt.Errorf("%w: название книги или номер главы не совпадает", ErrInvalidChapter)
	}
	if len(chapter.Verses) == 0 || len(chapter.Verses) > 200 {
		return Chapter{}, fmt.Errorf("%w: в главе должно быть от 1 до 200 стихов", ErrInvalidChapter)
	}
	for index, verse := range chapter.Verses {
		if verse.Number != index+1 || strings.TrimSpace(verse.Text) == "" || !utf8.ValidString(verse.Text) {
			return Chapter{}, fmt.Errorf("%w: стихи должны быть непустыми и идти подряд с номера 1", ErrInvalidChapter)
		}
	}
	if chapter.Translation == "" {
		chapter.Translation = "Синодальный перевод"
	}
	return chapter, nil
}
