package repository

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// GetNavigationAnswerState загружает сохранённый ответ и последнюю попытку одним
// SQL-запросом. Полная история попыток для экрана вопроса не требуется.
func (r *AnswerRepository) GetNavigationAnswerState(
	ctx context.Context,
	studentID, enrollmentID, questionID int64,
) (*domain.SavedAnswer, *domain.AnswerAttempt, error) {
	if studentID <= 0 || enrollmentID <= 0 || questionID <= 0 {
		return nil, nil, ErrInvalidInput
	}

	var (
		answerID       pgtype.Int8
		answerText     pgtype.Text
		answerUpdated  pgtype.Timestamptz
		answerStatus   pgtype.Text
		selectedIDs    []int64
		attemptID      pgtype.Int8
		attemptAnswer  pgtype.Int8
		attemptText    pgtype.Text
		attemptStatus  pgtype.Text
		result         pgtype.Text
		explanation    pgtype.Text
		missingJSON    []byte
		provider       pgtype.Text
		model          pgtype.Text
		durationMS     pgtype.Int4
		errorCode      pgtype.Text
		attemptCreated pgtype.Timestamptz
		completedAt    pgtype.Timestamptz
		reviewDecision pgtype.Text
		reviewFeedback pgtype.Text
		reviewedAt     pgtype.Timestamptz
		attemptOptions []int64
	)

	err := r.pool.QueryRow(ctx, `
		SELECT
			sa.id,
			sa.answer_text,
			sa.updated_at,
			latest.processing_status,
			COALESCE((
				SELECT array_agg(sao.option_id ORDER BY qo.sort_order)
				FROM student_answer_options sao
				JOIN question_options qo ON qo.id = sao.option_id AND qo.question_id = sao.question_id
				WHERE sao.answer_id = sa.id
			), ARRAY[]::BIGINT[]) AS selected_option_ids,
			latest.id,
			latest.answer_id,
			latest.submitted_answer_text,
			latest.processing_status,
			latest.evaluation_result,
			latest.explanation,
			latest.missing_points,
			latest.provider,
			latest.model,
			latest.duration_ms,
			latest.error_code,
			latest.created_at,
			latest.completed_at,
			review.decision,
			review.feedback,
			review.reviewed_at,
			COALESCE((
				SELECT array_agg(ao.option_id ORDER BY qo.sort_order)
				FROM answer_attempt_options ao
				JOIN question_options qo ON qo.id = ao.option_id AND qo.question_id = ao.question_id
				WHERE ao.attempt_id = latest.id
			), ARRAY[]::BIGINT[]) AS attempt_option_ids
		FROM lesson_enrollments le
		JOIN lesson_questions q
		  ON q.id = $3 AND q.lesson_version_id = le.lesson_version_id
		LEFT JOIN student_answers sa
		  ON sa.enrollment_id = le.id
		 AND sa.lesson_version_id = le.lesson_version_id
		 AND sa.question_id = q.id
		LEFT JOIN LATERAL (
			SELECT aa.*
			FROM answer_attempts aa
			WHERE aa.enrollment_id = le.id
			  AND aa.lesson_version_id = le.lesson_version_id
			  AND aa.question_id = q.id
			ORDER BY aa.created_at DESC, aa.id DESC
			LIMIT 1
		) latest ON TRUE
		LEFT JOIN answer_attempt_reviews review ON review.answer_attempt_id = latest.id
		WHERE le.student_id = $1 AND le.id = $2
	`, studentID, enrollmentID, questionID).Scan(
		&answerID, &answerText, &answerUpdated, &answerStatus, &selectedIDs,
		&attemptID, &attemptAnswer, &attemptText, &attemptStatus, &result,
		&explanation, &missingJSON, &provider, &model, &durationMS, &errorCode,
		&attemptCreated, &completedAt, &reviewDecision, &reviewFeedback, &reviewedAt,
		&attemptOptions,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, mapDatabaseError("загрузить состояние ответа для вопроса", err)
	}

	var saved *domain.SavedAnswer
	if answerID.Valid {
		saved = &domain.SavedAnswer{
			AnswerID:          answerID.Int64,
			AnswerText:        answerText.String,
			SelectedOptionIDs: append([]int64(nil), selectedIDs...),
			UpdatedAt:         answerUpdated.Time,
			ProcessingStatus:  answerStatus.String,
		}
	}

	var latest *domain.AnswerAttempt
	if attemptID.Valid {
		latest = &domain.AnswerAttempt{
			ID:                attemptID.Int64,
			AnswerID:          attemptAnswer.Int64,
			SubmittedAnswer:   attemptText.String,
			ProcessingStatus:  attemptStatus.String,
			SelectedOptionIDs: append([]int64(nil), attemptOptions...),
			CreatedAt:         attemptCreated.Time,
		}
		if result.Valid {
			value := result.String
			latest.EvaluationResult = &value
		}
		if explanation.Valid {
			value := explanation.String
			latest.Explanation = &value
		}
		if provider.Valid {
			value := provider.String
			latest.Provider = &value
		}
		if model.Valid {
			value := model.String
			latest.Model = &value
		}
		if durationMS.Valid {
			value := durationMS.Int32
			latest.DurationMS = &value
		}
		if errorCode.Valid {
			value := errorCode.String
			latest.ErrorCode = &value
		}
		if completedAt.Valid {
			value := completedAt.Time
			latest.CompletedAt = &value
		}
		if reviewDecision.Valid {
			value := reviewDecision.String
			latest.ReviewDecision = &value
		}
		if reviewFeedback.Valid {
			value := reviewFeedback.String
			latest.ReviewFeedback = &value
		}
		if reviewedAt.Valid {
			value := reviewedAt.Time
			latest.ReviewedAt = &value
		}
		if len(missingJSON) > 0 {
			if err := json.Unmarshal(missingJSON, &latest.MissingPoints); err != nil {
				return nil, nil, mapDatabaseError("разобрать недостающие пункты последней попытки", err)
			}
		}
	}
	return saved, latest, nil
}
