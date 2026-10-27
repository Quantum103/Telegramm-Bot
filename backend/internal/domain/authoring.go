package domain

import "time"

// CreateCourseParams задаёт содержимое нового чернового курса.
type CreateCourseParams struct {
	Slug             string
	Title            string
	Description      *string
	CreatedByStaffID int64
}

// CreateLessonParams создаёт самостоятельный урок и его первую черновую версию.
type CreateLessonParams struct {
	CourseID         int64
	Title            string
	Description      *string
	CreatedByStaffID int64
}

// ImportDraftLessonParams — проверенное содержимое урока для атомарного импорта в черновик.
type ImportDraftLessonParams struct {
	CourseID         int64
	Title            string
	Description      *string
	Questions        []ImportDraftQuestionParams
	CreatedByStaffID int64
}

type ImportDraftQuestionParams struct {
	Number              int
	Prompt              string
	AnswerType          string
	ExpectedAnswer      string
	ExpectedExplanation *string
	Note                *string
	Options             []ImportDraftOptionInput
	BiblePassages       []BiblePassageInput
}

type ImportDraftOptionInput struct {
	Text      string
	IsCorrect bool
}

type QuestionCriterionInput struct {
	Key         string
	Description string
	Weight      float64
	Required    *bool
}

type QuestionOptionInput struct {
	Key       string
	Text      string
	IsCorrect bool
}

type BiblePassageInput struct {
	BookName        string
	Chapter         int
	StartVerse      int
	EndVerse        int
	TranslationName *string
	PassageText     *string
	SourceNote      *string
}

// CreateQuestionParams описывает только входные материалы, предоставленные автором.
type CreateQuestionParams struct {
	LessonVersionID     int64
	QuestionNumber      int
	Prompt              string
	ExpectedAnswer      string
	AnswerType          string
	ExpectedExplanation *string
	Note                *string
	Criteria            []QuestionCriterionInput
	Options             []QuestionOptionInput
	CommonMistakes      []CommonMistake
	BiblePassages       []BiblePassageInput
	CreatedByStaffID    int64
}

// StaffLesson is the latest lesson version as exposed only to authenticated staff.
type StaffLesson struct {
	ID              int64      `json:"id"`
	CourseID        int64      `json:"course_id"`
	CourseTitle     string     `json:"course_title"`
	Position        int        `json:"position"`
	LessonVersionID int64      `json:"lesson_version_id"`
	VersionNumber   int        `json:"version_number"`
	VersionStatus   string     `json:"version_status"`
	Title           string     `json:"title"`
	Description     *string    `json:"description,omitempty"`
	PublishedAt     *time.Time `json:"published_at,omitempty"`
	QuestionCount   int        `json:"question_count"`
}
