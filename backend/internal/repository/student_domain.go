package repository

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (r *StudentRepository) EnsureByTelegramID(ctx context.Context, params EnsureStudentParams) (Student, error) {
	if params.TelegramID <= 0 || strings.TrimSpace(params.DisplayName) == "" {
		return Student{}, ErrInvalidInput
	}

	student, err := scanStudent(r.pool.QueryRow(ctx, `
		INSERT INTO students (
			telegram_id, display_name, telegram_username, preferred_name, last_active_at
		)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (telegram_id) DO UPDATE
		SET display_name = EXCLUDED.display_name,
		    telegram_username = EXCLUDED.telegram_username,
		    preferred_name = COALESCE(students.preferred_name, EXCLUDED.preferred_name),
		    account_status = CASE WHEN students.account_status = 'inactive' THEN 'active' ELSE students.account_status END,
		    last_active_at = now(),
		    updated_at = now()
		WHERE students.account_status <> 'blocked'
		RETURNING id, telegram_id, display_name, telegram_username, preferred_name,
		          account_status, registered_at, last_active_at, updated_at
	`, params.TelegramID, strings.TrimSpace(params.DisplayName),
		normalizeOptional(params.TelegramUsername), normalizeOptional(params.PreferredName)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Student{}, ErrInvalidState
		}
		return Student{}, mapDatabaseError("создать или обновить ученика Telegram", err)
	}
	return student, nil
}

func (r *StudentRepository) GetByID(ctx context.Context, studentID int64) (Student, error) {
	if studentID <= 0 {
		return Student{}, ErrInvalidInput
	}
	student, err := scanStudent(r.pool.QueryRow(ctx, `
		SELECT id, telegram_id, display_name, telegram_username, preferred_name,
		       account_status, registered_at, last_active_at, updated_at
		FROM students
		WHERE id = $1
	`, studentID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Student{}, ErrNotFound
		}
		return Student{}, mapDatabaseError("получить ученика по внутреннему ID", err)
	}
	return student, nil
}

func (r *StudentRepository) UpdateProfile(ctx context.Context, params UpdateStudentProfileParams) (Student, error) {
	if params.StudentID <= 0 || strings.TrimSpace(params.DisplayName) == "" {
		return Student{}, ErrInvalidInput
	}
	student, err := scanStudent(r.pool.QueryRow(ctx, `
		UPDATE students
		SET display_name = $2,
		    telegram_username = $3,
		    preferred_name = $4,
		    updated_at = now()
		WHERE id = $1 AND account_status <> 'blocked'
		RETURNING id, telegram_id, display_name, telegram_username, preferred_name,
		          account_status, registered_at, last_active_at, updated_at
	`, params.StudentID, strings.TrimSpace(params.DisplayName),
		normalizeOptional(params.TelegramUsername), normalizeOptional(params.PreferredName)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if checkErr := r.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM students WHERE id = $1)", params.StudentID).Scan(&exists); checkErr != nil {
				return Student{}, mapDatabaseError("проверить ученика перед обновлением профиля", checkErr)
			}
			if !exists {
				return Student{}, ErrNotFound
			}
			return Student{}, ErrInvalidState
		}
		return Student{}, mapDatabaseError("обновить профиль ученика", err)
	}
	return student, nil
}
