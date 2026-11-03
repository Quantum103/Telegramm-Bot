package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

func TestEnrollmentRepositoryAtomicQuestionNavigation(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	student, err := NewStudentRepository(pool).Create(ctx, CreateStudentParams{
		TelegramID: time.Now().UnixNano(), DisplayName: "navigation-" + suffix,
	})
	if err != nil {
		t.Fatalf("создать ученика: %v", err)
	}

	var courseID, lessonID, versionID, firstID, secondID, thirdID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO courses (slug, title, status, published_at)
		VALUES ($1, 'Навигационный тест', 'published', now())
		RETURNING id
	`, "navigation-"+suffix).Scan(&courseID); err != nil {
		t.Fatalf("создать курс: %v", err)
	}
	if err := pool.QueryRow(ctx, "INSERT INTO lessons DEFAULT VALUES RETURNING id").Scan(&lessonID); err != nil {
		t.Fatalf("создать урок: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO course_lessons (course_id, lesson_id, position) VALUES ($1, $2, 1)",
		courseID, lessonID,
	); err != nil {
		t.Fatalf("добавить урок в курс: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO lesson_versions (lesson_id, version_number, title, status)
		VALUES ($1, 1, 'Навигационная версия', 'draft')
		RETURNING id
	`, lessonID).Scan(&versionID); err != nil {
		t.Fatalf("создать версию урока: %v", err)
	}
	for _, item := range []struct {
		number int
		text   string
		target *int64
	}{
		{number: 2, text: "Первый вопрос", target: &firstID},
		{number: 5, text: "Второй вопрос", target: &secondID},
		{number: 9, text: "Третий вопрос", target: &thirdID},
	} {
		if err := pool.QueryRow(ctx, `
			INSERT INTO lesson_questions (lesson_version_id, question_number, prompt, expected_answer)
			VALUES ($1, $2, $3, 'Ответ')
			RETURNING id
		`, versionID, item.number, item.text).Scan(item.target); err != nil {
			t.Fatalf("создать вопрос %d: %v", item.number, err)
		}
	}
	if _, err := pool.Exec(ctx,
		"UPDATE lesson_versions SET status = 'published', published_at = now() WHERE id = $1",
		versionID,
	); err != nil {
		t.Fatalf("опубликовать версию: %v", err)
	}

	enrollments := NewEnrollmentRepository(pool)
	enrollment, err := enrollments.Start(ctx, StartEnrollmentParams{
		StudentID: student.ID, LessonID: lessonID, LessonVersionID: versionID, Mode: "ai",
	})
	if err != nil {
		t.Fatalf("начать прохождение: %v", err)
	}
	if _, err := enrollments.OpenQuestionPosition(ctx, student.ID, enrollment.ID, firstID); err != nil {
		t.Fatalf("открыть первый вопрос: %v", err)
	}

	progress, stale, err := enrollments.NavigateQuestion(ctx, student.ID, enrollment.ID, secondID, 1)
	if err != nil {
		t.Fatalf("проверить устаревший callback: %v", err)
	}
	if !stale || progress.Enrollment.CurrentQuestionID == nil ||
		*progress.Enrollment.CurrentQuestionID != firstID {
		t.Fatalf("устаревший callback изменил позицию: stale=%v progress=%+v", stale, progress)
	}

	progress, stale, err = enrollments.NavigateQuestion(ctx, student.ID, enrollment.ID, firstID, 1)
	if err != nil {
		t.Fatalf("перейти ко второму вопросу: %v", err)
	}
	if stale || progress.Enrollment.CurrentQuestionID == nil ||
		*progress.Enrollment.CurrentQuestionID != secondID ||
		progress.PreviousQuestionID == nil || *progress.PreviousQuestionID != firstID ||
		progress.NextQuestionID == nil || *progress.NextQuestionID != thirdID {
		t.Fatalf("неверный результат перехода вперёд: stale=%v progress=%+v", stale, progress)
	}

	progress, stale, err = enrollments.NavigateQuestion(ctx, student.ID, enrollment.ID, firstID, 1)
	if err != nil || !stale || progress.Enrollment.CurrentQuestionID == nil ||
		*progress.Enrollment.CurrentQuestionID != secondID {
		t.Fatalf("повторный старый callback не был отклонён: stale=%v progress=%+v err=%v", stale, progress, err)
	}

	progress, stale, err = enrollments.NavigateQuestion(ctx, student.ID, enrollment.ID, secondID, -1)
	if err != nil || stale || progress.Enrollment.CurrentQuestionID == nil ||
		*progress.Enrollment.CurrentQuestionID != firstID {
		t.Fatalf("переход назад не выполнен: stale=%v progress=%+v err=%v", stale, progress, err)
	}

	progress, stale, err = enrollments.NavigateQuestion(ctx, student.ID, enrollment.ID, firstID, -1)
	if !errors.Is(err, domain.ErrNoPreviousQuestion) || stale ||
		progress.Enrollment.CurrentQuestionID == nil ||
		*progress.Enrollment.CurrentQuestionID != firstID {
		t.Fatalf("граница первого вопроса обработана неправильно: stale=%v progress=%+v err=%v", stale, progress, err)
	}

	if _, _, err := enrollments.NavigateQuestion(ctx, student.ID+100000, enrollment.ID, firstID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("чужое прохождение должно быть недоступно, получено %v", err)
	}
	if _, err := pool.Exec(ctx,
		"UPDATE students SET account_status = 'inactive' WHERE id = $1",
		student.ID,
	); err != nil {
		t.Fatalf("отключить ученика для проверки доступа: %v", err)
	}
	if _, _, err := enrollments.NavigateQuestion(ctx, student.ID, enrollment.ID, firstID, 1); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("неактивный ученик не должен переходить между вопросами, получено %v", err)
	}
}
