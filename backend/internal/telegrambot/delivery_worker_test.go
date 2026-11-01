package telegrambot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type fakeConversationDeliveryQueue struct {
	sent             bool
	failed           bool
	retry            bool
	errorCode        string
	retryDelay       time.Duration
	stale            bool
	authorization    domain.DeliveryAuthorization
	authorizationSet bool
	adminCurrent     bool
	deferredUntil    time.Time
	cancelled        bool
}

func (f *fakeConversationDeliveryQueue) ClaimDueDelivery(context.Context, time.Duration) (domain.ConversationDelivery, error) {
	return domain.ConversationDelivery{}, domain.ErrNotFound
}
func (f *fakeConversationDeliveryQueue) MarkDeliverySent(context.Context, int64) error {
	f.sent = true
	return nil
}
func (f *fakeConversationDeliveryQueue) ScheduleDeliveryRetry(_ context.Context, _ int64, code string, delay time.Duration) error {
	f.retry, f.errorCode, f.retryDelay = true, code, delay
	return nil
}
func (f *fakeConversationDeliveryQueue) MarkDeliveryFailed(_ context.Context, _ int64, code string) error {
	f.failed, f.errorCode = true, code
	return nil
}
func (f *fakeConversationDeliveryQueue) CheckCuratorDelivery(context.Context, int64, int64, int64) (domain.DeliveryAuthorization, error) {
	if f.authorizationSet {
		return f.authorization, nil
	}
	return domain.DeliveryAuthorization{Current: !f.stale, NotificationsEnabled: true, RemindersEnabled: true}, nil
}
func (f *fakeConversationDeliveryQueue) IsCurrentAdminEscalation(context.Context, int64, int64, int64) (bool, error) {
	return f.adminCurrent, nil
}
func (f *fakeConversationDeliveryQueue) DeferDelivery(_ context.Context, _ int64, next time.Time) error {
	f.deferredUntil = next
	return nil
}
func (f *fakeConversationDeliveryQueue) MarkDeliveryCancelled(context.Context, int64) error {
	f.cancelled = true
	return nil
}

func TestQuietHoursMoveDeliveryAcrossMidnight(t *testing.T) {
	tests := []struct {
		name     string
		now      time.Time
		timezone string
		start    string
		end      string
		want     time.Time
	}{
		{
			name:     "same day end in Amsterdam",
			now:      time.Date(2026, 11, 1, 1, 30, 0, 0, time.UTC),
			timezone: "Europe/Amsterdam", start: "22:00", end: "08:00",
			want: time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC),
		},
		{
			name:     "after midnight quiet window crossing next day",
			now:      time.Date(2026, 11, 1, 23, 0, 0, 0, time.UTC),
			timezone: "Europe/Amsterdam", start: "22:00", end: "06:30",
			want: time.Date(2026, 11, 2, 5, 30, 0, 0, time.UTC),
		},
		{
			name:     "DST spring transition preserves local end",
			now:      time.Date(2026, 3, 29, 0, 30, 0, 0, time.UTC),
			timezone: "Europe/Amsterdam", start: "22:00", end: "07:00",
			want: time.Date(2026, 3, 29, 5, 0, 0, 0, time.UTC),
		},
		{
			name:     "DST autumn transition uses standard-time offset at end",
			now:      time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC),
			timezone: "Europe/Amsterdam", start: "22:00", end: "08:00",
			want: time.Date(2026, 10, 25, 7, 0, 0, 0, time.UTC),
		},
		{
			name:     "Tokyo local timezone",
			now:      time.Date(2026, 11, 1, 14, 0, 0, 0, time.UTC),
			timezone: "Asia/Tokyo", start: "22:00", end: "08:00",
			want: time.Date(2026, 11, 1, 23, 0, 0, 0, time.UTC),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := nextAllowedNotificationTime(tt.now, tt.timezone, tt.start, tt.end)
			if err != nil {
				t.Fatalf("nextAllowedNotificationTime: %v", err)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("next allowed = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestQuietHoursDoNotDeferOutsideWindow(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	got, err := nextAllowedNotificationTime(now, "UTC", "22:00", "08:00")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(now) {
		t.Fatalf("outside quiet hours should not defer: got %s", got)
	}
}

func TestDeliveryWorkerDefersWhenNotificationsArePaused(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	queue := &fakeConversationDeliveryQueue{
		authorization:    domain.DeliveryAuthorization{Current: true, NotificationsEnabled: false, RemindersEnabled: true},
		authorizationSet: true,
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":101}}`))
	}))
	defer server.Close()
	worker := NewDeliveryWorker(queue, newHTTPClient("fake-token", server.URL, server.Client()), nil)
	worker.now = func() time.Time { return now }
	worker.deliver(context.Background(), domain.ConversationDelivery{
		ID: 31, Channel: "curator_telegram", RecipientChatID: 1234, RecipientStaffID: 44,
		ConversationID: 55, AttemptCount: 1, NotificationsEnabled: true,
	})
	if calls != 0 || !queue.deferredUntil.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("paused notification should be deferred: calls=%d until=%s", calls, queue.deferredUntil)
	}
}

