package repository

import (
	"context"
	"strings"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StaffAuthRepository reads staff credentials and persists server-side sessions.
type StaffAuthRepository struct {
	pool *pgxpool.Pool
}

func NewStaffAuthRepository(pool *pgxpool.Pool) *StaffAuthRepository {
	return &StaffAuthRepository{pool: pool}
}

// GetCredentialsByEmail returns only the fields required to verify a login.
// If legacy data contains case-insensitive duplicate emails, authentication fails closed.
func (r *StaffAuthRepository) GetCredentialsByEmail(ctx context.Context, email string) (domain.StaffCredentials, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return domain.StaffCredentials{}, ErrInvalidInput
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, display_name, email, password_hash, account_status
		FROM staff_accounts
		WHERE lower(email) = lower($1)
		LIMIT 2
	`, email)
	if err != nil {
		return domain.StaffCredentials{}, mapDatabaseError("получить учетные данные сотрудника", err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return domain.StaffCredentials{}, mapDatabaseError("прочитать учетные данные сотрудника", err)
		}
		return domain.StaffCredentials{}, ErrNotFound
	}

	var credentials domain.StaffCredentials
	if err := rows.Scan(
		&credentials.ID, &credentials.DisplayName, &credentials.Email,
		&credentials.PasswordHash, &credentials.AccountStatus,
	); err != nil {
		return domain.StaffCredentials{}, mapDatabaseError("прочитать учетные данные сотрудника", err)
	}
	if rows.Next() {
		return domain.StaffCredentials{}, ErrInvalidInput
	}
	if err := rows.Err(); err != nil {
		return domain.StaffCredentials{}, mapDatabaseError("прочитать учетные данные сотрудника", err)
	}
	return credentials, nil
}

// CreateSession persists only token digests; raw tokens must never be stored.
func (r *StaffAuthRepository) CreateSession(ctx context.Context, session domain.StaffSession) error {
	if session.StaffID <= 0 || session.ExpiresAt.IsZero() || !session.ExpiresAt.After(time.Now()) {
		return ErrInvalidInput
	}
	const query = `
		INSERT INTO staff_sessions (staff_id, token_hash, csrf_token_hash, expires_at)
		VALUES ($1, $2, $3, $4)
	`
	_, err := r.pool.Exec(ctx, query,
		session.StaffID, session.TokenHash[:], session.CSRFTokenHash[:], session.ExpiresAt,
	)
	if err != nil {
		return mapDatabaseError("создать сессию сотрудника", err)
	}
	return nil
}
