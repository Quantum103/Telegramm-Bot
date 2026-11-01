package telegrambot

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

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
	CheckCuratorDelivery(context.Context, int64, int64, int64) (domain.DeliveryAuthorization, error)
	IsCurrentAdminEscalation(context.Context, int64, int64, int64) (bool, error)
	DeferDelivery(context.Context, int64, time.Time) error
	MarkDeliveryCancelled(context.Context, int64) error
}

// DeliveryWorker отправляет ответы куратора через уже используемый Telegram Bot API client.
type DeliveryWorker struct {
	queue    ConversationDeliveryQueue
	api      TelegramAPI
	logger   *slog.Logger
	panelURL string
	now      func() time.Time
}

func NewDeliveryWorker(queue ConversationDeliveryQueue, api TelegramAPI, logger *slog.Logger) *DeliveryWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &DeliveryWorker{queue: queue, api: api, logger: logger, now: time.Now}
}

func (w *DeliveryWorker) SetPanelURL(panelURL string) {
	parsed, err := url.Parse(strings.TrimSpace(panelURL))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		w.panelURL = ""
		return
	}
	w.panelURL = parsed.String()
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
	if delivery.PreparationErrorCode != "" {
		if err := w.queue.MarkDeliveryFailed(ctx, delivery.ID, delivery.PreparationErrorCode); err != nil {
			w.logger.Error("не удалось сохранить состояние Telegram-доставки", "delivery_id", delivery.ID, "staff_id", delivery.RecipientStaffID, "error", err)
		} else {
			w.logger.Warn("Telegram-доставка не отправлена", "delivery_id", delivery.ID, "staff_id", delivery.RecipientStaffID, "error_code", delivery.PreparationErrorCode)
		}
		return
	}

	now := time.Now
	if w.now != nil { now = w.now }
	var bodyText string
	var markup *InlineKeyboardMarkup
	switch delivery.Channel {
	case "curator_telegram", "curator_transfer", "curator_reminder_4h", "curator_reminder_24h":
		authorization, err := w.queue.CheckCuratorDelivery(ctx, delivery.ID, delivery.RecipientStaffID, delivery.RecipientChatID)
		if err != nil {
			if ctx.Err() == nil {
				if retryErr := w.queue.ScheduleDeliveryRetry(ctx, delivery.ID, "curator_authorization_check_failed", deliveryBackoff(delivery.AttemptCount)); retryErr != nil {
					w.logger.Error("не удалось повторить проверку адресата Telegram-уведомления", "delivery_id", delivery.ID, "error", retryErr)
				}
			}
			return
		}
		if !authorization.Current {
			if err := w.queue.MarkDeliveryCancelled(ctx, delivery.ID); err != nil {
				w.logger.Error("не удалось отменить неактуальное уведомление куратора", "delivery_id", delivery.ID, "error", err)
			}
			return
		}
		// Settings are fetched again after the lease was claimed so preference changes
		// made while the task was waiting take effect before the Telegram API call.
		delivery.NotificationsEnabled = authorization.NotificationsEnabled
		delivery.ReminderEnabled = authorization.RemindersEnabled
		delivery.QuietHoursEnabled = authorization.QuietHoursEnabled
		delivery.Timezone = authorization.Timezone
		delivery.QuietStart = authorization.QuietStart
		delivery.QuietEnd = authorization.QuietEnd
		if (delivery.Channel == "curator_reminder_4h" || delivery.Channel == "curator_reminder_24h") &&
			(!authorization.RemindersEnabled || !delivery.ReminderEnabled) {
			if err := w.queue.MarkDeliveryCancelled(ctx, delivery.ID); err != nil {
				w.logger.Error("не удалось отменить отключённое напоминание", "delivery_id", delivery.ID, "error", err)
			}
			return
		}
		if !delivery.NotificationsEnabled {
			if err := w.queue.DeferDelivery(ctx, delivery.ID, now().Add(15*time.Minute)); err != nil {
				w.logger.Error("не удалось перенести уведомление на время после паузы", "delivery_id", delivery.ID, "error", err)
			}
			return
		}
		if delivery.QuietHoursEnabled {
			nextAllowed, err := nextAllowedNotificationTime(now(), delivery.Timezone, delivery.QuietStart, delivery.QuietEnd)
			if err != nil {
				if markErr := w.queue.MarkDeliveryFailed(ctx, delivery.ID, "invalid_notification_timezone"); markErr != nil {
					w.logger.Error("не удалось сохранить ошибку часового пояса куратора", "delivery_id", delivery.ID, "error", markErr)
				}
				return
			}
			if nextAllowed.After(now()) {
				if err := w.queue.DeferDelivery(ctx, delivery.ID, nextAllowed); err != nil {
					w.logger.Error("не удалось перенести уведомление до окончания тихих часов", "delivery_id", delivery.ID, "error", err)
				}
				return
			}
		}
		switch delivery.Channel {
		case "curator_telegram":
			bodyText = formatCuratorNotification(delivery, w.conversationURL(delivery.ConversationID) != "")
		case "curator_transfer":
			bodyText = formatCuratorTransfer(delivery)
		default:
			bodyText = formatCuratorReminder(delivery)
		}
	case "admin_escalation":
		current, err := w.queue.IsCurrentAdminEscalation(ctx, delivery.ID, delivery.RecipientStaffID, delivery.RecipientChatID)
		if err != nil {
			if ctx.Err() == nil {
				if retryErr := w.queue.ScheduleDeliveryRetry(ctx, delivery.ID, "admin_authorization_check_failed", deliveryBackoff(delivery.AttemptCount)); retryErr != nil {
					w.logger.Error("не удалось повторить проверку администратора для эскалации", "delivery_id", delivery.ID, "error", retryErr)
				}
			}
			return
		}
		if !current {
			if err := w.queue.MarkDeliveryCancelled(ctx, delivery.ID); err != nil {
				w.logger.Error("не удалось отменить неактуальную эскалацию", "delivery_id", delivery.ID, "error", err)
			}
			return
		}
		bodyText = formatAdminEscalation(delivery)
	default:
		bodyText = delivery.BodyText
	}

	if conversationURL := w.conversationURL(delivery.ConversationID); conversationURL != "" &&
		(delivery.Channel == "curator_telegram" || delivery.Channel == "curator_transfer" ||
			delivery.Channel == "curator_reminder_4h" || delivery.Channel == "curator_reminder_24h" ||
			delivery.Channel == "admin_escalation") {
		markup = &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{
			{Text: "Открыть диалог", URL: conversationURL},
		}}}
	}

	var err error
	if markup != nil {
		_, err = w.api.SendMessageWithKeyboard(ctx, delivery.RecipientChatID, bodyText, markup)
	} else {
		err = w.api.SendMessage(ctx, delivery.RecipientChatID, bodyText)
	}
	if err == nil {
		if err := w.queue.MarkDeliverySent(ctx, delivery.ID); err != nil {
			// Telegram мог принять сообщение до ошибки PostgreSQL. После lease возможен повтор и дубль.
			w.logger.Error("Telegram принял сообщение, но статус доставки не сохранён", "delivery_id", delivery.ID, "error", err)
		}
		return
	}
	if ctx.Err() != nil { return }
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
	if retryAfter <= 0 { retryAfter = deliveryBackoff(delivery.AttemptCount) }
	if markErr := w.queue.ScheduleDeliveryRetry(ctx, delivery.ID, errorCode, retryAfter); markErr != nil {
		w.logger.Error("не удалось запланировать повтор Telegram-доставки", "delivery_id", delivery.ID, "error", markErr)
	}
}

