package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEnsureByTelegramIDOnlyWritesWhenNeeded(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := NewStudentRepository(pool)
	telegramID := time.Now().UnixNano()
	username := "initial_user"
	preferredName := "Initial"
	params := EnsureStudentParams{
		TelegramID: telegramID, DisplayName: "Initial Display",
		TelegramUsername: &username, PreferredName: &preferredName,
	}
	student, err := repo.EnsureByTelegramID(ctx, params)
	if err != nil {
		t.Fatalf("первичная регистрация: %v", err)
	}

	// Повторный callback в пределах минуты не должен физически обновлять строку.
	const sentinel = "2000-01-01 00:00:00+00"
	if _, err := pool.Exec(ctx, `
		UPDATE students
		SET last_active_at = now(), updated_at = $2::timestamptz
		WHERE id = $1
	`, student.ID, sentinel); err != nil {
		t.Fatalf("подготовить строку для проверки повторной регистрации: %v", err)
	}
	before := readStudentWriteState(t, ctx, pool, student.ID)
	again, err := repo.EnsureByTelegramID(ctx, params)
	if err != nil {
		t.Fatalf("повторная регистрация: %v", err)
	}
	after := readStudentWriteState(t, ctx, pool, student.ID)
	if again.ID != student.ID {
		t.Fatalf("повторная регистрация изменила внутренний ID: before=%d after=%d", student.ID, again.ID)
	}
	if before.xmin != after.xmin || !before.updatedAt.Equal(after.updatedAt) ||
		!before.lastActiveAt.Equal(after.lastActiveAt) {
		t.Fatalf("повторная регистрация без изменений создала UPDATE: before=%+v after=%+v", before, after)
	}

	// Профиль обновляется при реальном изменении, но preferred_name сохраняет
	// прежнюю семантику: заполняется только пока он NULL.
	changedUsername := "changed_user"
	changed, err := repo.EnsureByTelegramID(ctx, EnsureStudentParams{
		TelegramID: telegramID, DisplayName: "Changed Display",
		TelegramUsername: &changedUsername, PreferredName: stringPointer("Changed Preferred"),
	})
	if err != nil {
		t.Fatalf("обновить изменившийся профиль: %v", err)
	}
	if changed.DisplayName != "Changed Display" || changed.TelegramUsername == nil ||
		*changed.TelegramUsername != changedUsername || changed.PreferredName == nil ||
		*changed.PreferredName != preferredName {
		t.Fatalf("изменения профиля применены неверно: %+v", changed)
	}
	if changed.LastActiveAt == nil || !changed.LastActiveAt.Equal(before.lastActiveAt) {
		t.Fatalf("изменение профиля преждевременно обновило last_active_at: before=%v after=%v", before.lastActiveAt, changed.LastActiveAt)
	}

	// Пока с последней активности прошло меньше минуты, повторный callback
	// не должен менять ни last_active_at, ни updated_at, ни xmin.
	if _, err := pool.Exec(ctx, `
		UPDATE students
		SET last_active_at = now() - interval '30 seconds', updated_at = $2::timestamptz
		WHERE id = $1
	`, student.ID, sentinel); err != nil {
		t.Fatalf("подготовить интервал менее минуты: %v", err)
	}
	before = readStudentWriteState(t, ctx, pool, student.ID)
	if _, err := repo.EnsureByTelegramID(ctx, EnsureStudentParams{
		TelegramID: telegramID, DisplayName: "Changed Display", TelegramUsername: &changedUsername,
	}); err != nil {
		t.Fatalf("проверить ограничение обновления активности: %v", err)
	}
	after = readStudentWriteState(t, ctx, pool, student.ID)
	if before.xmin != after.xmin || !before.updatedAt.Equal(after.updatedAt) ||
		!before.lastActiveAt.Equal(after.lastActiveAt) {
		t.Fatalf("last_active_at обновился раньше минуты: before=%+v after=%+v", before, after)
	}

	// После истечения минуты активность обновляется.
	if _, err := pool.Exec(ctx, `
		UPDATE students
		SET last_active_at = now() - interval '90 seconds', updated_at = $2::timestamptz
		WHERE id = $1
	`, student.ID, sentinel); err != nil {
		t.Fatalf("подготовить интервал более минуты: %v", err)
	}
	due, err := repo.EnsureByTelegramID(ctx, EnsureStudentParams{
		TelegramID: telegramID, DisplayName: "Changed Display", TelegramUsername: &changedUsername,
	})
	if err != nil {
		t.Fatalf("обновить время активности: %v", err)
	}
	if due.LastActiveAt == nil || time.Since(*due.LastActiveAt) > 5*time.Second {
		t.Fatalf("last_active_at не обновился после истечения минуты: %v", due.LastActiveAt)
	}
	if !due.UpdatedAt.After(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("updated_at не обновился вместе с last_active_at: %v", due.UpdatedAt)
	}

	// Допустимый inactive-аккаунт восстанавливается без преждевременного
	// обновления last_active_at.
	if _, err := pool.Exec(ctx, `
		UPDATE students
		SET account_status = 'inactive', last_active_at = now(),
		    updated_at = $2::timestamptz
		WHERE id = $1
	`, student.ID, sentinel); err != nil {
		t.Fatalf("перевести ученика в inactive для теста: %v", err)
	}
	inactiveBefore := readStudentWriteState(t, ctx, pool, student.ID)
	restored, err := repo.EnsureByTelegramID(ctx, EnsureStudentParams{
		TelegramID: telegramID, DisplayName: "Changed Display", TelegramUsername: &changedUsername,
	})
	if err != nil {
		t.Fatalf("восстановить inactive-аккаунт: %v", err)
	}
	if restored.AccountStatus != "active" {
		t.Fatalf("inactive-аккаунт не восстановлен: %+v", restored)
	}
	inactiveAfter := readStudentWriteState(t, ctx, pool, student.ID)
	if inactiveBefore.lastActiveAt.IsZero() || !inactiveBefore.lastActiveAt.Equal(inactiveAfter.lastActiveAt) {
		t.Fatalf("восстановление inactive без необходимости изменило last_active_at: before=%v after=%v", inactiveBefore.lastActiveAt, inactiveAfter.lastActiveAt)
	}

	// Заблокированный аккаунт нельзя восстановить или изменить через Ensure.
	if _, err := pool.Exec(ctx, `
		UPDATE students
		SET account_status = 'blocked', display_name = 'blocked-original'
		WHERE id = $1
	`, student.ID); err != nil {
		t.Fatalf("заблокировать ученика для теста: %v", err)
	}
	_, err = repo.EnsureByTelegramID(ctx, EnsureStudentParams{
		TelegramID: telegramID, DisplayName: "must-not-be-applied",
		TelegramUsername: stringPointer("must_not_be_applied"),
	})
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("для заблокированного аккаунта ожидалась ErrInvalidState, получено %v", err)
	}
	var status, displayNameStored string
	if err := pool.QueryRow(ctx, `
		SELECT account_status, display_name FROM students WHERE id = $1
	`, student.ID).Scan(&status, &displayNameStored); err != nil {
		t.Fatalf("проверить заблокированный аккаунт: %v", err)
	}
	if status != "blocked" || displayNameStored != "blocked-original" {
		t.Fatalf("Ensure изменил заблокированный аккаунт: status=%q display_name=%q", status, displayNameStored)
	}
}

