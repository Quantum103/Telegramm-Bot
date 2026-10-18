package telegrambot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

type UpdateHandler interface {
	HandleUpdate(context.Context, Update) error
}

type Bot struct {
	api     BotAPI
	handler UpdateHandler
	logger  *slog.Logger
}

func NewBot(api BotAPI, handler UpdateHandler, logger *slog.Logger) *Bot {
	if logger == nil {
		logger = slog.Default()
	}
	return &Bot{api: api, handler: handler, logger: logger}
}

// Run проверяет токен и запускает единственный последовательный цикл long polling.
func (b *Bot) Run(ctx context.Context) error {
	startupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	me, err := b.api.GetMe(startupCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("проверка TELEGRAM_BOT_TOKEN через getMe: %w", err)
	}
	b.logger.Info("Telegram-бот авторизован, запускается long polling", "bot_username", me.Username)

	var offset int64
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		updates, err := b.api.GetUpdates(ctx, offset, 25)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return nil
			}
			var apiErr *APIError
			if errors.As(err, &apiErr) && (apiErr.Code == 409 || strings.Contains(strings.ToLower(apiErr.Description), "conflict")) {
				return fmt.Errorf("конфликт Telegram long polling: другой процесс использует этот токен; оставьте запущенным только один экземпляр бота: %w", err)
			}
			b.logger.Warn("ошибка получения обновлений Telegram; повторим запрос", "error", err, "retry_in", backoff)
			if !sleepContext(ctx, backoff) {
				return nil
			}
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = time.Second
		for _, update := range updates {
			if err := b.handler.HandleUpdate(ctx, update); err != nil {
				b.logger.Error("не удалось обработать обновление Telegram", "update_id", update.UpdateID, "error", err)
				if !sleepContext(ctx, backoff) {
					return nil
				}
				backoff = nextBackoff(backoff)
				break
			}
			offset = update.UpdateID + 1
		}
	}
}

func nextBackoff(current time.Duration) time.Duration {
	if current >= 30*time.Second {
		return 30 * time.Second
	}
	current *= 2
	if current > 30*time.Second {
		return 30 * time.Second
	}
	return current
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