func nextAllowedNotificationTime(now time.Time, timezone, quietStart, quietEnd string) (time.Time, error) {
	location, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil { return time.Time{}, err }
	start, err := time.Parse("15:04", quietStart)
	if err != nil { return time.Time{}, err }
	end, err := time.Parse("15:04", quietEnd)
	if err != nil { return time.Time{}, err }
	startMinute := start.Hour()*60 + start.Minute()
	endMinute := end.Hour()*60 + end.Minute()
	if startMinute == endMinute { return now, nil }
	localNow := now.In(location)
	currentMinute := localNow.Hour()*60 + localNow.Minute()
	inQuiet := false
	nextDay := false
	if startMinute < endMinute {
		inQuiet = currentMinute >= startMinute && currentMinute < endMinute
	} else {
		inQuiet = currentMinute >= startMinute || currentMinute < endMinute
		nextDay = currentMinute >= startMinute
	}
	if !inQuiet { return now, nil }
	year, month, day := localNow.Date()
	if nextDay {
		date := time.Date(year, month, day, 12, 0, 0, 0, location).AddDate(0, 0, 1)
		year, month, day = date.Date()
	}
	endLocal := time.Date(year, month, day, end.Hour(), end.Minute(), 0, 0, location)
	return endLocal.UTC(), nil
}

