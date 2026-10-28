package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5"
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
		SELECT account.id, account.display_name, account.email, account.password_hash,
		       account.account_status, account.must_change_password,
		       ARRAY(
		           SELECT DISTINCT role.role_code
		           FROM staff_role_assignments assignment
		           JOIN staff_roles role ON role.id = assignment.role_id
		           WHERE assignment.staff_id = account.id AND assignment.revoked_at IS NULL
		           ORDER BY role.role_code
		       )
		FROM staff_accounts account
		WHERE lower(account.email) = lower($1)
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
		&credentials.MustChangePassword, &credentials.Roles,
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

// GetCredentialsByID returns credentials for an authenticated staff account.
func (r *StaffAuthRepository) GetCredentialsByID(ctx context.Context, staffID int64) (domain.StaffCredentials, error) {
	if staffID <= 0 {
		return domain.StaffCredentials{}, ErrInvalidInput
	}
	var credentials domain.StaffCredentials
	err := r.pool.QueryRow(ctx, `
		SELECT account.id, account.display_name, account.email, account.password_hash,
		       account.account_status, account.must_change_password,
		       ARRAY(
		           SELECT DISTINCT role.role_code
		           FROM staff_role_assignments assignment
		           JOIN staff_roles role ON role.id = assignment.role_id
		           WHERE assignment.staff_id = account.id AND assignment.revoked_at IS NULL
		           ORDER BY role.role_code
		       )
		FROM staff_accounts account
		WHERE account.id = $1
	`, staffID).Scan(
		&credentials.ID, &credentials.DisplayName, &credentials.Email,
		&credentials.PasswordHash, &credentials.AccountStatus,
		&credentials.MustChangePassword, &credentials.Roles,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StaffCredentials{}, ErrNotFound
	}
	if err != nil {
		return domain.StaffCredentials{}, mapDatabaseError("получить учётные данные сотрудника по ID", err)
	}
	return credentials, nil
}

// UpdatePasswordAndRevokeSessions changes the password and invalidates sessions atomically.
func (r *StaffAuthRepository) UpdatePasswordAndRevokeSessions(ctx context.Context, staffID int64, passwordHash string) error {
	if staffID <= 0 || passwordHash == "" {
		return ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapDatabaseError("начать смену пароля сотрудника", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE staff_accounts
		SET password_hash = $2, must_change_password = FALSE, updated_at = now()
		WHERE id = $1 AND account_status = 'active'
	`, staffID, passwordHash)
	if err != nil {
		return mapDatabaseError("сохранить новый пароль сотрудника", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, "UPDATE staff_sessions SET revoked_at = now() WHERE staff_id = $1 AND revoked_at IS NULL", staffID); err != nil {
		return mapDatabaseError("отозвать сессии после смены пароля", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_events (actor_staff_id, action, entity_type, entity_id, details)
		VALUES ($1, 'staff.password.change', 'staff_account', $1, jsonb_build_object('sessions_revoked', TRUE))
	`, staffID); err != nil {
		return mapDatabaseError("записать аудит смены пароля", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return mapDatabaseError("зафиксировать смену пароля сотрудника", err)
	}
	return nil
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

// GetActiveSessionByTokenHash validates a session token digest and returns its staff identity.
// Expired/revoked sessions and sessions belonging to non-active accounts are indistinguishable
// from unknown sessions. Only the supplied digest is queried; raw tokens are never persisted.
func (r *StaffAuthRepository) GetActiveSessionByTokenHash(
	ctx context.Context,
	tokenHash [32]byte,
) (domain.StaffSessionIdentity, error) {
	var identity domain.StaffSessionIdentity
	var csrfHash []byte

	err := r.pool.QueryRow(ctx, `
		UPDATE staff_sessions AS s
		SET last_seen_at = now()
		FROM staff_accounts AS a
		WHERE a.id = s.staff_id
			AND s.token_hash = $1
			AND s.revoked_at IS NULL
			AND s.expires_at > now()
			AND a.account_status = 'active'
			AND a.email IS NOT NULL
			AND length(btrim(a.email)) > 0
		RETURNING s.staff_id, a.display_name, a.email, s.csrf_token_hash, s.expires_at,
			a.must_change_password, ARRAY(
				SELECT DISTINCT role.role_code
				FROM staff_role_assignments assignment
				JOIN staff_roles role ON role.id = assignment.role_id
				WHERE assignment.staff_id = s.staff_id
					AND assignment.revoked_at IS NULL
				ORDER BY role.role_code
			) AS active_roles
	`, tokenHash[:]).Scan(
		&identity.StaffID,
		&identity.DisplayName,
		&identity.Email,
		&csrfHash,
		&identity.ExpiresAt,
		&identity.MustChangePassword,
		&identity.Roles,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StaffSessionIdentity{}, ErrNotFound
	}
	if err != nil {
		return domain.StaffSessionIdentity{}, mapDatabaseError("проверить сессию сотрудника", err)
	}
	if len(csrfHash) != len(identity.CSRFTokenHash) {
		return domain.StaffSessionIdentity{}, fmt.Errorf("проверить сессию сотрудника: некорректная длина хеша CSRF-токена")
	}
	copy(identity.CSRFTokenHash[:], csrfHash)
	return identity, nil
}

// RevokeSessionByTokenHash invalidates a session without requiring the caller
// to know whether the token exists. Repeated logout calls are intentionally idempotent.
func (r *StaffAuthRepository) RevokeSessionByTokenHash(ctx context.Context, tokenHash [32]byte) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE staff_sessions
		SET revoked_at = now()
		WHERE token_hash = $1
			AND revoked_at IS NULL
	`, tokenHash[:])
	if err != nil {
		return mapDatabaseError("отозвать сессию сотрудника", err)
	}
	return nil
}
