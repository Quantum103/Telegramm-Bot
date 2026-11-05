package repository

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestGetStudentCabinetReturnsStudentAndCourseSummary(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	student, err := NewStudentRepository(pool).Create(ctx, CreateStudentParams{
		TelegramID:  time.Now().UnixNano(),
		DisplayName: "student-cabinet-integration",
	})
	if err != nil {
		t.Fatalf("создать ученика: %v", err)
	}

	cabinetRepository := NewCourseEnrollmentRepository(pool)
	empty, err := cabinetRepository.GetStudentCabinet(ctx, student.ID)
	if err != nil {
		t.Fatalf("получить пустой личный кабинет: %v", err)
	}
	if !empty.RegisteredAt.Equal(student.RegisteredAt) ||
		empty.CompletedCourses != 0 || empty.CompletedLessons != 0 || len(empty.Courses) != 0 {
		t.Fatalf("неожиданный пустой личный кабинет: %+v", empty)
	}

	courseTitle := "Личный кабинет · тестовый курс"
	courseID, _, _ := createCourseForEnrollmentTest(
		t, ctx, pool, fmt.Sprintf("%d", time.Now().UnixNano()), courseTitle, 2,
	)
	run, err := NewCourseEnrollmentRepository(pool).Start(ctx, student.ID, courseID, "ai")
	if err != nil {
		t.Fatalf("начать тестовый курс: %v", err)
	}

	cabinet, err := cabinetRepository.GetStudentCabinet(ctx, student.ID)
	if err != nil {
		t.Fatalf("получить личный кабинет с курсом: %v", err)
	}
	if !cabinet.RegisteredAt.Equal(student.RegisteredAt) ||
		cabinet.CompletedCourses != 0 || cabinet.CompletedLessons != 0 {
		t.Fatalf("неверная дата или общая статистика: %+v", cabinet)
	}
	if len(cabinet.Courses) != 1 {
		t.Fatalf("число курсов = %d, ожидался 1: %+v", len(cabinet.Courses), cabinet.Courses)
	}
	course := cabinet.Courses[0]
	if course.CourseID != courseID || course.Title != courseTitle ||
		course.Status != "in_progress" || course.TotalLessons != 2 ||
		course.CompletedLessons != 0 || run.Enrollment.CourseID != courseID {
		t.Fatalf("неверная сводка активного курса: %+v", course)
	}
}
