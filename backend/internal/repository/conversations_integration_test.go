package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

type conversationFixture struct {
	studentID  int64
	telegramID int64
	adminID    int64
	curator1ID int64
	curator2ID int64
}

func createConversationFixture(t *testing.T, pool *pgxpool.Pool, withAssignment bool) conversationFixture {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	telegramID := time.Now().UnixNano()
	var studentID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO students (telegram_id, display_name)
		VALUES ($1, $2) RETURNING id
	`, telegramID, "Тестовый ученик "+suffix).Scan(&studentID); err != nil {
		t.Fatalf("создать ученика: %v", err)
	}
	createStaff := func(name, roleCode, emailPrefix string) int64 {
		var staffID, roleID int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO staff_accounts (display_name, email, password_hash)
			VALUES ($1, $2, 'test-hash') RETURNING id
		`, name, emailPrefix+"-"+suffix+"@example.test").Scan(&staffID); err != nil {
			t.Fatalf("создать сотрудника: %v", err)
		}
		if err := pool.QueryRow(ctx, "SELECT id FROM staff_roles WHERE role_code = $1", roleCode).Scan(&roleID); err != nil {
			t.Fatalf("получить роль: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO staff_role_assignments (staff_id, role_id) VALUES ($1, $2)
		`, staffID, roleID); err != nil {
			t.Fatalf("назначить роль: %v", err)
		}
		return staffID
	}
	fixture := conversationFixture{
		studentID: studentID, telegramID: telegramID,
		adminID:    createStaff("Тестовый администратор", "administrator", "conversation-admin"),
		curator1ID: createStaff("Первый куратор", "curator", "conversation-curator-one"),
		curator2ID: createStaff("Второй куратор", "curator", "conversation-curator-two"),
	}
	if withAssignment {
		if _, err := pool.Exec(ctx, `
			INSERT INTO student_curator_assignments (student_id, curator_staff_id)
			VALUES ($1, $2)
		`, studentID, fixture.curator1ID); err != nil {
			t.Fatalf("назначить первого куратора: %v", err)
		}
	}
	return fixture
}

func TestConversationLifecycleTransferAndIdempotency(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, true)
	if _, err := pool.Exec(ctx, "UPDATE staff_accounts SET telegram_id = $2 WHERE id = $1", fixture.curator1ID, fixture.telegramID+1000); err != nil {
		t.Fatalf("привязать Telegram первого куратора для теста: %v", err)
	}
	repo := NewConversationRepository(pool)

	const concurrency = 8
	ids := make(chan int64, concurrency)
	errs := make(chan error, concurrency)
	var wait sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			conversation, err := repo.GetOrCreateForStudent(ctx, fixture.studentID)
			if err != nil {
				errs <- err
				return
			}
			ids <- conversation.ID
		}()
	}
	wait.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Errorf("параллельное создание диалога: %v", err)
	}
	var conversationID int64
	for id := range ids {
		if conversationID == 0 {
			conversationID = id
		} else if id != conversationID {
			t.Fatalf("созданы разные диалоги: %d и %d", conversationID, id)
		}
	}
	if conversationID == 0 {
		t.Fatal("диалог не создан")
	}

	if _, err := repo.ListMessagesForCurator(ctx, fixture.curator2ID, conversationID, 0, 50); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("не назначенный куратор получил доступ к чужому диалогу: %v", err)
	}
	params := domain.CreateStudentConversationMessageParams{
		StudentID: fixture.studentID, TelegramChatID: fixture.telegramID,
		TelegramMessageID: 987654321, BodyText: "Здравствуйте, нужна помощь.",
	}
	first, err := repo.CreateStudentMessage(ctx, params)
	if err != nil {
		t.Fatalf("сохранить сообщение ученика: %v", err)
	}
	replay, err := repo.CreateStudentMessage(ctx, params)
	if err != nil {
		t.Fatalf("повторить Telegram update: %v", err)
	}
	if replay.ID != first.ID || !replay.Replayed {
		t.Fatalf("повтор создал дубликат: first=%+v replay=%+v", first, replay)
	}
	var notificationCount int
	var notificationStaffID int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*), max(recipient_staff_id)
		FROM conversation_message_deliveries
		WHERE message_id = $1 AND channel = 'curator_telegram'
	`, first.ID).Scan(&notificationCount, &notificationStaffID); err != nil {
		t.Fatalf("проверить задачу уведомления: %v", err)
	}
	if notificationCount != 1 || notificationStaffID != fixture.curator1ID {
		t.Fatalf("задача уведомления должна создаваться один раз для назначенного куратора: count=%d staff=%d", notificationCount, notificationStaffID)
	}
	deliveryRepository := NewConversationDeliveryRepository(pool)
	// В fixture уже есть старый ответ куратора; отделяем его от проверки нового notification-канала.
	if _, err := pool.Exec(ctx, `
		UPDATE conversation_message_deliveries
		SET delivery_status = 'sent', sent_at = now()
		WHERE channel = 'telegram' AND delivery_status = 'pending'
	`); err != nil {
		t.Fatalf("подготовить очередь для проверки уведомления: %v", err)
	}
	notification, err := deliveryRepository.ClaimDueDelivery(ctx, time.Minute)
	if err != nil {
		t.Fatalf("захватить уведомление куратора: %v", err)
	}
	if notification.Channel != "curator_telegram" || notification.MessageID != first.ID ||
		notification.RecipientStaffID != fixture.curator1ID || notification.RecipientChatID != fixture.telegramID+1000 {
		t.Fatalf("неверный адресат уведомления: %+v", notification)
	}
	if err := deliveryRepository.MarkDeliverySent(ctx, notification.ID); err != nil {
		t.Fatalf("отметить уведомление отправленным: %v", err)
	}
	dialogs, err := repo.ListForCurator(ctx, fixture.curator1ID)
	if err != nil || len(dialogs) != 1 || dialogs[0].UnreadCount != 1 {
		t.Fatalf("диалоги и непрочитанные: dialogs=%+v err=%v", dialogs, err)
	}
	if marked, err := repo.MarkReadForCurator(ctx, fixture.curator1ID, conversationID); err != nil || marked != 1 {
		t.Fatalf("отметить прочтение: marked=%d err=%v", marked, err)
	}
	if count, err := repo.UnreadCountForCurator(ctx, fixture.curator1ID); err != nil || count != 0 {
		t.Fatalf("непрочитанные после отметки: count=%d err=%v", count, err)
	}
	reply, err := repo.SendCuratorMessage(ctx, fixture.curator1ID, conversationID, "Добрый день! Чем могу помочь?")
	if err != nil {
		t.Fatalf("ответить ученику: %v", err)
	}
	var deliveryStatus string
	var recipientChatID int64
	if err := pool.QueryRow(ctx, `
		SELECT delivery_status, recipient_chat_id
		FROM conversation_message_deliveries WHERE message_id = $1 AND channel = 'telegram'
	`, reply.ID).Scan(&deliveryStatus, &recipientChatID); err != nil {
		t.Fatalf("получить поставленную в очередь доставку: %v", err)
	}
	if deliveryStatus != "pending" || recipientChatID != fixture.telegramID {
		t.Fatalf("неверное намерение доставки: status=%s chat_id=%d", deliveryStatus, recipientChatID)
	}
	delivery, err := deliveryRepository.ClaimDueDelivery(ctx, time.Minute)
	if err != nil {
		t.Fatalf("захватить задание Telegram outbox: %v", err)
	}
	if delivery.MessageID != reply.ID || delivery.RecipientChatID != fixture.telegramID ||
		delivery.BodyText != "Добрый день! Чем могу помочь?" || delivery.AttemptCount != 1 {
		t.Fatalf("неверное задание Telegram outbox: %+v", delivery)
	}
	if err := deliveryRepository.MarkDeliverySent(ctx, delivery.ID); err != nil {
		t.Fatalf("отметить доставку завершённой: %v", err)
	}
	encodedReply, err := json.Marshal(reply)
	if err != nil {
		t.Fatalf("сериализовать ответ для API: %v", err)
	}
	if strings.Contains(string(encodedReply), "telegram") || strings.Contains(string(encodedReply), "email") {
		t.Fatalf("ответ API содержит личные контакты: %s", encodedReply)
	}
	if count, err := repo.UnreadCountForStudent(ctx, fixture.studentID); err != nil || count != 1 {
		t.Fatalf("непрочитанные ученика: count=%d err=%v", count, err)
	}
	history, err := repo.ListMessagesForCurator(ctx, fixture.curator1ID, conversationID, 0, 50)
	if err != nil || len(history) != 2 {
		t.Fatalf("история до передачи: messages=%+v err=%v", history, err)
	}
	if history[0].ID != reply.ID || history[0].DeliveryStatus != "sent" || history[0].DeliveryErrorCode != nil {
		t.Fatalf("история не показывает успешный статус доставки: %+v", history[0])
	}

	if err := NewStaffManagementRepository(pool).AssignStudentCurator(ctx, fixture.adminID, fixture.studentID, fixture.curator2ID); err != nil {
		t.Fatalf("передать ученика: %v", err)
	}
	oldDialogs, err := repo.ListForCurator(ctx, fixture.curator1ID)
	if err != nil || len(oldDialogs) != 0 {
		t.Fatalf("старый куратор сохранил доступ: %+v err=%v", oldDialogs, err)
	}
	if _, err := repo.ListMessagesForCurator(ctx, fixture.curator1ID, conversationID, 0, 50); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("старый куратор прочитал историю после передачи: %v", err)
	}
	if _, err := repo.SendCuratorMessage(ctx, fixture.curator1ID, conversationID, "Недопустимый ответ"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("старый куратор отправил ответ после передачи: %v", err)
	}
	newHistory, err := repo.ListMessagesForCurator(ctx, fixture.curator2ID, conversationID, 0, 50)
	if err != nil || len(newHistory) != 2 {
		t.Fatalf("новый куратор не получил историю: messages=%+v err=%v", newHistory, err)
	}
	if unread, err := repo.UnreadCountForCurator(ctx, fixture.curator2ID); err != nil || unread != 0 {
		t.Fatalf("старая история должна быть прочитана для нового куратора: unread=%d err=%v", unread, err)
	}
}

