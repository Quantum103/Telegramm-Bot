package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestSetStudentLabelEnforcesStaffAccessAndPersistsInStaffProjection(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	createStaff := func(name, prefix string) int64 {
		t.Helper()
		var id int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO staff_accounts (display_name, email, account_status)
			VALUES ($1, $2, 'active') RETURNING id
		`, name, prefix+"-"+suffix+"@example.test").Scan(&id); err != nil {
			t.Fatalf("create staff %s: %v", prefix, err)
		}
		return id
	}
	adminID := createStaff("label-admin", "label-admin")
	curatorID := createStaff("label-curator", "label-curator")
	otherCuratorID := createStaff("label-other-curator", "label-other-curator")

	var adminRoleID, curatorRoleID int64
	if err := pool.QueryRow(ctx, "SELECT id FROM staff_roles WHERE role_code = 'administrator'").Scan(&adminRoleID); err != nil {
		t.Fatalf("read administrator role: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT id FROM staff_roles WHERE role_code = 'curator'").Scan(&curatorRoleID); err != nil {
		t.Fatalf("read curator role: %v", err)
	}
	for staffID, roleID := range map[int64]int64{
		adminID: adminRoleID, curatorID: curatorRoleID, otherCuratorID: curatorRoleID,
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO staff_role_assignments (staff_id, role_id, granted_by_staff_id)
			VALUES ($1, $2, $3)
		`, staffID, roleID, adminID); err != nil {
			t.Fatalf("assign role to %d: %v", staffID, err)
		}
	}

	var studentID, unassignedStudentID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO students (telegram_id, display_name, preferred_name)
		VALUES ($1, 'Telegram Name', 'Профильное имя') RETURNING id
	`, time.Now().UnixNano()).Scan(&studentID); err != nil {
		t.Fatalf("create assigned student: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO students (telegram_id, display_name)
		VALUES ($1, 'Other Telegram Name') RETURNING id
	`, time.Now().UnixNano()+1).Scan(&unassignedStudentID); err != nil {
		t.Fatalf("create unassigned student: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO student_curator_assignments (student_id, curator_staff_id, assigned_by_staff_id)
		VALUES ($1, $2, $3)
	`, studentID, curatorID, adminID); err != nil {
		t.Fatalf("assign student to curator: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM audit_events WHERE entity_type = 'student' AND entity_id IN ($1, $2)", studentID, unassignedStudentID); err != nil {
			t.Errorf("cleanup student label audit events: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `
			DELETE FROM conversation_participants participant
			USING conversations conversation
			WHERE participant.conversation_id = conversation.id
			  AND conversation.student_id IN ($1, $2)
		`, studentID, unassignedStudentID); err != nil {
			t.Errorf("cleanup conversation participants: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM conversations WHERE student_id IN ($1, $2)", studentID, unassignedStudentID); err != nil {
			t.Errorf("cleanup test conversations: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM student_curator_assignments WHERE student_id IN ($1, $2)", studentID, unassignedStudentID); err != nil {
			t.Errorf("cleanup student assignments: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM students WHERE id IN ($1, $2)", studentID, unassignedStudentID); err != nil {
			t.Errorf("cleanup students: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM staff_role_assignments WHERE staff_id IN ($1, $2, $3)", adminID, curatorID, otherCuratorID); err != nil {
			t.Errorf("cleanup staff roles: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM staff_accounts WHERE id IN ($1, $2, $3)", adminID, curatorID, otherCuratorID); err != nil {
			t.Errorf("cleanup staff: %v", err)
		}
	})

	repo := NewStaffManagementRepository(pool)
	if err := repo.SetStudentLabel(ctx, curatorID, studentID, "  Ученик из Ростова  "); err != nil {
		t.Fatalf("assigned curator should update label: %v", err)
	}
	items, err := repo.ListStudentsForStaff(ctx, curatorID)
	if err != nil {
		t.Fatalf("list curator students: %v", err)
	}
	if len(items) != 1 || items[0].ID != studentID || items[0].StaffLabel == nil || *items[0].StaffLabel != "Ученик из Ростова" {
		t.Fatalf("staff label was not persisted in curator projection: %+v", items)
	}

	conversationRepo := NewConversationRepository(pool)
	if _, err := conversationRepo.GetOrCreateForCurator(ctx, curatorID, studentID); err != nil {
		t.Fatalf("create conversation for labelled student: %v", err)
	}
	dialogs, err := conversationRepo.ListForCurator(ctx, curatorID)
	if err != nil {
		t.Fatalf("list curator conversations: %v", err)
	}
	if len(dialogs) != 1 || dialogs[0].StudentDisplayName != "Ученик из Ростова" {
		t.Fatalf("conversation list should display staff label: %+v", dialogs)
	}

	if err := repo.SetStudentLabel(ctx, otherCuratorID, studentID, "Чужой ученик"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unassigned curator error = %v, want ErrNotFound", err)
	}
	if err := repo.SetStudentLabel(ctx, adminID, unassignedStudentID, "Набор для администратора"); err != nil {
		t.Fatalf("administrator should update any active student: %v", err)
	}
	var adminLabel string
	if err := pool.QueryRow(ctx, "SELECT staff_label FROM students WHERE id = $1", unassignedStudentID).Scan(&adminLabel); err != nil {
		t.Fatalf("read administrator label: %v", err)
	}
	if adminLabel != "Набор для администратора" {
		t.Fatalf("administrator label = %q", adminLabel)
	}

	if err := repo.SetStudentLabel(ctx, curatorID, studentID, "   "); err != nil {
		t.Fatalf("empty label should clear override: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT staff_label FROM students WHERE id = $1", studentID).Scan(&adminLabel); err != nil {
		t.Fatalf("read cleared label: %v", err)
	}
	if adminLabel != "" {
		t.Fatalf("cleared label should be NULL, got %q", adminLabel)
	}
}
