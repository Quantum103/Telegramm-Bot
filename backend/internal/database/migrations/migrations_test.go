package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationsHaveMatchingDirections(t *testing.T) {
	directory := filepath.Join("..", "..", "..", "db", "migrations")
	upFiles, err := filepath.Glob(filepath.Join(directory, "*.up.sql"))
	if err != nil {
		t.Fatalf("найти up-миграции: %v", err)
	}
	if len(upFiles) == 0 {
		t.Fatal("не найдено ни одной up-миграции")
	}

	for _, upPath := range upFiles {
		base := strings.TrimSuffix(upPath, ".up.sql")
		downPath := base + ".down.sql"
		if info, err := os.Stat(downPath); err != nil || info.IsDir() {
			t.Errorf("для %s отсутствует down-миграция %s", filepath.Base(upPath), filepath.Base(downPath))
		}
		content, err := os.ReadFile(upPath)
		if err != nil {
			t.Fatalf("прочитать %s: %v", upPath, err)
		}
		required := []string{}
		switch {
		case strings.HasPrefix(filepath.Base(upPath), "000001_"):
			required = []string{
				"CREATE TABLE students",
				"CREATE TABLE lesson_versions",
				"CREATE TABLE lesson_questions",
				"CREATE TABLE lesson_enrollments",
				"CREATE TABLE student_answers",
				"CREATE TABLE answer_attempts",
				"CREATE TABLE conversations",
				"CREATE TABLE ai_operations",
				"CREATE TABLE audit_events",
				"FOREIGN KEY",
				"UNIQUE",
			}
		case strings.HasPrefix(filepath.Base(upPath), "000002_"):
			required = []string{
				"ALTER TABLE answer_attempts",
				"evaluation_result",
				"uncertain",
			}
		}
		for _, item := range required {
			if !strings.Contains(string(content), item) {
				t.Errorf("%s не содержит обязательный элемент %q", filepath.Base(upPath), item)
			}
		}
	}
}
