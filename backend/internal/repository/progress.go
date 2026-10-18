package repository

import (
	"context"
	"errors"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (r *EnrollmentRepository) GetByIDForStudent(ctx context.Context, studentID, enrollmentID int64) (Enrollment, error) {
	if studentID <= 0 || enrollmentID <= 0 {
		return Enrollment{}, ErrInvalidInput
	}
	enrollment, err := scanEnrollment(r.pool.QueryRow(ctx, `
		SELECT id, student_id, lesson_id, lesson_version_id, mode,
		       current_question_id, status, started_at, completed_at,
		       last_activity_at, created_at, updated_at
		FROM lesson_enrollments
		WHERE student_id = $1 AND id = $2
	`, studentID, enrollmentID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Enrollment{}, ErrNotFound
		}
		return Enrollment{}, mapDatabaseError("получить прохождение ученика", err)
	}
	return enrollment, nil
}

func (r *EnrollmentRepository) SetCurrentQuestion(ctx context.Context, studentID, enrollmentID, questionID int64) error {
	if studentID <= 0 || enrollmentID <= 0 || questionID <= 0 {
		return ErrInvalidInput
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE lesson_enrollments e
		SET current_question_id = $3, last_activity_at = now(), updated_at = now()
		WHERE e.id = $2 AND e.student_id = $1 AND e.status <> 'cancelled'
		  AND EXISTS (
		      SELECT 1
		      FROM lesson_questions q
		      WHERE q.id = $3 AND q.lesson_version_id = e.lesson_version_id
		  )
	`, studentID, enrollmentID, questionID)
	if err != nil {
		return mapDatabaseError("сохранить позицию прохождения", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	if _, err := r.GetByIDForStudent(ctx, studentID, enrollmentID); err != nil {
		return err
	}
	return ErrNotFound
}

func (r *EnrollmentRepository) GetProgress(ctx context.Context, studentID, enrollmentID int64) (domain.Progress, error) {
	if studentID <= 0 || enrollmentID <= 0 {
		return domain.Progress{}, ErrInvalidInput
	}

	var progress domain.Progress
	var currentID, firstID, previousID, nextID pgtype.Int8
	var startedAt, completedAt, lastActivityAt pgtype.Timestamptz
	err := r.pool.QueryRow(ctx, `
		SELECT
			e.id, e.student_id, e.lesson_id, e.lesson_version_id, e.mode,
			e.current_question_id, e.status, e.started_at, e.completed_at,
			e.last_activity_at, e.created_at, e.updated_at,
			count(q.id)::INTEGER AS total_questions,
			count(sa.id)::INTEGER AS answered_questions,
			(
				SELECT first_question.id
				FROM lesson_questions first_question
				WHERE first_question.lesson_version_id = e.lesson_version_id
				ORDER BY first_question.question_number
				LIMIT 1
			) AS first_question_id,
			(
				SELECT previous_question.id
				FROM lesson_questions current_question
				JOIN lesson_questions previous_question
				  ON previous_question.lesson_version_id = current_question.lesson_version_id
				 AND previous_question.question_number < current_question.question_number
				WHERE current_question.id = e.current_question_id
				ORDER BY previous_question.question_number DESC
				LIMIT 1
			) AS previous_question_id,
			(
				SELECT next_question.id
				FROM lesson_questions current_question
				JOIN lesson_questions next_question
				  ON next_question.lesson_version_id = current_question.lesson_version_id
				 AND next_question.question_number > current_question.question_number
				WHERE current_question.id = e.current_question_id
				ORDER BY next_question.question_number
				LIMIT 1
			) AS next_question_id
		FROM lesson_enrollments e
		LEFT JOIN lesson_questions q ON q.lesson_version_id = e.lesson_version_id
		LEFT JOIN student_answers sa
		  ON sa.enrollment_id = e.id AND sa.lesson_version_id = e.lesson_version_id
		 AND sa.question_id = q.id
		WHERE e.id = $2 AND e.student_id = $1
		GROUP BY e.id
	`, studentID, enrollmentID).Scan(
		&progress.Enrollment.ID, &progress.Enrollment.StudentID,
		&progress.Enrollment.LessonID, &progress.Enrollment.LessonVersionID,
		&progress.Enrollment.Mode, &currentID, &progress.Enrollment.Status,
		&startedAt, &completedAt, &lastActivityAt, &progress.Enrollment.CreatedAt,
		&progress.Enrollment.UpdatedAt, &progress.TotalQuestions, &progress.AnsweredQuestions,
		&firstID, &previousID, &nextID,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Progress{}, ErrNotFound
		}
		return domain.Progress{}, mapDatabaseError("получить прогресс прохождения", err)
	}
	setNullableInt64(&progress.Enrollment.CurrentQuestionID, currentID)
	setNullableInt64(&progress.FirstQuestionID, firstID)
	setNullableInt64(&progress.PreviousQuestionID, previousID)
	setNullableInt64(&progress.NextQuestionID, nextID)
	if startedAt.Valid {
		value := startedAt.Time
		progress.Enrollment.StartedAt = &value
	}
	if completedAt.Valid {
		value := completedAt.Time
		progress.Enrollment.CompletedAt = &value
	}
	if lastActivityAt.Valid {
		value := lastActivityAt.Time
		progress.Enrollment.LastActivityAt = &value
	}
	progress.RemainingQuestions = progress.TotalQuestions - progress.AnsweredQuestions
	progress.IsComplete = progress.Enrollment.Status == "completed"
	return progress, nil
}

// Complete фиксирует завершение только после сохранения ответа на каждый вопрос.
// Результат смысловой проверки не подменяется количеством отправленных ответов.
func (r *EnrollmentRepository) Complete(ctx context.Context, studentID, enrollmentID int64) (Enrollment, error) {
	if studentID <= 0 || enrollmentID <= 0 {
		return Enrollment{}, ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Enrollment{}, mapDatabaseError("начать транзакцию завершения урока", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var enrollment Enrollment
	err = scanEnrollment(tx.QueryRow(ctx, `
		SELECT id, student_id, lesson_id, lesson_version_id, mode,
		       current_question_id, status, started_at, completed_at,
		       last_activity_at, created_at, updated_at
		FROM lesson_enrollments
		WHERE id = $2 AND student_id = $1
		FOR UPDATE
	`, studentID, enrollmentID)).assign(&enrollment)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Enrollment{}, ErrNotFound
		}
		return Enrollment{}, mapDatabaseError("проверить прохождение перед завершением", err)
	}
	if enrollment.Status == "cancelled" {
		return Enrollment{}, ErrInvalidState
	}
	if enrollment.Status == "completed" {
		if err := tx.Commit(ctx); err != nil {
			return Enrollment{}, mapDatabaseError("завершить повторный запрос", err)
		}
		return enrollment, nil
	}

	var totalQuestions, answeredQuestions int
	if err := tx.QueryRow(ctx, `
		SELECT count(q.id)::INTEGER, count(sa.id)::INTEGER
		FROM lesson_questions q
		LEFT JOIN student_answers sa
		  ON sa.enrollment_id = $1
		 AND sa.lesson_version_id = q.lesson_version_id
		 AND sa.question_id = q.id
		WHERE q.lesson_version_id = $2
	`, enrollmentID, enrollment.LessonVersionID).Scan(&totalQuestions, &answeredQuestions); err != nil {
		return Enrollment{}, mapDatabaseError("проверить ответы урока перед завершением", err)
	}
	if totalQuestions == 0 || answeredQuestions != totalQuestions {
		return Enrollment{}, ErrInvalidState
	}

	enrollment, err = scanEnrollment(tx.QueryRow(ctx, `
		UPDATE lesson_enrollments
		SET status = 'completed', completed_at = now(),
		    started_at = COALESCE(started_at, now()),
		    last_activity_at = now(), updated_at = now()
		WHERE id = $1
		RETURNING id, student_id, lesson_id, lesson_version_id, mode,
		          current_question_id, status, started_at, completed_at,
		          last_activity_at, created_at, updated_at
	`, enrollmentID))
	if err != nil {
		return Enrollment{}, mapDatabaseError("отметить завершение урока", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Enrollment{}, mapDatabaseError("зафиксировать завершение урока", err)
	}
	return enrollment, nil
}

type enrollmentScanner struct {
	row pgx.Row
}

func (s enrollmentScanner) assign(target *Enrollment) error {
	value, err := scanEnrollment(s.row)
	if err != nil {
		return err
	}
	*target = value
	return nil
}

func setNullableInt64(target **int64, value pgtype.Int8) {
	if value.Valid {
		number := value.Int64
		*target = &number
	}
}
