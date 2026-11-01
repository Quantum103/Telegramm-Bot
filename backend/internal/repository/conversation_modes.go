package repository

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// SetStudentChatMode хранит выбранный учеником режим в PostgreSQL, чтобы перезапуск бота не смешивал ответы уроков и чат.
func (r *ConversationRepository) SetStudentChatMode(ctx context.Context, studentID int64, enabled bool) error {
	if studentID <= 0 {
		return ErrInvalidInput
	}
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO student_chat_modes (student_id, chat_mode_enabled)
		SELECT id, $2 FROM students
		WHERE id = $1 AND account_status = 'active'
		ON CONFLICT (student_id) DO UPDATE
		SET chat_mode_enabled = EXCLUDED.chat_mode_enabled, updated_at = now()
	`, studentID, enabled)
	if err != nil {
		return mapDatabaseError("сохранить режим общения ученика", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *ConversationRepository) IsStudentChatMode(ctx context.Context, studentID int64) (bool, error) {
	if studentID <= 0 {
		return false, ErrInvalidInput
	}
	var enabled bool
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(
			(SELECT chat_mode_enabled FROM student_chat_modes WHERE student_id = $1),
			FALSE
		)
		FROM students WHERE id = $1 AND account_status = 'active'
	`, studentID).Scan(&enabled)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, ErrNotFound
		}
		return false, mapDatabaseError("получить режим общения ученика", err)
	}
	return enabled, nil
}
