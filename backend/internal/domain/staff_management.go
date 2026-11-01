package domain

import "time"

// StaffAccountSummary не содержит хеш пароля, токены или другие секреты сотрудника.
type StaffAccountSummary struct {
	ID                 int64     `json:"id"`
	DisplayName        string    `json:"display_name"`
	Email              string    `json:"email"`
	AccountStatus      string    `json:"account_status"`
	Roles              []string  `json:"roles"`
	MustChangePassword bool      `json:"must_change_password"`
	CreatedAt          time.Time `json:"created_at"`
}

// CreatedStaffAccount возвращает временный пароль только в ответе операции создания.
type CreatedStaffAccount struct {
	Account           StaffAccountSummary
	TemporaryPassword string
}

// StudentActiveCourse — краткая проекция текущего незавершённого запуска курса.
type StudentActiveCourse struct {
	CourseEnrollmentID int64  `json:"course_enrollment_id"`
	CourseID           int64  `json:"course_id"`
	CourseTitle        string `json:"course_title"`
	Mode               string `json:"mode"`
	Status             string `json:"status"`
	TotalLessons       int    `json:"total_lessons"`
	CompletedLessons   int    `json:"completed_lessons"`
	PendingReviews     int    `json:"pending_reviews"`
	NeedsRevision      int    `json:"needs_revision"`
}

// AdminStudent содержит список учеников, активный курс и сводку прогресса без текстов ответов.
type AdminStudent struct {
	ID                     int64                `json:"id"`
	TelegramID             int64                `json:"telegram_id"`
	DisplayName            string               `json:"display_name"`
	TelegramUsername       *string              `json:"telegram_username,omitempty"`
	PreferredName          *string              `json:"preferred_name,omitempty"`
	AccountStatus          string               `json:"account_status"`
	RegisteredAt           time.Time            `json:"registered_at"`
	LastActiveAt           *time.Time           `json:"last_active_at,omitempty"`
	CuratorStaffID         *int64               `json:"curator_staff_id,omitempty"`
	CuratorDisplayName     *string              `json:"curator_display_name,omitempty"`
	TotalEnrollments       int                  `json:"total_enrollments"`
	InProgressEnrollments  int                  `json:"in_progress_enrollments"`
	CompletedEnrollments   int                  `json:"completed_enrollments"`
	LastLearningActivityAt *time.Time           `json:"last_learning_activity_at,omitempty"`
	ActiveCourse           *StudentActiveCourse `json:"active_course,omitempty"`
}
