package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestStaffRoleAssignmentsAreAuditedAndDuplicateGrantConflicts(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	suffix := fmt.Sprintf("staff-role-%d", time.Now().UnixNano())
	var actorID, targetID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO staff_accounts (display_name, email, account_status)
		VALUES ('Role administrator', $1, 'active')
		RETURNING id
	`, suffix+"-admin@example.test").Scan(&actorID); err != nil {
		t.Fatalf("create administrator account: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO staff_accounts (display_name, email, account_status)
		VALUES ('Role target', $1, 'active')
		RETURNING id
	`, suffix+"-target@example.test").Scan(&targetID); err != nil {
		t.Fatalf("create target account: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, `
			DELETE FROM audit_events
			WHERE entity_type = 'staff_account' AND entity_id IN ($1, $2)
		`, actorID, targetID); err != nil {
			t.Errorf("cleanup role audit events: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM staff_role_assignments WHERE staff_id IN ($1, $2)", actorID, targetID); err != nil {
			t.Errorf("cleanup role assignments: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM staff_accounts WHERE id IN ($1, $2)", actorID, targetID); err != nil {
			t.Errorf("cleanup staff accounts: %v", err)
		}
	})

	var administratorRoleID int64
	if err := pool.QueryRow(ctx, "SELECT id FROM staff_roles WHERE role_code = 'administrator'").Scan(&administratorRoleID); err != nil {
		t.Fatalf("read administrator role: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO staff_role_assignments (staff_id, role_id, granted_by_staff_id)
		VALUES ($1, $2, $1)
	`, actorID, administratorRoleID); err != nil {
		t.Fatalf("assign administrator role to test actor: %v", err)
	}

	repo := NewCatalogRepository(pool)
	if err := repo.AssignStaffRole(ctx, actorID, targetID, "curator"); err != nil {
		t.Fatalf("assign curator role: %v", err)
	}
	if err := repo.AssignStaffRole(ctx, actorID, targetID, "curator"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate active role error = %v, want ErrConflict", err)
	}

	var activeAssignmentCount, grantAuditCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM staff_role_assignments
		WHERE staff_id = $1 AND revoked_at IS NULL
		  AND role_id = (SELECT id FROM staff_roles WHERE role_code = 'curator')
	`, targetID).Scan(&activeAssignmentCount); err != nil {
		t.Fatalf("count active curator assignments: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		WHERE actor_staff_id = $1 AND entity_type = 'staff_account'
		  AND entity_id = $2 AND action = 'staff.role.assign'
		  AND details->>'role_code' = 'curator'
	`, actorID, targetID).Scan(&grantAuditCount); err != nil {
		t.Fatalf("count role grant audit events: %v", err)
	}
	if activeAssignmentCount != 1 || grantAuditCount != 1 {
		t.Fatalf("active assignments/audit grants = %d/%d, want 1/1", activeAssignmentCount, grantAuditCount)
	}

	if err := repo.RevokeStaffRole(ctx, actorID, targetID, "curator"); err != nil {
		t.Fatalf("revoke curator role: %v", err)
	}
	if err := repo.RevokeStaffRole(ctx, actorID, targetID, "curator"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke already revoked role error = %v, want ErrNotFound", err)
	}
	var revokedAtCount, revokeAuditCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM staff_role_assignments
		WHERE staff_id = $1 AND revoked_at IS NOT NULL
		  AND role_id = (SELECT id FROM staff_roles WHERE role_code = 'curator')
	`, targetID).Scan(&revokedAtCount); err != nil {
		t.Fatalf("count revoked curator assignments: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		WHERE actor_staff_id = $1 AND entity_type = 'staff_account'
		  AND entity_id = $2 AND action = 'staff.role.revoke'
		  AND details->>'role_code' = 'curator'
	`, actorID, targetID).Scan(&revokeAuditCount); err != nil {
		t.Fatalf("count role revoke audit events: %v", err)
	}
	if revokedAtCount != 1 || revokeAuditCount != 1 {
		t.Fatalf("revoked assignments/audit revokes = %d/%d, want 1/1", revokedAtCount, revokeAuditCount)
	}
}
