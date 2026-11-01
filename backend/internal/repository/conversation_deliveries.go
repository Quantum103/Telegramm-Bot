package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ConversationDeliveryRepository struct {
	pool *pgxpool.Pool
}

func NewConversationDeliveryRepository(pool *pgxpool.Pool) *ConversationDeliveryRepository {
	return &ConversationDeliveryRepository{pool: pool}
}

// ClaimDueDelivery захватывает задачу PostgreSQL-lease; после рестарта просроченный processing снова доступен.
func (r *ConversationDeliveryRepository) ClaimDueDelivery(ctx context.Context, lease time.Duration) (domain.ConversationDelivery, error) {
	if lease <= 0 {
		lease = time.Minute
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.ConversationDelivery{}, mapDatabaseError("начать захват задания Telegram outbox", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var deliveryID int64
	err = tx.QueryRow(ctx, `
		SELECT id FROM conversation_message_deliveries
		WHERE (delivery_status IN ('pending', 'retry') AND next_attempt_at <= now())
		   OR (delivery_status = 'processing' AND locked_at < now() - ($1::double precision * interval '1 second'))
		ORDER BY next_attempt_at ASC, id ASC
		FOR UPDATE SKIP LOCKED LIMIT 1
	`, lease.Seconds()).Scan(&deliveryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ConversationDelivery{}, ErrNotFound
	}
	if err != nil {
		return domain.ConversationDelivery{}, mapDatabaseError("выбрать задание Telegram outbox", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE conversation_message_deliveries
		SET delivery_status = 'processing', attempt_count = attempt_count + 1, locked_at = now(), updated_at = now()
		WHERE id = $1
	`, deliveryID); err != nil {
		return domain.ConversationDelivery{}, mapDatabaseError("захватить задание Telegram outbox", err)
	}
	var currentCuratorID, currentTelegramID pgtype.Int8
	var accountStatus pgtype.Text
	var hasCuratorRole bool
	err = tx.QueryRow(ctx, `
		SELECT delivery.id, delivery.message_id, delivery.channel,
		       COALESCE(delivery.recipient_chat_id, 0), COALESCE(delivery.recipient_staff_id, 0),
		       message.body_text, conversation.id, student.display_name,
		       current_assignment.curator_staff_id, staff.telegram_id, staff.account_status,
		       COALESCE(role_state.has_curator_role, FALSE)
		FROM conversation_message_deliveries delivery
		JOIN conversation_messages message ON message.id = delivery.message_id
		JOIN conversations conversation ON conversation.id = message.conversation_id
		JOIN students student ON student.id = conversation.student_id
		LEFT JOIN LATERAL (
			SELECT assignment.curator_staff_id FROM student_curator_assignments assignment
			WHERE assignment.student_id = conversation.student_id AND assignment.status = 'active'
			ORDER BY assignment.assigned_at DESC, assignment.id DESC LIMIT 1
		) current_assignment ON TRUE
		LEFT JOIN staff_accounts staff ON staff.id = current_assignment.curator_staff_id
		LEFT JOIN LATERAL (
			SELECT EXISTS (
				SELECT 1 FROM staff_role_assignments assignment
				JOIN staff_roles role ON role.id = assignment.role_id
				WHERE assignment.staff_id = staff.id AND assignment.revoked_at IS NULL AND role.role_code = 'curator'
			) AS has_curator_role
		) role_state ON TRUE
		WHERE delivery.id = $1
	`, deliveryID).Scan(&delivery.ID, &delivery.MessageID, &delivery.Channel,
		&delivery.RecipientChatID, &delivery.RecipientStaffID, &delivery.BodyText, &delivery.ConversationID,
		&delivery.StudentDisplayName, &currentCuratorID, &currentTelegramID, &accountStatus, &hasCuratorRole)
	if err != nil {
		return domain.ConversationDelivery{}, mapDatabaseError("прочитать захваченное задание Telegram outbox", err)
	}
	if delivery.Channel == "curator_telegram" {
		if currentCuratorID.Valid {
			delivery.RecipientStaffID = currentCuratorID.Int64
			if _, err := tx.Exec(ctx, "UPDATE conversation_message_deliveries SET recipient_staff_id = $2, updated_at = now() WHERE id = $1", delivery.ID, delivery.RecipientStaffID); err != nil {
				return domain.ConversationDelivery{}, mapDatabaseError("обновить текущего адресата уведомления", err)
			}
		} else {
			delivery.PreparationErrorCode = "curator_assignment_missing"
		}
		if delivery.PreparationErrorCode == "" && (!accountStatus.Valid || accountStatus.String != "active") {
			delivery.PreparationErrorCode = "curator_account_inactive"
		}
		if delivery.PreparationErrorCode == "" && !hasCuratorRole {
			delivery.PreparationErrorCode = "curator_role_inactive"
		}
		if delivery.PreparationErrorCode == "" && currentTelegramID.Valid {
			delivery.RecipientChatID = currentTelegramID.Int64
		}
		if delivery.PreparationErrorCode == "" && delivery.RecipientChatID <= 0 {
			delivery.PreparationErrorCode = "curator_telegram_not_linked"
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ConversationDelivery{}, mapDatabaseError("зафиксировать захват задания Telegram outbox", err)
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
		    last_error_code = NULL, last_error_at = NULL, updated_at = now()
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
	if delay > 24*time.Hour {
		delay = 24 * time.Hour
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE conversation_message_deliveries
		SET delivery_status = 'retry',
		    next_attempt_at = now() + ($3::double precision * interval '1 second'),
		    locked_at = NULL, last_error_code = $2, last_error_at = now(), updated_at = now()
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
		    last_error_code = $2, last_error_at = now(), updated_at = now()
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
