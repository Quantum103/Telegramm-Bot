package repository

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (r *StudentRepository) EnsureByTelegramID(ctx context.Context, params EnsureStudentParams) (Student, error) {
	displayName := strings.TrimSpace(params.DisplayName)
	if params.TelegramID <= 0 || displayName == "" {
		return Student{}, ErrInvalidInput
	}

	username := normalizeOptionalString(params.TelegramUsername)
	preferredName := normalizeOptionalString(params.PreferredName)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Student{}, mapDatabaseError("начать регистрацию ученика Telegram", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	student, activityDue, err := scanStudentWithActivityDue(tx.QueryRow(ctx, `
		SELECT id, telegram_id, display_name, telegram_username, preferred_name,
		       account_status, registered_at, last_active_at, updated_at,
		       last_active_at IS NULL OR last_active_at <= now() - interval '1 minute'
		FROM students
		WHERE telegram_id = $1
		FOR UPDATE
	`, params.TelegramID))

	if errors.Is(err, pgx.ErrNoRows) {
		// DO NOTHING сохраняет атомарность регистрации при параллельных /start,
		// не создавая новую версию строки при конфликте.
		student, err = scanStudent(tx.QueryRow(ctx, `
			INSERT INTO students (
				telegram_id, display_name, telegram_username, preferred_name, last_active_at
			)
			VALUES ($1, $2, $3, $4, now())
			ON CONFLICT (telegram_id) DO NOTHING
			RETURNING id, telegram_id, display_name, telegram_username, preferred_name,
			          account_status, registered_at, last_active_at, updated_at
		`, params.TelegramID, displayName, username, preferredName))
		if err == nil {
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return Student{}, mapDatabaseError("завершить регистрацию ученика Telegram", commitErr)
			}
			return student, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Student{}, mapDatabaseError("создать ученика Telegram", err)
		}

		// Другой запрос мог зарегистрировать тот же Telegram ID после первого
		// SELECT. Новый SQL-оператор в READ COMMITTED увидит уже зафиксированную строку.
		student, activityDue, err = scanStudentWithActivityDue(tx.QueryRow(ctx, `
			SELECT id, telegram_id, display_name, telegram_username, preferred_name,
			       account_status, registered_at, last_active_at, updated_at,
			       last_active_at IS NULL OR last_active_at <= now() - interval '1 minute'
			FROM students
			WHERE telegram_id = $1
			FOR UPDATE
		`, params.TelegramID))
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Student{}, ErrNotFound
		}
		return Student{}, mapDatabaseError("найти ученика Telegram", err)
	}

	// Блокировка проверяется на актуальной заблокированной строке под row lock.
	// Никакого локального кэша статуса аккаунта здесь нет.
	if student.AccountStatus == "blocked" {
		return Student{}, ErrInvalidState
	}

	profileChanged := student.DisplayName != displayName ||
		!sameOptionalString(student.TelegramUsername, username) ||
		(student.PreferredName == nil && preferredName != nil)
	needsUpdate := profileChanged || student.AccountStatus == "inactive" || activityDue
	if !needsUpdate {
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return Student{}, mapDatabaseError("завершить проверку ученика Telegram", commitErr)
		}
		return student, nil
	}

	student, err = scanStudent(tx.QueryRow(ctx, `
		UPDATE students
		SET display_name = $2,
		    telegram_username = $3,
		    preferred_name = COALESCE(preferred_name, $4),
		    account_status = CASE WHEN account_status = 'inactive' THEN 'active' ELSE account_status END,
		    last_active_at = CASE WHEN $5 THEN now() ELSE last_active_at END,
		    updated_at = now()
		WHERE id = $1 AND account_status <> 'blocked'
		RETURNING id, telegram_id, display_name, telegram_username, preferred_name,
		          account_status, registered_at, last_active_at, updated_at
	`, student.ID, displayName, username, preferredName, activityDue))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Student{}, ErrInvalidState
		}
		return Student{}, mapDatabaseError("обновить ученика Telegram", err)
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		return Student{}, mapDatabaseError("завершить обновление ученика Telegram", commitErr)
	}
	return student, nil
}

func scanStudentWithActivityDue(row pgx.Row) (Student, bool, error) {
	var student Student
	var username, preferredName pgtype.Text
	var lastActiveAt pgtype.Timestamptz
	var activityDue bool

	err := row.Scan(
		&student.ID, &student.TelegramID, &student.DisplayName,
		&username, &preferredName, &student.AccountStatus,
		&student.RegisteredAt, &lastActiveAt, &student.UpdatedAt, &activityDue,
	)
	if err != nil {
		return Student{}, false, err
	}
	if username.Valid {
		value := username.String
		student.TelegramUsername = &value
	}
	if preferredName.Valid {
		value := preferredName.String
		student.PreferredName = &value
	}
	if lastActiveAt.Valid {
		value := lastActiveAt.Time
		student.LastActiveAt = &value
	}
	return student, activityDue, nil
}

func normalizeOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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
