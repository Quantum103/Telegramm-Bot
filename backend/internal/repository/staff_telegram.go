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
		           AND assignment.revoked_at IS NULL AND role.role_code = 'curator'
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
			  AND assignment.revoked_at IS NULL AND role.role_code = 'curator'
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
		       )
		FROM staff_accounts staff WHERE staff.id = $1 FOR UPDATE
	`, staffID).Scan(&account.StaffID, &account.DisplayName, &account.AccountStatus, &account.IsCurator)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StaffTelegramAccount{}, ErrNotFound
	}
	if err != nil {
		return domain.StaffTelegramAccount{}, mapDatabaseError("получить сотрудника для Telegram-привязки", err)
	}
	if account.AccountStatus != "active" || !account.IsCurator {
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
	if _, err := tx.Exec(ctx, `
		UPDATE conversation_message_deliveries
		SET delivery_status = 'pending', attempt_count = 0, next_attempt_at = now(),
		    locked_at = NULL, last_error_code = NULL, last_error_at = NULL, updated_at = now()
		WHERE channel = 'curator_telegram' AND recipient_staff_id = $1
		  AND delivery_status = 'failed'
		  AND last_error_code IN ('curator_telegram_not_linked', 'telegram_recipient_unavailable',
		                          'curator_account_inactive', 'curator_role_inactive')
	`, staffID); err != nil {
		return domain.StaffTelegramAccount{}, mapDatabaseError("восстановить уведомления после Telegram-привязки", err)
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
		       )
		FROM staff_accounts staff WHERE staff.telegram_id = $1
	`, telegramID).Scan(&account.StaffID, &account.DisplayName, &account.AccountStatus, &account.IsCurator)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StaffTelegramAccount{}, ErrNotFound
	}
	if err != nil {
		return domain.StaffTelegramAccount{}, mapDatabaseError("найти сотрудника по Telegram ID", err)
	}
	return account, nil
}

func (r *StaffTelegramRepository) RestoreNotifications(ctx context.Context, staffID int64) error {
	if staffID <= 0 {
		return ErrInvalidInput
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE conversation_message_deliveries delivery
		SET delivery_status = 'pending', attempt_count = 0, next_attempt_at = now(),
		    locked_at = NULL, last_error_code = NULL, last_error_at = NULL, updated_at = now()
		WHERE delivery.channel = 'curator_telegram' AND delivery.recipient_staff_id = $1
		  AND delivery.delivery_status = 'failed'
		  AND delivery.last_error_code IN ('curator_telegram_not_linked', 'telegram_recipient_unavailable',
		                                  'curator_account_inactive', 'curator_role_inactive')
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
	`, staffID)
	if err != nil {
		return mapDatabaseError("восстановить незавершённые уведомления куратора", err)
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
			JOIN staff_roles role ON role.id = assignment.role_id AND role.role_code = 'curator'
			WHERE staff.id = $1 AND staff.account_status = 'active'
		)
	`, staffID).Scan(&authorized); err != nil {
		return nil, mapDatabaseError("проверить право просмотра уведомлений", err)
	}
	if !authorized {
		return nil, ErrInvalidState
	}
	rows, err := r.pool.Query(ctx, `
		SELECT delivery.id, message.id, conversation.id, student.display_name,
		       delivery.delivery_status, delivery.attempt_count, delivery.last_error_code,
		       delivery.last_error_at, delivery.sent_at, delivery.created_at
		FROM conversation_message_deliveries delivery
		JOIN conversation_messages message ON message.id = delivery.message_id
		JOIN conversations conversation ON conversation.id = message.conversation_id
		JOIN students student ON student.id = conversation.student_id
		WHERE delivery.channel = 'curator_telegram' AND delivery.recipient_staff_id = $1
		  AND EXISTS (
		    SELECT 1 FROM student_curator_assignments assignment
		    WHERE assignment.student_id = conversation.student_id
		      AND assignment.curator_staff_id = $1 AND assignment.status = 'active'
		  )
		ORDER BY delivery.created_at DESC, delivery.id DESC LIMIT $2
	`, staffID, limit)
	if err != nil {
		return nil, mapDatabaseError("получить журнал уведомлений куратора", err)
	}
	defer rows.Close()
	result := make([]domain.CuratorNotification, 0)
	for rows.Next() {
		var item domain.CuratorNotification
		var errorCode pgtype.Text
		var lastErrorAt, sentAt pgtype.Timestamptz
		if err := rows.Scan(&item.ID, &item.MessageID, &item.ConversationID, &item.StudentDisplayName,
			&item.DeliveryStatus, &item.AttemptCount, &errorCode, &lastErrorAt, &sentAt, &item.CreatedAt); err != nil {
			return nil, mapDatabaseError("прочитать уведомление куратора", err)
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
		return nil, mapDatabaseError("обойти журнал уведомлений куратора", err)
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
} = (*StaffTelegramRepository)(nil)