func TestConversationWithoutAssignmentAndWrongTelegramChat(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, false)
	repo := NewConversationRepository(pool)
	conversation, err := repo.GetOrCreateForStudent(ctx, fixture.studentID)
	if err != nil || conversation.AssignedCuratorStaffID != 0 {
		t.Fatalf("диалог без куратора должен сохраняться без участника-куратора: %+v err=%v", conversation, err)
	}
	dialogs, err := repo.ListForCurator(ctx, fixture.curator1ID)
	if err != nil || len(dialogs) != 0 {
		t.Fatalf("неожиданные диалоги пустого куратора: %+v err=%v", dialogs, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO student_curator_assignments (student_id, curator_staff_id)
		VALUES ($1, $2)
	`, fixture.studentID, fixture.curator1ID); err != nil {
		t.Fatalf("назначить куратора: %v", err)
	}
	conversation, err = repo.GetOrCreateForStudent(ctx, fixture.studentID)
	if err != nil || conversation.AssignedCuratorStaffID != fixture.curator1ID {
		t.Fatalf("после назначения куратор должен получить диалог: %+v err=%v", conversation, err)
	}
	_, err = repo.CreateStudentMessage(ctx, domain.CreateStudentConversationMessageParams{
		StudentID: fixture.studentID, TelegramChatID: fixture.telegramID + 1,
		TelegramMessageID: 123, BodyText: "Поддельный чат",
	})
	if !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("чужой Telegram chat ID должен быть отклонён: %v", err)
	}
}

func TestConversationMessageAndDeliveryRollbackTogether(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, true)
	repo := NewConversationRepository(pool)
	conversation, err := repo.GetOrCreateForStudent(ctx, fixture.studentID)
	if err != nil {
		t.Fatalf("создать диалог для проверки транзакции: %v", err)
	}
	var beforeCount int64
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM conversation_messages WHERE conversation_id = $1", conversation.ID).Scan(&beforeCount); err != nil {
		t.Fatalf("получить число сообщений до теста: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION test_reject_conversation_delivery() RETURNS TRIGGER
		LANGUAGE plpgsql AS $test$
		BEGIN
			RAISE EXCEPTION 'forced delivery insert failure';
		END;
		$test$
	`); err != nil {
		t.Fatalf("создать функцию тестовой ошибки: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TRIGGER test_reject_conversation_delivery_trg
		BEFORE INSERT ON conversation_message_deliveries
		FOR EACH ROW EXECUTE FUNCTION test_reject_conversation_delivery()
	`); err != nil {
		t.Fatalf("создать триггер тестовой ошибки: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DROP TRIGGER IF EXISTS test_reject_conversation_delivery_trg ON conversation_message_deliveries")
		_, _ = pool.Exec(cleanupCtx, "DROP FUNCTION IF EXISTS test_reject_conversation_delivery()")
	})
	if _, err := repo.SendCuratorMessage(ctx, fixture.curator1ID, conversation.ID, "Это сообщение должно откатиться"); err == nil {
		t.Fatal("ожидалась ошибка вставки outbox, но транзакция завершилась успешно")
	}
	var afterCount int64
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM conversation_messages WHERE conversation_id = $1", conversation.ID).Scan(&afterCount); err != nil {
		t.Fatalf("получить число сообщений после теста: %v", err)
	}
	if afterCount != beforeCount {
		t.Fatalf("сообщение сохранилось без записи доставки: before=%d after=%d", beforeCount, afterCount)
	}
	// Тот же триггер отклоняет задачу уведомления ученика: само сообщение тоже должно откатиться.
	_, err = repo.CreateStudentMessage(ctx, domain.CreateStudentConversationMessageParams{
		StudentID: fixture.studentID, TelegramChatID: fixture.telegramID,
		TelegramMessageID: 887766, BodyText: "Это сообщение также должно откатиться",
	})
	if err == nil {
		t.Fatal("ожидалась ошибка создания задачи уведомления ученика")
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM conversation_messages WHERE conversation_id = $1", conversation.ID).Scan(&afterCount); err != nil {
		t.Fatalf("получить число сообщений после отката уведомления: %v", err)
	}
	if afterCount != beforeCount {
		t.Fatalf("сообщение ученика сохранилось без задачи уведомления: before=%d after=%d", beforeCount, afterCount)
	}
}

func TestCuratorNotificationClaimIsConcurrentAndLeaseRecovers(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, true)
	if _, err := pool.Exec(ctx, "UPDATE staff_accounts SET telegram_id = $2 WHERE id = $1", fixture.curator1ID, fixture.telegramID+2000); err != nil {
		t.Fatalf("привязать Telegram куратора: %v", err)
	}
	repo := NewConversationRepository(pool)
	message, err := repo.CreateStudentMessage(ctx, domain.CreateStudentConversationMessageParams{
		StudentID: fixture.studentID, TelegramChatID: fixture.telegramID,
		TelegramMessageID: 887767, BodyText: "Проверка конкурентного захвата",
	})
	if err != nil {
		t.Fatalf("создать сообщение ученика: %v", err)
	}
	// Повтор того же update не должен создавать вторую задачу.
	if _, err := repo.CreateStudentMessage(ctx, domain.CreateStudentConversationMessageParams{
		StudentID: fixture.studentID, TelegramChatID: fixture.telegramID,
		TelegramMessageID: 887767, BodyText: "Проверка конкурентного захвата",
	}); err != nil {
		t.Fatalf("повторить update: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM conversation_message_deliveries WHERE message_id = $1 AND channel = 'curator_telegram'", message.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("ожидалась одна задача после повторного update: count=%d err=%v", count, err)
	}
	deliveries := NewConversationDeliveryRepository(pool)
	const workers = 8
	var wait sync.WaitGroup
	var mu sync.Mutex
	claimed := 0
	var claimedID int64
	failures := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			item, err := deliveries.ClaimDueDelivery(ctx, time.Minute)
			if errors.Is(err, ErrNotFound) {
				return
			}
			if err != nil {
				failures <- err
				return
			}
			mu.Lock()
			claimed++
			claimedID = item.ID
			mu.Unlock()
		}()
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Errorf("параллельный claim: %v", err)
	}
	if claimed != 1 || claimedID == 0 {
		t.Fatalf("задачу должен захватить ровно один worker: claimed=%d id=%d", claimed, claimedID)
	}
	if _, err := pool.Exec(ctx, "UPDATE conversation_message_deliveries SET locked_at = now() - interval '3 minutes' WHERE id = $1", claimedID); err != nil {
		t.Fatalf("имитировать просроченный lease: %v", err)
	}
	recovered, err := deliveries.ClaimDueDelivery(ctx, time.Minute)
	if err != nil {
		t.Fatalf("восстановить просроченную задачу: %v", err)
	}
	if recovered.ID != claimedID || recovered.AttemptCount != 2 {
		t.Fatalf("просроченная задача не была восстановлена: %+v", recovered)
	}
}

func TestCuratorNotificationFollowsNewAssignment(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, true)
	newCuratorTelegramID := fixture.telegramID + 3000
	if _, err := pool.Exec(ctx, "UPDATE staff_accounts SET telegram_id = $2 WHERE id = $1", fixture.curator2ID, newCuratorTelegramID); err != nil {
		t.Fatalf("привязать Telegram нового куратора: %v", err)
	}
	repo := NewConversationRepository(pool)
	message, err := repo.CreateStudentMessage(ctx, domain.CreateStudentConversationMessageParams{
		StudentID: fixture.studentID, TelegramChatID: fixture.telegramID,
		TelegramMessageID: 887768, BodyText: "Сообщение перед переводом",
	})
	if err != nil {
		t.Fatalf("создать сообщение ученика: %v", err)
	}
	if err := NewStaffManagementRepository(pool).AssignStudentCurator(ctx, fixture.adminID, fixture.studentID, fixture.curator2ID); err != nil {
		t.Fatalf("передать ученика новому куратору: %v", err)
	}
	delivery, err := NewConversationDeliveryRepository(pool).ClaimDueDelivery(ctx, time.Minute)
	if err != nil {
		t.Fatalf("захватить перенаправленное уведомление: %v", err)
	}
	if delivery.MessageID != message.ID || delivery.RecipientStaffID != fixture.curator2ID || delivery.RecipientChatID != newCuratorTelegramID {
		t.Fatalf("уведомление не перенаправлено текущему куратору: %+v", delivery)
	}
}

func TestCuratorNotificationRequiresLinkedActiveCurator(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, true)
	repo := NewConversationRepository(pool)
	message, err := repo.CreateStudentMessage(ctx, domain.CreateStudentConversationMessageParams{
		StudentID: fixture.studentID, TelegramChatID: fixture.telegramID,
		TelegramMessageID: 887769, BodyText: "Уведомление без привязки",
	})
	if err != nil {
		t.Fatalf("создать сообщение ученика: %v", err)
	}
	delivery, err := NewConversationDeliveryRepository(pool).ClaimDueDelivery(ctx, time.Minute)
	if err != nil {
		t.Fatalf("захватить задачу без привязки: %v", err)
	}
	if delivery.MessageID != message.ID || delivery.PreparationErrorCode != "curator_telegram_not_linked" {
		t.Fatalf("неверное состояние при отсутствии Telegram-привязки: %+v", delivery)
	}
	if _, err := pool.Exec(ctx, "UPDATE staff_accounts SET account_status = 'inactive', telegram_id = $2 WHERE id = $1", fixture.curator1ID, fixture.telegramID+4000); err != nil {
		t.Fatalf("отключить куратора с привязанным Telegram: %v", err)
	}
	// Новое сообщение создаёт новую задачу, claim должен увидеть отключённую учётную запись.
	message, err = repo.CreateStudentMessage(ctx, domain.CreateStudentConversationMessageParams{
		StudentID: fixture.studentID, TelegramChatID: fixture.telegramID,
		TelegramMessageID: 887770, BodyText: "Уведомление отключённому куратору",
	})
	if err != nil {
		t.Fatalf("создать второе сообщение ученика: %v", err)
	}
	delivery, err = NewConversationDeliveryRepository(pool).ClaimDueDelivery(ctx, time.Minute)
	if err != nil {
		t.Fatalf("захватить задачу отключённого куратора: %v", err)
	}
	if delivery.MessageID != message.ID || delivery.PreparationErrorCode != "curator_account_inactive" {
		t.Fatalf("отключённый сотрудник не был отклонён: %+v", delivery)
	}
}

func TestProcessingCuratorNotificationIsRedirectedDuringTransfer(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, true)
	oldTelegramID, newTelegramID := fixture.telegramID+6100, fixture.telegramID+6200
	if _, err := pool.Exec(ctx, "UPDATE staff_accounts SET telegram_id = $2 WHERE id = $1", fixture.curator1ID, oldTelegramID); err != nil {
		t.Fatalf("привязать старый Telegram куратора: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE staff_accounts SET telegram_id = $2 WHERE id = $1", fixture.curator2ID, newTelegramID); err != nil {
		t.Fatalf("привязать новый Telegram куратора: %v", err)
	}
	conversations := NewConversationRepository(pool)
	message, err := conversations.CreateStudentMessage(ctx, domain.CreateStudentConversationMessageParams{
		StudentID: fixture.studentID, TelegramChatID: fixture.telegramID,
		TelegramMessageID: 887772, BodyText: "Передача во время обработки",
	})
	if err != nil {
		t.Fatalf("создать сообщение ученика: %v", err)
	}
	deliveries := NewConversationDeliveryRepository(pool)
	claimed, err := deliveries.ClaimDueDelivery(ctx, time.Minute)
	if err != nil {
		t.Fatalf("захватить уведомление старым куратором: %v", err)
	}
	if claimed.MessageID != message.ID || claimed.RecipientStaffID != fixture.curator1ID || claimed.RecipientChatID != oldTelegramID {
		t.Fatalf("неверная исходная задача: %+v", claimed)
	}
	valid, err := deliveries.IsCurrentCuratorDelivery(ctx, claimed.ID, claimed.RecipientStaffID, claimed.RecipientChatID)
	if err != nil || !valid {
		t.Fatalf("старый адресат должен быть действующим до перевода: valid=%v err=%v", valid, err)
	}
	if err := NewStaffManagementRepository(pool).AssignStudentCurator(ctx, fixture.adminID, fixture.studentID, fixture.curator2ID); err != nil {
		t.Fatalf("передать ученика новому куратору: %v", err)
	}
	valid, err = deliveries.IsCurrentCuratorDelivery(ctx, claimed.ID, claimed.RecipientStaffID, claimed.RecipientChatID)
	if err != nil {
		t.Fatalf("повторно проверить старого адресата: %v", err)
	}
	if valid {
		t.Fatal("после перевода уведомление больше не должно быть доступно старому куратору")
	}
	var status string
	var recipientStaffID int64
	if err := pool.QueryRow(ctx, "SELECT delivery_status, recipient_staff_id FROM conversation_message_deliveries WHERE id = $1", claimed.ID).Scan(&status, &recipientStaffID); err != nil {
		t.Fatalf("прочитать перенаправленную задачу: %v", err)
	}
	if status != "pending" || recipientStaffID != fixture.curator2ID {
		t.Fatalf("задача processing должна вернуться в очередь новому куратору: status=%s recipient=%d", status, recipientStaffID)
	}
	next, err := deliveries.ClaimDueDelivery(ctx, time.Minute)
	if err != nil {
		t.Fatalf("захватить уведомление новым куратором: %v", err)
	}
	if next.ID != claimed.ID || next.RecipientStaffID != fixture.curator2ID || next.RecipientChatID != newTelegramID {
		t.Fatalf("новый claim не использовал актуального куратора: %+v", next)
	}
}

func TestStudentMessageBeforeCuratorAssignmentIsStoredAndDeliveredAfterAssignment(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, false)
	repo := NewConversationRepository(pool)

	message, err := repo.CreateStudentMessage(ctx, domain.CreateStudentConversationMessageParams{
		StudentID: fixture.studentID, TelegramChatID: fixture.telegramID,
		TelegramMessageID: 887771, BodyText: "Сообщение до назначения куратора",
	})
	if err != nil {
		t.Fatalf("сообщение ученика должно сохраниться до назначения куратора: %v", err)
	}
	deliveryRepo := NewConversationDeliveryRepository(pool)
	delivery, err := deliveryRepo.ClaimDueDelivery(ctx, time.Minute)
	if err != nil {
		t.Fatalf("захватить уведомление без назначенного куратора: %v", err)
	}
	if delivery.MessageID != message.ID || delivery.PreparationErrorCode != "curator_assignment_missing" {
		t.Fatalf("ожидалась задача без адресата и с понятной ошибкой: %+v", delivery)
	}
	if err := deliveryRepo.MarkDeliveryFailed(ctx, delivery.ID, delivery.PreparationErrorCode); err != nil {
		t.Fatalf("сохранить отсутствие назначенного куратора: %v", err)
	}
	if err := NewStaffManagementRepository(pool).AssignStudentCurator(ctx, fixture.adminID, fixture.studentID, fixture.curator1ID); err != nil {
		t.Fatalf("назначить первого куратора: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE staff_accounts SET telegram_id = $2 WHERE id = $1", fixture.curator1ID, fixture.telegramID+5000); err != nil {
		t.Fatalf("привязать Telegram нового куратора: %v", err)
	}
	delivery, err = deliveryRepo.ClaimDueDelivery(ctx, time.Minute)
	if err != nil {
		t.Fatalf("повторно захватить задачу после назначения: %v", err)
	}
	if delivery.MessageID != message.ID || delivery.RecipientStaffID != fixture.curator1ID ||
		delivery.RecipientChatID != fixture.telegramID+5000 || delivery.PreparationErrorCode != "" {
		t.Fatalf("задача не была восстановлена для нового куратора: %+v", delivery)
	}
}

func TestResponseWaitCreatesOneGenerationAndResetsOnNewStudentMessage(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, true)
	repo := NewConversationRepository(pool)

	first, err := repo.CreateStudentMessage(ctx, domain.CreateStudentConversationMessageParams{
		StudentID: fixture.studentID, TelegramChatID: fixture.telegramID,
		TelegramMessageID: 990001, BodyText: "Первый вопрос",
		FirstReminderAfter: 2 * time.Hour, SecondReminderAfter: 3 * time.Hour, EscalationAfter: 4 * time.Hour,
	})
	if err != nil {
		t.Fatalf("создать первое сообщение: %v", err)
	}

	var waitID int64
	var generation int
	var waitMessageID int64
	var firstDelay, secondDelay, escalationDelay float64
	if err := pool.QueryRow(ctx, `
		SELECT id, generation, student_message_id,
		       EXTRACT(EPOCH FROM first_reminder_at - opened_at),
		       EXTRACT(EPOCH FROM second_reminder_at - opened_at),
		       EXTRACT(EPOCH FROM escalation_at - opened_at)
		FROM conversation_response_waits WHERE conversation_id = $1
	`, first.ConversationID).Scan(&waitID, &generation, &waitMessageID, &firstDelay, &secondDelay, &escalationDelay); err != nil {
		t.Fatalf("прочитать состояние ожидания: %v", err)
	}
	if generation != 1 || waitMessageID != first.ID ||
		firstDelay != 7200 || secondDelay != 10800 || escalationDelay != 14400 {
		t.Fatalf("неверные исходные сроки ожидания: generation=%d message=%d delays=%v/%v/%v",
			generation, waitMessageID, firstDelay, secondDelay, escalationDelay)
	}
	var firstTasks int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM conversation_message_deliveries WHERE response_wait_id = $1 AND response_wait_generation = 1", waitID).Scan(&firstTasks); err != nil {
		t.Fatalf("посчитать задачи первого поколения: %v", err)
	}
	if firstTasks != 3 {
		t.Fatalf("ожидались две напоминалки и одна эскалация, got %d", firstTasks)
	}

	replay, err := repo.CreateStudentMessage(ctx, domain.CreateStudentConversationMessageParams{
		StudentID: fixture.studentID, TelegramChatID: fixture.telegramID,
		TelegramMessageID: 990001, BodyText: "Первый вопрос",
		FirstReminderAfter: time.Hour, SecondReminderAfter: 2 * time.Hour, EscalationAfter: 3 * time.Hour,
	})
	if err != nil || !replay.Replayed {
		t.Fatalf("повтор update должен быть идемпотентен: replay=%+v err=%v", replay, err)
	}
	if err := pool.QueryRow(ctx, "SELECT generation FROM conversation_response_waits WHERE id = $1", waitID).Scan(&generation); err != nil || generation != 1 {
		t.Fatalf("повтор не должен создавать новое поколение: generation=%d err=%v", generation, err)
	}

	second, err := repo.CreateStudentMessage(ctx, domain.CreateStudentConversationMessageParams{
		StudentID: fixture.studentID, TelegramChatID: fixture.telegramID,
		TelegramMessageID: 990002, BodyText: "Уточнение к вопросу",
		FirstReminderAfter: time.Hour, SecondReminderAfter: 2 * time.Hour, EscalationAfter: 3 * time.Hour,
	})
	if err != nil {
		t.Fatalf("создать новое сообщение ученика: %v", err)
	}
	var currentGeneration int
	var currentMessageID int64
	if err := pool.QueryRow(ctx, "SELECT generation, student_message_id FROM conversation_response_waits WHERE id = $1", waitID).Scan(&currentGeneration, &currentMessageID); err != nil {
		t.Fatalf("прочитать новое поколение: %v", err)
	}
	if currentGeneration != 2 || currentMessageID != second.ID {
		t.Fatalf("ожидание не было сброшено: generation=%d message=%d", currentGeneration, currentMessageID)
	}
	var oldCancelled, newPending int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE response_wait_generation = 1 AND delivery_status = 'cancelled'),
		       count(*) FILTER (WHERE response_wait_generation = 2 AND delivery_status = 'pending')
		FROM conversation_message_deliveries WHERE response_wait_id = $1
	`, waitID).Scan(&oldCancelled, &newPending); err != nil {
		t.Fatalf("проверить поколения outbox: %v", err)
	}
	if oldCancelled != 3 || newPending != 3 {
		t.Fatalf("старые задачи должны отмениться, новые создаться один раз: old=%d new=%d", oldCancelled, newPending)
	}
}

func TestCuratorReplyClosesWaitAndCancelsPendingTasks(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, true)
	conversations := NewConversationRepository(pool)
	message, err := conversations.CreateStudentMessage(ctx, domain.CreateStudentConversationMessageParams{
		StudentID: fixture.studentID, TelegramChatID: fixture.telegramID,
		TelegramMessageID: 990003, BodyText: "Жду ответа",
	})
	if err != nil {
		t.Fatalf("создать сообщение ученика: %v", err)
	}
	if _, err := conversations.SendCuratorMessage(ctx, fixture.curator1ID, message.ConversationID, "Ответ куратора"); err != nil {
		t.Fatalf("ответить от имени куратора: %v", err)
	}
	var respondedAt time.Time
	var pendingTasks, cancelledTasks int
	if err := pool.QueryRow(ctx, "SELECT responded_at FROM conversation_response_waits WHERE conversation_id = $1", message.ConversationID).Scan(&respondedAt); err != nil {
		t.Fatalf("прочитать закрытие ожидания: %v", err)
	}
	if respondedAt.IsZero() {
		t.Fatal("ответ куратора должен закрыть ожидание")
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE delivery_status IN ('pending', 'retry', 'processing')),
		       count(*) FILTER (WHERE delivery_status = 'cancelled')
		FROM conversation_message_deliveries
		WHERE channel IN ('curator_reminder_4h', 'curator_reminder_24h', 'admin_escalation')
		  AND response_wait_id = (SELECT id FROM conversation_response_waits WHERE conversation_id = $1)
	`, message.ConversationID).Scan(&pendingTasks, &cancelledTasks); err != nil {
		t.Fatalf("проверить отмену напоминаний: %v", err)
	}
	if pendingTasks != 0 || cancelledTasks != 3 {
		t.Fatalf("после ответа задачи ожидания должны быть отменены: pending=%d cancelled=%d", pendingTasks, cancelledTasks)
	}
}

func TestTransferMovesReminderTasksButKeepsSingleEscalation(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, true)
	adminTelegramID := fixture.telegramID + 80001
	oldCuratorTelegramID := fixture.telegramID + 80002
	newCuratorTelegramID := fixture.telegramID + 80003
	for staffID, telegramID := range map[int64]int64{
		fixture.adminID:    adminTelegramID,
		fixture.curator1ID: oldCuratorTelegramID,
		fixture.curator2ID: newCuratorTelegramID,
	} {
		if _, err := pool.Exec(ctx, "UPDATE staff_accounts SET telegram_id = $2 WHERE id = $1", staffID, telegramID); err != nil {
			t.Fatalf("привязать Telegram сотрудника %d: %v", staffID, err)
		}
	}
	conversations := NewConversationRepository(pool)
	message, err := conversations.CreateStudentMessage(ctx, domain.CreateStudentConversationMessageParams{
		StudentID: fixture.studentID, TelegramChatID: fixture.telegramID, TelegramMessageID: 990004,
		BodyText:           "Ожидание до передачи",
		FirstReminderAfter: time.Hour, SecondReminderAfter: 2 * time.Hour, EscalationAfter: 3 * time.Hour,
	})
	if err != nil {
		t.Fatalf("создать сообщение: %v", err)
	}

	preferences := NewStaffTelegramRepository(pool)
	prefs, err := preferences.GetNotificationPreferences(ctx, fixture.curator1ID)
	if err != nil {
		t.Fatalf("прочитать настройки куратора: %v", err)
	}
	prefs.RemindersEnabled = false
	if err := preferences.UpdateNotificationPreferences(ctx, fixture.curator1ID, prefs); err != nil {
		t.Fatalf("отключить напоминания старому куратору: %v", err)
	}
	var waitID int64
	var generation int
	if err := pool.QueryRow(ctx, "SELECT id, generation FROM conversation_response_waits WHERE conversation_id = $1", message.ConversationID).Scan(&waitID, &generation); err != nil {
		t.Fatalf("прочитать ожидание: %v", err)
	}
	var escalationID int64
	if err := pool.QueryRow(ctx, "SELECT id FROM conversation_message_deliveries WHERE response_wait_id = $1 AND channel = 'admin_escalation'", waitID).Scan(&escalationID); err != nil {
		t.Fatalf("прочитать исходную эскалацию: %v", err)
	}
	if err := NewStaffManagementRepository(pool).AssignStudentCurator(ctx, fixture.adminID, fixture.studentID, fixture.curator2ID); err != nil {
		t.Fatalf("передать ученика новому куратору: %v", err)
	}
	var newGeneration int
	var newCuratorID int64
	if err := pool.QueryRow(ctx, "SELECT generation, curator_staff_id FROM conversation_response_waits WHERE id = $1", waitID).Scan(&newGeneration, &newCuratorID); err != nil {
		t.Fatalf("проверить переназначение ожидания: %v", err)
	}
	if newGeneration != generation || newCuratorID != fixture.curator2ID {
		t.Fatalf("передача не должна создавать новое ожидание: generation=%d curator=%d", newGeneration, newCuratorID)
	}
	var reminderCount, reminderRecipient int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE recipient_staff_id = $2 AND delivery_status = 'pending')
		FROM conversation_message_deliveries
		WHERE response_wait_id = $1 AND channel IN ('curator_reminder_4h', 'curator_reminder_24h')
	`, waitID, fixture.curator2ID).Scan(&reminderCount, &reminderRecipient); err != nil {
		t.Fatalf("проверить перенос напоминаний: %v", err)
	}
	if reminderCount != 2 || reminderRecipient != 2 {
		t.Fatalf("задачи должны перейти новому куратору: count=%d recipient=%d", reminderCount, reminderRecipient)
	}
	var newEscalationID int64
	if err := pool.QueryRow(ctx, "SELECT id FROM conversation_message_deliveries WHERE response_wait_id = $1 AND channel = 'admin_escalation'", waitID).Scan(&newEscalationID); err != nil {
		t.Fatalf("прочитать эскалацию после передачи: %v", err)
	}
	if newEscalationID != escalationID {
		t.Fatalf("передача не должна создавать вторую эскалацию: old=%d new=%d", escalationID, newEscalationID)
	}
}

