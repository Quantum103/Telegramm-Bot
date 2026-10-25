package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// AssignStaffRole grants an allow-listed role and records the action atomically.
// Locking the administrator role serializes role changes and protects the last-admin invariant.
func (r *CatalogRepository) AssignStaffRole(ctx context.Context, actorStaffID, targetStaffID int64, roleCode string) error {
	if actorStaffID <= 0 || targetStaffID <= 0 || actorStaffID == targetStaffID ||
		(roleCode != "administrator" && roleCode != "curator") {
		return ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapDatabaseError("начать назначение роли сотруднику", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var administratorRoleID int64
	if err := tx.QueryRow(ctx, "SELECT id FROM staff_roles WHERE role_code = 'administrator' FOR UPDATE").Scan(&administratorRoleID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidState
		}
		return mapDatabaseError("заблокировать роль администратора", err)
	}
	roleID := administratorRoleID
	if roleCode != "administrator" {
		if err := tx.QueryRow(ctx, "SELECT id FROM staff_roles WHERE role_code = $1 FOR UPDATE", roleCode).Scan(&roleID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrInvalidInput
			}
			return mapDatabaseError("найти назначаемую роль", err)
		}
	}
	var actorIsAdministrator bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM staff_role_assignments WHERE staff_id = $1 AND role_id = $2 AND revoked_at IS NULL)", actorStaffID, administratorRoleID).Scan(&actorIsAdministrator); err != nil {
		return mapDatabaseError("проверить права назначающего сотрудника", err)
	}
	if !actorIsAdministrator {
		return ErrInvalidState
	}
	var targetStatus string
	if err := tx.QueryRow(ctx, "SELECT account_status FROM staff_accounts WHERE id = $1 FOR UPDATE", targetStaffID).Scan(&targetStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return mapDatabaseError("проверить целевую учётную запись", err)
	}
	if targetStatus != "active" {
		return ErrInvalidState
	}
	if _, err := tx.Exec(ctx, "INSERT INTO staff_role_assignments (staff_id, role_id, granted_by_staff_id) VALUES ($1, $2, $3)", targetStaffID, roleID, actorStaffID); err != nil {
		return mapDatabaseError("назначить роль сотруднику", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO audit_events (actor_staff_id, action, entity_type, entity_id, details) VALUES ($1, 'staff.role.assign', 'staff_account', $2, jsonb_build_object('role_code', $3::text))", actorStaffID, targetStaffID, roleCode); err != nil {
		return mapDatabaseError("записать аудит назначения роли сотруднику", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return mapDatabaseError("зафиксировать назначение роли сотруднику", err)
	}
	return nil
}

// RevokeStaffRole revokes an active role, but never the final active administrator.
func (r *CatalogRepository) RevokeStaffRole(ctx context.Context, actorStaffID, targetStaffID int64, roleCode string) error {
	if actorStaffID <= 0 || targetStaffID <= 0 || actorStaffID == targetStaffID ||
		(roleCode != "administrator" && roleCode != "curator") {
		return ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapDatabaseError("начать отзыв роли сотрудника", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var administratorRoleID int64
	if err := tx.QueryRow(ctx, "SELECT id FROM staff_roles WHERE role_code = 'administrator' FOR UPDATE").Scan(&administratorRoleID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidState
		}
		return mapDatabaseError("заблокировать роль администратора", err)
	}
	roleID := administratorRoleID
	if roleCode != "administrator" {
		if err := tx.QueryRow(ctx, "SELECT id FROM staff_roles WHERE role_code = $1 FOR UPDATE", roleCode).Scan(&roleID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrInvalidInput
			}
			return mapDatabaseError("найти отзываемую роль", err)
		}
	}
	var actorIsAdministrator bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM staff_role_assignments WHERE staff_id = $1 AND role_id = $2 AND revoked_at IS NULL)", actorStaffID, administratorRoleID).Scan(&actorIsAdministrator); err != nil {
		return mapDatabaseError("проверить права отзывающего сотрудника", err)
	}
	if !actorIsAdministrator {
		return ErrInvalidState
	}
	var targetStatus string
	if err := tx.QueryRow(ctx, "SELECT account_status FROM staff_accounts WHERE id = $1 FOR UPDATE", targetStaffID).Scan(&targetStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return mapDatabaseError("проверить целевую учётную запись", err)
	}
	if targetStatus != "active" {
		return ErrInvalidState
	}
	var assignmentID int64
	if err := tx.QueryRow(ctx, "SELECT id FROM staff_role_assignments WHERE staff_id = $1 AND role_id = $2 AND revoked_at IS NULL FOR UPDATE", targetStaffID, roleID).Scan(&assignmentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return mapDatabaseError("найти активное назначение роли", err)
	}
	if roleCode == "administrator" {
		var activeAdministrators int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM staff_role_assignments assignment JOIN staff_accounts account ON account.id = assignment.staff_id WHERE assignment.role_id = $1 AND assignment.revoked_at IS NULL AND account.account_status = 'active'", administratorRoleID).Scan(&activeAdministrators); err != nil {
			return mapDatabaseError("проверить оставшихся администраторов", err)
		}
		if activeAdministrators <= 1 {
			return ErrInvalidState
		}
	}
	if _, err := tx.Exec(ctx, "UPDATE staff_role_assignments SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL", assignmentID); err != nil {
		return mapDatabaseError("отозвать назначение роли сотрудника", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO audit_events (actor_staff_id, action, entity_type, entity_id, details) VALUES ($1, 'staff.role.revoke', 'staff_account', $2, jsonb_build_object('role_code', $3::text))", actorStaffID, targetStaffID, roleCode); err != nil {
		return mapDatabaseError("записать аудит отзыва роли сотрудника", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return mapDatabaseError("зафиксировать отзыв роли сотрудника", err)
	}
	return nil
}
