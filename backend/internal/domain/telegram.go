package domain

import "time"

// TelegramDialogState хранит состояние интерфейса отдельно от учебного прогресса.
type TelegramDialogState struct {
	StudentID         int64
	ChatID            int64
	MessageID         *int64
	Screen            string
	CourseID          *int64
	LessonID          *int64
	EnrollmentID      *int64
	QuestionID        *int64
	AwaitingText      bool
	SelectedOptionIDs []int64
	UpdatedAt         time.Time
}
