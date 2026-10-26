package docximport

import (
	"errors"
	"strings"
	"testing"
)

func TestParseCourseStructuresCourseAndLessons(t *testing.T) {
	input := strings.Join([]string{
		"Курс: Основы Go",
		"Slug: osnovy-go",
		"Описание курса:",
		"Введение в язык.",
		"Практический курс.",
		"",
		"Урок: Переменные",
		"Объявление переменных.",
		"Примеры и упражнения.",
		"",
		"Урок: Типы данных",
		"Основные типы данных.",
	}, "\n")

	got, err := ParseCourse(input)
	if err != nil {
		t.Fatalf("ParseCourse() error = %v", err)
	}
	if got.Title != "Основы Go" || got.Slug != "osnovy-go" {
		t.Fatalf("unexpected course metadata: %+v", got)
	}
	if got.Description == nil || *got.Description != "Введение в язык.\nПрактический курс." {
		t.Fatalf("unexpected course description: %#v", got.Description)
	}
	if len(got.Lessons) != 2 {
		t.Fatalf("lesson count = %d, want 2", len(got.Lessons))
	}
	if got.Lessons[0].Title != "Переменные" || got.Lessons[0].Description == nil ||
		*got.Lessons[0].Description != "Объявление переменных.\nПримеры и упражнения." {
		t.Fatalf("unexpected first lesson: %+v", got.Lessons[0])
	}
	if got.Lessons[1].Title != "Типы данных" || got.Lessons[1].Description == nil ||
		*got.Lessons[1].Description != "Основные типы данных." {
		t.Fatalf("unexpected second lesson: %+v", got.Lessons[1])
	}
}

func TestParseCourseAllowsOptionalDescriptions(t *testing.T) {
	got, err := ParseCourse("Курс: Курс\nSlug: course\nУрок: Урок")
	if err != nil {
		t.Fatalf("ParseCourse() error = %v", err)
	}
	if got.Description != nil || len(got.Lessons) != 1 || got.Lessons[0].Description != nil {
		t.Fatalf("unexpected optional descriptions: %+v", got)
	}
}

func TestParseCourseRejectsInvalidStructure(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{name: "empty", text: ""},
		{name: "missing course marker", text: "Название\nSlug: course\nУрок: Первый"},
		{name: "empty course title", text: "Курс:   \nSlug: course\nУрок: Первый"},
		{name: "missing slug", text: "Курс: Курс\nУрок: Первый"},
		{name: "uppercase slug", text: "Курс: Курс\nSlug: Course\nУрок: Первый"},
		{name: "slug with repeated hyphen", text: "Курс: Курс\nSlug: my--course\nУрок: Первый"},
		{name: "no lessons", text: "Курс: Курс\nSlug: course"},
		{name: "empty lesson title", text: "Курс: Курс\nSlug: course\nУрок:   "},
		{name: "unexpected text before lesson", text: "Курс: Курс\nSlug: course\nнеожиданный текст\nУрок: Первый"},
		{name: "repeated course description marker", text: "Курс: Курс\nSlug: course\nОписание курса:\nОписание курса:\nУрок: Первый"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseCourse(test.text); !errors.Is(err, ErrInvalidStructure) {
				t.Fatalf("ParseCourse() error = %v, want ErrInvalidStructure", err)
			}
		})
	}
}

func TestParseCourseEnforcesLimits(t *testing.T) {
	tooLongTitle := "Курс: " + strings.Repeat("я", 201) + "\nSlug: course\nУрок: Первый"
	if _, err := ParseCourse(tooLongTitle); !errors.Is(err, ErrInvalidStructure) {
		t.Fatalf("long title error = %v, want ErrInvalidStructure", err)
	}

	tooLongDescription := "Курс: Курс\nSlug: course\nОписание курса:\n" +
		strings.Repeat("x", 4001) + "\nУрок: Первый"
	if _, err := ParseCourse(tooLongDescription); !errors.Is(err, ErrInvalidStructure) {
		t.Fatalf("long course description error = %v, want ErrInvalidStructure", err)
	}

	tooLongLessonDescription := "Курс: Курс\nSlug: course\nУрок: Первый\n" +
		strings.Repeat("x", 4001)
	if _, err := ParseCourse(tooLongLessonDescription); !errors.Is(err, ErrInvalidStructure) {
		t.Fatalf("long lesson description error = %v, want ErrInvalidStructure", err)
	}

	if _, err := ParseCourse(strings.Repeat("x", maxExtractedBytes+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized input error = %v, want ErrTooLarge", err)
	}
}

func TestParseCourseRejectsTooManyLessons(t *testing.T) {
	var input strings.Builder
	input.WriteString("Курс: Курс\nSlug: course\n")
	for i := 0; i <= maxImportedLessons; i++ {
		input.WriteString("Урок: Урок ")
		input.WriteString(strings.Repeat("x", 1))
		input.WriteByte('\n')
	}
	if _, err := ParseCourse(input.String()); !errors.Is(err, ErrInvalidStructure) {
		t.Fatalf("ParseCourse() error = %v, want ErrInvalidStructure", err)
	}
}
