package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBibleChaptersMigrationDefinesStorage(t *testing.T) {
	directory := filepath.Join("..", "..", "..", "db", "migrations")
	up, err := os.ReadFile(filepath.Join(directory, "000006_bible_chapters.up.sql"))
	if err != nil {
		t.Fatalf("прочитать up-миграцию: %v", err)
	}
	down, err := os.ReadFile(filepath.Join(directory, "000006_bible_chapters.down.sql"))
	if err != nil {
		t.Fatalf("прочитать down-миграцию: %v", err)
	}

	required := []string{
		"CREATE TABLE bible_chapters",
		"verses JSONB NOT NULL",
		"source_provider TEXT NOT NULL",
		"source_version TEXT NOT NULL",
		"source_copyright TEXT",
		"content_sha256 TEXT NOT NULL",
		"fetched_at TIMESTAMPTZ NOT NULL",
		"created_at TIMESTAMPTZ NOT NULL",
		"updated_at TIMESTAMPTZ NOT NULL",
		"PRIMARY KEY (translation_code, book_id, chapter_number)",
		"CREATE TABLE bible_chapter_telegraph_links",
		"PRIMARY KEY (translation_code, book_id, chapter_number, content_sha256)",
		"REFERENCES bible_chapters",
		"ON DELETE CASCADE",
	}
	for _, fragment := range required {
		if !strings.Contains(string(up), fragment) {
			t.Errorf("up-миграция не содержит %q", fragment)
		}
	}

	if !strings.Contains(string(down), "DROP TABLE IF EXISTS bible_chapter_telegraph_links") ||
		!strings.Contains(string(down), "DROP TABLE IF EXISTS bible_chapters") {
		t.Fatal("down-миграция не удаляет обе таблицы в корректном порядке")
	}
}
