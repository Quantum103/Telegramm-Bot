package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
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
		empty.CompletedCourses != 0 || empty.CompletedLessons != 0 ||
		empty.UnreadConversationMessages != 0 || len(empty.Courses) != 0 {
		t.Fatalf("неожиданный пустой личный кабинет: %+v", empty)
	}

	courseTitle := "Личный кабинет · тестовый курс"
	courseID, lessonIDs, _ := createCourseForEnrollmentTest(
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
		cabinet.CompletedCourses != 0 || cabinet.CompletedLessons != 0 ||
		cabinet.UnreadConversationMessages != 0 {
		t.Fatalf("неверная дата или общая статистика: %+v", cabinet)
	}
	if len(cabinet.Courses) != 1 {
		t.Fatalf("число курсов = %d, ожидался 1: %+v", len(cabinet.Courses), cabinet.Courses)
	}
	course := cabinet.Courses[0]
	if course.CourseID != courseID || course.Title != courseTitle || course.Mode != "ai" ||
		course.Status != "in_progress" || course.TotalLessons != 2 ||
		course.CompletedLessons != 0 || run.Enrollment.CourseID != courseID {
		t.Fatalf("неверная сводка активного курса: %+v", course)
	}

	curatorStudent, err := NewStudentRepository(pool).Create(ctx, CreateStudentParams{
		TelegramID:  time.Now().UnixNano() + 1,
		DisplayName: "student-cabinet-curator-integration",
	})
	if err != nil {
		t.Fatalf("создать ученика для режима координатора: %v", err)
	}
	curatorID := createStaffForCourseTest(t, ctx, pool, "curator")
	defer func() {
		_, _ = pool.Exec(context.Background(), `
			UPDATE staff_accounts SET account_status = 'inactive', telegram_id = NULL WHERE id = $1
		`, curatorID)
	}()
	if _, err := pool.Exec(ctx, "UPDATE staff_accounts SET telegram_id = $2 WHERE id = $1", curatorID, time.Now().UnixNano()+2); err != nil {
		t.Fatalf("привязать Telegram координатора: %v", err)
	}
	curatorRun, err := cabinetRepository.Start(ctx, curatorStudent.ID, courseID, "curator")
	if err != nil {
		t.Fatalf("начать курс с координатором: %v", err)
	}
	if curatorRun.Enrollment.Mode != "curator" {
		t.Fatalf("курс должен быть запущен с координатором: %+v", curatorRun.Enrollment)
	}
	firstLesson := startCuratorTestLesson(t, ctx, pool, curatorStudent.ID, curatorRun, lessonIDs[0], "Первый ответ")
	secondLesson := startCuratorTestLesson(t, ctx, pool, curatorStudent.ID, curatorRun, lessonIDs[1], "Второй ответ")
	enrollments := NewEnrollmentRepository(pool)
	for _, submitted := range []curatorTestLessonRun{firstLesson, secondLesson} {
		if _, err := enrollments.Complete(ctx, curatorStudent.ID, submitted.enrollment.ID); err != nil {
			t.Fatalf("отправить урок координатору: %v", err)
		}
	}
	if _, err := NewCuratorLearningRepository(pool).ReviewAttempt(ctx, domain.ReviewAnswerAttemptParams{
		CuratorStaffID: curatorID,
		AttemptID:      secondLesson.answer.AttemptID,
		Decision:       "needs_revision",
		Feedback:       "Добавьте пояснение.",
	}); err != nil {
		t.Fatalf("сохранить замечание координатора: %v", err)
	}
	curatorCabinet, err := cabinetRepository.GetStudentCabinet(ctx, curatorStudent.ID)
	if err != nil {
		t.Fatalf("получить личный кабинет ученика с координатором: %v", err)
	}
	if len(curatorCabinet.Courses) != 1 ||
		curatorCabinet.Courses[0].Mode != "curator" ||
		curatorCabinet.Courses[0].PendingReviews != 1 ||
		curatorCabinet.Courses[0].NeedsRevisionAnswers != 1 ||
		curatorCabinet.UnreadConversationMessages != 1 {
		t.Fatalf("личный кабинет неверно показывает статусы проверки и непрочитанные сообщения: %+v", curatorCabinet)
	}

	conversation, err := NewConversationRepository(pool).GetOrCreateForStudent(ctx, curatorStudent.ID)
	if err != nil {
		t.Fatalf("получить диалог ученика после замечания координатора: %v", err)
	}
	if marked, err := NewConversationRepository(pool).MarkReadForStudent(ctx, curatorStudent.ID, conversation.ID); err != nil || marked != 1 {
		t.Fatalf("отметить замечание координатора прочитанным: marked=%d err=%v", marked, err)
	}
	readCabinet, err := cabinetRepository.GetStudentCabinet(ctx, curatorStudent.ID)
	if err != nil || readCabinet.UnreadConversationMessages != 0 {
		t.Fatalf("после просмотра истории непрочитанных сообщений должно быть 0: cabinet=%+v err=%v", readCabinet, err)
	}
}
