package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ConversationDeliveryRepository struct {
	pool *pgxpool.Pool
}

func NewConversationDeliveryRepository(pool *pgxpool.Pool) *ConversationDeliveryRepository {
	return &ConversationDeliveryRepository{pool: pool}
}

// ClaimDueDelivery атомарно захватывает одно задание; зависшие lease автоматически восстанавливаются.
func (r *ConversationDeliveryRepository) ClaimDueDelivery(ctx context.Context, lease time.Duration) (domain.ConversationDelivery, error) {
	if lease <= 0 {
		lease = time.Minute
	}
	var delivery domain.ConversationDelivery
	err := r.pool.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id
			FROM conversation_message_deliveries
			WHERE (
				delivery_status IN ('pending', 'retry') AND next_attempt_at <= now()
			) OR (
				delivery_status = 'processing'
				AND locked_at < now() - ($1::double precision * interval '1 second')
			)
			ORDER BY next_attempt_at ASC, id ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE conversation_message_deliveries delivery
		SET delivery_status = 'processing',
		    attempt_count = delivery.attempt_count + 1,
		    locked_at = now(),
		    updated_at = now()
		FROM candidate, conversation_messages message
		WHERE delivery.id = candidate.id AND message.id = delivery.message_id
		RETURNING delivery.id, delivery.message_id, delivery.recipient_chat_id,
		          message.body_text, delivery.attempt_count
	`, lease.Seconds()).Scan(
		&delivery.ID, &delivery.MessageID, &delivery.RecipientChatID,
		&delivery.BodyText, &delivery.AttemptCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ConversationDelivery{}, ErrNotFound
	}
	if err != nil {
		return domain.ConversationDelivery{}, mapDatabaseError("захватить задание Telegram outbox", err)
	}
	return delivery, nil
}

func (r *ConversationDeliveryRepository) MarkDeliverySent(ctx context.Context, deliveryID int64) error {
	if deliveryID <= 0 {
		return ErrInvalidInput
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE conversation_message_deliveries
		SET delivery_status = 'sent', sent_at = now(), locked_at = NULL,
		    last_error_code = NULL, updated_at = now()
		WHERE id = $1 AND delivery_status = 'processing'
	`, deliveryID)
	if err != nil {
		return mapDatabaseError("отметить Telegram-сообщение доставленным", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidState
	}
	return nil
}

func (r *ConversationDeliveryRepository) ScheduleDeliveryRetry(ctx context.Context, deliveryID int64, errorCode string, delay time.Duration) error {
	if deliveryID <= 0 || errorCode == "" {
		return ErrInvalidInput
	}
	if delay < time.Second {
		delay = time.Second
	}
	if delay > 24 * time.Hour {
		delay = 24*time.Hour
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE conversation_message_deliveries
		SET delivery_status = 'retry',
		    next_attempt_at = now() + ($3::double precision * interval '1 second'),
		    locked_at = NULL, last_error_code = $2, updated_at = now()
		WHERE id = $1 AND delivery_status = 'processing'
	`, deliveryID, errorCode, delay.Seconds())
	if err != nil {
		return mapDatabaseError("запланировать повтор Telegram-доставки", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidState
	}
	return nil
}

func (r *ConversationDeliveryRepository) MarkDeliveryFailed(ctx context.Context, deliveryID int64, errorCode string) error {
	if deliveryID <= 0 || errorCode == "" {
		return ErrInvalidInput
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE conversation_message_deliveries
		SET delivery_status = 'failed', locked_at = NULL,
		    last_error_code = $2, updated_at = now()
		WHERE id = $1 AND delivery_status = 'processing'
	`, deliveryID, errorCode)
	if err != nil {
		return mapDatabaseError("зафиксировать окончательную ошибку Telegram-доставки", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidState
	}
	return nil
}
