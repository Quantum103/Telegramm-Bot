package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AnswerRepository struct {
	pool *pgxpool.Pool
}

func NewAnswerRepository(pool *pgxpool.Pool) *AnswerRepository {
	return &AnswerRepository{pool: pool}
}

type SaveTextAnswerParams = domain.SaveTextAnswerParams
type AnswerSubmission = domain.AnswerSubmission
type SavedAnswer = domain.SavedAnswer
type EvaluationParams = domain.EvaluationParams
type AttemptFailureParams = domain.AttemptFailureParams
type AnswerAttempt = domain.AnswerAttempt

// SaveTextAnswer сохраняет свободный текст через общий путь отправки ответа.
func (r *AnswerRepository) SaveTextAnswer(ctx context.Context, params SaveTextAnswerParams) (SavedAnswer, error) {
	return r.Submit(ctx, AnswerSubmission{
		StudentID: params.StudentID, EnrollmentID: params.EnrollmentID,
		QuestionID: params.QuestionID, AnswerText: params.AnswerText,
		IdempotencyKey: params.IdempotencyKey,
	})
}

// GetSavedTextAnswer сохраняет совместимость с ранними вызовами репозитория.
func (r *AnswerRepository) GetSavedTextAnswer(ctx context.Context, studentID, enrollmentID, questionID int64) (SavedAnswer, error) {
	return r.GetSavedAnswer(ctx, studentID, enrollmentID, questionID)
}

