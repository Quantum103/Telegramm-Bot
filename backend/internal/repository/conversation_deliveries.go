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

	var delivery domain.ConversationDelivery
	var currentCuratorID, currentTelegramID, adminStaffID, adminTelegramID pgtype.Int8
	var accountStatus pgtype.Text
	var hasCuratorRole, waitValid bool
	var waitOpenedAt, waitRespondedAt pgtype.Timestamptz
	var waitID, waitGeneration, waitMessageID pgtype.Int8
	var quietStart, quietEnd string
	var lastCuratorStatus pgtype.Text
	err = tx.QueryRow(ctx, `
		SELECT delivery.id, delivery.message_id, delivery.channel,
		       COALESCE(delivery.recipient_chat_id, 0), COALESCE(delivery.recipient_staff_id, 0),
		       delivery.attempt_count, COALESCE(message.body_text, ''), conversation.id, student.display_name,
		       EXISTS (
		           SELECT 1 FROM student_chat_modes screen_mode
		           WHERE screen_mode.student_id = conversation.student_id
		             AND screen_mode.lesson_mode_enabled = TRUE
		       ),
		       COALESCE((
		           SELECT sender_staff.display_name
		           FROM conversation_participants sender_participant
		           JOIN staff_accounts sender_staff ON sender_staff.id = sender_participant.staff_id
		           WHERE sender_participant.id = message.sender_participant_id
		             AND sender_participant.conversation_id = conversation.id
		       ), ''),
		       current_assignment.curator_staff_id, curator.telegram_id, curator.account_status,
		       COALESCE(curator_role.has_curator_role, FALSE), COALESCE(curator.display_name, ''),
		       admin.staff_id, admin.telegram_id,
		       wait.id, wait.generation, wait.student_message_id, wait.responded_at, wait.opened_at,
		       COALESCE(wait.id IS NOT NULL AND wait.generation = delivery.response_wait_generation
		           AND wait.student_message_id = delivery.message_id AND wait.responded_at IS NULL, FALSE),
		       COALESCE(preferences.reminders_enabled, TRUE),
		       COALESCE(preferences.notifications_enabled, TRUE),
		       COALESCE(preferences.timezone, 'UTC'),
		       COALESCE(preferences.quiet_hours_enabled, FALSE),
		       to_char(COALESCE(preferences.quiet_start, TIME '22:00'), 'HH24:MI'),
		       to_char(COALESCE(preferences.quiet_end, TIME '08:00'), 'HH24:MI'),
		       COALESCE(last_curator.delivery_status, '')
		FROM conversation_message_deliveries delivery
		JOIN conversation_messages message ON message.id = delivery.message_id
		JOIN conversations conversation ON conversation.id = message.conversation_id
		JOIN students student ON student.id = conversation.student_id
		LEFT JOIN conversation_response_waits wait ON wait.id = delivery.response_wait_id
		LEFT JOIN LATERAL (
			SELECT assignment.curator_staff_id FROM student_curator_assignments assignment
			WHERE assignment.student_id = conversation.student_id AND assignment.status = 'active'
			ORDER BY assignment.assigned_at DESC, assignment.id DESC LIMIT 1
		) current_assignment ON TRUE
		LEFT JOIN staff_accounts curator ON curator.id = current_assignment.curator_staff_id
		LEFT JOIN LATERAL (
			SELECT EXISTS (
				SELECT 1 FROM staff_role_assignments assignment
				JOIN staff_roles role ON role.id = assignment.role_id
				WHERE assignment.staff_id = curator.id AND assignment.revoked_at IS NULL AND role.role_code = 'curator'
			) AS has_curator_role
		) curator_role ON TRUE
		LEFT JOIN staff_notification_preferences preferences ON preferences.staff_id = current_assignment.curator_staff_id
		LEFT JOIN LATERAL (
			SELECT staff.id AS staff_id, staff.telegram_id
			FROM staff_accounts staff
			JOIN staff_role_assignments assignment ON assignment.staff_id = staff.id AND assignment.revoked_at IS NULL
			JOIN staff_roles role ON role.id = assignment.role_id AND role.role_code = 'administrator'
			WHERE staff.account_status = 'active' AND staff.telegram_id IS NOT NULL
			ORDER BY staff.created_at ASC, staff.id ASC LIMIT 1
		) admin ON TRUE
		LEFT JOIN LATERAL (
			SELECT status.delivery_status
			FROM conversation_message_deliveries status
			WHERE status.message_id = wait.student_message_id
			  AND status.channel IN ('curator_telegram', 'curator_transfer', 'curator_reminder_4h', 'curator_reminder_24h')
			ORDER BY status.updated_at DESC, status.id DESC LIMIT 1
		) last_curator ON TRUE
		WHERE delivery.id = $1
	`, deliveryID).Scan(
		&delivery.ID, &delivery.MessageID, &delivery.Channel,
		&delivery.RecipientChatID, &delivery.RecipientStaffID, &delivery.AttemptCount,
		&delivery.BodyText, &delivery.ConversationID, &delivery.StudentDisplayName, &delivery.StudentInLessonMode, &delivery.MessageSenderDisplayName,
		&currentCuratorID, &currentTelegramID, &accountStatus, &hasCuratorRole, &delivery.CuratorDisplayName,
		&adminStaffID, &adminTelegramID,
		&waitID, &waitGeneration, &waitMessageID, &waitRespondedAt, &waitOpenedAt, &waitValid,
		&delivery.ReminderEnabled, &delivery.NotificationsEnabled, &delivery.Timezone, &delivery.QuietHoursEnabled,
		&quietStart, &quietEnd, &lastCuratorStatus,
	)
	if err != nil {
		return domain.ConversationDelivery{}, mapDatabaseError("прочитать захваченное задание Telegram outbox", err)
	}
	delivery.QuietStart, delivery.QuietEnd = quietStart, quietEnd
	if waitID.Valid {
		delivery.ResponseWaitID = waitID.Int64
	}
	if waitGeneration.Valid {
		delivery.ResponseWaitGeneration = int(waitGeneration.Int64)
	}
	delivery.LastCuratorStatus = lastCuratorStatus.String
	if waitOpenedAt.Valid {
		delivery.WaitedFor = time.Since(waitOpenedAt.Time)
		if delivery.WaitedFor < 0 {
			delivery.WaitedFor = 0
		}
	}

	switch delivery.Channel {
	case "curator_telegram", "curator_transfer", "curator_reminder_4h", "curator_reminder_24h":
		if delivery.Channel != "curator_telegram" && !waitValid {
			if _, err := tx.Exec(ctx, "UPDATE conversation_message_deliveries SET delivery_status = 'cancelled', locked_at = NULL, updated_at = now() WHERE id = $1", delivery.ID); err != nil {
				return domain.ConversationDelivery{}, mapDatabaseError("отменить устаревшее напоминание", err)
			}
			if err := tx.Commit(ctx); err != nil {
				return domain.ConversationDelivery{}, mapDatabaseError("зафиксировать отмену устаревшего напоминания", err)
			}
			return domain.ConversationDelivery{}, ErrNotFound
		}
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
		if (delivery.Channel == "curator_reminder_4h" || delivery.Channel == "curator_reminder_24h") && !delivery.ReminderEnabled {
			if _, err := tx.Exec(ctx, "UPDATE conversation_message_deliveries SET delivery_status = 'cancelled', locked_at = NULL, updated_at = now() WHERE id = $1", delivery.ID); err != nil {
				return domain.ConversationDelivery{}, mapDatabaseError("отменить отключённое напоминание", err)
			}
			if err := tx.Commit(ctx); err != nil {
				return domain.ConversationDelivery{}, mapDatabaseError("зафиксировать отключение напоминания", err)
			}
			return domain.ConversationDelivery{}, ErrNotFound
		}
	case "admin_escalation":
		if !waitValid {
			if _, err := tx.Exec(ctx, "UPDATE conversation_message_deliveries SET delivery_status = 'cancelled', locked_at = NULL, updated_at = now() WHERE id = $1", delivery.ID); err != nil {
				return domain.ConversationDelivery{}, mapDatabaseError("отменить неактуальную эскалацию", err)
			}
			if err := tx.Commit(ctx); err != nil {
				return domain.ConversationDelivery{}, mapDatabaseError("зафиксировать отмену эскалации", err)
			}
			return domain.ConversationDelivery{}, ErrNotFound
		}
		if adminStaffID.Valid {
			delivery.RecipientStaffID = adminStaffID.Int64
			if adminTelegramID.Valid {
				delivery.RecipientChatID = adminTelegramID.Int64
			}
			if _, err := tx.Exec(ctx, "UPDATE conversation_message_deliveries SET recipient_staff_id = $2, updated_at = now() WHERE id = $1", delivery.ID, delivery.RecipientStaffID); err != nil {
				return domain.ConversationDelivery{}, mapDatabaseError("обновить адресата эскалации", err)
			}
		} else {
			delivery.PreparationErrorCode = "admin_telegram_not_linked"
		}
		if delivery.RecipientChatID <= 0 && delivery.PreparationErrorCode == "" {
			delivery.PreparationErrorCode = "admin_telegram_not_linked"
		}
	}
	delivery.DeliveryKind = delivery.Channel
	if err := tx.Commit(ctx); err != nil {
		return domain.ConversationDelivery{}, mapDatabaseError("зафиксировать захват задания Telegram outbox", err)
	}
	return delivery, nil
}