func TestDeliveryWorkerDefersImmediateNotificationDuringQuietHours(t *testing.T) {
	now := time.Date(2026, 11, 1, 1, 30, 0, 0, time.UTC)
	queue := &fakeConversationDeliveryQueue{authorization: domain.DeliveryAuthorization{
		Current: true, NotificationsEnabled: true, RemindersEnabled: true,
		QuietHoursEnabled: true, Timezone: "Europe/Amsterdam",
		QuietStart: "22:00", QuietEnd: "08:00",
	}, authorizationSet: true}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":105}}`))
	}))
	defer server.Close()
	worker := NewDeliveryWorker(queue, newHTTPClient("fake-token", server.URL, server.Client()), nil)
	worker.now = func() time.Time { return now }
	worker.deliver(context.Background(), domain.ConversationDelivery{
		ID: 37, Channel: "curator_telegram", RecipientChatID: 123456, RecipientStaffID: 44,
		ConversationID: 55, AttemptCount: 1, NotificationsEnabled: true,
		QuietHoursEnabled: false, Timezone: "UTC", QuietStart: "00:00", QuietEnd: "00:01",
	})
	want := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
	if calls != 0 || !queue.deferredUntil.Equal(want) {
		t.Fatalf("initial curator notification should defer until quiet hours end: calls=%d until=%s want=%s", calls, queue.deferredUntil, want)
	}
}

func TestDeliveryWorkerDefersBothReminderTypesUntilEndOfQuietHours(t *testing.T) {
	for _, channel := range []string{"curator_reminder_4h", "curator_reminder_24h"} {
		t.Run(channel, func(t *testing.T) {
			now := time.Date(2026, 11, 1, 1, 30, 0, 0, time.UTC)
			queue := &fakeConversationDeliveryQueue{authorization: domain.DeliveryAuthorization{
				Current: true, NotificationsEnabled: true, RemindersEnabled: true,
				QuietHoursEnabled: true, Timezone: "Europe/Amsterdam",
				QuietStart: "22:00", QuietEnd: "08:00",
			}, authorizationSet: true}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":102}}`))
			}))
			defer server.Close()
			worker := NewDeliveryWorker(queue, newHTTPClient("fake-token", server.URL, server.Client()), nil)
			worker.now = func() time.Time { return now }
			worker.deliver(context.Background(), domain.ConversationDelivery{
				ID: 32, Channel: channel, RecipientChatID: 1234, RecipientStaffID: 44,
				ConversationID: 55, AttemptCount: 1, ReminderEnabled: true, NotificationsEnabled: true,
				QuietHoursEnabled: false, Timezone: "UTC", QuietStart: "00:00", QuietEnd: "00:01",
			})
			want := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
			if calls != 0 || !queue.deferredUntil.Equal(want) {
				t.Fatalf("quiet-hours reminder should be deferred using fresh settings: calls=%d until=%s want=%s", calls, queue.deferredUntil, want)
			}
		})
	}
}