func TestConsecutiveCuratorTransfersKeepOneTaskForLastAssignee(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, true)
	message, err := NewConversationRepository(pool).CreateStudentMessage(ctx, domain.CreateStudentConversationMessageParams{
		StudentID: fixture.studentID, TelegramChatID: fixture.telegramID, TelegramMessageID: 990006,
		BodyText: "Проверка нескольких последовательных передач",
		FirstReminderAfter: time.Hour, SecondReminderAfter: 2 * time.Hour, EscalationAfter: 3 * time.Hour,
	})
	if err != nil {
		t.Fatalf("создать сообщение и ожидание ответа: %v", err)
	}
	var waitID int64
	var generation int
	if err := pool.QueryRow(ctx, "SELECT id, generation FROM conversation_response_waits WHERE conversation_id = $1", message.ConversationID).Scan(&waitID, &generation); err != nil {
		t.Fatalf("получить исходное ожидание: %v", err)
	}

	assignments := []int64{fixture.curator2ID, fixture.curator1ID, fixture.curator2ID, fixture.curator2ID}
	for index, curatorID := range assignments {
		if err := NewStaffManagementRepository(pool).AssignStudentCurator(ctx, fixture.adminID, fixture.studentID, curatorID); err != nil {
			t.Fatalf("передача ученика на шаге %d куратору %d: %v", index+1, curatorID, err)
		}
	}

	var taskCount int
	var recipientID int64
	var taskStatus string
	if err := pool.QueryRow(ctx, `
		SELECT count(*), max(recipient_staff_id), max(delivery_status)
		FROM conversation_message_deliveries
		WHERE response_wait_id = $1 AND response_wait_generation = $2 AND channel = 'curator_transfer'
	`, waitID, generation).Scan(&taskCount, &recipientID, &taskStatus); err != nil {
		t.Fatalf("проверить единственную задачу передачи: %v", err)
	}
	if taskCount != 1 || recipientID != fixture.curator2ID || taskStatus != "pending" {
		t.Fatalf("после нескольких передач должна остаться одна задача последнему куратору: count=%d recipient=%d status=%s",
			taskCount, recipientID, taskStatus)
	}

	var currentGeneration int
	var currentCuratorID int64
	if err := pool.QueryRow(ctx, "SELECT generation, curator_staff_id FROM conversation_response_waits WHERE id = $1", waitID).Scan(&currentGeneration, &currentCuratorID); err != nil {
		t.Fatalf("проверить ожидание после передач: %v", err)
	}
	if currentGeneration != generation || currentCuratorID != fixture.curator2ID {
		t.Fatalf("передачи не должны сбрасывать ожидание; должен остаться последний куратор: generation=%d curator=%d",
			currentGeneration, currentCuratorID)
	}
	var escalationCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM conversation_message_deliveries
		WHERE response_wait_id = $1 AND response_wait_generation = $2 AND channel = 'admin_escalation'
	`, waitID, generation).Scan(&escalationCount); err != nil {
		t.Fatalf("проверить единственную эскалацию: %v", err)
	}
	if escalationCount != 1 {
		t.Fatalf("последовательные передачи создали дополнительные эскалации: count=%d", escalationCount)
	}
}

func TestNotificationPreferencesDisableAndReenableReminders(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, true)
	conversations := NewConversationRepository(pool)
	message, err := conversations.CreateStudentMessage(ctx, domain.CreateStudentConversationMessageParams{
		StudentID: fixture.studentID, TelegramChatID: fixture.telegramID, TelegramMessageID: 990005,
		BodyText: "Проверка настроек", FirstReminderAfter: time.Hour,
		SecondReminderAfter: 2 * time.Hour, EscalationAfter: 3 * time.Hour,
	})
	if err != nil {
		t.Fatalf("создать сообщение: %v", err)
	}
	repo := NewStaffTelegramRepository(pool)
	prefs, err := repo.GetNotificationPreferences(ctx, fixture.curator1ID)
	if err != nil {
		t.Fatalf("получить настройки по умолчанию: %v", err)
	}
	if !prefs.RemindersEnabled || !prefs.NotificationsEnabled || prefs.Timezone != "UTC" || prefs.QuietHoursEnabled {
		t.Fatalf("неверные настройки по умолчанию: %+v", prefs)
	}
	prefs.RemindersEnabled = false
	if err := repo.UpdateNotificationPreferences(ctx, fixture.curator1ID, prefs); err != nil {
		t.Fatalf("отключить напоминания: %v", err)
	}
	var cancelled int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM conversation_message_deliveries
		WHERE message_id = $1 AND channel IN ('curator_reminder_4h', 'curator_reminder_24h')
		  AND delivery_status = 'cancelled'
	`, message.ID).Scan(&cancelled); err != nil {
		t.Fatalf("проверить отключение напоминаний: %v", err)
	}
	if cancelled != 2 {
		t.Fatalf("ожидались две отменённые задачи, got %d", cancelled)
	}
	prefs.RemindersEnabled = true
	if err := repo.UpdateNotificationPreferences(ctx, fixture.curator1ID, prefs); err != nil {
		t.Fatalf("включить напоминания: %v", err)
	}
	// Simulate an ordinary notification that was deferred during a pause.
	if _, err := pool.Exec(ctx, `
		UPDATE conversation_message_deliveries
		SET next_attempt_at = now() + INTERVAL '15 minutes'
		WHERE message_id = $1 AND channel = 'curator_telegram' AND delivery_status = 'pending'
	`, message.ID); err != nil {
		t.Fatalf("подготовить отложенное обычное уведомление: %v", err)
	}
	prefs.NotificationsEnabled = false
	if err := repo.UpdateNotificationPreferences(ctx, fixture.curator1ID, prefs); err != nil {
		t.Fatalf("поставить уведомления на паузу: %v", err)
	}
	prefs.NotificationsEnabled = true
	if err := repo.UpdateNotificationPreferences(ctx, fixture.curator1ID, prefs); err != nil {
		t.Fatalf("снять паузу уведомлений: %v", err)
	}
	var resumedDue bool
	if err := pool.QueryRow(ctx, `
		SELECT next_attempt_at <= now()
		FROM conversation_message_deliveries
		WHERE message_id = $1 AND channel = 'curator_telegram'
	`, message.ID).Scan(&resumedDue); err != nil {
		t.Fatalf("проверить возобновление обычного уведомления: %v", err)
	}
	if !resumedDue {
		t.Fatal("после снятия паузы обычное уведомление должно быть разблокировано немедленно")
	}
	var pending int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM conversation_message_deliveries
		WHERE message_id = $1 AND channel IN ('curator_reminder_4h', 'curator_reminder_24h')
		  AND delivery_status = 'pending'
	`, message.ID).Scan(&pending); err != nil {
		t.Fatalf("проверить восстановление напоминаний: %v", err)
	}
	if pending != 2 {
		t.Fatalf("после повторного включения ожидаются две задачи, got %d", pending)
	}
}

func TestResponseWaitOutboxSurvivesRepositoryRestart(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, true)
	message, err := NewConversationRepository(pool).CreateStudentMessage(ctx, domain.CreateStudentConversationMessageParams{
		StudentID: fixture.studentID, TelegramChatID: fixture.telegramID, TelegramMessageID: 990006,
		BodyText:           "Восстановление после перезапуска",
		FirstReminderAfter: time.Hour, SecondReminderAfter: 2 * time.Hour, EscalationAfter: 3 * time.Hour,
	})
	if err != nil {
		t.Fatalf("создать сообщение: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE conversation_message_deliveries
		SET delivery_status = 'sent', sent_at = now()
		WHERE message_id = $1 AND channel = 'curator_telegram'
	`, message.ID); err != nil {
		t.Fatalf("закрыть немедленное уведомление: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE staff_accounts SET telegram_id = $2 WHERE id = $1", fixture.curator1ID, fixture.telegramID+88000); err != nil {
		t.Fatalf("привязать Telegram куратора: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE conversation_message_deliveries SET next_attempt_at = '1970-01-01T00:00:00Z'::timestamptz
		WHERE message_id = $1 AND channel = 'curator_reminder_4h'
	`, message.ID); err != nil {
		t.Fatalf("сделать задачу ожидающей: %v", err)
	}
	restarted := NewConversationDeliveryRepository(pool)
	delivery, err := restarted.ClaimDueDelivery(ctx, time.Minute)
	if err != nil {
		t.Fatalf("новый repository должен восстановить задачу из PostgreSQL: %v", err)
	}
	if delivery.Channel != "curator_reminder_4h" || delivery.ResponseWaitID <= 0 || delivery.ResponseWaitGeneration != 1 {
		t.Fatalf("неверно восстановленная задача: %+v", delivery)
	}
}

func TestStudentChatModePersistsInPostgres(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, false)
	repo := NewConversationRepository(pool)

	enabled, err := repo.IsStudentChatMode(ctx, fixture.studentID)
	if err != nil || enabled {
		t.Fatalf("режим общения по умолчанию должен быть выключен: enabled=%v err=%v", enabled, err)
	}
	if err := repo.SetStudentChatMode(ctx, fixture.studentID, true); err != nil {
		t.Fatalf("включить режим общения: %v", err)
	}
	// Новый экземпляр репозитория имитирует чтение после перезапуска backend.
	restartedRepository := NewConversationRepository(pool)
	enabled, err = restartedRepository.IsStudentChatMode(ctx, fixture.studentID)
	if err != nil || !enabled {
		t.Fatalf("режим общения не сохранился в PostgreSQL: enabled=%v err=%v", enabled, err)
	}
	if err := restartedRepository.SetStudentChatMode(ctx, fixture.studentID, false); err != nil {
		t.Fatalf("выключить режим общения: %v", err)
	}
	enabled, err = repo.IsStudentChatMode(ctx, fixture.studentID)
	if err != nil || enabled {
		t.Fatalf("режим общения не выключился: enabled=%v err=%v", enabled, err)
	}
}

func TestConversationDeliveryFailureIsVisibleToCurator(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, true)
	conversations := NewConversationRepository(pool)
	dialog, err := conversations.GetOrCreateForStudent(ctx, fixture.studentID)
	if err != nil {
		t.Fatalf("создать диалог: %v", err)
	}
	message, err := conversations.SendCuratorMessage(ctx, fixture.curator1ID, dialog.ID, "Проверка статуса доставки")
	if err != nil {
		t.Fatalf("сохранить ответ куратора: %v", err)
	}

	deliveries := NewConversationDeliveryRepository(pool)
	delivery, err := deliveries.ClaimDueDelivery(ctx, time.Minute)
	if err != nil {
		t.Fatalf("захватить задание доставки: %v", err)
	}
	if delivery.MessageID != message.ID {
		t.Fatalf("захвачено другое сообщение: %+v", delivery)
	}
	if err := deliveries.MarkDeliveryFailed(ctx, delivery.ID, "telegram_recipient_unavailable"); err != nil {
		t.Fatalf("сохранить окончательную ошибку: %v", err)
	}

	history, err := conversations.ListMessagesForCurator(ctx, fixture.curator1ID, dialog.ID, 0, 50)
	if err != nil || len(history) != 1 {
		t.Fatalf("получить историю после ошибки доставки: messages=%+v err=%v", history, err)
	}
	if history[0].DeliveryStatus != "failed" || history[0].DeliveryErrorCode == nil ||
		*history[0].DeliveryErrorCode != "telegram_recipient_unavailable" {
		t.Fatalf("ошибка доставки не отображается в истории: %+v", history[0])
	}

	items, err := conversations.ListForCurator(ctx, fixture.curator1ID)
	if err != nil || len(items) != 1 || items[0].LastMessage == nil ||
		items[0].LastMessage.DeliveryStatus != "failed" ||
		items[0].LastMessage.DeliveryErrorCode == nil ||
		*items[0].LastMessage.DeliveryErrorCode != "telegram_recipient_unavailable" {
		t.Fatalf("список диалогов не показывает ошибку последнего сообщения: items=%+v err=%v", items, err)
	}
}
