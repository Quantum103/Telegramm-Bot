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

type StaffTelegramRepository struct{ pool *pgxpool.Pool }

func NewStaffTelegramRepository(pool *pgxpool.Pool) *StaffTelegramRepository {
	return &StaffTelegramRepository{pool: pool}
}

func (r *StaffTelegramRepository) GetBindingStatus(ctx context.Context, staffID int64) (domain.StaffTelegramBindingStatus, error) {
	if staffID <= 0 {
		return domain.StaffTelegramBindingStatus{}, ErrInvalidInput
	}
	var telegramID pgtype.Int8
	var username pgtype.Text
	var active bool
	err := r.pool.QueryRow(ctx, `
		SELECT staff.telegram_id, staff.telegram_username,
		       staff.account_status = 'active' AND EXISTS (
		         SELECT 1 FROM staff_role_assignments assignment
		         JOIN staff_roles role ON role.id = assignment.role_id
		         WHERE assignment.staff_id = staff.id
		           AND assignment.revoked_at IS NULL AND role.role_code IN ('curator', 'administrator')
		       )
		FROM staff_accounts staff WHERE staff.id = $1
	`, staffID).Scan(&telegramID, &username, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StaffTelegramBindingStatus{}, ErrNotFound
	}
	if err != nil {
		return domain.StaffTelegramBindingStatus{}, mapDatabaseError("получить состояние Telegram-привязки", err)
	}
	status := domain.StaffTelegramBindingStatus{Linked: telegramID.Valid && active}
	if username.Valid && status.Linked {
		value := username.String
		status.TelegramUsername = &value
	}
	return status, nil
}