func TestDeliveryWorkerDoesNotDeferWhenFreshQuietHoursAreDisabled(t *testing.T) {
	now := time.Date(2026, 11, 1, 1, 30, 0, 0, time.UTC)
	queue := &fakeConversationDeliveryQueue{authorization: domain.DeliveryAuthorization{
		Current: true, NotificationsEnabled: true, RemindersEnabled: true,
		QuietHoursEnabled: false, Timezone: "UTC", QuietStart: "22:00", QuietEnd: "08:00",
	}, authorizationSet: true}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":106}}`))
	}))
	defer server.Close()
	worker := NewDeliveryWorker(queue, newHTTPClient("fake-token", server.URL, server.Client()), nil)
	worker.now = func() time.Time { return now }
	worker.deliver(context.Background(), domain.ConversationDelivery{
		ID: 38, Channel: "curator_telegram", RecipientChatID: 123456, RecipientStaffID: 44,
		ConversationID: 55, AttemptCount: 1, NotificationsEnabled: true,
		QuietHoursEnabled: true, Timezone: "Europe/Amsterdam", QuietStart: "22:00", QuietEnd: "08:00",
	})
	if calls != 1 || !queue.sent || !queue.deferredUntil.IsZero() {
		t.Fatalf("freshly disabled quiet hours must not defer: calls=%d queue=%+v", calls, queue)
	}
}

func TestDeliveryWorkerSendsOneAdminEscalationWithRequiredDetails(t *testing.T) {
	var got struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":103}}`))
	}))
	defer server.Close()
	queue := &fakeConversationDeliveryQueue{adminCurrent: true}
	worker := NewDeliveryWorker(queue, newHTTPClient("fake-token", server.URL, server.Client()), nil)
	worker.SetPanelURL("https://panel.example/")
	worker.deliver(context.Background(), domain.ConversationDelivery{
		ID: 33, MessageID: 77, Channel: "admin_escalation",
		RecipientChatID: 555, RecipientStaffID: 88, ConversationID: 99,
		StudentDisplayName: "Мария", CuratorDisplayName: "Иван",
		WaitedFor: 48 * time.Hour, LastCuratorStatus: "failed", AttemptCount: 1,
	})
	for _, value := range []string{"Диалог: №99", "Мария", "Иван", "48 ч", "failed"} {
		if !strings.Contains(got.Text, value) {
			t.Errorf("escalation lacks %q: %s", value, got.Text)
		}
	}
	if got.ChatID != 555 || !queue.sent || queue.retry || queue.failed {
		t.Fatalf("escalation not delivered once to selected admin: chat=%d queue=%+v", got.ChatID, queue)
	}
}

func TestDeliveryWorkerNotifiesNewCuratorAboutTransferredWait(t *testing.T) {
	var payload struct {
		ChatID      int64  `json:"chat_id"`
		Text        string `json:"text"`
		ReplyMarkup struct {
			InlineKeyboard [][]struct {
				Text string `json:"text"`
				URL  string `json:"url"`
			} `json:"inline_keyboard"`
		} `json:"reply_markup"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode transfer notification: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":104}}`))
	}))
	defer server.Close()
	queue := &fakeConversationDeliveryQueue{}
	worker := NewDeliveryWorker(queue, newHTTPClient("fake-token", server.URL, server.Client()), nil)
	worker.SetPanelURL("https://panel.example/")
	worker.deliver(context.Background(), domain.ConversationDelivery{
		ID: 36, MessageID: 78, Channel: "curator_transfer",
		RecipientChatID: 777, RecipientStaffID: 101, ConversationID: 102,
		StudentDisplayName: "Анна", NotificationsEnabled: true, AttemptCount: 1,
	})
	if payload.ChatID != 777 || !strings.Contains(payload.Text, "Вам передан") ||
		!strings.Contains(payload.Text, "Анна") {
		t.Fatalf("новый куратор не получил понятное уведомление о передаче: %+v", payload)
	}
	if !queue.sent || queue.failed || queue.retry {
		t.Fatalf("неверный статус доставки передачи: %+v", queue)
	}
	if len(payload.ReplyMarkup.InlineKeyboard) != 1 ||
		payload.ReplyMarkup.InlineKeyboard[0][0].URL != "https://panel.example/?conversation_id=102&page=dialogs" {
		t.Fatalf("уведомление о передаче должно вести к конкретному диалогу: %+v", payload.ReplyMarkup)
	}
}