func formatCuratorTransfer(delivery domain.ConversationDelivery) string {
	name := strings.TrimSpace(delivery.StudentDisplayName)
	if name == "" { name = "Ученик" }
	return "Вам передан диалог с учеником " + name + " (№" +
		strconv.FormatInt(delivery.ConversationID, 10) +
		"). Последнее сообщение ещё ожидает ответа. Откройте диалог в веб-панели и продолжите общение."
}

func formatCuratorReminder(delivery domain.ConversationDelivery) string {
	kind := "Первое напоминание"
	if delivery.Channel == "curator_reminder_24h" { kind = "Повторное напоминание" }
	name := strings.TrimSpace(delivery.StudentDisplayName)
	if name == "" { name = "Ученик" }
	snippet := safeNotificationSnippet(delivery.BodyText)
	return kind + ": ученик " + name + " ожидает ответа в диалоге №" +
		strconv.FormatInt(delivery.ConversationID, 10) + ".\n\nПоследнее сообщение: «" + snippet +
		"»\n\nОтветьте в веб-панели, раздел «Диалоги»."
}

func formatAdminEscalation(delivery domain.ConversationDelivery) string {
	name := strings.TrimSpace(delivery.StudentDisplayName)
	if name == "" { name = "Ученик" }
	curator := strings.TrimSpace(delivery.CuratorDisplayName)
	if curator == "" { curator = "не назначен" }
	waitDuration := delivery.WaitedFor.Round(time.Minute)
	if waitDuration < 0 { waitDuration = 0 }
	waited := strconv.FormatInt(int64(waitDuration/time.Hour), 10) + " ч " +
		strconv.FormatInt(int64((waitDuration%time.Hour)/time.Minute), 10) + " мин"
	status := delivery.LastCuratorStatus
	if status == "" { status = "уведомление не отправлялось или статус неизвестен" }
	return "Эскалация: ученик ожидает ответа более установленного срока.\n\nДиалог: №" +
		strconv.FormatInt(delivery.ConversationID, 10) + "\nУченик: " + name +
		"\nОтветственный куратор: " + curator + "\nВремя ожидания: " + waited +
		"\nПоследний статус уведомления куратора: " + status +
		"\n\nПроверьте диалог в веб-панели."
}

func safeNotificationSnippet(value string) string {
	snippet := strings.Join(strings.Fields(value), " ")
	snippet = notificationTokenPattern.ReplaceAllString(snippet, "[токен скрыт]")
	snippet = notificationEmailPattern.ReplaceAllString(snippet, "[email скрыт]")
	snippet = notificationURLPattern.ReplaceAllString(snippet, "[ссылка скрыта]")
	snippet = notificationPhonePattern.ReplaceAllString(snippet, "[номер скрыт]")
	runes := []rune(snippet)
	if len(runes) > 160 { return string(runes[:160]) + "…" }
	if snippet == "" { return "Новое сообщение" }
	return snippet
}

func (w *DeliveryWorker) conversationURL(conversationID int64) string {
	if w.panelURL == "" || conversationID <= 0 {
		return ""
	}
	parsed, err := url.Parse(w.panelURL)
	if err != nil {
		return ""
	}
	query := parsed.Query()
	query.Set("page", "dialogs")
	query.Set("conversation_id", strconv.FormatInt(conversationID, 10))
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

var (
	notificationEmailPattern = regexp.MustCompile(`(?i)[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}`)
	notificationURLPattern   = regexp.MustCompile(`(?i)https?://[^\s]+`)
	notificationPhonePattern = regexp.MustCompile(`(?:\+|00)[0-9][0-9 ()-]{7,}[0-9]`)
	notificationTokenPattern = regexp.MustCompile(`[0-9]{5,}:[A-Za-z0-9_-]{20,}`)
)

func formatCuratorNotification(delivery domain.ConversationDelivery, hasButton bool) string {
	name := strings.Join(strings.Fields(delivery.StudentDisplayName), " ")
	if name == "" {
		name = "Ученик"
	}
	if runes := []rune(name); len(runes) > 64 {
		name = string(runes[:64]) + "…"
	}
	snippet := safeNotificationSnippet(delivery.BodyText)
	snippet = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, snippet)
	if runes := []rune(snippet); len(runes) > 160 {
		snippet = string(runes[:160]) + "…"
	}
	if snippet == "" {
		snippet = "Новое сообщение"
	}
	destination := "Ответьте в веб-панели: раздел «Диалоги»."
	if hasButton {
		destination = "Откройте переписку кнопкой ниже и ответьте ученику."
	}
	return "Новое сообщение от ученика: " + name + "\n\n«" + snippet + "»\n\n" + destination
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
