package repository

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

func TestStaffTelegramBindingUsesSingleUseTokenAndDetectsInactiveAccount(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, true)
	repo := NewStaffTelegramRepository(pool)
	telegramID := fixture.telegramID + 9000

	if _, err := repo.FindByTelegramID(ctx, telegramID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("непривязанный Telegram ID должен быть неизвестен: %v", err)
	}
	tokenHash := sha256.Sum256([]byte("stage4-single-use-token"))
	if err := repo.IssueLinkToken(ctx, fixture.curator1ID, tokenHash, time.Now().Add(5*time.Minute)); err != nil {
		t.Fatalf("выдать одноразовый токен: %v", err)
	}
	account, err := repo.BindTelegramAccount(ctx, telegramID, "curator_test", tokenHash)
	if err != nil {
		t.Fatalf("подтвердить Telegram-привязку: %v", err)
	}
	if account.StaffID != fixture.curator1ID || account.AccountStatus != "active" || !account.IsCurator {
		t.Fatalf("неверный сотрудник после привязки: %+v", account)
	}
	status, err := repo.GetBindingStatus(ctx, fixture.curator1ID)
	if err != nil || !status.Linked || status.TelegramUsername == nil || *status.TelegramUsername != "curator_test" {
		t.Fatalf("состояние привязки неверно: %+v err=%v", status, err)
	}
	if _, err := repo.BindTelegramAccount(ctx, telegramID, "curator_test", tokenHash); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("погашенный токен нельзя применить повторно: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE staff_accounts SET account_status = 'inactive' WHERE id = $1", fixture.curator1ID); err != nil {
		t.Fatalf("отключить учётную запись: %v", err)
	}
	found, err := repo.FindByTelegramID(ctx, telegramID)
	if err != nil || found.AccountStatus != "inactive" {
		t.Fatalf("репозиторий должен обнаружить отключённую учётную запись: %+v err=%v", found, err)
	}
	newHash := sha256.Sum256([]byte("stage4-inactive-token"))
	if err := repo.IssueLinkToken(ctx, fixture.curator1ID, newHash, time.Now().Add(5*time.Minute)); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("отключённому сотруднику нельзя выдать код привязки: %v", err)
	}
}

func TestAdministratorCanLinkTelegramForEscalations(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, true)
	repo := NewStaffTelegramRepository(pool)
	telegramID := fixture.telegramID + 9100
	tokenHash := sha256.Sum256([]byte("stage5-admin-single-use-token"))
	if err := repo.IssueLinkToken(ctx, fixture.adminID, tokenHash, time.Now().Add(5*time.Minute)); err != nil {
		t.Fatalf("администратору должен выдаваться код привязки Telegram: %v", err)
	}
	account, err := repo.BindTelegramAccount(ctx, telegramID, "admin_test", tokenHash)
	if err != nil {
		t.Fatalf("подтвердить Telegram-привязку администратора: %v", err)
	}
	if account.StaffID != fixture.adminID || !account.IsAdministrator || account.IsCurator {
		t.Fatalf("Telegram должен привязаться к роли администратора: %+v", account)
	}
	found, err := repo.FindByTelegramID(ctx, telegramID)
	if err != nil || found.StaffID != fixture.adminID || !found.IsAdministrator {
		t.Fatalf("привязка администратора не находится по Telegram ID: %+v err=%v", found, err)
	}
	status, err := repo.GetBindingStatus(ctx, fixture.adminID)
	if err != nil || !status.Linked {
		t.Fatalf("статус Telegram администратора должен быть linked: %+v err=%v", status, err)
	}
}

func TestListNotificationsReturnsOnlyUnreadMessages(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, true)
	conversations := NewConversationRepository(pool)

	message, err := conversations.CreateStudentMessage(ctx, domain.CreateStudentConversationMessageParams{
		StudentID: fixture.studentID, TelegramChatID: fixture.telegramID,
		TelegramMessageID: 778899, BodyText: "Непрочитанный вопрос ученика",
	})
	if err != nil {
		t.Fatalf("создать сообщение ученика: %v", err)
	}

	notifications := NewStaffTelegramRepository(pool)
	curatorItems, err := notifications.ListNotifications(ctx, fixture.curator1ID, 50)
	if err != nil {
		t.Fatalf("получить непрочитанные уведомления куратора: %v", err)
	}
	if len(curatorItems) != 1 || curatorItems[0].MessageID != message.ID ||
		(curatorItems[0].Channel != "curator_telegram" && curatorItems[0].Channel != "curator_transfer") {
		t.Fatalf("журнал должен показывать одно непрочитанное сообщение без строк напоминаний: %+v", curatorItems)
	}

	if tag, err := pool.Exec(ctx, `
		UPDATE conversation_message_deliveries
		SET recipient_staff_id = $2
		WHERE message_id = $1 AND channel = 'admin_escalation'
	`, message.ID, fixture.adminID); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("подготовить адресата эскалации для администратора: rows=%d err=%v", tag.RowsAffected(), err)
	}
	adminItems, err := notifications.ListNotifications(ctx, fixture.adminID, 50)
	if err != nil || len(adminItems) != 1 || adminItems[0].MessageID != message.ID {
		t.Fatalf("администратор должен видеть эскалацию по непрочитанному сообщению: items=%+v err=%v", adminItems, err)
	}

	if marked, err := conversations.MarkReadForCurator(ctx, fixture.curator1ID, message.ConversationID); err != nil || marked != 1 {
		t.Fatalf("отметить сообщение прочитанным: marked=%d err=%v", marked, err)
	}
	curatorItems, err = notifications.ListNotifications(ctx, fixture.curator1ID, 50)
	if err != nil || len(curatorItems) != 0 {
		t.Fatalf("прочитанное сообщение не должно оставаться в журнале куратора: items=%+v err=%v", curatorItems, err)
	}
	adminItems, err = notifications.ListNotifications(ctx, fixture.adminID, 50)
	if err != nil || len(adminItems) != 0 {
		t.Fatalf("эскалация по прочитанному сообщению не должна оставаться в журнале администратора: items=%+v err=%v", adminItems, err)
	}
}
