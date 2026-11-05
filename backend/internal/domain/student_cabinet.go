package domain

import "time"

// StudentCabinet содержит краткую статистику и последние состояния курсов ученика.
type StudentCabinet struct {
	RegisteredAt      time.Time
	CompletedCourses  int
	CompletedLessons  int
	Courses            []StudentCabinetCourse
}

// StudentCabinetCourse описывает последний запуск одного курса.
type StudentCabinetCourse struct {
	CourseID         int64
	Title            string
	Status           string
	TotalLessons     int
	CompletedLessons int
}
