package repository

import (
	"context"
	"errors"
	"strings"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StaffManagementRepository выполняет операции управления сотрудниками и назначениями в PostgreSQL.
type StaffManagementRepository struct {
	pool *pgxpool.Pool
}

func NewStaffManagementRepository(pool *pgxpool.Pool) *StaffManagementRepository {
	return &StaffManagementRepository{pool: pool}
}

func (r *StaffManagementRepository) ListStaffAccounts(ctx context.Context, actorID int64) ([]domain.StaffAccountSummary, error) {
	if actorID <= 0 {
		return nil, ErrInvalidInput
	}
	var isAdmin bool
	if err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM staff_role_assignments assignment
			JOIN staff_roles role ON role.id = assignment.role_id
			JOIN staff_accounts actor ON actor.id = assignment.staff_id
			WHERE assignment.staff_id = $1 AND assignment.revoked_at IS NULL
			  AND role.role_code = 'administrator' AND actor.account_status = 'active'
		)
	`, actorID).Scan(&isAdmin); err != nil {
		return nil, mapDatabaseError("проверить полномочия для списка сотрудников", err)
	}
	if !isAdmin {
		return nil, ErrInvalidState
	}

	rows, err := r.pool.Query(ctx, `
		SELECT account.id, account.display_name, account.email, account.account_status,
		       account.must_change_password, account.created_at,
		       ARRAY(
		           SELECT role.role_code
		           FROM staff_role_assignments assignment
		           JOIN staff_roles role ON role.id = assignment.role_id
		           WHERE assignment.staff_id = account.id AND assignment.revoked_at IS NULL
		           ORDER BY role.role_code
		       )
		FROM staff_accounts account
		WHERE account.email IS NOT NULL
		ORDER BY account.created_at DESC, account.id DESC
	`)
	if err != nil {
		return nil, mapDatabaseError("получить список сотрудников", err)
	}
	defer rows.Close()

	accounts := make([]domain.StaffAccountSummary, 0)
	for rows.Next() {
		var account domain.StaffAccountSummary
		if err := rows.Scan(&account.ID, &account.DisplayName, &account.Email, &account.AccountStatus,
			&account.MustChangePassword, &account.CreatedAt, &account.Roles); err != nil {
			return nil, mapDatabaseError("прочитать сотрудника", err)
		}
		if account.Roles == nil {
			account.Roles = []string{}
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("обойти список сотрудников", err)
	}
	return accounts, nil
}

// CreateStaffAccount создаёт аккаунт и назначение роли атомарно. Открытый пароль не попадает в БД.
func (r *StaffManagementRepository) CreateStaffAccount(ctx context.Context, actorID int64, displayName, email, passwordHash, roleCode string) (domain.StaffAccountSummary, error) {
	displayName = strings.TrimSpace(displayName)
	email = strings.ToLower(strings.TrimSpace(email))
	if actorID <= 0 || displayName == "" || len(displayName) > 200 ||
		email == "" || len(email) > 254 || passwordHash == "" ||
		(roleCode != "administrator" && roleCode != "curator") {
		return domain.StaffAccountSummary{}, ErrInvalidInput
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.StaffAccountSummary{}, mapDatabaseError("начать создание сотрудника", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var administratorRoleID int64
	if err := tx.QueryRow(ctx, "SELECT id FROM staff_roles WHERE role_code = 'administrator' FOR UPDATE").Scan(&administratorRoleID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.StaffAccountSummary{}, ErrInvalidState
		}
		return domain.StaffAccountSummary{}, mapDatabaseError("заблокировать роль администратора при создании сотрудника", err)
	}
	var actorIsAdmin bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM staff_role_assignments assignment
			JOIN staff_accounts actor ON actor.id = assignment.staff_id
			WHERE assignment.staff_id = $1 AND assignment.role_id = $2
			  AND assignment.revoked_at IS NULL AND actor.account_status = 'active'
		)
	`, actorID, administratorRoleID).Scan(&actorIsAdmin); err != nil {
		return domain.StaffAccountSummary{}, mapDatabaseError("проверить право создания сотрудника", err)
	}
	if !actorIsAdmin {
		return domain.StaffAccountSummary{}, ErrInvalidState
	}

	var roleID int64
	if err := tx.QueryRow(ctx, "SELECT id FROM staff_roles WHERE role_code = $1 FOR UPDATE", roleCode).Scan(&roleID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.StaffAccountSummary{}, ErrInvalidInput
		}
		return domain.StaffAccountSummary{}, mapDatabaseError("найти роль нового сотрудника", err)
	}
	var account domain.StaffAccountSummary
	err = tx.QueryRow(ctx, `
		INSERT INTO staff_accounts (display_name, email, password_hash, account_status, must_change_password)
		VALUES ($1, $2, $3, 'active', TRUE)
		RETURNING id, display_name, email, account_status, must_change_password, created_at
	`, displayName, email, passwordHash).Scan(
		&account.ID, &account.DisplayName, &account.Email,
		&account.AccountStatus, &account.MustChangePassword, &account.CreatedAt,
	)
	if err != nil {
		return domain.StaffAccountSummary{}, mapDatabaseError("создать учётную запись сотрудника", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO staff_role_assignments (staff_id, role_id, granted_by_staff_id)
		VALUES ($1, $2, $3)
	`, account.ID, roleID, actorID); err != nil {
		return domain.StaffAccountSummary{}, mapDatabaseError("назначить роль новому сотруднику", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_events (actor_staff_id, action, entity_type, entity_id, details)
		VALUES ($1, 'staff.account.create', 'staff_account', $2,
		        jsonb_build_object('role_code', $3::text, 'email', $4::text, 'must_change_password', TRUE))
	`, actorID, account.ID, roleCode, email); err != nil {
		return domain.StaffAccountSummary{}, mapDatabaseError("записать аудит создания сотрудника", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.StaffAccountSummary{}, mapDatabaseError("зафиксировать создание сотрудника", err)
	}
	account.Roles = []string{roleCode}
	return account, nil
}

func (r *StaffManagementRepository) ListStudentsForStaff(ctx context.Context, actorID int64) ([]domain.AdminStudent, error) {
	if actorID <= 0 {
		return nil, ErrInvalidInput
	}
	var isAdmin, isCurator bool
	if err := r.pool.QueryRow(ctx, `
		SELECT
			EXISTS (
				SELECT 1 FROM staff_role_assignments assignment
				JOIN staff_roles role ON role.id = assignment.role_id
				JOIN staff_accounts actor ON actor.id = assignment.staff_id
				WHERE assignment.staff_id = $1 AND assignment.revoked_at IS NULL
				  AND role.role_code = 'administrator' AND actor.account_status = 'active'
			),
			EXISTS (
				SELECT 1 FROM staff_role_assignments assignment
				JOIN staff_roles role ON role.id = assignment.role_id
				JOIN staff_accounts actor ON actor.id = assignment.staff_id
				WHERE assignment.staff_id = $1 AND assignment.revoked_at IS NULL
				  AND role.role_code = 'curator' AND actor.account_status = 'active'
			)
	`, actorID).Scan(&isAdmin, &isCurator); err != nil {
		return nil, mapDatabaseError("проверить полномочия для списка учеников", err)
	}
	if !isAdmin && !isCurator {
		return nil, ErrInvalidState
	}

	rows, err := r.pool.Query(ctx, `
		SELECT student.id, student.telegram_id, student.display_name, student.telegram_username,
		       student.preferred_name, student.account_status, student.registered_at, student.last_active_at,
		       current_assignment.curator_staff_id, current_assignment.curator_display_name,
		       COALESCE(progress.total_enrollments, 0)::INTEGER,
		       COALESCE(progress.completed_enrollments, 0)::INTEGER,
		       progress.last_learning_activity_at
		FROM students AS student
		LEFT JOIN LATERAL (
			SELECT assignment.curator_staff_id, curator.display_name AS curator_display_name
			FROM student_curator_assignments assignment
			JOIN staff_accounts curator ON curator.id = assignment.curator_staff_id
			WHERE assignment.student_id = student.id AND assignment.status = 'active'
			ORDER BY assignment.assigned_at DESC, assignment.id DESC
			LIMIT 1
		) AS current_assignment ON TRUE
		LEFT JOIN LATERAL (
			SELECT count(*) AS total_enrollments,
			       count(*) FILTER (WHERE enrollment.status = 'completed') AS completed_enrollments,
			       max(COALESCE(enrollment.last_activity_at, enrollment.updated_at)) AS last_learning_activity_at
			FROM lesson_enrollments enrollment WHERE enrollment.student_id = student.id
		) AS progress ON TRUE
		WHERE ($2::BOOLEAN = TRUE OR current_assignment.curator_staff_id = $1)
		ORDER BY student.registered_at DESC, student.id DESC
	`, actorID, isAdmin)
	if err != nil {
		return nil, mapDatabaseError("получить список учеников с прогрессом", err)
	}
	defer rows.Close()

	students := make([]domain.AdminStudent, 0)
	for rows.Next() {
		var student domain.AdminStudent
		var username, preferredName, curatorName pgtype.Text
		var lastActiveAt, lastLearningAt pgtype.Timestamptz
		var curatorID pgtype.Int8
		if err := rows.Scan(
			&student.ID, &student.TelegramID, &student.DisplayName, &username,
			&preferredName, &student.AccountStatus, &student.RegisteredAt, &lastActiveAt,
			&curatorID, &curatorName, &student.TotalEnrollments,
			&student.CompletedEnrollments, &lastLearningAt,
		); err != nil {
			return nil, mapDatabaseError("прочитать ученика с прогрессом", err)
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
		if curatorID.Valid {
			value := curatorID.Int64
			student.CuratorStaffID = &value
		}
		if curatorName.Valid {
			value := curatorName.String
			student.CuratorDisplayName = &value
		}
		if lastLearningAt.Valid {
			value := lastLearningAt.Time
			student.LastLearningActivityAt = &value
		}
		students = append(students, student)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("обойти список учеников с прогрессом", err)
	}
	return students, nil
}

// AssignStudentCurator assigns or transfers a student under a curator in one transaction.
// It updates active conversations in the same transaction so a transfer is reflected consistently.
func (r *StaffManagementRepository) AssignStudentCurator(ctx context.Context, actorID, studentID, curatorStaffID int64) error {
	if actorID <= 0 || studentID <= 0 || curatorStaffID <= 0 {
		return ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapDatabaseError("начать назначение куратора", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var adminRoleID int64
	if err := tx.QueryRow(ctx, "SELECT id FROM staff_roles WHERE role_code = 'administrator' FOR UPDATE").Scan(&adminRoleID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidState
		}
		return mapDatabaseError("заблокировать роль администратора при назначении куратора", err)
	}
	var actorIsAdmin bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM staff_role_assignments assignment
			JOIN staff_accounts actor ON actor.id = assignment.staff_id
			WHERE assignment.staff_id = $1 AND assignment.role_id = $2
			  AND assignment.revoked_at IS NULL AND actor.account_status = 'active'
		)
	`, actorID, adminRoleID).Scan(&actorIsAdmin); err != nil {
		return mapDatabaseError("проверить право назначать куратора", err)
	}
	if !actorIsAdmin {
		return ErrInvalidState
	}

	var studentStatus string
	if err := tx.QueryRow(ctx, "SELECT account_status FROM students WHERE id = $1 FOR UPDATE", studentID).Scan(&studentStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return mapDatabaseError("проверить ученика для назначения куратора", err)
	}
	if studentStatus != "active" {
		return ErrInvalidState
	}

	var curatorStatus string
	if err := tx.QueryRow(ctx, `
		SELECT staff.account_status
		FROM staff_accounts staff
		JOIN staff_role_assignments assignment ON assignment.staff_id = staff.id AND assignment.revoked_at IS NULL
		JOIN staff_roles role ON role.id = assignment.role_id AND role.role_code = 'curator'
		WHERE staff.id = $1
		FOR UPDATE OF staff
	`, curatorStaffID).Scan(&curatorStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return mapDatabaseError("проверить назначаемого куратора", err)
	}
	if curatorStatus != "active" {
		return ErrInvalidState
	}

	var previousCuratorID pgtype.Int8
	if err := tx.QueryRow(ctx, `
		SELECT curator_staff_id FROM student_curator_assignments
		WHERE student_id = $1 AND status = 'active'
		ORDER BY assigned_at DESC, id DESC LIMIT 1 FOR UPDATE
	`, studentID).Scan(&previousCuratorID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return mapDatabaseError("получить текущее назначение куратора", err)
	}
	if previousCuratorID.Valid && previousCuratorID.Int64 == curatorStaffID {
		return nil
	}
	if previousCuratorID.Valid {
		if _, err := tx.Exec(ctx, `
			UPDATE student_curator_assignments SET status = 'ended', ended_at = now()
			WHERE student_id = $1 AND status = 'active'
		`, studentID); err != nil {
			return mapDatabaseError("завершить предыдущее назначение куратора", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO student_curator_assignments (student_id, curator_staff_id, assigned_by_staff_id)
		VALUES ($1, $2, $3)
	`, studentID, curatorStaffID, actorID); err != nil {
		return mapDatabaseError("создать назначение куратора", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE conversations SET assigned_curator_staff_id = $2, updated_at = now()
		WHERE student_id = $1 AND status = 'active'
	`, studentID, curatorStaffID); err != nil {
		return mapDatabaseError("обновить назначение куратора в активных диалогах", err)
	}
	action := "student.curator.assign"
	if previousCuratorID.Valid {
		action = "student.curator.transfer"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_events (actor_staff_id, action, entity_type, entity_id, details)
		VALUES ($1, $2, 'student', $3,
		        jsonb_build_object('curator_staff_id', $4::bigint, 'previous_curator_staff_id', $5::bigint))
	`, actorID, action, studentID, curatorStaffID, nullableInt64(previousCuratorID)); err != nil {
		return mapDatabaseError("записать аудит назначения куратора", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return mapDatabaseError("зафиксировать назначение куратора", err)
	}
	return nil
}

func (r *StaffManagementRepository) EndStudentCuratorAssignment(ctx context.Context, actorID, studentID int64) error {
	if actorID <= 0 || studentID <= 0 {
		return ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapDatabaseError("начать завершение назначения куратора", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var adminRoleID int64
	if err := tx.QueryRow(ctx, "SELECT id FROM staff_roles WHERE role_code = 'administrator' FOR UPDATE").Scan(&adminRoleID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidState
		}
		return mapDatabaseError("заблокировать роль администратора при завершении назначения", err)
	}
	var actorIsAdmin bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM staff_role_assignments assignment
			JOIN staff_accounts actor ON actor.id = assignment.staff_id
			WHERE assignment.staff_id = $1 AND assignment.role_id = $2
			  AND assignment.revoked_at IS NULL AND actor.account_status = 'active'
		)
	`, actorID, adminRoleID).Scan(&actorIsAdmin); err != nil {
		return mapDatabaseError("проверить право завершать назначение куратора", err)
	}
	if !actorIsAdmin {
		return ErrInvalidState
	}
	var curatorStaffID int64
	if err := tx.QueryRow(ctx, `
		SELECT curator_staff_id FROM student_curator_assignments
		WHERE student_id = $1 AND status = 'active'
		ORDER BY assigned_at DESC, id DESC LIMIT 1 FOR UPDATE
	`, studentID).Scan(&curatorStaffID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return mapDatabaseError("найти активное назначение для завершения", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE student_curator_assignments SET status = 'ended', ended_at = now()
		WHERE student_id = $1 AND status = 'active'
	`, studentID); err != nil {
		return mapDatabaseError("завершить назначение куратора", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE conversations SET assigned_curator_staff_id = NULL, updated_at = now()
		WHERE student_id = $1 AND status = 'active'
	`, studentID); err != nil {
		return mapDatabaseError("снять куратора с активных диалогов", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_events (actor_staff_id, action, entity_type, entity_id, details)
		VALUES ($1, 'student.curator.end', 'student', $2,
		        jsonb_build_object('previous_curator_staff_id', $3::bigint))
	`, actorID, studentID, curatorStaffID); err != nil {
		return mapDatabaseError("записать аудит завершения назначения куратора", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return mapDatabaseError("зафиксировать завершение назначения куратора", err)
	}
	return nil
}


func (r *StaffManagementRepository) SetStaffAccountStatus(ctx context.Context, actorID, targetID int64, status string) error {
	if actorID <= 0 || targetID <= 0 || actorID == targetID || (status != "active" && status != "inactive") {
		return ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapDatabaseError("начать изменение статуса сотрудника", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var adminRoleID int64
	if err := tx.QueryRow(ctx, "SELECT id FROM staff_roles WHERE role_code = 'administrator' FOR UPDATE").Scan(&adminRoleID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) { return ErrInvalidState }
		return mapDatabaseError("заблокировать роль администратора при смене статуса", err)
	}
	var actorIsAdmin bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM staff_role_assignments assignment
			JOIN staff_accounts actor ON actor.id = assignment.staff_id
			WHERE assignment.staff_id = $1 AND assignment.role_id = $2
			  AND assignment.revoked_at IS NULL AND actor.account_status = 'active'
		)
	`, actorID, adminRoleID).Scan(&actorIsAdmin); err != nil {
		return mapDatabaseError("проверить право изменения статуса сотрудника", err)
	}
	if !actorIsAdmin { return ErrInvalidState }

	var oldStatus string
	if err := tx.QueryRow(ctx, "SELECT account_status FROM staff_accounts WHERE id = $1 FOR UPDATE", targetID).Scan(&oldStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) { return ErrNotFound }
		return mapDatabaseError("получить статус сотрудника", err)
	}
	if oldStatus == status { return nil }

	var targetIsAdmin bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM staff_role_assignments assignment
			WHERE assignment.staff_id = $1 AND assignment.role_id = $2 AND assignment.revoked_at IS NULL
		)
	`, targetID, adminRoleID).Scan(&targetIsAdmin); err != nil {
		return mapDatabaseError("проверить роль деактивируемого сотрудника", err)
	}
	if targetIsAdmin && oldStatus == "active" && status == "inactive" {
		var activeAdmins int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM staff_role_assignments assignment
			JOIN staff_accounts account ON account.id = assignment.staff_id
			WHERE assignment.role_id = $1 AND assignment.revoked_at IS NULL AND account.account_status = 'active'
		`, adminRoleID).Scan(&activeAdmins); err != nil {
			return mapDatabaseError("проверить оставшихся администраторов", err)
		}
		if activeAdmins <= 1 { return ErrInvalidState }
	}
	if _, err := tx.Exec(ctx, "UPDATE staff_accounts SET account_status = $2, updated_at = now() WHERE id = $1", targetID, status); err != nil {
		return mapDatabaseError("обновить статус сотрудника", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE staff_sessions SET revoked_at = now() WHERE staff_id = $1 AND revoked_at IS NULL", targetID); err != nil {
		return mapDatabaseError("отозвать сессии после смены статуса сотрудника", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_events (actor_staff_id, action, entity_type, entity_id, details)
		VALUES ($1, 'staff.account.status_change', 'staff_account', $2,
		        jsonb_build_object('from', $3::text, 'to', $4::text, 'sessions_revoked', TRUE))
	`, actorID, targetID, oldStatus, status); err != nil {
		return mapDatabaseError("записать аудит изменения статуса сотрудника", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return mapDatabaseError("зафиксировать статус сотрудника", err)
	}
	return nil
}

func (r *StaffManagementRepository) ResetStaffPassword(ctx context.Context, actorID, targetID int64, passwordHash string) error {
	if actorID <= 0 || targetID <= 0 || actorID == targetID || passwordHash == "" {
		return ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapDatabaseError("начать сброс пароля сотрудника", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var adminRoleID int64
	if err := tx.QueryRow(ctx, "SELECT id FROM staff_roles WHERE role_code = 'administrator' FOR UPDATE").Scan(&adminRoleID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) { return ErrInvalidState }
		return mapDatabaseError("заблокировать роль администратора при сбросе пароля", err)
	}
	var actorIsAdmin bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM staff_role_assignments assignment
			JOIN staff_accounts actor ON actor.id = assignment.staff_id
			WHERE assignment.staff_id = $1 AND assignment.role_id = $2
			  AND assignment.revoked_at IS NULL AND actor.account_status = 'active'
		)
	`, actorID, adminRoleID).Scan(&actorIsAdmin); err != nil {
		return mapDatabaseError("проверить право сброса пароля сотрудника", err)
	}
	if !actorIsAdmin { return ErrInvalidState }
	var targetStatus string
	if err := tx.QueryRow(ctx, "SELECT account_status FROM staff_accounts WHERE id = $1 FOR UPDATE", targetID).Scan(&targetStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) { return ErrNotFound }
		return mapDatabaseError("проверить статус сотрудника для сброса пароля", err)
	}
	if targetStatus != "active" { return ErrInvalidState }
	if _, err := tx.Exec(ctx, "UPDATE staff_accounts SET password_hash = $2, must_change_password = TRUE, updated_at = now() WHERE id = $1", targetID, passwordHash); err != nil {
		return mapDatabaseError("сохранить новый временный пароль сотрудника", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE staff_sessions SET revoked_at = now() WHERE staff_id = $1 AND revoked_at IS NULL", targetID); err != nil {
		return mapDatabaseError("отозвать сессии после сброса пароля", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_events (actor_staff_id, action, entity_type, entity_id, details)
		VALUES ($1, 'staff.password.reset', 'staff_account', $2, jsonb_build_object('must_change_password', TRUE, 'sessions_revoked', TRUE))
	`, actorID, targetID); err != nil {
		return mapDatabaseError("записать аудит сброса пароля", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return mapDatabaseError("зафиксировать сброс пароля сотрудника", err)
	}
	return nil
}


func nullableInt64(value pgtype.Int8) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

// Compile-time assertions keep the HTTP/service contract explicit.
var _ interface {
	ListStaffAccounts(context.Context, int64) ([]domain.StaffAccountSummary, error)
	CreateStaffAccount(context.Context, int64, string, string, string, string) (domain.StaffAccountSummary, error)
	ListStudentsForStaff(context.Context, int64) ([]domain.AdminStudent, error)
	AssignStudentCurator(context.Context, int64, int64, int64) error
	EndStudentCuratorAssignment(context.Context, int64, int64) error
	SetStaffAccountStatus(context.Context, int64, int64, string) error
	ResetStaffPassword(context.Context, int64, int64, string) error
} = (*StaffManagementRepository)(nil)