func (r *AnswerRepository) MarkAttemptRunning(ctx context.Context, attemptID int64) error {
	if attemptID <= 0 {
		return ErrInvalidInput
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE answer_attempts
		SET processing_status = 'running'
		WHERE id = $1 AND processing_status = 'pending'
	`, attemptID)
	if err != nil {
		return mapDatabaseError("начать проверку ответа", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	return r.checkAttemptState(ctx, attemptID)
}

func (r *AnswerRepository) CompleteEvaluation(ctx context.Context, params EvaluationParams) error {
	if params.AttemptID <= 0 || !validEvaluationResult(params.Result) ||
		strings.TrimSpace(params.Provider) == "" || strings.TrimSpace(params.Model) == "" ||
		params.Duration < 0 {
		return ErrInvalidInput
	}
	if params.MissingPoints == nil {
		params.MissingPoints = []string{}
	}
	missingJSON, err := json.Marshal(params.MissingPoints)
	if err != nil {
		return fmt.Errorf("сериализовать недостающие пункты: %w", err)
	}
	durationMS := params.Duration.Milliseconds()
	if durationMS > int64(^uint32(0)>>1) {
		return fmt.Errorf("%w: слишком большая длительность проверки", ErrInvalidInput)
	}

	tag, err := r.pool.Exec(ctx, `
		UPDATE answer_attempts
		SET processing_status = 'completed',
		    evaluation_result = $2,
		    explanation = $3,
		    missing_points = $4::jsonb,
		    provider = $5,
		    model = $6,
		    duration_ms = $7,
		    completed_at = now()
		WHERE id = $1 AND processing_status IN ('pending', 'running')
	`, params.AttemptID, params.Result, params.Explanation, string(missingJSON),
		strings.TrimSpace(params.Provider), strings.TrimSpace(params.Model), int32(durationMS))
	if err != nil {
		return mapDatabaseError("сохранить результат проверки ответа", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	return r.checkAttemptState(ctx, params.AttemptID)
}

// FailAttempt фиксирует техническую ошибку отдельным статусом, не превращая её в неправильный ответ.
func (r *AnswerRepository) FailAttempt(ctx context.Context, params AttemptFailureParams) error {
	if params.AttemptID <= 0 || strings.TrimSpace(params.Provider) == "" ||
		strings.TrimSpace(params.Model) == "" || strings.TrimSpace(params.ErrorCode) == "" ||
		params.Duration < 0 {
		return ErrInvalidInput
	}
	durationMS := params.Duration.Milliseconds()
	if durationMS > int64(^uint32(0)>>1) {
		return fmt.Errorf("%w: слишком большая длительность проверки", ErrInvalidInput)
	}

	tag, err := r.pool.Exec(ctx, `
		UPDATE answer_attempts
		SET processing_status = 'failed',
		    provider = $2,
		    model = $3,
		    duration_ms = $4,
		    error_code = $5,
		    completed_at = now()
		WHERE id = $1 AND processing_status IN ('pending', 'running')
	`, params.AttemptID, strings.TrimSpace(params.Provider), strings.TrimSpace(params.Model),
		int32(durationMS), strings.TrimSpace(params.ErrorCode))
	if err != nil {
		return mapDatabaseError("сохранить ошибку проверки ответа", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	return r.checkAttemptState(ctx, params.AttemptID)
}

func (r *AnswerRepository) ListAttemptHistory(ctx context.Context, studentID, enrollmentID, questionID int64) ([]AnswerAttempt, error) {
	if studentID <= 0 || enrollmentID <= 0 || questionID <= 0 {
		return nil, ErrInvalidInput
	}

	rows, err := r.pool.Query(ctx, `
		SELECT aa.id, aa.answer_id, aa.submitted_answer_text, aa.processing_status,
		       aa.evaluation_result, aa.explanation, aa.missing_points,
		       aa.provider, aa.model, aa.duration_ms, aa.error_code,
		       aa.created_at, aa.completed_at
		FROM answer_attempts aa
		JOIN lesson_enrollments le
		  ON le.id = aa.enrollment_id AND le.lesson_version_id = aa.lesson_version_id
		WHERE le.student_id = $1 AND le.id = $2 AND aa.question_id = $3
		ORDER BY aa.created_at DESC, aa.id DESC
	`, studentID, enrollmentID, questionID)
	if err != nil {
		return nil, mapDatabaseError("получить историю попыток", err)
	}
	defer rows.Close()

	attempts := make([]AnswerAttempt, 0)
	for rows.Next() {
		var attempt AnswerAttempt
		var result, explanation, provider, model, errorCode pgtype.Text
		var durationMS pgtype.Int4
		var completedAt pgtype.Timestamptz
		var missingJSON []byte

		if err := rows.Scan(
			&attempt.ID, &attempt.AnswerID, &attempt.SubmittedAnswer, &attempt.ProcessingStatus,
			&result, &explanation, &missingJSON, &provider, &model, &durationMS, &errorCode,
			&attempt.CreatedAt, &completedAt,
		); err != nil {
			return nil, mapDatabaseError("прочитать историю попыток", err)
		}
		if result.Valid {
			value := result.String
			attempt.EvaluationResult = &value
		}
		if explanation.Valid {
			value := explanation.String
			attempt.Explanation = &value
		}
		if provider.Valid {
			value := provider.String
			attempt.Provider = &value
		}
		if model.Valid {
			value := model.String
			attempt.Model = &value
		}
		if errorCode.Valid {
			value := errorCode.String
			attempt.ErrorCode = &value
		}
		if durationMS.Valid {
			value := durationMS.Int32
			attempt.DurationMS = &value
		}
		if completedAt.Valid {
			value := completedAt.Time
			attempt.CompletedAt = &value
		}
		if err := json.Unmarshal(missingJSON, &attempt.MissingPoints); err != nil {
			return nil, mapDatabaseError("разобрать недостающие пункты ответа", err)
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("обойти историю попыток", err)
	}
	return attempts, nil
}

func (r *AnswerRepository) checkAttemptState(ctx context.Context, attemptID int64) error {
	var state string
	err := r.pool.QueryRow(ctx, `SELECT processing_status FROM answer_attempts WHERE id = $1`, attemptID).Scan(&state)
	if err == pgx.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return mapDatabaseError("проверить состояние попытки", err)
	}
	return ErrInvalidState
}

func validEvaluationResult(result string) bool {
	switch result {
	case "correct", "partial", "incorrect", "ambiguous":
		return true
	default:
		return false
	}
}

func isCanonicalUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}
