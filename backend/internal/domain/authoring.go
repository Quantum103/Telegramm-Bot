package domain

// CreateCourseParams задаёт содержимое нового чернового курса.
type CreateCourseParams struct {
	Slug             string
	Title            string
	Description      *string
	CreatedByStaffID int64
}

// CreateLessonParams создаёт самостоятельный урок и его первую черновую версию.
type CreateLessonParams struct {
	CourseID    int64
	Title       string
	Description *string
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
	Criteria            []QuestionCriterionInput
	Options             []QuestionOptionInput
	CommonMistakes      []CommonMistake
	BiblePassages       []BiblePassageInput
}
