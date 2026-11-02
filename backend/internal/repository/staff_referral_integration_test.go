package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/service"
)

func TestReferralCodesAndAssignmentsRespectCuratorHistory(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	createStaff := func(name, status string) int64 {
		t.Helper()
		var id int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO staff_accounts (display_name, email, account_status)
			VALUES ($1, $2, $3) RETURNING id
		`, name, name+"-"+suffix+"@example.test", status).Scan(&id); err != nil {
			t.Fatalf("создать сотрудника: %v", err)
		}
		return id
	}
	adminID := createStaff("referral-admin", "active")
	firstCuratorID := createStaff("referral-curator-a", "active")
	secondCuratorID := createStaff("referral-curator-b", "active")
	inactiveCuratorID := createStaff("referral-curator-inactive", "inactive")
	staffIDs := []int64{adminID, firstCuratorID, secondCuratorID, inactiveCuratorID}
	var students []int64
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM staff_referral_codes WHERE staff_id = ANY($1)", staffIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM student_curator_assignments WHERE student_id = ANY($1)", students)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM staff_role_assignments WHERE staff_id = ANY($1)", staffIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM students WHERE id = ANY($1)", students)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM staff_accounts WHERE id = ANY($1)", staffIDs)
	})

	var adminRoleID, curatorRoleID int64
	if err := pool.QueryRow(ctx, "SELECT id FROM staff_roles WHERE role_code = 'administrator'").Scan(&adminRoleID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT id FROM staff_roles WHERE role_code = 'curator'").Scan(&curatorRoleID); err != nil {
		t.Fatal(err)
	}
	for staffID, roleID := range map[int64]int64{
		adminID: adminRoleID, firstCuratorID: curatorRoleID,
		secondCuratorID: curatorRoleID, inactiveCuratorID: curatorRoleID,
	} {
		if _, err := pool.Exec(ctx, "INSERT INTO staff_role_assignments (staff_id, role_id, granted_by_staff_id) VALUES ($1, $2, $3)", staffID, roleID, adminID); err != nil {
			t.Fatal(err)
		}
	}
	var telegramID int64 = time.Now().UnixNano()
	createStudent := func(name, status string) int64 {
		t.Helper()
		telegramID++
		var id int64
		if err := pool.QueryRow(ctx, "INSERT INTO students (telegram_id, display_name, account_status) VALUES ($1, $2, $3) RETURNING id", telegramID, name, status).Scan(&id); err != nil {
			t.Fatalf("создать ученика: %v", err)
		}
		students = append(students, id)
		return id
	}
	newStudentID := createStudent("referral-new", "active")
	otherAssignedStudentID := createStudent("referral-other-assigned", "active")
	endedStudentID := createStudent("referral-ended", "active")
	concurrentStudentID := createStudent("referral-concurrent", "active")
	blockedStudentID := createStudent("referral-blocked", "blocked")
	rollbackStudentID := createStudent("referral-rollback", "active")

	repo := NewStaffManagementRepository(pool)
	referrals := service.NewStaffReferralService(repo)
	firstCode, err := referrals.GetOrCreateReferralCode(ctx, firstCuratorID)
	if err != nil {
		t.Fatal(err)
	}
	firstAgain, err := referrals.GetOrCreateReferralCode(ctx, firstCuratorID)
	if err != nil {
		t.Fatal(err)
	}
	secondCode, err := referrals.GetOrCreateReferralCode(ctx, secondCuratorID)
	if err != nil {
		t.Fatal(err)
	}
	if firstCode.Code != firstAgain.Code || firstCode.Code == secondCode.Code {
		t.Fatalf("коды должны быть постоянными и уникальными: %q / %q / %q", firstCode.Code, firstAgain.Code, secondCode.Code)
	}
	if _, err := referrals.GetOrCreateReferralCode(ctx, adminID); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("администратор без роли куратора получил код: %v", err)
	}
	if _, err := referrals.GetOrCreateReferralCode(ctx, inactiveCuratorID); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("неактивный куратор получил код: %v", err)
	}
	unknownCode := "c_" + strings.Repeat("A", 43)
	if unknownCode == firstCode.Code || unknownCode == secondCode.Code {
		t.Fatal("неожиданное совпадение кода")
	}
	if _, err := referrals.AssignStudentByReferral(ctx, newStudentID, unknownCode); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("неизвестный код не отклонён: %v", err)
	}

	firstResult, err := referrals.AssignStudentByReferral(ctx, newStudentID, firstCode.Code)
	if err != nil || !firstResult.Assigned || firstResult.CuratorStaffID != firstCuratorID {
		t.Fatalf("новый ученик не назначен: result=%+v err=%v", firstResult, err)
	}
	repeatResult, err := referrals.AssignStudentByReferral(ctx, newStudentID, firstCode.Code)
	if err != nil || !repeatResult.AlreadyAssigned || repeatResult.Assigned {
		t.Fatalf("повторная ссылка не идемпотентна: result=%+v err=%v", repeatResult, err)
	}
	var assignmentCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM student_curator_assignments WHERE student_id = $1", newStudentID).Scan(&assignmentCount); err != nil || assignmentCount != 1 {
		t.Fatalf("число назначений = %d, err=%v", assignmentCount, err)
	}

	if err := repo.AssignStudentCurator(ctx, adminID, otherAssignedStudentID, secondCuratorID); err != nil {
		t.Fatal(err)
	}
	if _, err := referrals.AssignStudentByReferral(ctx, otherAssignedStudentID, firstCode.Code); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("ссылка перевела ученика от другого куратора: %v", err)
	}
	if err := repo.AssignStudentCurator(ctx, adminID, endedStudentID, firstCuratorID); err != nil {
		t.Fatal(err)
	}
	if err := repo.EndStudentCuratorAssignment(ctx, adminID, endedStudentID); err != nil {
		t.Fatal(err)
	}
	if _, err := referrals.AssignStudentByReferral(ctx, endedStudentID, firstCode.Code); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("ссылка повторно назначила ученика с завершённой историей: %v", err)
	}
	if _, err := referrals.AssignStudentByReferral(ctx, blockedStudentID, firstCode.Code); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("назначение заблокированному ученику не отклонено: %v", err)
	}

	type attempt struct {
		result domain.ReferralAssignmentResult
		err    error
	}
	results := make(chan attempt, 2)
	var wg sync.WaitGroup
	for _, code := range []string{firstCode.Code, secondCode.Code} {
		wg.Add(1)
		go func(value string) {
			defer wg.Done()
			result, err := referrals.AssignStudentByReferral(ctx, concurrentStudentID, value)
			results <- attempt{result: result, err: err}
		}(code)
	}
	wg.Wait()
	close(results)
	assigned, conflicts := 0, 0
	for result := range results {
		if result.err == nil && result.result.Assigned {
			assigned++
			continue
		}
		if errors.Is(result.err, domain.ErrConflict) {
			conflicts++
			continue
		}
		t.Errorf("неожиданная конкурентная попытка: %+v", result)
	}
	if assigned != 1 || conflicts != 1 {
		t.Fatalf("конкуренция дала assigned=%d conflicts=%d, ожидалось 1/1", assigned, conflicts)
	}

	functionName := "reject_referral_audit_" + suffix
	triggerName := "reject_referral_audit_trigger_" + suffix
	if _, err := pool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.action = 'student.curator.referral_assign' THEN
				RAISE EXCEPTION 'forced referral audit failure';
			END IF;
			RETURN NEW;
		END;
		$$
	`, functionName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON audit_events", triggerName))
		_, _ = pool.Exec(cleanupCtx, fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", functionName))
	})
	if _, err := pool.Exec(ctx, fmt.Sprintf(`
		CREATE TRIGGER %s BEFORE INSERT ON audit_events
		FOR EACH ROW EXECUTE FUNCTION %s()
	`, triggerName, functionName)); err != nil {
		t.Fatal(err)
	}
	if _, err := referrals.AssignStudentByReferral(ctx, rollbackStudentID, firstCode.Code); err == nil {
		t.Fatal("ожидался отказ аудита")
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM student_curator_assignments WHERE student_id = $1", rollbackStudentID).Scan(&assignmentCount); err != nil || assignmentCount != 0 {
		t.Fatalf("после rollback сохранилось %d назначений, err=%v", assignmentCount, err)
	}
}
