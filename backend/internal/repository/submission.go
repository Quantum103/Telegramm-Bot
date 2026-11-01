package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (r *AnswerRepository) Submit(ctx context.Context, params AnswerSubmission) (SavedAnswer, error) {
	answerText := strings.TrimSpace(params.AnswerText)
	if params.StudentID <= 0 || params.EnrollmentID <= 0 || params.QuestionID <= 0 {
		return SavedAnswer{}, ErrInvalidInput
	}
	if params.IdempotencyKey != "" && !isCanonicalUUID(params.IdempotencyKey) {
		return SavedAnswer{}, fmt.Errorf("%w: idempotency key должен быть UUID", ErrInvalidInput)
	}

	uniqueOptions := make(map[int64]struct{}, len(params.SelectedOptionIDs))
	for _, optionID := range params.SelectedOptionIDs {
		if optionID <= 0 {
			return SavedAnswer{}, ErrInvalidInput
		}
		if _, exists := uniqueOptions[optionID]; exists {
			return SavedAnswer{}, fmt.Errorf("%w: варианты ответа не должны повторяться", ErrInvalidInput)
		}
		uniqueOptions[optionID] = struct{}{}
	}
	params.AnswerText = answerText

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SavedAnswer{}, mapDatabaseError("начать транзакцию отправки ответа", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lessonVersionID int64
	var enrollmentStatus string
	var courseEnrollmentID pgtype.Int8
	if err := tx.QueryRow(ctx, `
		SELECT lesson_version_id, status, course_enrollment_id
		FROM lesson_enrollments
		WHERE id = $1 AND student_id = $2
		FOR UPDATE
	`, params.EnrollmentID, params.StudentID).Scan(
		&lessonVersionID, &enrollmentStatus, &courseEnrollmentID,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SavedAnswer{}, ErrNotFound
		}
		return SavedAnswer{}, mapDatabaseError("проверить прохождение ученика", err)
	}
	if enrollmentStatus == "cancelled" {
		return SavedAnswer{}, ErrInvalidState
	}
	if courseEnrollmentID.Valid {
		if enrollmentStatus != "in_progress" {
			return SavedAnswer{}, ErrInvalidState
		}
		var courseStatus string
		var courseLessonMatches bool
		if err := tx.QueryRow(ctx, `
			SELECT ce.status,
			       EXISTS (
			           SELECT 1
			           FROM course_enrollment_lessons cel
			           JOIN lesson_enrollments lesson
			             ON lesson.id = $3
			           WHERE cel.course_enrollment_id = ce.id
			             AND cel.lesson_id = lesson.lesson_id
			             AND cel.lesson_version_id = lesson.lesson_version_id
			       )
			FROM course_enrollments ce
			WHERE ce.id = $1 AND ce.student_id = $2
			FOR UPDATE
		`, courseEnrollmentID.Int64, params.StudentID, params.EnrollmentID).Scan(
			&courseStatus, &courseLessonMatches,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return SavedAnswer{}, ErrNotFound
			}
			return SavedAnswer{}, mapDatabaseError("проверить состояние курса перед отправкой ответа", err)
		}
		if courseStatus != "in_progress" || !courseLessonMatches {
			return SavedAnswer{}, ErrInvalidState
		}
	}

	var answerType string
	if err := tx.QueryRow(ctx, `
		SELECT q.answer_type
		FROM lesson_questions q
		JOIN lesson_versions lv ON lv.id = q.lesson_version_id
		WHERE q.id = $1
		  AND q.lesson_version_id = $2
		  AND lv.status IN ('published', 'archived')
	`, params.QuestionID, lessonVersionID).Scan(&answerType); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SavedAnswer{}, ErrNotFound
		}
		return SavedAnswer{}, mapDatabaseError("проверить вопрос прохождения", err)
	}
	switch answerType {
	case "text":
		if answerText == "" || len(params.SelectedOptionIDs) != 0 {
			return SavedAnswer{}, fmt.Errorf("%w: для текстового вопроса требуется только текст", ErrInvalidInput)
		}
	case "single_choice":
		if answerText != "" || len(params.SelectedOptionIDs) != 1 {
			return SavedAnswer{}, fmt.Errorf("%w: необходимо выбрать ровно один вариант", ErrInvalidInput)
		}
	case "multiple_choice":
		if answerText != "" || len(params.SelectedOptionIDs) == 0 {
			return SavedAnswer{}, fmt.Errorf("%w: выберите один или несколько вариантов", ErrInvalidInput)
		}
	default:
		return SavedAnswer{}, ErrInvalidState
	}

	if len(params.SelectedOptionIDs) > 0 {
		var validCount int
		if err := tx.QueryRow(ctx, `
			SELECT count(*)
			FROM question_options
			WHERE question_id = $1 AND id = ANY($2::bigint[])
		`, params.QuestionID, params.SelectedOptionIDs).Scan(&validCount); err != nil {
			return SavedAnswer{}, mapDatabaseError("проверить варианты ответа", err)
		}
		if validCount != len(params.SelectedOptionIDs) {
			return SavedAnswer{}, fmt.Errorf("%w: один или несколько вариантов не принадлежат вопросу", ErrInvalidInput)
		}
	}

	if params.IdempotencyKey != "" {
		var replay SavedAnswer
		var previousQuestionID int64
		err := tx.QueryRow(ctx, `
			SELECT aa.id, aa.answer_id, aa.question_id, aa.submitted_answer_text,
			       aa.processing_status, sa.updated_at,
			       COALESCE((
			           SELECT array_agg(ao.option_id ORDER BY qo.sort_order)
			           FROM answer_attempt_options ao
			           JOIN question_options qo ON qo.id = ao.option_id AND qo.question_id = ao.question_id
			           WHERE ao.attempt_id = aa.id
			       ), ARRAY[]::BIGINT[])
			FROM answer_attempts aa
			JOIN student_answers sa ON sa.id = aa.answer_id
			WHERE aa.enrollment_id = $1 AND aa.idempotency_key = $2::uuid
		`, params.EnrollmentID, params.IdempotencyKey).Scan(
			&replay.AttemptID, &replay.AnswerID, &previousQuestionID, &replay.AnswerText,
			&replay.ProcessingStatus, &replay.UpdatedAt, &replay.SelectedOptionIDs,
		)
		switch {
		case err == nil:
			if previousQuestionID != params.QuestionID {
				return SavedAnswer{}, ErrConflict
			}
			if err := tx.Commit(ctx); err != nil {
				return SavedAnswer{}, mapDatabaseError("завершить повторную отправку ответа", err)
			}
			replay.Replayed = true
			return replay, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return SavedAnswer{}, mapDatabaseError("проверить ключ идемпотентности", err)
		}
	}

	var answerID int64
	var answerUpdatedAt pgtype.Timestamptz
	if err := tx.QueryRow(ctx, `
		INSERT INTO student_answers (enrollment_id, lesson_version_id, question_id, answer_text)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (enrollment_id, question_id)
		DO UPDATE SET answer_text = EXCLUDED.answer_text, updated_at = now()
		RETURNING id, updated_at
	`, params.EnrollmentID, lessonVersionID, params.QuestionID, answerText).Scan(&answerID, &answerUpdatedAt); err != nil {
		return SavedAnswer{}, mapDatabaseError("сохранить текущий ответ", err)
	}

	if _, err := tx.Exec(ctx, "DELETE FROM student_answer_options WHERE answer_id = $1", answerID); err != nil {
		return SavedAnswer{}, mapDatabaseError("обновить выбранные варианты", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO student_answer_options (answer_id, question_id, option_id)
		SELECT $1, $2, selected.option_id
		FROM unnest($3::bigint[]) AS selected(option_id)
	`, answerID, params.QuestionID, params.SelectedOptionIDs); err != nil {
		return SavedAnswer{}, mapDatabaseError("сохранить выбранные варианты", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE lesson_enrollments
		SET current_question_id = $2,
		    status = CASE WHEN status IN ('not_started', 'paused', 'completed') THEN 'in_progress' ELSE status END,
		    completed_at = NULL,
		    started_at = COALESCE(started_at, now()),
		    last_activity_at = now(),
		    updated_at = now()
		WHERE id = $1
	`, params.EnrollmentID, params.QuestionID); err != nil {
		return SavedAnswer{}, mapDatabaseError("обновить позицию и прогресс прохождения", err)
	}

	var idempotencyKey any
	if params.IdempotencyKey != "" {
		idempotencyKey = params.IdempotencyKey
	}
	var attemptID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO answer_attempts (
			answer_id, enrollment_id, lesson_version_id, question_id,
			idempotency_key, submitted_answer_text, processing_status
		)
		VALUES ($1, $2, $3, $4, $5::uuid, $6, 'pending')
		RETURNING id
	`, answerID, params.EnrollmentID, lessonVersionID, params.QuestionID,
		idempotencyKey, answerText).Scan(&attemptID); err != nil {
		return SavedAnswer{}, mapDatabaseError("создать историю попыток", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO answer_attempt_options (attempt_id, question_id, option_id)
		SELECT $1, $2, selected.option_id
		FROM unnest($3::bigint[]) AS selected(option_id)
	`, attemptID, params.QuestionID, params.SelectedOptionIDs); err != nil {
		return SavedAnswer{}, mapDatabaseError("сохранить варианты попытки", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return SavedAnswer{}, mapDatabaseError("зафиксировать ответ и прогресс", err)
	}
	return SavedAnswer{
		AnswerID: answerID, AttemptID: attemptID, AnswerText: answerText,
		SelectedOptionIDs: append([]int64{}, params.SelectedOptionIDs...),
		ProcessingStatus:  "pending", UpdatedAt: answerUpdatedAt.Time,
	}, nil
}

