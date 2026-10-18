package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AnswerRepository struct {
	pool *pgxpool.Pool
}

func NewAnswerRepository(pool *pgxpool.Pool) *AnswerRepository {
	return &AnswerRepository{pool: pool}
}

type SaveTextAnswerParams struct {
	StudentID      int64
	EnrollmentID   int64
	QuestionID     int64
	AnswerText     string
	IdempotencyKey string
}

type SavedAnswer struct {
	AnswerID         int64
	AttemptID        int64
	AnswerText       string
	ProcessingStatus string
	Replayed         bool
	UpdatedAt        time.Time
}

type EvaluationParams struct {
	AttemptID       int64
	Result          string
	Explanation     *string
	MissingPoints   []string
	Provider        string
	Model           string
	Duration        time.Duration
}

type AttemptFailureParams struct {
	AttemptID int64
	Provider  string
	Model     string
	ErrorCode string
	Duration  time.Duration
}

type AnswerAttempt struct {
	ID               int64
	AnswerID         int64
	SubmittedAnswer  string
	ProcessingStatus string
	EvaluationResult *string
	Explanation      *string
	MissingPoints    []string
	Provider         *string
	Model            *string
	DurationMS       *int32
	ErrorCode        *string
	CreatedAt        time.Time
	CompletedAt      *time.Time
}

// SaveTextAnswer записывает текущий ответ и новую попытку одной транзакцией.
// Блокировка прохождения сериализует повторные запросы с одним idempotency key.
func (r *AnswerRepository) SaveTextAnswer(ctx context.Context, params SaveTextAnswerParams) (SavedAnswer, error) {
	if params.StudentID <= 0 || params.EnrollmentID <= 0 || params.QuestionID <= 0 ||
		strings.TrimSpace(params.AnswerText) == "" {
		return SavedAnswer{}, ErrInvalidInput
	}
	if params.IdempotencyKey != "" && !isCanonicalUUID(params.IdempotencyKey) {
		return SavedAnswer{}, fmt.Errorf("%w: idempotency key должен быть UUID", ErrInvalidInput)
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SavedAnswer{}, mapDatabaseError("начать транзакцию сохранения ответа", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lessonVersionID int64
	var enrollmentStatus string
	err = tx.QueryRow(ctx, `
		SELECT lesson_version_id, status
		FROM lesson_enrollments
		WHERE id = $1 AND student_id = $2
		FOR UPDATE
	`, params.EnrollmentID, params.StudentID).Scan(&lessonVersionID, &enrollmentStatus)
	if err != nil {
		if err == pgx.ErrNoRows {
			return SavedAnswer{}, ErrNotFound
		}
		return SavedAnswer{}, mapDatabaseError("проверить прохождение ученика", err)
	}
	if enrollmentStatus == "cancelled" {
		return SavedAnswer{}, ErrInvalidState
	}

	var questionExists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM lesson_questions
			WHERE id = $1 AND lesson_version_id = $2
		)
	`, params.QuestionID, lessonVersionID).Scan(&questionExists); err != nil {
		return SavedAnswer{}, mapDatabaseError("проверить вопрос прохождения", err)
	}
	if !questionExists {
		return SavedAnswer{}, ErrNotFound
	}

	if params.IdempotencyKey != "" {
		var replay SavedAnswer
		var previousQuestionID int64
		err := tx.QueryRow(ctx, `
			SELECT aa.id, aa.answer_id, aa.question_id, aa.submitted_answer_text,
			       aa.processing_status, sa.updated_at
			FROM answer_attempts aa
			JOIN student_answers sa ON sa.id = aa.answer_id
			WHERE aa.enrollment_id = $1 AND aa.idempotency_key = $2::uuid
		`, params.EnrollmentID, params.IdempotencyKey).Scan(
			&replay.AttemptID, &replay.AnswerID, &previousQuestionID, &replay.AnswerText,
			&replay.ProcessingStatus, &replay.UpdatedAt,
		)
		switch {
		case err == nil:
			if previousQuestionID != params.QuestionID {
				return SavedAnswer{}, ErrConflict
			}
			if err := tx.Commit(ctx); err != nil {
				return SavedAnswer{}, mapDatabaseError("завершить транзакцию повторного запроса", err)
			}
			replay.Replayed = true
			return replay, nil
		case err != pgx.ErrNoRows:
			return SavedAnswer{}, mapDatabaseError("проверить ключ идемпотентности", err)
		}
	}

	var answerID int64
	var answerUpdatedAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO student_answers (
			enrollment_id, lesson_version_id, question_id, answer_text
		)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (enrollment_id, question_id)
		DO UPDATE SET answer_text = EXCLUDED.answer_text, updated_at = now()
		RETURNING id, updated_at
	`, params.EnrollmentID, lessonVersionID, params.QuestionID, params.AnswerText).Scan(&answerID, &answerUpdatedAt)
	if err != nil {
		return SavedAnswer{}, mapDatabaseError("сохранить текущий ответ", err)
	}

	// При смене типа ответа со вариантов на текст старые выборы не должны оставаться.
	if _, err := tx.Exec(ctx, `DELETE FROM student_answer_options WHERE answer_id = $1`, answerID); err != nil {
		return SavedAnswer{}, mapDatabaseError("обновить выбранные варианты ответа", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE lesson_enrollments
		SET current_question_id = $2,
		    status = CASE WHEN status IN ('not_started', 'paused') THEN 'in_progress' ELSE status END,
		    started_at = COALESCE(started_at, now()),
		    last_activity_at = now(),
		    updated_at = now()
		WHERE id = $1
	`, params.EnrollmentID, params.QuestionID); err != nil {
		return SavedAnswer{}, mapDatabaseError("обновить прогресс прохождения", err)
	}

	var idempotencyKey any
	if params.IdempotencyKey != "" {
		idempotencyKey = params.IdempotencyKey
	}
	var attemptID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO answer_attempts (
			answer_id, enrollment_id, lesson_version_id, question_id,
			idempotency_key, submitted_answer_text, processing_status
		)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending')
		RETURNING id
	`, answerID, params.EnrollmentID, lessonVersionID, params.QuestionID,
		idempotencyKey, params.AnswerText).Scan(&attemptID)
	if err != nil {
		return SavedAnswer{}, mapDatabaseError("создать историю попыток ответа", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return SavedAnswer{}, mapDatabaseError("зафиксировать ответ и прогресс", err)
	}
	return SavedAnswer{
		AnswerID: answerID, AttemptID: attemptID, AnswerText: params.AnswerText,
		ProcessingStatus: "pending", UpdatedAt: answerUpdatedAt,
	}, nil
}

func (r *AnswerRepository) GetSavedTextAnswer(ctx context.Context, studentID, enrollmentID, questionID int64) (SavedAnswer, error) {
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
		if err == pgx.ErrNoRows {
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
	return answer, nil
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
