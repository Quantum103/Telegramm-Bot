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
	sent       bool
	failed     bool
	retry      bool
	errorCode  string
	retryDelay time.Duration
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