func TestDeliveryWorkerMarksTelegramUnavailableReminderAsFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":403,"description":"bot was blocked by the user"}`))
	}))
	defer server.Close()
	queue := &fakeConversationDeliveryQueue{authorization: domain.DeliveryAuthorization{
		Current: true, NotificationsEnabled: true, RemindersEnabled: true,
	}, authorizationSet: true}
	worker := NewDeliveryWorker(queue, newHTTPClient("fake-token", server.URL, server.Client()), nil)
	worker.deliver(context.Background(), domain.ConversationDelivery{
		ID: 35, Channel: "curator_reminder_4h", RecipientChatID: 123456,
		RecipientStaffID: 99, ReminderEnabled: true, NotificationsEnabled: true,
		AttemptCount: 1,
	})
	if !queue.failed || queue.retry || queue.sent || queue.errorCode != "telegram_recipient_unavailable" {
		t.Fatalf("недоступный Telegram для напоминания должен завершиться постоянной ошибкой: %+v", queue)
	}
}

func TestDeliveryWorkerCancelsStaleReminderInsteadOfSending(t *testing.T) {
	queue := &fakeConversationDeliveryQueue{stale: true}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
	}))
	defer server.Close()
	worker := NewDeliveryWorker(queue, newHTTPClient("fake-token", server.URL, server.Client()), nil)
	worker.deliver(context.Background(), domain.ConversationDelivery{
		ID: 34, Channel: "curator_reminder_4h", RecipientChatID: 1234,
		RecipientStaffID: 44, AttemptCount: 1,
	})
	if calls != 0 || !queue.cancelled {
		t.Fatalf("stale reminder should be cancelled, calls=%d queue=%+v", calls, queue)
	}
}

func TestDeliveryWorkerUsesTelegramAPIAndMarksSuccessfulDelivery(t *testing.T) {
	var received struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botfake-token/sendMessage" {
			t.Errorf("unexpected Telegram endpoint: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode sendMessage request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":91}}`))
	}))
	defer server.Close()

	queue := &fakeConversationDeliveryQueue{}
	api := newHTTPClient("fake-token", server.URL, server.Client())
	NewDeliveryWorker(queue, api, nil).deliver(context.Background(), domain.ConversationDelivery{
		ID: 1, MessageID: 2, RecipientChatID: 123456, BodyText: "Ответ куратора", AttemptCount: 1,
	})
	if !queue.sent || queue.failed || queue.retry {
		t.Fatalf("неверный статус успешной отправки: %+v", queue)
	}
	if received.ChatID != 123456 || received.Text != "Ответ куратора" {
		t.Fatalf("неверное содержимое sendMessage: %+v", received)
	}
}

func TestDeliveryWorkerMarksBlockedBotAsPermanentFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":403,"description":"bot was blocked by the user"}`))
	}))
	defer server.Close()

	queue := &fakeConversationDeliveryQueue{}
	api := newHTTPClient("fake-token", server.URL, server.Client())
	NewDeliveryWorker(queue, api, nil).deliver(context.Background(), domain.ConversationDelivery{
		ID: 2, MessageID: 3, RecipientChatID: 123456, BodyText: "Ответ куратора", AttemptCount: 1,
	})
	if !queue.failed || queue.retry || queue.sent || queue.errorCode != "telegram_recipient_unavailable" {
		t.Fatalf("блокировка Telegram-бота не обработана как окончательная ошибка: %+v", queue)
	}
}

func TestDeliveryWorkerMarksMissingTelegramChatAsPermanentFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
	}))
	defer server.Close()

	queue := &fakeConversationDeliveryQueue{}
	api := newHTTPClient("fake-token", server.URL, server.Client())
	NewDeliveryWorker(queue, api, nil).deliver(context.Background(), domain.ConversationDelivery{
		ID: 20, MessageID: 30, RecipientChatID: 123456, BodyText: "Ответ куратора", AttemptCount: 1,
	})
	if !queue.failed || queue.retry || queue.sent || queue.errorCode != "telegram_recipient_unavailable" {
		t.Fatalf("отсутствующий/не начатый Telegram-чат не обработан как окончательная ошибка: %+v", queue)
	}
}

func TestDeliveryWorkerHonorsTelegramRateLimitRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":19}}`))
	}))
	defer server.Close()

	queue := &fakeConversationDeliveryQueue{}
	api := newHTTPClient("fake-token", server.URL, server.Client())
	NewDeliveryWorker(queue, api, nil).deliver(context.Background(), domain.ConversationDelivery{
		ID: 3, MessageID: 4, RecipientChatID: 123456, BodyText: "Ответ куратора", AttemptCount: 1,
	})
	if !queue.retry || queue.failed || queue.sent || queue.errorCode != "telegram_rate_limited" || queue.retryDelay != 19*time.Second {
		t.Fatalf("неверная обработка 429/retry_after: %+v", queue)
	}
}

