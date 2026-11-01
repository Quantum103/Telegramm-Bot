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
