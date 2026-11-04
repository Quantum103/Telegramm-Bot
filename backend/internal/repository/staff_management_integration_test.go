package repository

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestStudentCuratorAssignmentScopesStudentsAndTransfersAtomically(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var adminID, firstCuratorID, secondCuratorID, studentID, otherStudentID int64
	var err error
	createStaff := func(name, email string) int64 {
		t.Helper()
		var id int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO staff_accounts (display_name, email, account_status)
			VALUES ($1, $2, 'active') RETURNING id
		`, name, email).Scan(&id); err != nil {
			t.Fatalf("создать тестового сотрудника %q: %v", email, err)
		}
		return id
	}
	adminID = createStaff("assignment-admin", "assignment-admin-"+suffix+"@example.test")
	firstCuratorID = createStaff("assignment-curator-1", "assignment-curator-1-"+suffix+"@example.test")
	secondCuratorID = createStaff("assignment-curator-2", "assignment-curator-2-"+suffix+"@example.test")

	var adminRoleID, curatorRoleID int64
	if err := pool.QueryRow(ctx, "SELECT id FROM staff_roles WHERE role_code = 'administrator'").Scan(&adminRoleID); err != nil {
		t.Fatalf("получить роль администратора: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT id FROM staff_roles WHERE role_code = 'curator'").Scan(&curatorRoleID); err != nil {
		t.Fatalf("получить роль куратора: %v", err)
	}
	for staffID, roleID := range map[int64]int64{adminID: adminRoleID, firstCuratorID: curatorRoleID, secondCuratorID: curatorRoleID} {
		if _, err := pool.Exec(ctx, "INSERT INTO staff_role_assignments (staff_id, role_id, granted_by_staff_id) VALUES ($1, $2, $3)", staffID, roleID, adminID); err != nil {
			t.Fatalf("назначить тестовую роль сотруднику %d: %v", staffID, err)
		}
	}
	createStudent := func(telegramID int64, name string) int64 {
		t.Helper()
		var id int64
		if err := pool.QueryRow(ctx, "INSERT INTO students (telegram_id, display_name) VALUES ($1, $2) RETURNING id", telegramID, name).Scan(&id); err != nil {
			t.Fatalf("создать тестового ученика: %v", err)
		}
		return id
	}
	studentTelegramID := time.Now().UnixNano()
	studentID = createStudent(studentTelegramID, "student-assigned")
	otherStudentID = createStudent(time.Now().UnixNano()+1, "student-not-assigned")

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM audit_events WHERE entity_type = 'student' AND entity_id IN ($1, $2)", studentID, otherStudentID); err != nil {
			t.Errorf("очистить события аудита назначения куратора: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM student_curator_assignments WHERE student_id IN ($1, $2)", studentID, otherStudentID); err != nil {
			t.Errorf("очистить назначения кураторов: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `
			DELETE FROM conversation_message_reads read_mark
			USING conversations conversation
			WHERE read_mark.conversation_id = conversation.id
			  AND conversation.student_id IN ($1, $2)
		`, studentID, otherStudentID); err != nil {
			t.Errorf("очистить отметки прочтения тестовых диалогов: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `
			DELETE FROM conversation_message_deliveries delivery
			USING conversation_messages message, conversations conversation
			WHERE delivery.message_id = message.id
			  AND message.conversation_id = conversation.id
			  AND conversation.student_id IN ($1, $2)
		`, studentID, otherStudentID); err != nil {
			t.Errorf("очистить доставки тестовых сообщений: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `
			DELETE FROM conversation_messages message
			USING conversations conversation
			WHERE message.conversation_id = conversation.id
			  AND conversation.student_id IN ($1, $2)
		`, studentID, otherStudentID); err != nil {
			t.Errorf("очистить сообщения тестовых диалогов: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `
			DELETE FROM conversation_participants participant
			USING conversations conversation
			WHERE participant.conversation_id = conversation.id
			  AND conversation.student_id IN ($1, $2)
		`, studentID, otherStudentID); err != nil {
			t.Errorf("очистить участников тестовых диалогов: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM conversations WHERE student_id IN ($1, $2)", studentID, otherStudentID); err != nil {
			t.Errorf("очистить тестовые диалоги: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM staff_role_assignments WHERE staff_id IN ($1, $2, $3)", adminID, firstCuratorID, secondCuratorID); err != nil {
			t.Errorf("очистить роли тестовых сотрудников: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM students WHERE id IN ($1, $2)", studentID, otherStudentID); err != nil {
			t.Errorf("очистить тестовых учеников: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM staff_accounts WHERE id IN ($1, $2, $3)", adminID, firstCuratorID, secondCuratorID); err != nil {
			t.Errorf("очистить тестовых сотрудников: %v", err)
		}
	})

	repo := NewStaffManagementRepository(pool)
	if err := repo.AssignStudentCurator(ctx, adminID, studentID, firstCuratorID); err != nil {
		t.Fatalf("первое назначение куратора: %v", err)
	}
	conversationRepo := NewConversationRepository(pool)
	firstDialogs, err := conversationRepo.ListForCurator(ctx, firstCuratorID)
	if err != nil {
		t.Fatalf("получить диалоги сразу после назначения: %v", err)
	}
	if len(firstDialogs) != 1 || firstDialogs[0].StudentID != studentID {
		t.Fatalf("диалог должен появиться у куратора сразу после назначения, получено: %+v", firstDialogs)
	}
	conversationID := firstDialogs[0].ID
	firstMessages, err := conversationRepo.ListMessagesForCurator(ctx, firstCuratorID, conversationID, 0, 50)
	if err != nil {
		t.Fatalf("открыть пустой диалог до первого сообщения ученика: %v", err)
	}
	if len(firstMessages) != 0 {
		t.Fatalf("новый диалог должен быть пустым до первого сообщения: %+v", firstMessages)
	}
	var studentParticipants, curatorParticipants int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE student_id = $2),
		       count(*) FILTER (WHERE staff_id = $3 AND left_at IS NULL)
		FROM conversation_participants
		WHERE conversation_id = $1
	`, conversationID, studentID, firstCuratorID).Scan(&studentParticipants, &curatorParticipants); err != nil {
		t.Fatalf("проверить участников нового диалога: %v", err)
	}
	if studentParticipants != 1 || curatorParticipants != 1 {
		t.Fatalf("новый диалог должен сразу включать ученика и куратора: student=%d curator=%d", studentParticipants, curatorParticipants)
	}
	firstReply, err := conversationRepo.SendCuratorMessage(ctx, firstCuratorID, conversationID, "Здравствуйте! Это первое сообщение куратора.")
	if err != nil {
		t.Fatalf("куратор должен иметь возможность написать первым в пустом диалоге: %v", err)
	}
	var deliveryStatus string
	var recipientChatID int64
	if err := pool.QueryRow(ctx, `
		SELECT delivery_status, recipient_chat_id
		FROM conversation_message_deliveries
		WHERE message_id = $1 AND channel = 'telegram'
	`, firstReply.ID).Scan(&deliveryStatus, &recipientChatID); err != nil {
		t.Fatalf("получить Telegram-доставку первого сообщения куратора: %v", err)
	}
	if deliveryStatus != "pending" || recipientChatID != studentTelegramID {
		t.Fatalf("первое сообщение куратора должно быть поставлено в Telegram outbox: status=%s recipient=%d", deliveryStatus, recipientChatID)
	}
	firstView, err := repo.ListStudentsForStaff(ctx, firstCuratorID)
	if err != nil {
		t.Fatalf("получить учеников первого куратора: %v", err)
	}
	if len(firstView) != 1 || firstView[0].ID != studentID || firstView[0].CuratorStaffID == nil || *firstView[0].CuratorStaffID != firstCuratorID {
		t.Fatalf("куратор должен видеть только назначенного ему ученика, получено: %+v", firstView)
	}
	if err := repo.AssignStudentCurator(ctx, adminID, studentID, secondCuratorID); err != nil {
		t.Fatalf("перевести ученика второму куратору: %v", err)
	}
	firstDialogs, err = conversationRepo.ListForCurator(ctx, firstCuratorID)
	if err != nil {
		t.Fatalf("получить диалоги прежнего куратора после передачи: %v", err)
	}
	if len(firstDialogs) != 0 {
		t.Fatalf("прежний куратор сохранил доступ к диалогу после передачи: %+v", firstDialogs)
	}
	secondDialogs, err := conversationRepo.ListForCurator(ctx, secondCuratorID)
	if err != nil {
		t.Fatalf("получить диалоги нового куратора после передачи: %v", err)
	}
	if len(secondDialogs) != 1 || secondDialogs[0].ID != conversationID || secondDialogs[0].StudentID != studentID {
		t.Fatalf("при передаче должен сохраниться тот же диалог и появиться у нового куратора: %+v", secondDialogs)
	}
	firstView, err = repo.ListStudentsForStaff(ctx, firstCuratorID)
	if err != nil {
		t.Fatalf("повторно получить учеников первого куратора: %v", err)
	}
	if len(firstView) != 0 {
		t.Fatalf("бывший куратор сохранил доступ к ученику после перевода: %+v", firstView)
	}
	secondView, err := repo.ListStudentsForStaff(ctx, secondCuratorID)
	if err != nil {
		t.Fatalf("получить учеников второго куратора: %v", err)
	}
	if len(secondView) != 1 || secondView[0].ID != studentID {
		t.Fatalf("новый куратор не видит назначенного ученика: %+v", secondView)
	}
	adminView, err := repo.ListStudentsForStaff(ctx, adminID)
	if err != nil {
		t.Fatalf("получить список учеников администратором: %v", err)
	}
	if len(adminView) != 2 {
		t.Fatalf("администратор должен видеть всех тестовых учеников, got %d rows", len(adminView))
	}

	for _, student := range adminView {
		if student.ID != studentID && student.ID != otherStudentID {
			continue
		}
		if student.TotalEnrollments != 0 || student.InProgressEnrollments != 0 || student.CompletedEnrollments != 0 {
			t.Errorf("новый ученик должен иметь нулевой прогресс, получено: %+v", student)
		}
	}

	var activeCount, endedCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status = 'active'), count(*) FILTER (WHERE status = 'ended')
		FROM student_curator_assignments WHERE student_id = $1
	`, studentID).Scan(&activeCount, &endedCount); err != nil {
		t.Fatalf("проверить историю назначений: %v", err)
	}
	if activeCount != 1 || endedCount != 1 {
		t.Fatalf("активных/завершённых назначений = %d/%d, want 1/1", activeCount, endedCount)
	}
}