func TestEnsureByTelegramIDConcurrentRegistrationIsAtomic(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := NewStudentRepository(pool)
	telegramID := time.Now().UnixNano()
	const workers = 8

	type result struct {
		student Student
		err     error
	}
	results := make(chan result, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			student, err := repo.EnsureByTelegramID(ctx, EnsureStudentParams{
				TelegramID: telegramID, DisplayName: fmt.Sprintf("parallel-%d", telegramID),
			})
			results <- result{student: student, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var studentID int64
	for result := range results {
		if result.err != nil {
			t.Fatalf("параллельная регистрация завершилась ошибкой: %v", result.err)
		}
		if studentID == 0 {
			studentID = result.student.ID
		}
		if result.student.ID != studentID {
			t.Fatalf("для одного Telegram ID созданы разные ученики: %d и %d", studentID, result.student.ID)
		}
	}

	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM students WHERE telegram_id = $1", telegramID).Scan(&count); err != nil {
		t.Fatalf("посчитать записи ученика: %v", err)
	}
	if count != 1 {
		t.Fatalf("для Telegram ID создано %d строк вместо одной", count)
	}
}

type studentWriteState struct {
	xmin         string
	updatedAt    time.Time
	lastActiveAt time.Time
}

func readStudentWriteState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, studentID int64) studentWriteState {
	t.Helper()
	var state studentWriteState
	if err := pool.QueryRow(ctx, `
		SELECT xmin::text, updated_at, last_active_at
		FROM students WHERE id = $1
	`, studentID).Scan(&state.xmin, &state.updatedAt, &state.lastActiveAt); err != nil {
		t.Fatalf("прочитать состояние строки ученика: %v", err)
	}
	return state
}

func stringPointer(value string) *string {
	return &value
}
