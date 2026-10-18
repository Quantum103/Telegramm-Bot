package domain

import "time"

// Course описывает курс, доступный ученику после публикации.
type Course struct {
	ID          int64      `json:"id"`
	Slug        string     `json:"slug"`
	Title       string     `json:"title"`
	Description *string    `json:"description,omitempty"`
	Status      string     `json:"status"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LessonCount int        `json:"lesson_count"`
}

// CourseLesson — проекция урока внутри опубликованного курса.
type CourseLesson struct {
	CourseTitle     string
	LessonID        int64
	LessonVersionID int64
	VersionNumber   int
	Position        int
	Title           string
	PublishedAt     time.Time
}

// Lesson описывает опубликованную версию урока, выбранную для ученика.
type Lesson struct {
	ID              int64      `json:"id"`
	CourseID        int64      `json:"course_id"`
	CourseTitle     string     `json:"course_title"`
	Position        int        `json:"position"`
	LessonVersionID int64      `json:"lesson_version_id"`
	VersionNumber   int        `json:"version_number"`
	Title           string     `json:"title"`
	Description     *string    `json:"description,omitempty"`
	PublishedAt     *time.Time `json:"published_at,omitempty"`
	QuestionCount   int        `json:"question_count"`
}

// Student хранит профиль ученика. Telegram ID не заменяется изменяемым username.
type Student struct {
	ID               int64      `json:"id"`
	TelegramID       int64      `json:"telegram_id"`
	DisplayName      string     `json:"display_name"`
	TelegramUsername *string    `json:"telegram_username,omitempty"`
	PreferredName    *string    `json:"preferred_name,omitempty"`
	AccountStatus    string     `json:"account_status"`
	RegisteredAt     time.Time  `json:"registered_at"`
	LastActiveAt     *time.Time `json:"last_active_at,omitempty"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type CreateStudentParams struct {
	TelegramID       int64
	DisplayName      string
	TelegramUsername *string
	PreferredName    *string
}

type EnsureStudentParams = CreateStudentParams

type UpdateStudentProfileParams struct {
	StudentID        int64
	DisplayName      string
	TelegramUsername *string
	PreferredName    *string
}

// QuestionCriterion используется только внутренней проверкой и не отдаётся ученику.
type QuestionCriterion struct {
	Key         string  `json:"key"`
	Description string  `json:"description"`
	Weight      float64 `json:"weight"`
	Required    bool    `json:"required"`
}

type QuestionOption struct {
	ID        int64  `json:"id"`
	Key       string `json:"key"`
	Text      string `json:"text"`
	IsCorrect bool   `json:"-"`
}

type CommonMistake struct {
	Text        string `json:"text"`
	Explanation string `json:"explanation"`
}

type BiblePassage struct {
	ID              int64   `json:"id"`
	BookName        string  `json:"book_name"`
	Chapter         int     `json:"chapter"`
	StartVerse      int     `json:"start_verse"`
	EndVerse        int     `json:"end_verse"`
	TranslationName *string `json:"translation_name,omitempty"`
	PassageText     *string `json:"passage_text,omitempty"`
	SourceNote      *string `json:"source_note,omitempty"`
}

// LessonQuestion содержит внутренние материалы; ученические ответы API строятся отдельными DTO.
type LessonQuestion struct {
	ID                  int64               `json:"id"`
	LessonVersionID     int64               `json:"lesson_version_id"`
	QuestionNumber      int                 `json:"question_number"`
	Prompt              string              `json:"prompt"`
	ExpectedAnswer      string              `json:"-"`
	AnswerType          string              `json:"answer_type"`
	ExpectedExplanation *string             `json:"-"`
	Criteria            []QuestionCriterion `json:"-"`
	CommonMistakes      []CommonMistake     `json:"-"`
	BiblePassages       []BiblePassage      `json:"-"`
	Options             []QuestionOption    `json:"options"`
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
}

type LessonDetail struct {
	Lesson    Lesson
	Questions []LessonQuestion
}

type Enrollment struct {
	ID                int64      `json:"id"`
	StudentID         int64      `json:"student_id"`
	LessonID          int64      `json:"lesson_id"`
	LessonVersionID   int64      `json:"lesson_version_id"`
	Mode              string     `json:"mode"`
	CurrentQuestionID *int64     `json:"current_question_id,omitempty"`
	Status            string     `json:"status"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
	LastActivityAt    *time.Time `json:"last_activity_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type StartEnrollmentParams struct {
	StudentID       int64
	LessonID        int64
	LessonVersionID int64
	Mode            string
}

type Progress struct {
	Enrollment         Enrollment `json:"enrollment"`
	AnsweredQuestions  int        `json:"answered_questions"`
	TotalQuestions     int        `json:"total_questions"`
	RemainingQuestions int        `json:"remaining_questions"`
	IsComplete         bool       `json:"is_complete"`
	FirstQuestionID    *int64     `json:"first_question_id,omitempty"`
	PreviousQuestionID *int64     `json:"previous_question_id,omitempty"`
	NextQuestionID     *int64     `json:"next_question_id,omitempty"`
}

type SaveTextAnswerParams struct {
	StudentID      int64
	EnrollmentID   int64
	QuestionID     int64
	AnswerText     string
	IdempotencyKey string
}

type AnswerSubmission struct {
	StudentID         int64
	EnrollmentID      int64
	QuestionID        int64
	AnswerText        string
	SelectedOptionIDs []int64
	IdempotencyKey    string
}

type SavedAnswer struct {
	AnswerID          int64     `json:"answer_id"`
	AttemptID         int64     `json:"attempt_id,omitempty"`
	AnswerText        string    `json:"answer_text"`
	SelectedOptionIDs []int64   `json:"selected_option_ids"`
	ProcessingStatus  string    `json:"processing_status"`
	Replayed          bool      `json:"replayed,omitempty"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type EvaluationParams struct {
	AttemptID     int64
	Result        string
	Explanation   *string
	MissingPoints []string
	Provider      string
	Model         string
	Duration      time.Duration
}

type AttemptFailureParams struct {
	AttemptID int64
	Provider  string
	Model     string
	ErrorCode string
	Duration  time.Duration
}

type AnswerAttempt struct {
	ID               int64      `json:"id"`
	AnswerID          int64      `json:"answer_id"`
	SubmittedAnswer  string     `json:"submitted_answer"`
	ProcessingStatus  string     `json:"processing_status"`
	EvaluationResult *string    `json:"evaluation_result,omitempty"`
	Explanation      *string    `json:"explanation,omitempty"`
	MissingPoints    []string   `json:"missing_points"`
	Provider         *string    `json:"provider,omitempty"`
	Model            *string    `json:"model,omitempty"`
	DurationMS       *int32     `json:"duration_ms,omitempty"`
	ErrorCode        *string    `json:"error_code,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
}
