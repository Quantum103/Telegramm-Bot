package repository

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const staffReferralCodePrefix = "c_"

func validStaffReferralCode(code string) bool {
	if !strings.HasPrefix(code, staffReferralCodePrefix) {
		return false
	}
	idText := strings.TrimPrefix(code, staffReferralCodePrefix)
	id, err := strconv.ParseInt(idText, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == idText
}

func (r *StaffManagementRepository) GetOrCreateReferralCode(
	ctx context.Context,
	staffID int64,
	candidateCode string,
) (domain.StaffReferralCode, error) {
	if staffID <= 0 || !validStaffReferralCode(candidateCode) || candidateCode != staffReferralCodePrefix+strconv.FormatInt(staffID, 10) {
		return domain.StaffReferralCode{}, ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.StaffReferralCode{}, mapDatabaseError("начать получение реферального кода", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var accountStatus string
	var hasCuratorRole bool
	err = tx.QueryRow(ctx, `
		SELECT staff.account_status,
		       EXISTS (
		         SELECT 1
		         FROM staff_role_assignments assignment
		         JOIN staff_roles role ON role.id = assignment.role_id
		         WHERE assignment.staff_id = staff.id
		           AND assignment.revoked_at IS NULL
		           AND role.role_code = 'curator'
		       )
		FROM staff_accounts staff
		WHERE staff.id = $1
		FOR UPDATE OF staff
	`, staffID).Scan(&accountStatus, &hasCuratorRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StaffReferralCode{}, ErrNotFound
	}
	if err != nil {
		return domain.StaffReferralCode{}, mapDatabaseError("проверить сотрудника для реферального кода", err)
	}
	if accountStatus != "active" || !hasCuratorRole {
		return domain.StaffReferralCode{}, ErrInvalidState
	}

	var referral domain.StaffReferralCode
	err = tx.QueryRow(ctx, `
		SELECT staff_id, code, created_at
		FROM staff_referral_codes
		WHERE staff_id = $1
	`, staffID).Scan(&referral.StaffID, &referral.Code, &referral.CreatedAt)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return domain.StaffReferralCode{}, mapDatabaseError("зафиксировать получение реферального кода", err)
		}
		return referral, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.StaffReferralCode{}, mapDatabaseError("получить существующий реферальный код", err)
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO staff_referral_codes (staff_id, code)
		VALUES ($1, $2)
		RETURNING staff_id, code, created_at
	`, staffID, candidateCode).Scan(&referral.StaffID, &referral.Code, &referral.CreatedAt)
	if err != nil {
		return domain.StaffReferralCode{}, mapDatabaseError("сохранить реферальный код", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.StaffReferralCode{}, mapDatabaseError("зафиксировать реферальный код", err)
	}
	return referral, nil
}

func (r *StaffManagementRepository) AssignStudentByReferral(
	ctx context.Context,
	studentID int64,
	code string,
) (domain.ReferralAssignmentResult, error) {
	if studentID <= 0 || !validStaffReferralCode(code) {
		return domain.ReferralAssignmentResult{}, ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.ReferralAssignmentResult{}, mapDatabaseError("начать реферальное назначение", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var studentStatus string
	err = tx.QueryRow(ctx, `
		SELECT account_status FROM students WHERE id = $1 FOR UPDATE
	`, studentID).Scan(&studentStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ReferralAssignmentResult{}, ErrNotFound
	}
	if err != nil {
		return domain.ReferralAssignmentResult{}, mapDatabaseError("заблокировать ученика для реферального назначения", err)
	}
	if studentStatus != "active" {
		return domain.ReferralAssignmentResult{}, ErrInvalidState
	}

	var curatorStaffID int64
	err = tx.QueryRow(ctx, `
		SELECT staff.id
		FROM staff_referral_codes referral
		JOIN staff_accounts staff ON staff.id = referral.staff_id
		WHERE referral.code = $1
		  AND staff.account_status = 'active'
		  AND EXISTS (
		    SELECT 1
		    FROM staff_role_assignments assignment
		    JOIN staff_roles role ON role.id = assignment.role_id
		    WHERE assignment.staff_id = staff.id
		      AND assignment.revoked_at IS NULL
		      AND role.role_code = 'curator'
		  )
		FOR UPDATE OF staff
	`, code).Scan(&curatorStaffID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ReferralAssignmentResult{}, ErrNotFound
	}
	if err != nil {
		return domain.ReferralAssignmentResult{}, mapDatabaseError("проверить реферальный код и куратора", err)
	}

	var previousCuratorID int64
	var previousStatus string
	err = tx.QueryRow(ctx, `
		SELECT curator_staff_id, status
		FROM student_curator_assignments
		WHERE student_id = $1
		ORDER BY assigned_at DESC, id DESC
		LIMIT 1
		FOR UPDATE
	`, studentID).Scan(&previousCuratorID, &previousStatus)
	if err == nil {
		if previousStatus == "active" && previousCuratorID == curatorStaffID {
			if err := tx.Commit(ctx); err != nil {
				return domain.ReferralAssignmentResult{}, mapDatabaseError("зафиксировать повторную проверку реферального назначения", err)
			}
			return domain.ReferralAssignmentResult{CuratorStaffID: curatorStaffID, AlreadyAssigned: true}, nil
		}
		return domain.ReferralAssignmentResult{}, ErrConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.ReferralAssignmentResult{}, mapDatabaseError("проверить историю назначений куратора", err)
	}

	if err := assignStudentCuratorTx(ctx, tx, studentID, curatorStaffID, nil, pgtype.Int8{}, "student.curator.referral_assign"); err != nil {
		return domain.ReferralAssignmentResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ReferralAssignmentResult{}, mapDatabaseError("зафиксировать реферальное назначение куратора", err)
	}
	return domain.ReferralAssignmentResult{CuratorStaffID: curatorStaffID, Assigned: true}, nil
}
