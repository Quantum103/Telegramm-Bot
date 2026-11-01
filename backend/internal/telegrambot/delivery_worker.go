package telegrambot

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

const (
	deliveryPollInterval = time.Second
	deliveryLease        = 2 * time.Minute
	maxDeliveryAttempts  = 12
)

type ConversationDeliveryQueue interface {
	ClaimDueDelivery(context.Context, time.Duration) (domain.ConversationDelivery, error)
	MarkDeliverySent(context.Context, int64) error
	ScheduleDeliveryRetry(context.Context, int64, string, time.Duration) error
	MarkDeliveryFailed(context.Context, int64, string) error
}

// DeliveryWorker отправляет ответы куратора через уже используемый Telegram Bot API client.
type DeliveryWorker struct {
	queue  ConversationDeliveryQueue
	api    TelegramAPI
	logger *slog.Logger
}

func NewDeliveryWorker(queue ConversationDeliveryQueue, api TelegramAPI, logger *slog.Logger) *DeliveryWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &DeliveryWorker{queue: queue, api: api, logger: logger}
}

func (w *DeliveryWorker) Run(ctx context.Context) error {
	if w.queue == nil || w.api == nil {
		return errors.New("Telegram delivery worker is not configured")
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		delivery, err := w.queue.ClaimDueDelivery(ctx, deliveryLease)
		if errors.Is(err, domain.ErrNotFound) {
			if !wait(ctx, deliveryPollInterval) {
				return nil
			}
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			w.logger.Error("не удалось получить задание Telegram outbox", "error", err)
			if !wait(ctx, 2*time.Second) {
				return nil
			}
			continue
		}
		w.deliver(ctx, delivery)
	}
}

func (w *DeliveryWorker) deliver(ctx context.Context, delivery domain.ConversationDelivery) {
	if delivery.AttemptCount > maxDeliveryAttempts {
		if err := w.queue.MarkDeliveryFailed(ctx, delivery.ID, "retry_limit_exceeded"); err != nil {
			w.logger.Error("не удалось завершить задание после исчерпания попыток", "delivery_id", delivery.ID, "error", err)
		}
		return
	}

	err := w.api.SendMessage(ctx, delivery.RecipientChatID, delivery.BodyText)
	if err == nil {
		if err := w.queue.MarkDeliverySent(ctx, delivery.ID); err != nil {
			// Telegram мог принять сообщение до ошибки PostgreSQL. После lease возможен повтор и дубль.
			w.logger.Error("Telegram принял сообщение, но статус доставки не сохранён", "delivery_id", delivery.ID, "error", err)
		}
		return
	}
	if ctx.Err() != nil {
		return
	}

	errorCode, permanent, retryAfter := classifyDeliveryError(err)
	if permanent {
		if markErr := w.queue.MarkDeliveryFailed(ctx, delivery.ID, errorCode); markErr != nil {
			w.logger.Error("не удалось сохранить окончательную ошибку Telegram", "delivery_id", delivery.ID, "error", markErr)
		}
		w.logger.Warn("Telegram не принял сообщение; повторная доставка не планируется",
			"delivery_id", delivery.ID, "error_code", errorCode)
		return
	}
	if delivery.AttemptCount >= maxDeliveryAttempts {
		if markErr := w.queue.MarkDeliveryFailed(ctx, delivery.ID, "retry_limit_exceeded"); markErr != nil {
			w.logger.Error("не удалось сохранить исчерпание попыток Telegram", "delivery_id", delivery.ID, "error", markErr)
		}
		return
	}
	if retryAfter <= 0 {
		retryAfter = deliveryBackoff(delivery.AttemptCount)
	}
	if markErr := w.queue.ScheduleDeliveryRetry(ctx, delivery.ID, errorCode, retryAfter); markErr != nil {
		w.logger.Error("не удалось запланировать повтор Telegram-доставки", "delivery_id", delivery.ID, "error", markErr)
	}
}

func classifyDeliveryError(err error) (errorCode string, permanent bool, retryAfter time.Duration) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Code == http.StatusTooManyRequests:
			return "telegram_rate_limited", false, apiErr.RetryAfter
		case apiErr.Code == http.StatusForbidden:
			return "telegram_recipient_unavailable", true, 0
		case apiErr.Code == http.StatusBadRequest:
			description := strings.ToLower(apiErr.Description)
			switch {
			case strings.Contains(description, "blocked by the user"),
				strings.Contains(description, "chat not found"),
				strings.Contains(description, "user is deactivated"),
				strings.Contains(description, "can't initiate conversation"),
				strings.Contains(description, "bot was kicked"):
				return "telegram_recipient_unavailable", true, 0
			default:
				return "telegram_rejected_message", true, 0
			}
		case apiErr.Code >= 500:
			return "telegram_server_error", false, 0
		default:
			return "telegram_api_error", false, 0
		}
	}
	// При сетевом таймауте исход отправки неизвестен; повтор снижает риск потери,
	// но Bot API не предоставляет idempotency key для sendMessage.
	return "delivery_outcome_unknown", false, 0
}

func deliveryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := 5 * time.Second
	for i := 1; i < attempt && delay < 15*time.Minute; i++ {
		delay *= 2
	}
	if delay > 15*time.Minute {
		return 15 * time.Minute
	}
	return delay
}
