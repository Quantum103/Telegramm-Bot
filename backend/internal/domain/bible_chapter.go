package domain

import "time"

// BibleVerse хранит один пронумерованный стих полной главы.
type BibleVerse struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
}

// BibleChapter — сохранённая версия главы библейского перевода.
type BibleChapter struct {
	TranslationCode string       `json:"translation_code"`
	BookID          int          `json:"book_id"`
	BookName        string       `json:"book_name"`
	ChapterNumber   int          `json:"chapter_number"`
	Verses          []BibleVerse `json:"verses"`
	SourceProvider  string       `json:"source_provider"`
	SourceVersion   string       `json:"source_version"`
	SourceCopyright *string      `json:"source_copyright,omitempty"`
	ContentSHA256   string       `json:"content_sha256"`
	FetchedAt       time.Time    `json:"fetched_at"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
}

// TelegraphChapterLink связывает конкретную версию главы со статьёй Telegraph.
type TelegraphChapterLink struct {
	TranslationCode string    `json:"translation_code"`
	BookID          int       `json:"book_id"`
	ChapterNumber   int       `json:"chapter_number"`
	ContentSHA256   string    `json:"content_sha256"`
	PageURL         string    `json:"page_url"`
	CreatedAt       time.Time `json:"created_at"`
}
