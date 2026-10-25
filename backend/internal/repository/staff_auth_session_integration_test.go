package repository

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestGetActiveSessionByTokenHash(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	repo := NewStaffAuthRepository(pool)
	prefix := fmt.Sprintf("staff-session-test-%d-", time.Now().UnixNano())

	insertStaff := func(status, suffix string) int64 {
		t.Helper()
		var id int64
		err := pool.QueryRow(ctx, `
			INSERT INTO staff_accounts (display_name, email, account_status)
			VALUES ($1, $2, $3)
			RETURNING id
		`, "Session test "+suffix, prefix+suffix+"@example.test", status).Scan(&id)
		if err != nil {
			t.Fatalf("create %s staff account: %v", suffix, err)
		}
		return id
	}

	activeStaffID := insertStaff("active", "active")
	inactiveStaffID := insertStaff("inactive", "inactive")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM staff_accounts WHERE id IN ($1, $2)", activeStaffID, inactiveStaffID); err != nil {
			t.Errorf("cleanup staff auth integration data: %v", err)
		}
	})

	now := time.Now()
	validExpiry := now.Add(time.Hour)
	validTokenHash := sha256.Sum256([]byte(prefix + "valid-token"))
	validCSRFHash := sha256.Sum256([]byte(prefix + "csrf-token"))

	insertSession := func(staffID int64, tokenHash, csrfHash [32]byte, expiresAt time.Time, revokedAt *time.Time) {
		t.Helper()
		_, err := pool.Exec(ctx, `
			INSERT INTO staff_sessions (staff_id, token_hash, csrf_token_hash, created_at, expires_at, revoked_at)
			VALUES ($1, $2, $3, now() - INTERVAL '2 hours', $4, $5)
		`, staffID, tokenHash[:], csrfHash[:], expiresAt, revokedAt)
		if err != nil {
			t.Fatalf("create session fixture: %v", err)
		}
	}

	insertSession(activeStaffID, validTokenHash, validCSRFHash, validExpiry, nil)

	revokedAt := now.Add(-time.Minute)
	revokedHash := sha256.Sum256([]byte(prefix + "revoked-token"))
	insertSession(activeStaffID, revokedHash, sha256.Sum256([]byte(prefix+"revoked-csrf")), now.Add(time.Hour), &revokedAt)

	expiredHash := sha256.Sum256([]byte(prefix + "expired-token"))
	insertSession(activeStaffID, expiredHash, sha256.Sum256([]byte(prefix+"expired-csrf")), now.Add(-time.Hour), nil)

	inactiveHash := sha256.Sum256([]byte(prefix + "inactive-account-token"))
	insertSession(inactiveStaffID, inactiveHash, sha256.Sum256([]byte(prefix+"inactive-csrf")), now.Add(time.Hour), nil)

	identity, err := repo.GetActiveSessionByTokenHash(ctx, validTokenHash)
	if err != nil {
		t.Fatalf("GetActiveSessionByTokenHash(valid): %v", err)
	}
	if identity.StaffID != activeStaffID ||
		identity.DisplayName != "Session test active" ||
		identity.Email != prefix+"active@example.test" {
		t.Fatalf("unexpected authenticated staff identity: %+v", identity)
	}
	if identity.CSRFTokenHash != validCSRFHash {
		t.Fatal("returned CSRF digest does not match stored digest")
	}
	if identity.ExpiresAt.Before(time.Now()) {
		t.Fatal("returned valid session is already expired")
	}

	var lastSeen, createdAt time.Time
	if err := pool.QueryRow(ctx, "SELECT last_seen_at, created_at FROM staff_sessions WHERE token_hash = $1", validTokenHash[:]).Scan(&lastSeen, &createdAt); err != nil {
		t.Fatalf("read session timestamps: %v", err)
	}
	if !lastSeen.After(createdAt) {
		t.Fatal("valid session lookup did not refresh last_seen_at")
	}

	unknownHash := sha256.Sum256([]byte(prefix + "unknown-token"))
	for name, digest := range map[string][32]byte{
		"unknown token":          unknownHash,
		"revoked session":        revokedHash,
		"expired session":        expiredHash,
		"inactive staff account": inactiveHash,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := repo.GetActiveSessionByTokenHash(ctx, digest)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetActiveSessionByTokenHash() error = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestRevokeSessionByTokenHash(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	repo := NewStaffAuthRepository(pool)
	prefix := fmt.Sprintf("staff-session-revoke-test-%d-", time.Now().UnixNano())
	var staffID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO staff_accounts (display_name, email, account_status)
		VALUES ('Session revoke test', $1, 'active')
		RETURNING id
	`, prefix+"@example.test").Scan(&staffID); err != nil {
		t.Fatalf("create staff account: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM staff_accounts WHERE id = $1", staffID); err != nil {
			t.Errorf("cleanup staff account: %v", err)
		}
	})

	tokenHash := sha256.Sum256([]byte(prefix + "token"))
	csrfHash := sha256.Sum256([]byte(prefix + "csrf"))
	_, err := pool.Exec(ctx, `
		INSERT INTO staff_sessions (staff_id, token_hash, csrf_token_hash, expires_at)
		VALUES ($1, $2, $3, now() + INTERVAL '1 hour')
	`, staffID, tokenHash[:], csrfHash[:])
	if err != nil {
		t.Fatalf("create test session: %v", err)
	}
	if err := repo.RevokeSessionByTokenHash(ctx, tokenHash); err != nil {
		t.Fatalf("RevokeSessionByTokenHash(): %v", err)
	}
	if _, err := repo.GetActiveSessionByTokenHash(ctx, tokenHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked session lookup error = %v, want ErrNotFound", err)
	}
	if err := repo.RevokeSessionByTokenHash(ctx, tokenHash); err != nil {
		t.Fatalf("repeated revocation should be idempotent, got %v", err)
	}
}
