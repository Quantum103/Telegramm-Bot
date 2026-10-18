package repository

import (
	"context"
	"strings"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Student = domain.Student
type CreateStudentParams = domain.CreateStudentParams
type EnsureStudentParams = domain.EnsureStudentParams
type UpdateStudentProfileParams = domain.UpdateStudentProfileParams

type StudentRepository struct {
	pool *pgxpool.Pool
}

func NewStudentRepository(pool *pgxpool.Pool) *StudentRepository {
	return &StudentRepository{pool: pool}
}

func (r *StudentRepository) Create(ctx context.Context, params CreateStudentParams) (Student, error) {
	if params.TelegramID <= 0 || strings.TrimSpace(params.DisplayName) == "" {
		return Student{}, ErrInvalidInput
	}

	const query = `
		INSERT INTO students (telegram_id, display_name, telegram_username, preferred_name)
		VALUES ($1, $2, $3, $4)
		RETURNING id, telegram_id, display_name, telegram_username, preferred_name,
		          account_status, registered_at, last_active_at, updated_at
	`

	student, err := scanStudent(r.pool.QueryRow(
		ctx, query, params.TelegramID, strings.TrimSpace(params.DisplayName),
		normalizeOptional(params.TelegramUsername), normalizeOptional(params.PreferredName),
	))
	if err != nil {
		return Student{}, mapDatabaseError("создать ученика", err)
	}
	return student, nil
}

func (r *StudentRepository) GetByTelegramID(ctx context.Context, telegramID int64) (Student, error) {
	if telegramID <= 0 {
		return Student{}, ErrInvalidInput
	}

	const query = `
		SELECT id, telegram_id, display_name, telegram_username, preferred_name,
		       account_status, registered_at, last_active_at, updated_at
		FROM students
		WHERE telegram_id = $1
	`
	student, err := scanStudent(r.pool.QueryRow(ctx, query, telegramID))
	if err != nil {
		if err == pgx.ErrNoRows {
			return Student{}, ErrNotFound
		}
		return Student{}, mapDatabaseError("найти ученика по Telegram ID", err)
	}
	return student, nil
}

func scanStudent(row pgx.Row) (Student, error) {
	var student Student
	var username, preferredName pgtype.Text
	var lastActiveAt pgtype.Timestamptz

	err := row.Scan(
		&student.ID, &student.TelegramID, &student.DisplayName,
		&username, &preferredName, &student.AccountStatus,
		&student.RegisteredAt, &lastActiveAt, &student.UpdatedAt,
	)
	if err != nil {
		return Student{}, err
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
	return student, nil
}

func normalizeOptional(value *string) any {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return normalized
}