func TestDeliveryWorkerRetriesTelegramServerErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"Internal Server Error"}`))
	}))
	defer server.Close()

	queue := &fakeConversationDeliveryQueue{}
	api := newHTTPClient("fake-token", server.URL, server.Client())
	NewDeliveryWorker(queue, api, nil).deliver(context.Background(), domain.ConversationDelivery{
		ID: 4, MessageID: 5, RecipientChatID: 123456, BodyText: "Ответ куратора", AttemptCount: 1,
	})
	if !queue.retry || queue.failed || queue.sent || queue.errorCode != "telegram_server_error" || queue.retryDelay != 5*time.Second {
		t.Fatalf("неверная обработка временной ошибки Telegram: %+v", queue)
	}
}

func TestDeliveryWorkerRetriesUnknownTransportResult(t *testing.T) {
	client := &http.Client{Transport: workerRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("simulated network timeout")
	})}
	queue := &fakeConversationDeliveryQueue{}
	api := newHTTPClient("fake-token", "http://telegram.test", client)
	NewDeliveryWorker(queue, api, nil).deliver(context.Background(), domain.ConversationDelivery{
		ID: 5, MessageID: 6, RecipientChatID: 123456, BodyText: "Ответ куратора", AttemptCount: 1,
	})
	if !queue.retry || queue.failed || queue.sent || queue.errorCode != "delivery_outcome_unknown" {
		t.Fatalf("неизвестный исход запроса не запланирован на повтор: %+v", queue)
	}
}

func TestCuratorNotificationMasksCommonSensitiveFragments(t *testing.T) {
	message := domain.ConversationDelivery{
		StudentDisplayName: "  Мария   Иванова  ",
		BodyText:           "Свяжитесь mail@example.com, сайт https://example.com/private, телефон +7 (999) 123-45-67, токен 123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi. Остальной текст нужен.",
	}
	got := formatCuratorNotification(message, false)
	for _, secret := range []string{"mail@example.com", "https://example.com/private", "+7 (999) 123-45-67", "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi"} {
		if strings.Contains(got, secret) {
			t.Errorf("уведомление содержит не замаскированные чувствительные данные %q: %s", secret, got)
		}
	}
	for _, masked := range []string{"[email скрыт]", "[ссылка скрыта]", "[номер скрыт]", "[токен скрыт]", "Остальной текст нужен."} {
		if !strings.Contains(got, masked) {
			t.Errorf("уведомление не содержит ожидаемый фрагмент %q: %s", masked, got)
		}
	}
}

func TestDeliveryWorkerSendsCuratorNotificationWithConversationButton(t *testing.T) {
	var payload struct {
		ChatID      int64  `json:"chat_id"`
		Text        string `json:"text"`
		ReplyMarkup struct {
			InlineKeyboard [][]struct {
				Text string `json:"text"`
				URL  string `json:"url"`
			} `json:"inline_keyboard"`
		} `json:"reply_markup"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode sendMessage request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":92}}`))
	}))
	defer server.Close()

	queue := &fakeConversationDeliveryQueue{}
	worker := NewDeliveryWorker(queue, newHTTPClient("fake-token", server.URL, server.Client()), nil)
	worker.SetPanelURL("https://panel.example/")
	worker.deliver(context.Background(), domain.ConversationDelivery{
		ID: 10, MessageID: 11, Channel: "curator_telegram", RecipientChatID: 123456,
		RecipientStaffID: 99, ConversationID: 321, StudentDisplayName: "Мария",
		BodyText: "Помогите с заданием", AttemptCount: 1,
	})
	if !queue.sent || queue.failed || queue.retry {
		t.Fatalf("уведомление не отмечено как доставленное: %+v", queue)
	}
	if payload.ChatID != 123456 || !strings.Contains(payload.Text, "Мария") ||
		!strings.Contains(payload.Text, "Помогите с заданием") {
		t.Fatalf("неверный текст уведомления: %+v", payload)
	}
	if len(payload.ReplyMarkup.InlineKeyboard) != 1 || len(payload.ReplyMarkup.InlineKeyboard[0]) != 1 {
		t.Fatalf("кнопка открытия диалога отсутствует: %+v", payload.ReplyMarkup)
	}
	button := payload.ReplyMarkup.InlineKeyboard[0][0]
	if button.Text != "Открыть диалог" || button.URL != "https://panel.example/?conversation_id=321&page=dialogs" {
		t.Fatalf("неверная ссылка на диалог: %+v", button)
	}
}

