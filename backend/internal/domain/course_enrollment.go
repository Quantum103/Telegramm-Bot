package domain

import "time"

// CourseEnrollment фиксирует неизменяемый выбор режима в рамках одного запуска курса.
type CourseEnrollment struct {
	ID             int64      `json:"id"`
	StudentID      int64      `json:"student_id"`
	CourseID       int64      `json:"course_id"`
	CourseTitle    string     `json:"course_title"`
	RunNumber      int        `json:"run_number"`
	Mode           string     `json:"mode"`
	CuratorStaffID *int64     `json:"curator_staff_id,omitempty"`
	Status         string     `json:"status"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// CourseEnrollmentProgress рассчитывается по зафиксированному списку обязательных уроков.
type CourseEnrollmentProgress struct {
	Enrollment       CourseEnrollment `json:"enrollment"`
	TotalLessons     int              `json:"total_lessons"`
	CompletedLessons int              `json:"completed_lessons"`
	RemainingLessons int              `json:"remaining_lessons"`
	IsComplete       bool             `json:"is_complete"`
}

// CourseEnrollmentLesson содержит состояние обязательного урока в конкретном запуске курса.
type CourseEnrollmentLesson struct {
	LessonID        int64  `json:"lesson_id"`
	LessonVersionID int64  `json:"lesson_version_id"`
	Position        int    `json:"position"`
	Title           string `json:"title"`
	EnrollmentID   *int64  `json:"enrollment_id,omitempty"`
	Status          string `json:"status"`
	TotalQuestions  int    `json:"total_questions"`
	AnsweredQuestions int  `json:"answered_questions"`
	PendingReviews  int    `json:"pending_reviews"`
	NeedsRevision   int    `json:"needs_revision"`
}

// AnswerAttemptReview — неизменяемое решение куратора по конкретной отправленной попытке.
type AnswerAttemptReview struct {
	ID             int64     `json:"id"`
	AnswerAttemptID int64    `json:"answer_attempt_id"`
	CuratorStaffID int64     `json:"curator_staff_id"`
	Decision       string    `json:"decision"`
	Feedback       string    `json:"feedback"`
	ReviewedAt     time.Time `json:"reviewed_at"`
}

type ReviewAnswerAttemptParams struct {
	CuratorStaffID int64
	AttemptID      int64
	Decision       string
	Feedback       string
}
