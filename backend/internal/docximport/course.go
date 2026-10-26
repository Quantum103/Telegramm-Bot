package docximport

import (
	"errors"
	"strings"
)

const maxImportedLessons = 100

var ErrInvalidStructure = errors.New("invalid course document structure")

// CourseImport is a validated, in-memory representation of a course import.
// It does not write to storage or publish any content.
type CourseImport struct {
	Slug        string
	Title       string
	Description *string
	Lessons     []LessonImport
}

// LessonImport contains the title and optional plain-text description of one lesson.
type LessonImport struct {
	Title       string
	Description *string
}

// ParseCourse parses extracted DOCX text using this explicit paragraph format:
//
//	Курс: <название>
//	Slug: <lowercase-ascii-slug>
//	Описание курса:     (optional; following paragraphs form the course description)
//	Урок: <lesson title>
//	(following paragraphs form the lesson description, until the next Урок: marker)
//
// The title and slug lines are required and must appear first in that order.
// Lesson descriptions are optional. The parser only validates and structures data;
// it never persists or publishes it.
func ParseCourse(text string) (CourseImport, error) {
	if len(text) == 0 || len(text) > maxExtractedBytes {
		if len(text) > maxExtractedBytes {
			return CourseImport{}, ErrTooLarge
		}
		return CourseImport{}, ErrInvalidStructure
	}

	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	index := nextNonEmptyLine(lines, 0)
	if index >= len(lines) || !strings.HasPrefix(lines[index], "Курс: ") {
		return CourseImport{}, ErrInvalidStructure
	}
	title := strings.TrimSpace(strings.TrimPrefix(lines[index], "Курс: "))
	if title == "" || len(title) > 200 {
		return CourseImport{}, ErrInvalidStructure
	}

	index = nextNonEmptyLine(lines, index+1)
	if index >= len(lines) || !strings.HasPrefix(lines[index], "Slug: ") {
		return CourseImport{}, ErrInvalidStructure
	}
	slug := strings.TrimSpace(strings.TrimPrefix(lines[index], "Slug: "))
	if !validImportSlug(slug) {
		return CourseImport{}, ErrInvalidStructure
	}

	result := CourseImport{
		Slug:    slug,
		Title:   title,
		Lessons: make([]LessonImport, 0),
	}
	index = nextNonEmptyLine(lines, index+1)

	if index < len(lines) && lines[index] == "Описание курса:" {
		index++
		var description []string
		for index < len(lines) {
			line := strings.TrimSpace(lines[index])
			if strings.HasPrefix(line, "Урок: ") {
				break
			}
			if line == "Описание курса:" {
				return CourseImport{}, ErrInvalidStructure
			}
			if line != "" {
				description = append(description, line)
			}
			index++
		}
		value := strings.TrimSpace(strings.Join(description, "\n"))
		if len(value) > 4000 {
			return CourseImport{}, ErrInvalidStructure
		}
		if value != "" {
			result.Description = &value
		}
		index = nextNonEmptyLine(lines, index)
	}

	for index < len(lines) {
		line := strings.TrimSpace(lines[index])
		if !strings.HasPrefix(line, "Урок: ") {
			return CourseImport{}, ErrInvalidStructure
		}
		lessonTitle := strings.TrimSpace(strings.TrimPrefix(line, "Урок: "))
		if lessonTitle == "" || len(lessonTitle) > 200 || len(result.Lessons) >= maxImportedLessons {
			return CourseImport{}, ErrInvalidStructure
		}
		index++

		var description []string
		for index < len(lines) {
			line = strings.TrimSpace(lines[index])
			if strings.HasPrefix(line, "Урок: ") {
				break
			}
			if line != "" {
				description = append(description, line)
			}
			index++
		}
		descriptionText := strings.TrimSpace(strings.Join(description, "\n"))
		if len(descriptionText) > 4000 {
			return CourseImport{}, ErrInvalidStructure
		}
		lesson := LessonImport{Title: lessonTitle}
		if descriptionText != "" {
			lesson.Description = &descriptionText
		}
		result.Lessons = append(result.Lessons, lesson)
		index = nextNonEmptyLine(lines, index)
	}

	if len(result.Lessons) == 0 {
		return CourseImport{}, ErrInvalidStructure
	}
	return result, nil
}

func nextNonEmptyLine(lines []string, index int) int {
	for index < len(lines) && strings.TrimSpace(lines[index]) == "" {
		index++
	}
	return index
}

func validImportSlug(slug string) bool {
	if slug == "" || len(slug) > 100 || strings.HasPrefix(slug, "-") || strings.HasSuffix(slug, "-") {
		return false
	}
	previousHyphen := false
	for _, char := range slug {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
			previousHyphen = false
		case char == '-' && !previousHyphen:
			previousHyphen = true
		default:
			return false
		}
	}
	return true
}