func (r *AnswerRepository) GetSavedAnswer(ctx context.Context, studentID, enrollmentID, questionID int64) (SavedAnswer, error) {
	if studentID <= 0 || enrollmentID <= 0 || questionID <= 0 {
		return SavedAnswer{}, ErrInvalidInput
	}
	var answer SavedAnswer
	var latestAttemptID pgtype.Int8
	var latestStatus pgtype.Text
	err := r.pool.QueryRow(ctx, `
		SELECT sa.id, sa.answer_text, sa.updated_at,
		       latest_attempt.id, latest_attempt.processing_status
		FROM student_answers sa
		JOIN lesson_enrollments le
		  ON le.id = sa.enrollment_id AND le.lesson_version_id = sa.lesson_version_id
		LEFT JOIN LATERAL (
			SELECT id, processing_status
			FROM answer_attempts
			WHERE answer_id = sa.id
			ORDER BY created_at DESC, id DESC
			LIMIT 1
		) latest_attempt ON TRUE
		WHERE le.student_id = $1 AND le.id = $2 AND sa.question_id = $3
	`, studentID, enrollmentID, questionID).Scan(
		&answer.AnswerID, &answer.AnswerText, &answer.UpdatedAt,
		&latestAttemptID, &latestStatus,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SavedAnswer{}, ErrNotFound
		}
		return SavedAnswer{}, mapDatabaseError("получить сохранённый ответ", err)
	}
	if latestAttemptID.Valid {
		answer.AttemptID = latestAttemptID.Int64
	}
	if latestStatus.Valid {
		answer.ProcessingStatus = latestStatus.String
	}
	rows, err := r.pool.Query(ctx, `
		SELECT sao.option_id
		FROM student_answer_options sao
		JOIN question_options qo ON qo.id = sao.option_id AND qo.question_id = sao.question_id
		WHERE sao.answer_id = $1
		ORDER BY qo.sort_order
	`, answer.AnswerID)
	if err != nil {
		return SavedAnswer{}, mapDatabaseError("получить сохранённые варианты", err)
	}
	defer rows.Close()
	answer.SelectedOptionIDs = make([]int64, 0)
	for rows.Next() {
		var optionID int64
		if err := rows.Scan(&optionID); err != nil {
			return SavedAnswer{}, mapDatabaseError("прочитать сохранённый вариант", err)
		}
		answer.SelectedOptionIDs = append(answer.SelectedOptionIDs, optionID)
	}
	if err := rows.Err(); err != nil {
		return SavedAnswer{}, mapDatabaseError("обойти сохранённые варианты", err)
	}
	return answer, nil
}
