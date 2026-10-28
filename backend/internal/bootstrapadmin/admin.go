// Package bootstrapadmin creates the first administrator for local installations.
package bootstrapadmin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultAdminEmail = "admin@localhost.test"
	adminDisplayName  = "Администратор"
)

// Result describes the one-time administrator initialization result.
// GeneratedPassword is populated only when a new password was generated.
type Result struct {
	Created           bool
	Email             string
	GeneratedPassword string
}

// EnsureInitialAdministrator creates an administrator only when no active
// administrator exists. The role row lock makes concurrent backend starts safe.
func EnsureInitialAdministrator(ctx context.Context, pool *pgxpool.Pool, email, password string) (Result, error) {
	if pool == nil {
		return Result{}, errors.New("PostgreSQL pool is nil")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("begin administrator bootstrap transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var administratorRoleID int64
	if err := tx.QueryRow(ctx, `
		SELECT id FROM staff_roles WHERE role_code = 'administrator' FOR UPDATE
	`).Scan(&administratorRoleID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, errors.New("administrator role is missing; apply database migrations first")
		}
		return Result{}, fmt.Errorf("lock administrator role: %w", err)
	}

	var hasActiveAdministrator bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM staff_role_assignments assignment
			JOIN staff_accounts account ON account.id = assignment.staff_id
			WHERE assignment.role_id = $1
				AND assignment.revoked_at IS NULL
				AND account.account_status = 'active'
		)
	`, administratorRoleID).Scan(&hasActiveAdministrator); err != nil {
		return Result{}, fmt.Errorf("check for an existing administrator: %w", err)
	}
	if hasActiveAdministrator {
		return Result{}, nil
	}

	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		email = defaultAdminEmail
	}
	if err := validateEmail(email); err != nil {
		return Result{}, err
	}

	generatedPassword := ""
	if password == "" {
		password, err = newPassword()
		if err != nil {
			return Result{}, fmt.Errorf("generate initial administrator password: %w", err)
		}
		generatedPassword = password
	} else if strings.TrimSpace(password) == "" {
		return Result{}, errors.New("BOOTSTRAP_ADMIN_PASSWORD cannot contain only whitespace")
	}

	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		return Result{}, fmt.Errorf("hash initial administrator password: %w", err)
	}

	var staffID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO staff_accounts (display_name, email, password_hash, account_status, must_change_password)
		VALUES ($1, $2, $3, 'active', $4)
		RETURNING id
	`, adminDisplayName, email, passwordHash, generatedPassword != "").Scan(&staffID); err != nil {
		return Result{}, fmt.Errorf("create initial administrator account for %q: %w", email, err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO staff_role_assignments (staff_id, role_id, granted_by_staff_id)
		VALUES ($1, $2, NULL)
	`, staffID, administratorRoleID); err != nil {
		return Result{}, fmt.Errorf("assign administrator role: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_events (actor_staff_id, action, entity_type, entity_id, details)
		VALUES (NULL, 'staff.bootstrap.admin_created', 'staff_account', $1,
			jsonb_build_object('role_code', 'administrator', 'source', 'startup'))
	`, staffID); err != nil {
		return Result{}, fmt.Errorf("record initial administrator creation: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("commit initial administrator creation: %w", err)
	}

	return Result{
		Created: true, Email: email, GeneratedPassword: generatedPassword,
	}, nil
}

func newPassword() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func validateEmail(email string) error {
	if len(email) == 0 || len(email) > 254 {
		return errors.New("BOOTSTRAP_ADMIN_EMAIL must contain 1–254 characters")
	}
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || strings.ContainsAny(email, "\r\n") {
		return errors.New("BOOTSTRAP_ADMIN_EMAIL must be a plain email address")
	}
	return nil
}