func TestDeliveryWorkerDoesNotSendWhenCuratorIsNotLinked(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":93}}`))
	}))
	defer server.Close()
	queue := &fakeConversationDeliveryQueue{}
	worker := NewDeliveryWorker(queue, newHTTPClient("fake-token", server.URL, server.Client()), nil)
	worker.deliver(context.Background(), domain.ConversationDelivery{
		ID: 12, MessageID: 13, Channel: "curator_telegram",
		PreparationErrorCode: "curator_telegram_not_linked", AttemptCount: 1,
	})
	if !queue.failed || queue.errorCode != "curator_telegram_not_linked" || calls != 0 {
		t.Fatalf("при отсутствии привязки Telegram не должен вызываться: queue=%+v calls=%d", queue, calls)
	}
}

type workerRoundTripFunc func(*http.Request) (*http.Response, error)

func (f workerRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestDeliveryWorkerIdentifiesCuratorAsMessageSender(t *testing.T) {
	var payload struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
		ReplyMarkup struct {
			InlineKeyboard [][]struct {
				Text string `json:"text"`
				CallbackData string `json:"callback_data"`
			} `json:"inline_keyboard"`
		} `json:"reply_markup"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode curator reply request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":94}}`))
	}))
	defer server.Close()

	queue := &fakeConversationDeliveryQueue{}
	worker := NewDeliveryWorker(queue, newHTTPClient("fake-token", server.URL, server.Client()), nil)
	worker.deliver(context.Background(), domain.ConversationDelivery{
		ID: 20, MessageID: 21, Channel: "telegram", RecipientChatID: 123456,
		MessageSenderDisplayName: "  Анна   Иванова\n", BodyText: "Добрый день! Чем могу помочь?",
		AttemptCount: 1,
	})
	if !queue.sent || queue.failed || queue.retry {
		t.Fatalf("ответ куратора не доставлен: %+v", queue)
	}
	if payload.ChatID != 123456 || payload.Text != "Сообщение от куратора — Анна Иванова:\n\nДобрый день! Чем могу помочь?" {
		t.Fatalf("ученик должен видеть явного автора сообщения: %+v", payload)
	}
	if len(payload.ReplyMarkup.InlineKeyboard) != 2 ||
		payload.ReplyMarkup.InlineKeyboard[0][0].CallbackData != "courses" ||
		payload.ReplyMarkup.InlineKeyboard[1][0].CallbackData != "chat:start" {
		t.Fatalf("после ответа куратора ученик должен получить кнопки навигации: %+v", payload.ReplyMarkup)
	}
}

func TestDeliveryWorkerDefersCuratorReplyUntilLessonIsCompleted(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	queue := &fakeConversationDeliveryQueue{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":95}}`))
	}))
	defer server.Close()
	worker := NewDeliveryWorker(queue, newHTTPClient("fake-token", server.URL, server.Client()), nil)
	worker.now = func() time.Time { return now }

	worker.deliver(context.Background(), domain.ConversationDelivery{
		ID: 21, MessageID: 22, Channel: "telegram", RecipientChatID: 123456,
		BodyText: "Ответ куратора", StudentLessonInProgress: true, AttemptCount: 1,
	})
	if calls != 0 || queue.sent || !queue.deferredUntil.Equal(now.Add(time.Minute)) {
		t.Fatalf("ответ нельзя отправлять во время урока: calls=%d sent=%v deferred=%s", calls, queue.sent, queue.deferredUntil)
	}

	queue.deferredUntil = time.Time{}
	worker.deliver(context.Background(), domain.ConversationDelivery{
		ID: 21, MessageID: 22, Channel: "telegram", RecipientChatID: 123456,
		BodyText: "Ответ куратора", StudentLessonInProgress: false, AttemptCount: 1,
	})
	if calls != 1 || !queue.sent {
		t.Fatalf("после завершения урока ответ должен быть доставлен: calls=%d queue=%+v", calls, queue)
	}
}