func (r *ConversationDeliveryRepository) CheckCuratorDelivery(ctx context.Context, deliveryID, staffID, telegramID int64) (domain.DeliveryAuthorization, error) {
	if deliveryID <= 0 || staffID <= 0 || telegramID <= 0 {
		return domain.DeliveryAuthorization{}, ErrInvalidInput
	}
	var result domain.DeliveryAuthorization
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM conversation_message_deliveries delivery
			JOIN conversation_messages message ON message.id = delivery.message_id
			JOIN conversations conversation ON conversation.id = message.conversation_id
			JOIN student_curator_assignments assignment
			  ON assignment.student_id = conversation.student_id
			 AND assignment.curator_staff_id = $2 AND assignment.status = 'active'
			JOIN staff_accounts staff ON staff.id = $2
			  AND staff.account_status = 'active' AND staff.telegram_id = $3
			JOIN staff_role_assignments role_assignment
			  ON role_assignment.staff_id = staff.id AND role_assignment.revoked_at IS NULL
			JOIN staff_roles role ON role.id = role_assignment.role_id AND role.role_code = 'curator'
			WHERE delivery.id = $1
			  AND delivery.channel IN ('curator_telegram', 'curator_transfer', 'curator_reminder_4h', 'curator_reminder_24h')
			  AND delivery.delivery_status = 'processing'
			  AND delivery.recipient_staff_id = $2
			  AND conversation.assigned_curator_staff_id = $2
			  AND (
			    delivery.channel = 'curator_telegram'
			    OR EXISTS (
			      SELECT 1 FROM conversation_response_waits wait
			      WHERE wait.id = delivery.response_wait_id
			        AND wait.generation = delivery.response_wait_generation
			        AND wait.student_message_id = delivery.message_id
			        AND wait.responded_at IS NULL
			    )
			  )
		),
		COALESCE(preferences.notifications_enabled, TRUE),
		COALESCE(preferences.reminders_enabled, TRUE),
		COALESCE(preferences.quiet_hours_enabled, FALSE),
		COALESCE(preferences.timezone, 'UTC'),
		to_char(COALESCE(preferences.quiet_start, TIME '22:00'), 'HH24:MI'),
		to_char(COALESCE(preferences.quiet_end, TIME '08:00'), 'HH24:MI')
		FROM conversation_message_deliveries delivery
		JOIN conversation_messages message ON message.id = delivery.message_id
		JOIN conversations conversation ON conversation.id = message.conversation_id
		LEFT JOIN student_curator_assignments current_assignment
		  ON current_assignment.student_id = conversation.student_id AND current_assignment.status = 'active'
		LEFT JOIN staff_notification_preferences preferences
		  ON preferences.staff_id = current_assignment.curator_staff_id
		WHERE delivery.id = $1
		LIMIT 1
	`, deliveryID, staffID, telegramID).Scan(
		&result.Current, &result.NotificationsEnabled, &result.RemindersEnabled,
		&result.QuietHoursEnabled, &result.Timezone, &result.QuietStart, &result.QuietEnd,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.DeliveryAuthorization{}, ErrNotFound
		}
		return domain.DeliveryAuthorization{}, mapDatabaseError("повторно проверить адресата Telegram-уведомления", err)
	}
	return result, nil
}

func (r *ConversationDeliveryRepository) IsCurrentCuratorDelivery(ctx context.Context, deliveryID, staffID, telegramID int64) (bool, error) {
	state, err := r.CheckCuratorDelivery(ctx, deliveryID, staffID, telegramID)
	return state.Current, err
}

func (r *ConversationDeliveryRepository) IsCurrentAdminEscalation(ctx context.Context, deliveryID, staffID, telegramID int64) (bool, error) {
	if deliveryID <= 0 || staffID <= 0 || telegramID <= 0 {
		return false, ErrInvalidInput
	}
	var current bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM conversation_message_deliveries delivery
			JOIN conversation_messages message ON message.id = delivery.message_id
			JOIN conversation_response_waits wait
			  ON wait.id = delivery.response_wait_id
			 AND wait.generation = delivery.response_wait_generation
			 AND wait.student_message_id = message.id
			 AND wait.responded_at IS NULL
			JOIN staff_accounts staff ON staff.id = $2
			  AND staff.account_status = 'active' AND staff.telegram_id = $3
			JOIN staff_role_assignments assignment
			  ON assignment.staff_id = staff.id AND assignment.revoked_at IS NULL
			JOIN staff_roles role ON role.id = assignment.role_id AND role.role_code = 'administrator'
			WHERE delivery.id = $1 AND delivery.channel = 'admin_escalation'
			  AND delivery.delivery_status = 'processing'
			  AND delivery.recipient_staff_id = $2
		)
	`, deliveryID, staffID, telegramID).Scan(&current)
	if err != nil {
		return false, mapDatabaseError("повторно проверить адресата административной эскалации", err)
	}
	return current, nil
}

func (r *ConversationDeliveryRepository) DeferDelivery(ctx context.Context, deliveryID int64, nextAttemptAt time.Time) error {
	if deliveryID <= 0 || nextAttemptAt.IsZero() {
		return ErrInvalidInput
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE conversation_message_deliveries
		SET delivery_status = 'pending', attempt_count = GREATEST(0, attempt_count - 1),
		    next_attempt_at = $2, locked_at = NULL, updated_at = now()
		WHERE id = $1 AND delivery_status = 'processing'
	`, deliveryID, nextAttemptAt)
	if err != nil {
		return mapDatabaseError("перенести Telegram-доставку на разрешённое время", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidState
	}
	return nil
}

func (r *ConversationDeliveryRepository) MarkDeliveryCancelled(ctx context.Context, deliveryID int64) error {
	if deliveryID <= 0 {
		return ErrInvalidInput
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE conversation_message_deliveries
		SET delivery_status = 'cancelled', locked_at = NULL, updated_at = now()
		WHERE id = $1 AND delivery_status IN ('pending', 'processing', 'retry')
	`, deliveryID)
	if err != nil {
		return mapDatabaseError("отменить неактуальную Telegram-доставку", err)
	}
	return nil
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
