package repository

import (
	"context"
	"errors"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TelegramDialogRepository struct {
	pool *pgxpool.Pool
}

func NewTelegramDialogRepository(pool *pgxpool.Pool) *TelegramDialogRepository {
	return &TelegramDialogRepository{pool: pool}
}

func (r *TelegramDialogRepository) Get(ctx context.Context, studentID int64) (domain.TelegramDialogState, error) {
	if studentID <= 0 {
		return domain.TelegramDialogState{}, ErrInvalidInput
	}
	var state domain.TelegramDialogState
	var messageID, courseID, lessonID, enrollmentID, questionID pgtype.Int8
	err := r.pool.QueryRow(ctx, `
		SELECT student_id, chat_id, message_id, screen, course_id, lesson_id,
		       enrollment_id, question_id, awaiting_text, selected_option_ids, updated_at
		FROM telegram_dialog_states WHERE student_id = $1
	`, studentID).Scan(
		&state.StudentID, &state.ChatID, &messageID, &state.Screen, &courseID,
		&lessonID, &enrollmentID, &questionID, &state.AwaitingText,
		&state.SelectedOptionIDs, &state.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.TelegramDialogState{}, ErrNotFound
		}
		return domain.TelegramDialogState{}, mapDatabaseError("получить состояние диалога Telegram", err)
	}
	if messageID.Valid {
		state.MessageID = int64Pointer(messageID.Int64)
	}
	if courseID.Valid {
		state.CourseID = int64Pointer(courseID.Int64)
	}
	if lessonID.Valid {
		state.LessonID = int64Pointer(lessonID.Int64)
	}
	if enrollmentID.Valid {
		state.EnrollmentID = int64Pointer(enrollmentID.Int64)
	}
	if questionID.Valid {
		state.QuestionID = int64Pointer(questionID.Int64)
	}
	if state.SelectedOptionIDs == nil {
		state.SelectedOptionIDs = []int64{}
	}
	return state, nil
}

func (r *TelegramDialogRepository) Save(ctx context.Context, state domain.TelegramDialogState) error {
	if state.StudentID <= 0 || state.ChatID <= 0 {
		return ErrInvalidInput
	}
	if state.Screen == "" {
		state.Screen = "home"
	}
	if state.SelectedOptionIDs == nil {
		state.SelectedOptionIDs = []int64{}
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO telegram_dialog_states (
			student_id, chat_id, message_id, screen, course_id, lesson_id,
			enrollment_id, question_id, awaiting_text, selected_option_ids, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
		ON CONFLICT (student_id) DO UPDATE SET
			chat_id = EXCLUDED.chat_id,
			message_id = EXCLUDED.message_id,
			screen = EXCLUDED.screen,
			course_id = EXCLUDED.course_id,
			lesson_id = EXCLUDED.lesson_id,
			enrollment_id = EXCLUDED.enrollment_id,
			question_id = EXCLUDED.question_id,
			awaiting_text = EXCLUDED.awaiting_text,
			selected_option_ids = EXCLUDED.selected_option_ids,
			updated_at = now()
	`, state.StudentID, state.ChatID, pointerValue(state.MessageID), state.Screen,
		pointerValue(state.CourseID), pointerValue(state.LessonID), pointerValue(state.EnrollmentID),
		pointerValue(state.QuestionID), state.AwaitingText, state.SelectedOptionIDs)
	if err != nil {
		return mapDatabaseError("сохранить состояние диалога Telegram", err)
	}
	return nil
}

// BeginUpdate позволяет повторить незавершённое обновление, но пропускает уже завершённое.
func (r *TelegramDialogRepository) BeginUpdate(ctx context.Context, updateID int64) (bool, error) {
	if updateID < 0 {
		return false, ErrInvalidInput
	}
	var claimed int64
	err := r.pool.QueryRow(ctx, `
		INSERT INTO telegram_processed_updates (update_id, status, started_at, processed_at)
		VALUES ($1, 'processing', now(), NULL)
		ON CONFLICT (update_id) DO UPDATE SET
			status = 'processing', started_at = now(), processed_at = NULL
		WHERE telegram_processed_updates.status <> 'processed'
		RETURNING update_id
	`, updateID).Scan(&claimed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, mapDatabaseError("зарегистрировать обновление Telegram", err)
	}
	return true, nil
}

func (r *TelegramDialogRepository) CompleteUpdate(ctx context.Context, updateID int64) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE telegram_processed_updates
		SET status = 'processed', processed_at = now()
		WHERE update_id = $1
	`, updateID)
	if err != nil {
		return mapDatabaseError("завершить обработку обновления Telegram", err)
	}
	_, err = r.pool.Exec(ctx, `
		DELETE FROM telegram_processed_updates
		WHERE status = 'processed' AND processed_at < now() - interval '30 days'
	`)
	if err != nil {
		return mapDatabaseError("очистить историю обновлений Telegram", err)
	}
	return nil
}

func (r *TelegramDialogRepository) FailUpdate(ctx context.Context, updateID int64) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE telegram_processed_updates
		SET status = 'failed', processed_at = NULL
		WHERE update_id = $1 AND status <> 'processed'
	`, updateID)
	if err != nil {
		return mapDatabaseError("пометить неудачную обработку Telegram update", err)
	}
	return nil
}

func int64Pointer(value int64) *int64 { return &value }

func pointerValue(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
