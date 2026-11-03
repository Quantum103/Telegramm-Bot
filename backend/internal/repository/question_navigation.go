package repository

import (
	"context"
	"errors"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// NavigateQuestion проверяет владельца прохождения, активность ученика и запуска курса,
// проверяет ожидаемую позицию callback, выбирает соседний вопрос и сохраняет позицию
// одной короткой транзакцией. При stale callback возвращает актуальный прогресс без записи.
func (r *EnrollmentRepository) NavigateQuestion(
	ctx context.Context,
	studentID, enrollmentID, expectedQuestionID int64,
	step int,
) (domain.Progress, bool, error) {
	if step < -1 || step > 1 {
		return domain.Progress{}, false, ErrInvalidInput
	}
	return r.changeQuestionPosition(ctx, studentID, enrollmentID, expectedQuestionID, 0, step)
}

// OpenQuestionPosition сохраняет выбранный вопрос после проверки, что он принадлежит
// версии урока данного прохождения.
func (r *EnrollmentRepository) OpenQuestionPosition(
	ctx context.Context,
	studentID, enrollmentID, questionID int64,
) (domain.Progress, error) {
	if questionID <= 0 {
		return domain.Progress{}, ErrInvalidInput
	}
	progress, _, err := r.changeQuestionPosition(ctx, studentID, enrollmentID, 0, questionID, 0)
	return progress, err
}

func (r *EnrollmentRepository) changeQuestionPosition(
	ctx context.Context,
	studentID, enrollmentID, expectedQuestionID, fixedQuestionID int64,
	step int,
) (domain.Progress, bool, error) {
	if studentID <= 0 || enrollmentID <= 0 {
		return domain.Progress{}, false, ErrInvalidInput
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Progress{}, false, mapDatabaseError("начать транзакцию навигации по вопросам", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var currentQuestionID pgtype.Int8
	var lessonVersionID int64
	var enrollmentStatus, accountStatus string
	var courseRunActive bool
	err = tx.QueryRow(ctx, `
		SELECT e.current_question_id, e.lesson_version_id, e.status,
		       s.account_status,
		       (
		           e.course_enrollment_id IS NULL OR EXISTS (
		               SELECT 1
		               FROM course_enrollments ce
		               JOIN course_enrollment_lessons cel
		                 ON cel.course_enrollment_id = ce.id
		                AND cel.lesson_id = e.lesson_id
		                AND cel.lesson_version_id = e.lesson_version_id
		               WHERE ce.id = e.course_enrollment_id
		                 AND ce.student_id = e.student_id
		                 AND ce.status = 'in_progress'
		                 AND ce.mode = e.mode
		           )
		       ) AS course_run_active
		FROM lesson_enrollments e
		JOIN students s ON s.id = e.student_id
		WHERE e.id = $2 AND e.student_id = $1
		FOR UPDATE OF e
	`, studentID, enrollmentID).Scan(
		&currentQuestionID, &lessonVersionID, &enrollmentStatus,
		&accountStatus, &courseRunActive,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Progress{}, false, ErrNotFound
		}
		return domain.Progress{}, false, mapDatabaseError("проверить прохождение перед навигацией", err)
	}
	if accountStatus != "active" || enrollmentStatus == "cancelled" || !courseRunActive {
		return domain.Progress{}, false, ErrInvalidState
	}

	currentID := int64(0)
	if currentQuestionID.Valid {
		currentID = currentQuestionID.Int64
	}
	if expectedQuestionID > 0 && currentID != expectedQuestionID {
		progress, progressErr := getProgress(ctx, tx, studentID, enrollmentID)
		if progressErr != nil {
			return domain.Progress{}, false, progressErr
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Progress{}, false, mapDatabaseError("завершить проверку устаревшего callback", err)
		}
		return progress, true, nil
	}

	targetQuestionID := int64(0)
	if step == 0 && fixedQuestionID == 0 {
		targetQuestionID = currentID
		if targetQuestionID == 0 {
			return domain.Progress{}, false, ErrInvalidState
		}
	} else {
		var currentIDArg any
		if currentQuestionID.Valid {
			currentIDArg = currentQuestionID.Int64
		}
		targetErr := tx.QueryRow(ctx, `
			SELECT q.id
			FROM lesson_questions q
			JOIN lesson_versions lv ON lv.id = q.lesson_version_id
			WHERE q.lesson_version_id = $1
			  AND lv.status IN ('published', 'archived')
			  AND (
			      ($2 > 0 AND q.id = $2)
			      OR (
			          $2 = 0 AND $3 = 1 AND (
			              ($4::BIGINT IS NULL AND NOT EXISTS (
			                  SELECT 1 FROM lesson_questions previous
			                  WHERE previous.lesson_version_id = q.lesson_version_id
			                    AND previous.question_number < q.question_number
			              ))
			              OR ($4::BIGINT IS NOT NULL AND q.question_number > (
			                  SELECT current_question.question_number
			                  FROM lesson_questions current_question
			                  WHERE current_question.id = $4
			                    AND current_question.lesson_version_id = q.lesson_version_id
			              ))
			          )
			      )
			      OR (
			          $2 = 0 AND $3 = -1 AND $4::BIGINT IS NOT NULL
			          AND q.question_number < (
			              SELECT current_question.question_number
			              FROM lesson_questions current_question
			              WHERE current_question.id = $4
			                AND current_question.lesson_version_id = q.lesson_version_id
			          )
			      )
			  )
			ORDER BY
			    CASE WHEN $3 = -1 THEN q.question_number END DESC,
			    CASE WHEN $3 <> -1 THEN q.question_number END ASC
			LIMIT 1
		`, lessonVersionID, fixedQuestionID, step, currentIDArg).Scan(&targetQuestionID)
		if targetErr != nil {
			if !errors.Is(targetErr, pgx.ErrNoRows) {
				return domain.Progress{}, false, mapDatabaseError("найти целевой вопрос", targetErr)
			}
			if fixedQuestionID > 0 {
				return domain.Progress{}, false, ErrNotFound
			}
			progress, progressErr := getProgress(ctx, tx, studentID, enrollmentID)
			if progressErr != nil {
				return domain.Progress{}, false, progressErr
			}
			if err := tx.Commit(ctx); err != nil {
				return domain.Progress{}, false, mapDatabaseError("завершить проверку границы навигации", err)
			}
			if step < 0 {
				return progress, false, domain.ErrNoPreviousQuestion
			}
			return progress, false, domain.ErrNoNextQuestion
		}
	}

	if targetQuestionID != currentID {
		var expectedCurrent any
		if currentQuestionID.Valid {
			expectedCurrent = currentQuestionID.Int64
		}
		tag, updateErr := tx.Exec(ctx, `
			UPDATE lesson_enrollments e
			SET current_question_id = $3,
			    last_activity_at = now(),
			    updated_at = now()
			WHERE e.id = $2 AND e.student_id = $1
			  AND e.status <> 'cancelled'
			  AND e.current_question_id IS NOT DISTINCT FROM $4::BIGINT
			  AND EXISTS (
			      SELECT 1 FROM students s
			      WHERE s.id = e.student_id AND s.account_status = 'active'
			  )
			  AND (
			      e.course_enrollment_id IS NULL OR EXISTS (
			          SELECT 1
			          FROM course_enrollments ce
			          JOIN course_enrollment_lessons cel
			            ON cel.course_enrollment_id = ce.id
			           AND cel.lesson_id = e.lesson_id
			           AND cel.lesson_version_id = e.lesson_version_id
			          WHERE ce.id = e.course_enrollment_id
			            AND ce.student_id = e.student_id
			            AND ce.status = 'in_progress'
			            AND ce.mode = e.mode
			      )
			  )
			  AND EXISTS (
			      SELECT 1
			      FROM lesson_questions q
			      JOIN lesson_versions lv ON lv.id = q.lesson_version_id
			      WHERE q.id = $3 AND q.lesson_version_id = e.lesson_version_id
			        AND lv.status IN ('published', 'archived')
			  )
		`, studentID, enrollmentID, targetQuestionID, expectedCurrent)
		if updateErr != nil {
			return domain.Progress{}, false, mapDatabaseError("сохранить текущий вопрос прохождения", updateErr)
		}
		if tag.RowsAffected() != 1 {
			return domain.Progress{}, false, ErrInvalidState
		}
	}

	progress, err := getProgress(ctx, tx, studentID, enrollmentID)
	if err != nil {
		return domain.Progress{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Progress{}, false, mapDatabaseError("зафиксировать навигацию по вопросам", err)
	}
	return progress, false, nil
}