// IssueLinkToken хранит только SHA-256 токена; предыдущие неиспользованные коды отзываются.
func (r *StaffTelegramRepository) IssueLinkToken(ctx context.Context, staffID int64, tokenHash [32]byte, expiresAt time.Time) error {
	if staffID <= 0 || expiresAt.IsZero() || !expiresAt.After(time.Now()) {
		return ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapDatabaseError("начать создание Telegram-кода привязки", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var activeCurator bool
	err = tx.QueryRow(ctx, `
		SELECT staff.account_status = 'active' AND EXISTS (
			SELECT 1 FROM staff_role_assignments assignment
			JOIN staff_roles role ON role.id = assignment.role_id
			WHERE assignment.staff_id = staff.id
			  AND assignment.revoked_at IS NULL AND role.role_code IN ('curator', 'administrator')
		)
		FROM staff_accounts staff WHERE staff.id = $1 FOR UPDATE
	`, staffID).Scan(&activeCurator)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return mapDatabaseError("проверить сотрудника для Telegram-привязки", err)
	}
	if !activeCurator {
		return ErrInvalidState
	}
	if _, err := tx.Exec(ctx, "DELETE FROM staff_telegram_link_tokens WHERE staff_id = $1 AND consumed_at IS NULL", staffID); err != nil {
		return mapDatabaseError("отозвать предыдущие Telegram-коды привязки", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO staff_telegram_link_tokens (staff_id, token_hash, expires_at)
		VALUES ($1, $2, $3)
	`, staffID, tokenHash[:], expiresAt); err != nil {
		return mapDatabaseError("сохранить Telegram-код привязки", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return mapDatabaseError("зафиксировать Telegram-код привязки", err)
	}
	return nil
}

// BindTelegramAccount подтверждает владение Telegram ID одноразовым кодом из авторизованной веб-сессии.
func (r *StaffTelegramRepository) BindTelegramAccount(ctx context.Context, telegramID int64, username string, tokenHash [32]byte) (domain.StaffTelegramAccount, error) {
	if telegramID <= 0 {
		return domain.StaffTelegramAccount{}, ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.StaffTelegramAccount{}, mapDatabaseError("начать подтверждение Telegram-привязки", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var staffID int64
	err = tx.QueryRow(ctx, `
		SELECT staff_id FROM staff_telegram_link_tokens
		WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > now()
		FOR UPDATE
	`, tokenHash[:]).Scan(&staffID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StaffTelegramAccount{}, ErrNotFound
	}
	if err != nil {
		return domain.StaffTelegramAccount{}, mapDatabaseError("проверить одноразовый Telegram-код", err)
	}
	var account domain.StaffTelegramAccount
	err = tx.QueryRow(ctx, `
		SELECT staff.id, staff.display_name, staff.account_status,
		       EXISTS (
		         SELECT 1 FROM staff_role_assignments assignment
		         JOIN staff_roles role ON role.id = assignment.role_id
		         WHERE assignment.staff_id = staff.id
		           AND assignment.revoked_at IS NULL AND role.role_code = 'curator'
		       ),
		       EXISTS (
		         SELECT 1 FROM staff_role_assignments assignment
		         JOIN staff_roles role ON role.id = assignment.role_id
		         WHERE assignment.staff_id = staff.id
		           AND assignment.revoked_at IS NULL AND role.role_code = 'administrator'
		       )
		FROM staff_accounts staff WHERE staff.id = $1 FOR UPDATE
	`, staffID).Scan(&account.StaffID, &account.DisplayName, &account.AccountStatus, &account.IsCurator, &account.IsAdministrator)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StaffTelegramAccount{}, ErrNotFound
	}
	if err != nil {
		return domain.StaffTelegramAccount{}, mapDatabaseError("получить сотрудника для Telegram-привязки", err)
	}
	if account.AccountStatus != "active" || (!account.IsCurator && !account.IsAdministrator) {
		return domain.StaffTelegramAccount{}, ErrInvalidState
	}
	if _, err := tx.Exec(ctx, `
		UPDATE staff_accounts
		SET telegram_id = $2, telegram_username = NULLIF($3, ''), updated_at = now()
		WHERE id = $1
	`, staffID, telegramID, username); err != nil {
		return domain.StaffTelegramAccount{}, mapDatabaseError("сохранить Telegram ID сотрудника", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE staff_telegram_link_tokens SET consumed_at = now() WHERE token_hash = $1", tokenHash[:]); err != nil {
		return domain.StaffTelegramAccount{}, mapDatabaseError("погасить одноразовый Telegram-код", err)
	}
	if err := restoreStaffNotificationsTx(ctx, tx, staffID); err != nil {
		return domain.StaffTelegramAccount{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.StaffTelegramAccount{}, mapDatabaseError("зафиксировать Telegram-привязку", err)
	}
	return account, nil
}

func (r *StaffTelegramRepository) FindByTelegramID(ctx context.Context, telegramID int64) (domain.StaffTelegramAccount, error) {
	if telegramID <= 0 {
		return domain.StaffTelegramAccount{}, ErrInvalidInput
	}
	var account domain.StaffTelegramAccount
	err := r.pool.QueryRow(ctx, `
		SELECT staff.id, staff.display_name, staff.account_status,
		       EXISTS (
		         SELECT 1 FROM staff_role_assignments assignment
		         JOIN staff_roles role ON role.id = assignment.role_id
		         WHERE assignment.staff_id = staff.id
		           AND assignment.revoked_at IS NULL AND role.role_code = 'curator'
		       ),
		       EXISTS (
		         SELECT 1 FROM staff_role_assignments assignment
		         JOIN staff_roles role ON role.id = assignment.role_id
		         WHERE assignment.staff_id = staff.id
		           AND assignment.revoked_at IS NULL AND role.role_code = 'administrator'
		       )
		FROM staff_accounts staff WHERE staff.telegram_id = $1
	`, telegramID).Scan(&account.StaffID, &account.DisplayName, &account.AccountStatus, &account.IsCurator, &account.IsAdministrator)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StaffTelegramAccount{}, ErrNotFound
	}
	if err != nil {
		return domain.StaffTelegramAccount{}, mapDatabaseError("найти сотрудника по Telegram ID", err)
	}
	return account, nil
}

func restoreStaffNotificationsTx(ctx context.Context, tx pgx.Tx, staffID int64) error {
	// Restore curator notifications and reminders only while the employee remains the active assignee.
	if _, err := tx.Exec(ctx, `
		UPDATE conversation_message_deliveries delivery
		SET delivery_status = 'pending', recipient_staff_id = $1, attempt_count = 0,
		    next_attempt_at = now(), locked_at = NULL, last_error_code = NULL,
		    last_error_at = NULL, updated_at = now()
		WHERE delivery.channel IN ('curator_telegram', 'curator_transfer', 'curator_reminder_4h', 'curator_reminder_24h')
		  AND delivery.recipient_staff_id = $1 AND delivery.delivery_status = 'failed'
		  AND delivery.last_error_code IN ('curator_telegram_not_linked', 'telegram_recipient_unavailable',
		                                  'curator_account_inactive', 'curator_role_inactive',
		                                  'curator_assignment_missing')
		  AND EXISTS (
		    SELECT 1 FROM staff_accounts staff
		    JOIN staff_role_assignments assignment ON assignment.staff_id = staff.id AND assignment.revoked_at IS NULL
		    JOIN staff_roles role ON role.id = assignment.role_id AND role.role_code = 'curator'
		    WHERE staff.id = $1 AND staff.account_status = 'active' AND staff.telegram_id IS NOT NULL
		  )
		  AND EXISTS (
		    SELECT 1 FROM conversation_messages message
		    JOIN conversations conversation ON conversation.id = message.conversation_id
		    JOIN student_curator_assignments assignment
		      ON assignment.student_id = conversation.student_id
		     AND assignment.curator_staff_id = $1 AND assignment.status = 'active'
		    WHERE message.id = delivery.message_id
		  )
		  AND (
		    delivery.channel = 'curator_telegram'
		    OR EXISTS (
		      SELECT 1 FROM conversation_response_waits wait
		      WHERE wait.id = delivery.response_wait_id
		        AND wait.generation = delivery.response_wait_generation
		        AND wait.student_message_id = delivery.message_id AND wait.responded_at IS NULL
		    )
		  )
	`, staffID); err != nil {
		return mapDatabaseError("восстановить уведомления куратора", err)
	}
	// Deterministic administrator policy: the oldest active administrator with a linked Telegram receives escalations.
	if _, err := tx.Exec(ctx, `
		UPDATE conversation_message_deliveries delivery
		SET delivery_status = 'pending', recipient_staff_id = $1, attempt_count = 0,
		    next_attempt_at = now(), locked_at = NULL, last_error_code = NULL,
		    last_error_at = NULL, updated_at = now()
		WHERE delivery.channel = 'admin_escalation' AND delivery.delivery_status = 'failed'
		  AND delivery.last_error_code IN ('admin_telegram_not_linked', 'telegram_recipient_unavailable',
		                                  'curator_account_inactive', 'curator_role_inactive')
		  AND $1 = (
		    SELECT staff.id FROM staff_accounts staff
		    JOIN staff_role_assignments assignment ON assignment.staff_id = staff.id AND assignment.revoked_at IS NULL
		    JOIN staff_roles role ON role.id = assignment.role_id AND role.role_code = 'administrator'
		    WHERE staff.account_status = 'active' AND staff.telegram_id IS NOT NULL
		    ORDER BY staff.created_at ASC, staff.id ASC LIMIT 1
		  )
		  AND EXISTS (
		    SELECT 1 FROM conversation_response_waits wait
		    WHERE wait.id = delivery.response_wait_id
		      AND wait.generation = delivery.response_wait_generation
		      AND wait.student_message_id = delivery.message_id AND wait.responded_at IS NULL
		  )
	`, staffID); err != nil {
		return mapDatabaseError("восстановить эскалации администратора", err)
	}
	return nil
}

func (r *StaffTelegramRepository) RestoreNotifications(ctx context.Context, staffID int64) error {
	if staffID <= 0 {
		return ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapDatabaseError("начать восстановление Telegram-уведомлений", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := restoreStaffNotificationsTx(ctx, tx, staffID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return mapDatabaseError("зафиксировать восстановление Telegram-уведомлений", err)
	}
	return nil
}

func (r *StaffTelegramRepository) GetNotificationPreferences(ctx context.Context, staffID int64) (domain.StaffNotificationPreferences, error) {
	if staffID <= 0 {
		return domain.StaffNotificationPreferences{}, ErrInvalidInput
	}
	var allowed bool
	if err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM staff_accounts staff
			JOIN staff_role_assignments assignment ON assignment.staff_id = staff.id AND assignment.revoked_at IS NULL
			JOIN staff_roles role ON role.id = assignment.role_id
			WHERE staff.id = $1 AND staff.account_status = 'active'
			  AND role.role_code IN ('curator', 'administrator')
		)
	`, staffID).Scan(&allowed); err != nil {
		return domain.StaffNotificationPreferences{}, mapDatabaseError("проверить доступ к настройкам уведомлений", err)
	}
	if !allowed {
		return domain.StaffNotificationPreferences{}, ErrInvalidState
	}
	if _, err := r.pool.Exec(ctx, "INSERT INTO staff_notification_preferences (staff_id) VALUES ($1) ON CONFLICT (staff_id) DO NOTHING", staffID); err != nil {
		return domain.StaffNotificationPreferences{}, mapDatabaseError("создать настройки уведомлений по умолчанию", err)
	}
	var prefs domain.StaffNotificationPreferences
	if err := r.pool.QueryRow(ctx, `
		SELECT reminders_enabled, notifications_enabled, timezone, quiet_hours_enabled,
		       to_char(quiet_start, 'HH24:MI'), to_char(quiet_end, 'HH24:MI')
		FROM staff_notification_preferences WHERE staff_id = $1
	`, staffID).Scan(&prefs.RemindersEnabled, &prefs.NotificationsEnabled, &prefs.Timezone,
		&prefs.QuietHoursEnabled, &prefs.QuietStart, &prefs.QuietEnd); err != nil {
		return domain.StaffNotificationPreferences{}, mapDatabaseError("прочитать настройки уведомлений", err)
	}
	return prefs, nil
}

func (r *StaffTelegramRepository) UpdateNotificationPreferences(ctx context.Context, staffID int64, prefs domain.StaffNotificationPreferences) error {
	if staffID <= 0 {
		return ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapDatabaseError("начать изменение настроек уведомлений", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var allowed bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM staff_accounts staff
			JOIN staff_role_assignments assignment ON assignment.staff_id = staff.id AND assignment.revoked_at IS NULL
			JOIN staff_roles role ON role.id = assignment.role_id
			WHERE staff.id = $1 AND staff.account_status = 'active'
			  AND role.role_code IN ('curator', 'administrator')
		)
	`, staffID).Scan(&allowed); err != nil {
		return mapDatabaseError("проверить право менять настройки уведомлений", err)
	}
	if !allowed {
		return ErrInvalidState
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO staff_notification_preferences
			(staff_id, reminders_enabled, notifications_enabled, timezone, quiet_hours_enabled, quiet_start, quiet_end, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6::time, $7::time, now())
		ON CONFLICT (staff_id) DO UPDATE SET
			reminders_enabled = EXCLUDED.reminders_enabled,
			notifications_enabled = EXCLUDED.notifications_enabled,
			timezone = EXCLUDED.timezone,
			quiet_hours_enabled = EXCLUDED.quiet_hours_enabled,
			quiet_start = EXCLUDED.quiet_start,
			quiet_end = EXCLUDED.quiet_end,
			updated_at = now()
	`, staffID, prefs.RemindersEnabled, prefs.NotificationsEnabled, prefs.Timezone, prefs.QuietHoursEnabled, prefs.QuietStart, prefs.QuietEnd); err != nil {
		return mapDatabaseError("сохранить настройки уведомлений", err)
	}
	if prefs.NotificationsEnabled {
		// Wake only ordinary curator tasks that were deferred while notifications were paused.
		// Future reminder deadlines remain untouched.
		if _, err := tx.Exec(ctx, `
			UPDATE conversation_message_deliveries delivery
			SET next_attempt_at = now(), updated_at = now()
			WHERE delivery.recipient_staff_id = $1
			  AND delivery.delivery_status = 'pending'
			  AND (
			    delivery.channel IN ('curator_telegram', 'curator_transfer')
			    OR (
			      delivery.channel IN ('curator_reminder_4h', 'curator_reminder_24h')
			      AND EXISTS (
			        SELECT 1 FROM conversation_response_waits wait
			        WHERE wait.id = delivery.response_wait_id
			          AND wait.generation = delivery.response_wait_generation
			          AND wait.responded_at IS NULL
			          AND (
			            (delivery.channel = 'curator_reminder_4h' AND wait.first_reminder_at <= now())
			            OR
			            (delivery.channel = 'curator_reminder_24h' AND wait.second_reminder_at <= now())
			          )
			      )
			    )
			  )
		`, staffID); err != nil {
			return mapDatabaseError("возобновить отложенные уведомления куратора", err)
		}
	}
	if !prefs.RemindersEnabled {
		if _, err := tx.Exec(ctx, `
			UPDATE conversation_message_deliveries
			SET delivery_status = 'cancelled', locked_at = NULL, updated_at = now()
			WHERE channel IN ('curator_reminder_4h', 'curator_reminder_24h')
			  AND recipient_staff_id = $1 AND delivery_status IN ('pending', 'retry', 'processing')
		`, staffID); err != nil {
			return mapDatabaseError("отключить напоминания куратора", err)
		}
	} else {
		if _, err := tx.Exec(ctx, `
			INSERT INTO conversation_message_deliveries
				(message_id, channel, recipient_staff_id, delivery_status, next_attempt_at, response_wait_id, response_wait_generation)
			SELECT wait.student_message_id, task.channel, $1, 'pending', GREATEST(task.due_at, now()), wait.id, wait.generation
			FROM conversation_response_waits wait
			JOIN conversations conversation ON conversation.id = wait.conversation_id
			CROSS JOIN LATERAL (VALUES
				('curator_reminder_4h'::text, wait.first_reminder_at),
				('curator_reminder_24h'::text, wait.second_reminder_at)
			) AS task(channel, due_at)
			WHERE wait.curator_staff_id = $1 AND wait.responded_at IS NULL
			  AND conversation.assigned_curator_staff_id = $1 AND conversation.status = 'active'
			ON CONFLICT (message_id, channel) DO UPDATE SET
				recipient_staff_id = EXCLUDED.recipient_staff_id,
				delivery_status = 'pending', attempt_count = 0,
				next_attempt_at = EXCLUDED.next_attempt_at, locked_at = NULL,
				last_error_code = NULL, last_error_at = NULL, updated_at = now()
			WHERE conversation_message_deliveries.delivery_status = 'cancelled'
		`, staffID); err != nil {
			return mapDatabaseError("восстановить активные напоминания куратора", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return mapDatabaseError("зафиксировать настройки уведомлений", err)
	}
	return nil
}

func (r *StaffTelegramRepository) ListNotifications(ctx context.Context, staffID int64, limit int) ([]domain.CuratorNotification, error) {
	if staffID <= 0 {
		return nil, ErrInvalidInput
	}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	var authorized bool
	if err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM staff_accounts staff
			JOIN staff_role_assignments assignment ON assignment.staff_id = staff.id AND assignment.revoked_at IS NULL
			JOIN staff_roles role ON role.id = assignment.role_id AND role.role_code IN ('curator', 'administrator')
			WHERE staff.id = $1 AND staff.account_status = 'active'
		)
	`, staffID).Scan(&authorized); err != nil {
		return nil, mapDatabaseError("проверить право просмотра уведомлений", err)
	}
	if !authorized {
		return nil, ErrInvalidState
	}

	rows, err := r.pool.Query(ctx, `
		SELECT unread.id, unread.message_id, unread.conversation_id, unread.student_display_name,
		       unread.channel, unread.delivery_status, unread.attempt_count, unread.last_error_code,
		       unread.last_error_at, unread.sent_at, unread.created_at
		FROM (
			SELECT DISTINCT ON (message.id)
			       delivery.id, message.id AS message_id, conversation.id AS conversation_id,
			       student.display_name AS student_display_name, delivery.channel,
			       delivery.delivery_status, delivery.attempt_count, delivery.last_error_code,
			       delivery.last_error_at, delivery.sent_at, delivery.created_at
			FROM conversation_message_deliveries delivery
			JOIN conversation_messages message ON message.id = delivery.message_id
			JOIN conversations conversation ON conversation.id = message.conversation_id
			JOIN students student ON student.id = conversation.student_id
			WHERE delivery.recipient_staff_id = $1
			  AND (
				(
				  delivery.channel IN ('curator_telegram', 'curator_transfer')
				  AND EXISTS (
					SELECT 1
					FROM student_curator_assignments assignment
					JOIN conversation_participants participant
					  ON participant.conversation_id = conversation.id
					 AND participant.staff_id = $1 AND participant.left_at IS NULL
					WHERE assignment.student_id = conversation.student_id
					  AND assignment.curator_staff_id = $1 AND assignment.status = 'active'
					  AND conversation.assigned_curator_staff_id = $1
					  AND participant.id <> message.sender_participant_id
					  AND NOT EXISTS (
						SELECT 1 FROM conversation_message_reads read_mark
						WHERE read_mark.message_id = message.id
						  AND read_mark.participant_id = participant.id
						  AND read_mark.conversation_id = conversation.id
					  )
				  )
				)
				OR (
				  delivery.channel = 'admin_escalation'
				  AND EXISTS (
					SELECT 1 FROM staff_accounts staff
					JOIN staff_role_assignments assignment ON assignment.staff_id = staff.id AND assignment.revoked_at IS NULL
					JOIN staff_roles role ON role.id = assignment.role_id AND role.role_code = 'administrator'
					WHERE staff.id = $1 AND staff.account_status = 'active'
				  )
				  AND EXISTS (
					SELECT 1
					FROM student_curator_assignments assignment
					JOIN conversation_participants participant
					  ON participant.conversation_id = conversation.id
					 AND participant.staff_id = assignment.curator_staff_id
					 AND participant.left_at IS NULL
					WHERE assignment.student_id = conversation.student_id
					  AND assignment.curator_staff_id = conversation.assigned_curator_staff_id
					  AND assignment.status = 'active'
					  AND participant.id <> message.sender_participant_id
					  AND NOT EXISTS (
						SELECT 1 FROM conversation_message_reads read_mark
						WHERE read_mark.message_id = message.id
						  AND read_mark.participant_id = participant.id
						  AND read_mark.conversation_id = conversation.id
					  )
				  )
				)
			  )
			ORDER BY message.id,
			         CASE WHEN delivery.channel = 'curator_transfer' THEN 0 ELSE 1 END,
			         delivery.created_at DESC, delivery.id DESC
		) unread
		ORDER BY unread.created_at DESC, unread.id DESC
		LIMIT $2
	`, staffID, limit)
	if err != nil {
		return nil, mapDatabaseError("получить журнал непрочитанных уведомлений", err)
	}
	defer rows.Close()
	result := make([]domain.CuratorNotification, 0)
	for rows.Next() {
		var item domain.CuratorNotification
		var errorCode pgtype.Text
		var lastErrorAt, sentAt pgtype.Timestamptz
		if err := rows.Scan(&item.ID, &item.MessageID, &item.ConversationID, &item.StudentDisplayName, &item.Channel,
			&item.DeliveryStatus, &item.AttemptCount, &errorCode, &lastErrorAt, &sentAt, &item.CreatedAt); err != nil {
			return nil, mapDatabaseError("прочитать непрочитанное уведомление", err)
		}
		if errorCode.Valid {
			value := errorCode.String
			item.LastErrorCode = &value
		}
		if lastErrorAt.Valid {
			value := lastErrorAt.Time
			item.LastErrorAt = &value
		}
		if sentAt.Valid {
			value := sentAt.Time
			item.SentAt = &value
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("обойти журнал непрочитанных уведомлений", err)
	}
	return result, nil
}
var _ interface {
	GetBindingStatus(context.Context, int64) (domain.StaffTelegramBindingStatus, error)
	IssueLinkToken(context.Context, int64, [32]byte, time.Time) error
	BindTelegramAccount(context.Context, int64, string, [32]byte) (domain.StaffTelegramAccount, error)
	FindByTelegramID(context.Context, int64) (domain.StaffTelegramAccount, error)
	RestoreNotifications(context.Context, int64) error
	ListNotifications(context.Context, int64, int) ([]domain.CuratorNotification, error)
	GetNotificationPreferences(context.Context, int64) (domain.StaffNotificationPreferences, error)
	UpdateNotificationPreferences(context.Context, int64, domain.StaffNotificationPreferences) error
} = (*StaffTelegramRepository)(nil)
