package repository

import (
	"context"
	"errors"
	"strings"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ConversationRepository struct {
	pool *pgxpool.Pool
}

func NewConversationRepository(pool *pgxpool.Pool) *ConversationRepository {
	return &ConversationRepository{pool: pool}
}

// GetOrCreateForStudent сериализует создание через блокировку строки ученика.
func (r *ConversationRepository) GetOrCreateForStudent(ctx context.Context, studentID int64) (domain.Conversation, error) {
	if studentID <= 0 {
		return domain.Conversation{}, ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Conversation{}, mapDatabaseError("начать получение диалога", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := lockActiveStudent(ctx, tx, studentID); err != nil {
		return domain.Conversation{}, err
	}
	conversation, _, _, err := getOrCreateConversationTx(ctx, tx, studentID)
	if err != nil {
		return domain.Conversation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Conversation{}, mapDatabaseError("зафиксировать получение диалога", err)
	}
	return conversation, nil
}

func lockActiveStudent(ctx context.Context, tx pgx.Tx, studentID int64) (int64, error) {
	var telegramID int64
	var status string
	err := tx.QueryRow(ctx, `
		SELECT telegram_id, account_status FROM students WHERE id = $1 FOR UPDATE
	`, studentID).Scan(&telegramID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, mapDatabaseError("заблокировать ученика", err)
	}
	if status != "active" {
		return 0, ErrInvalidState
	}
	return telegramID, nil
}

func currentCuratorTx(ctx context.Context, tx pgx.Tx, studentID int64) (int64, error) {
	var curatorID int64
	err := tx.QueryRow(ctx, `
		SELECT assignment.curator_staff_id
		FROM student_curator_assignments assignment
		JOIN staff_accounts staff ON staff.id = assignment.curator_staff_id
		JOIN staff_role_assignments role_assignment
		  ON role_assignment.staff_id = staff.id AND role_assignment.revoked_at IS NULL
		JOIN staff_roles role
		  ON role.id = role_assignment.role_id AND role.role_code = 'curator'
		WHERE assignment.student_id = $1 AND assignment.status = 'active'
		  AND staff.account_status = 'active'
		ORDER BY assignment.assigned_at DESC, assignment.id DESC
		LIMIT 1 FOR UPDATE OF assignment
	`, studentID).Scan(&curatorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrInvalidState
	}
	if err != nil {
		return 0, mapDatabaseError("получить текущего куратора", err)
	}
	return curatorID, nil
}

func getOrCreateConversationTx(ctx context.Context, tx pgx.Tx, studentID int64) (domain.Conversation, int64, int64, error) {
	curatorID, err := currentCuratorTx(ctx, tx, studentID)
	if err != nil {
		return domain.Conversation{}, 0, 0, err
	}
	var conversation domain.Conversation
	err = tx.QueryRow(ctx, `
		SELECT id, student_id, COALESCE(assigned_curator_staff_id, 0), status, created_at, updated_at
		FROM conversations
		WHERE student_id = $1 AND status = 'active'
		ORDER BY created_at DESC, id DESC LIMIT 1 FOR UPDATE
	`, studentID).Scan(
		&conversation.ID, &conversation.StudentID, &conversation.AssignedCuratorStaffID,
		&conversation.Status, &conversation.CreatedAt, &conversation.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `
			INSERT INTO conversations (student_id, assigned_curator_staff_id, status)
			VALUES ($1, $2, 'active')
			RETURNING id, student_id, assigned_curator_staff_id, status, created_at, updated_at
		`, studentID, curatorID).Scan(
			&conversation.ID, &conversation.StudentID, &conversation.AssignedCuratorStaffID,
			&conversation.Status, &conversation.CreatedAt, &conversation.UpdatedAt,
		)
	}
	if err != nil {
		return domain.Conversation{}, 0, 0, mapDatabaseError("получить или создать диалог", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO conversation_participants (conversation_id, student_id)
		VALUES ($1, $2)
		ON CONFLICT (conversation_id, student_id) WHERE student_id IS NOT NULL DO NOTHING
	`, conversation.ID, studentID); err != nil {
		return domain.Conversation{}, 0, 0, mapDatabaseError("добавить ученика в диалог", err)
	}
	var studentParticipantID int64
	if err := tx.QueryRow(ctx, `
		SELECT id FROM conversation_participants
		WHERE conversation_id = $1 AND student_id = $2 AND left_at IS NULL
	`, conversation.ID, studentID).Scan(&studentParticipantID); err != nil {
		return domain.Conversation{}, 0, 0, mapDatabaseError("получить участника-ученика", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE conversation_participants SET left_at = now()
		WHERE conversation_id = $1 AND staff_id IS NOT NULL
		  AND left_at IS NULL AND staff_id <> $2
	`, conversation.ID, curatorID); err != nil {
		return domain.Conversation{}, 0, 0, mapDatabaseError("закрыть прежних участников-кураторов", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO conversation_participants (conversation_id, staff_id)
		VALUES ($1, $2)
		ON CONFLICT (conversation_id, staff_id)
		WHERE staff_id IS NOT NULL AND left_at IS NULL DO NOTHING
	`, conversation.ID, curatorID); err != nil {
		return domain.Conversation{}, 0, 0, mapDatabaseError("добавить текущего куратора", err)
	}
	var curatorParticipantID int64
	if err := tx.QueryRow(ctx, `
		SELECT id FROM conversation_participants
		WHERE conversation_id = $1 AND staff_id = $2 AND left_at IS NULL
	`, conversation.ID, curatorID).Scan(&curatorParticipantID); err != nil {
		return domain.Conversation{}, 0, 0, mapDatabaseError("получить участника-куратора", err)
	}
	if conversation.AssignedCuratorStaffID != curatorID {
		if _, err := tx.Exec(ctx, "UPDATE conversations SET assigned_curator_staff_id = $2, updated_at = now() WHERE id = $1", conversation.ID, curatorID); err != nil {
			return domain.Conversation{}, 0, 0, mapDatabaseError("синхронизировать куратора диалога", err)
		}
		conversation.AssignedCuratorStaffID = curatorID
	}
	return conversation, studentParticipantID, curatorParticipantID, nil
}

func (r *ConversationRepository) CreateStudentMessage(ctx context.Context, params domain.CreateStudentConversationMessageParams) (domain.ConversationMessage, error) {
	body := strings.TrimSpace(params.BodyText)
	if params.StudentID <= 0 || params.TelegramChatID <= 0 || params.TelegramMessageID <= 0 || body == "" {
		return domain.ConversationMessage{}, ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.ConversationMessage{}, mapDatabaseError("начать сохранение сообщения ученика", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	telegramID, err := lockActiveStudent(ctx, tx, params.StudentID)
	if err != nil {
		return domain.ConversationMessage{}, err
	}
	if telegramID != params.TelegramChatID {
		return domain.ConversationMessage{}, ErrInvalidState
	}
	conversation, studentParticipantID, _, err := getOrCreateConversationTx(ctx, tx, params.StudentID)
	if err != nil {
		return domain.ConversationMessage{}, err
	}
	var message domain.ConversationMessage
	message.SenderType, message.MessageType, message.BodyText = "student", "text", &body
	err = tx.QueryRow(ctx, `
		INSERT INTO conversation_messages
			(conversation_id, sender_participant_id, message_type, body_text, telegram_chat_id, telegram_message_id)
		VALUES ($1, $2, 'text', $3, $4, $5)
		ON CONFLICT (telegram_chat_id, telegram_message_id)
		WHERE telegram_chat_id IS NOT NULL AND telegram_message_id IS NOT NULL
		DO NOTHING
		RETURNING id, created_at
	`, conversation.ID, studentParticipantID, body, params.TelegramChatID, params.TelegramMessageID).Scan(&message.ID, &message.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		var existingConversationID, existingSenderID int64
		err = tx.QueryRow(ctx, `
			SELECT id, conversation_id, sender_participant_id, created_at
			FROM conversation_messages
			WHERE telegram_chat_id = $1 AND telegram_message_id = $2
		`, params.TelegramChatID, params.TelegramMessageID).Scan(&message.ID, &existingConversationID, &existingSenderID, &message.CreatedAt)
		if err != nil {
			return domain.ConversationMessage{}, mapDatabaseError("прочитать ранее сохранённое сообщение", err)
		}
		if existingConversationID != conversation.ID || existingSenderID != studentParticipantID {
			return domain.ConversationMessage{}, ErrConflict
		}
		message.Replayed = true
	} else if err != nil {
		return domain.ConversationMessage{}, mapDatabaseError("сохранить сообщение ученика", err)
	} else if _, err := tx.Exec(ctx, "UPDATE conversations SET updated_at = now() WHERE id = $1", conversation.ID); err != nil {
		return domain.ConversationMessage{}, mapDatabaseError("обновить активность диалога", err)
	}
	message.ConversationID, message.SenderParticipantID = conversation.ID, studentParticipantID
	if err := tx.Commit(ctx); err != nil {
		return domain.ConversationMessage{}, mapDatabaseError("зафиксировать сообщение ученика", err)
	}
	return message, nil
}

func (r *ConversationRepository) ListForCurator(ctx context.Context, curatorStaffID int64) ([]domain.ConversationSummary, error) {
	if curatorStaffID <= 0 {
		return nil, ErrInvalidInput
	}
	rows, err := r.pool.Query(ctx, `
		SELECT c.id, c.student_id, student.display_name, c.status, c.updated_at,
		       last_message.id, last_message.sender_participant_id,
		       CASE WHEN last_sender.student_id IS NOT NULL THEN 'student' ELSE 'curator' END,
		       last_message.message_type, last_message.body_text, last_message.created_at,
		       last_message.delivery_status, last_message.last_error_code,
		       (SELECT count(*) FROM conversation_messages unread_message
		        WHERE unread_message.conversation_id = c.id
		          AND unread_message.sender_participant_id <> participant.id
		          AND NOT EXISTS (
		            SELECT 1 FROM conversation_message_reads read_mark
		            WHERE read_mark.message_id = unread_message.id AND read_mark.participant_id = participant.id
		          ))
		FROM conversations c
		JOIN students student ON student.id = c.student_id
		JOIN conversation_participants participant
		  ON participant.conversation_id = c.id AND participant.staff_id = $1 AND participant.left_at IS NULL
		LEFT JOIN LATERAL (
			SELECT message.id, message.sender_participant_id, message.message_type,
			       message.body_text, message.created_at, delivery.delivery_status,
			       delivery.last_error_code
			FROM conversation_messages message
			LEFT JOIN conversation_message_deliveries delivery
			  ON delivery.message_id = message.id AND delivery.channel = 'telegram'
			WHERE message.conversation_id = c.id
			ORDER BY message.created_at DESC, message.id DESC LIMIT 1
		) last_message ON TRUE
		LEFT JOIN conversation_participants last_sender ON last_sender.id = last_message.sender_participant_id
		WHERE c.status = 'active' AND c.assigned_curator_staff_id = $1
		  AND EXISTS (
		    SELECT 1 FROM student_curator_assignments assignment
		    JOIN staff_accounts staff ON staff.id = assignment.curator_staff_id
		    JOIN staff_role_assignments ra ON ra.staff_id = staff.id AND ra.revoked_at IS NULL
		    JOIN staff_roles role ON role.id = ra.role_id AND role.role_code = 'curator'
		    WHERE assignment.student_id = c.student_id AND assignment.curator_staff_id = $1
		      AND assignment.status = 'active' AND staff.account_status = 'active'
		  )
		ORDER BY COALESCE(last_message.created_at, c.updated_at) DESC, c.id DESC
	`, curatorStaffID)
	if err != nil {
		return nil, mapDatabaseError("получить диалоги куратора", err)
	}
	defer rows.Close()
	result := make([]domain.ConversationSummary, 0)
	for rows.Next() {
		var item domain.ConversationSummary
		var lastID, lastSenderID pgtype.Int8
		var senderType, messageType, body, deliveryStatus, deliveryError pgtype.Text
		var lastAt pgtype.Timestamptz
		if err := rows.Scan(&item.ID, &item.StudentID, &item.StudentDisplayName, &item.Status, &item.LastActivityAt,
			&lastID, &lastSenderID, &senderType, &messageType, &body, &lastAt,
			&deliveryStatus, &deliveryError, &item.UnreadCount); err != nil {
			return nil, mapDatabaseError("прочитать диалог куратора", err)
		}
		if lastID.Valid {
			last := domain.ConversationMessage{
				ID: lastID.Int64, ConversationID: item.ID, SenderParticipantID: lastSenderID.Int64,
				SenderType: senderType.String, MessageType: messageType.String, CreatedAt: lastAt.Time,
			}
			if body.Valid {
				value := body.String
				last.BodyText = &value
			}
			if deliveryStatus.Valid {
				last.DeliveryStatus = deliveryStatus.String
			}
			if deliveryError.Valid {
				value := deliveryError.String
				last.DeliveryErrorCode = &value
			}
			item.LastMessage = &last
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("обойти диалоги куратора", err)
	}
	return result, nil
}

func (r *ConversationRepository) curatorParticipantTx(ctx context.Context, tx pgx.Tx, staffID, conversationID int64) (int64, error) {
	var studentID int64
	if err := tx.QueryRow(ctx, "SELECT student_id FROM conversations WHERE id = $1", conversationID).Scan(&studentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, mapDatabaseError("найти диалог", err)
	}
	if err := tx.QueryRow(ctx, "SELECT id FROM students WHERE id = $1 FOR UPDATE", studentID).Scan(&studentID); err != nil {
		return 0, mapDatabaseError("заблокировать ученика перед доступом к диалогу", err)
	}
	var participantID int64
	err := tx.QueryRow(ctx, `
		SELECT participant.id
		FROM conversations c
		JOIN student_curator_assignments assignment
		  ON assignment.student_id = c.student_id AND assignment.curator_staff_id = $2 AND assignment.status = 'active'
		JOIN staff_accounts staff ON staff.id = assignment.curator_staff_id AND staff.account_status = 'active'
		JOIN staff_role_assignments ra ON ra.staff_id = staff.id AND ra.revoked_at IS NULL
		JOIN staff_roles role ON role.id = ra.role_id AND role.role_code = 'curator'
		JOIN conversation_participants participant
		  ON participant.conversation_id = c.id AND participant.staff_id = $2 AND participant.left_at IS NULL
		WHERE c.id = $1 AND c.student_id = $3
		  AND c.status = 'active' AND c.assigned_curator_staff_id = $2
		FOR UPDATE OF c, assignment
	`, conversationID, staffID, studentID).Scan(&participantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, mapDatabaseError("проверить текущий доступ к диалогу", err)
	}
	return participantID, nil
}

func (r *ConversationRepository) ListMessagesForCurator(ctx context.Context, staffID, conversationID, beforeID int64, limit int) ([]domain.ConversationMessage, error) {
	if staffID <= 0 || conversationID <= 0 || beforeID < 0 {
		return nil, ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, mapDatabaseError("начать чтение истории куратора", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := r.curatorParticipantTx(ctx, tx, staffID, conversationID); err != nil {
		return nil, err
	}
	messages, err := listConversationMessagesTx(ctx, tx, conversationID, beforeID, normalizeConversationLimit(limit))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, mapDatabaseError("зафиксировать чтение истории куратора", err)
	}
	return messages, nil
}

func (r *ConversationRepository) ListMessagesForStudent(ctx context.Context, studentID, conversationID, beforeID int64, limit int) ([]domain.ConversationMessage, error) {
	if studentID <= 0 || conversationID <= 0 || beforeID < 0 {
		return nil, ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, mapDatabaseError("начать чтение истории ученика", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var participantID int64
	err = tx.QueryRow(ctx, `
		SELECT participant.id
		FROM conversation_participants participant
		JOIN conversations c ON c.id = participant.conversation_id
		WHERE c.id = $1 AND c.student_id = $2 AND participant.student_id = $2
	`, conversationID, studentID).Scan(&participantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, mapDatabaseError("проверить владельца истории", err)
	}
	messages, err := listConversationMessagesTx(ctx, tx, conversationID, beforeID, normalizeConversationLimit(limit))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, mapDatabaseError("зафиксировать чтение истории ученика", err)
	}
	_ = participantID
	return messages, nil
}

func listConversationMessagesTx(ctx context.Context, tx pgx.Tx, conversationID, beforeID int64, limit int) ([]domain.ConversationMessage, error) {
	rows, err := tx.Query(ctx, `
		SELECT message.id, message.conversation_id, message.sender_participant_id,
		       CASE WHEN sender.student_id IS NOT NULL THEN 'student' ELSE 'curator' END,
		       message.message_type, message.body_text, message.created_at,
		       COALESCE(delivery.delivery_status, ''),
		       NULLIF(delivery.last_error_code, '')
		FROM conversation_messages message
		JOIN conversation_participants sender ON sender.id = message.sender_participant_id
		LEFT JOIN conversation_message_deliveries delivery
		  ON delivery.message_id = message.id AND delivery.channel = 'telegram'
		WHERE message.conversation_id = $1
		  AND ($3 = 0 OR (message.created_at, message.id) < (
		    SELECT cursor_message.created_at, cursor_message.id
		    FROM conversation_messages cursor_message
		    WHERE cursor_message.id = $3 AND cursor_message.conversation_id = $1
		  ))
		ORDER BY message.created_at DESC, message.id DESC
		LIMIT $2
	`, conversationID, limit, beforeID)
	if err != nil {
		return nil, mapDatabaseError("получить историю сообщений", err)
	}
	defer rows.Close()
	result := make([]domain.ConversationMessage, 0)
	for rows.Next() {
		var message domain.ConversationMessage
		var body, deliveryStatus, deliveryError pgtype.Text
		if err := rows.Scan(&message.ID, &message.ConversationID, &message.SenderParticipantID,
			&message.SenderType, &message.MessageType, &body, &message.CreatedAt,
			&deliveryStatus, &deliveryError); err != nil {
			return nil, mapDatabaseError("прочитать сообщение", err)
		}
		if body.Valid {
			value := body.String
			message.BodyText = &value
		}
		if deliveryStatus.Valid {
			message.DeliveryStatus = deliveryStatus.String
		}
		if deliveryError.Valid {
			value := deliveryError.String
			message.DeliveryErrorCode = &value
		}
		result = append(result, message)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("обойти историю сообщений", err)
	}
	return result, nil
}

func (r *ConversationRepository) SendCuratorMessage(ctx context.Context, staffID, conversationID int64, bodyText string) (domain.ConversationMessage, error) {
	body := strings.TrimSpace(bodyText)
	if staffID <= 0 || conversationID <= 0 || body == "" {
		return domain.ConversationMessage{}, ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.ConversationMessage{}, mapDatabaseError("начать отправку ответа куратора", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	participantID, err := r.curatorParticipantTx(ctx, tx, staffID, conversationID)
	if err != nil {
		return domain.ConversationMessage{}, err
	}
	var message domain.ConversationMessage
	err = tx.QueryRow(ctx, `
		INSERT INTO conversation_messages (conversation_id, sender_participant_id, message_type, body_text)
		VALUES ($1, $2, 'text', $3) RETURNING id, created_at
	`, conversationID, participantID, body).Scan(&message.ID, &message.CreatedAt)
	if err != nil {
		return domain.ConversationMessage{}, mapDatabaseError("сохранить ответ куратора", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE conversations SET updated_at = now() WHERE id = $1", conversationID); err != nil {
		return domain.ConversationMessage{}, mapDatabaseError("обновить активность диалога", err)
	}

	var recipientChatID int64
	if err := tx.QueryRow(ctx, `
		SELECT student.telegram_id
		FROM conversations c
		JOIN students student ON student.id = c.student_id
		WHERE c.id = $1
	`, conversationID).Scan(&recipientChatID); err != nil {
		return domain.ConversationMessage{}, mapDatabaseError("получить Telegram chat ID получателя", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO conversation_message_deliveries
			(message_id, channel, recipient_chat_id, delivery_status, next_attempt_at)
		VALUES ($1, 'telegram', $2, 'pending', now())
	`, message.ID, recipientChatID); err != nil {
		return domain.ConversationMessage{}, mapDatabaseError("поставить ответ куратора в очередь доставки", err)
	}
	message.ConversationID, message.SenderParticipantID = conversationID, participantID
	message.SenderType, message.MessageType, message.BodyText = "curator", "text", &body
	message.DeliveryStatus = "pending"
	if err := tx.Commit(ctx); err != nil {
		return domain.ConversationMessage{}, mapDatabaseError("зафиксировать ответ куратора", err)
	}
	return message, nil
}

func (r *ConversationRepository) MarkReadForCurator(ctx context.Context, staffID, conversationID int64) (int64, error) {
	if staffID <= 0 || conversationID <= 0 {
		return 0, ErrInvalidInput
	}
	return r.markRead(ctx, conversationID, func(ctx context.Context, tx pgx.Tx) (int64, error) {
		return r.curatorParticipantTx(ctx, tx, staffID, conversationID)
	})
}

func (r *ConversationRepository) MarkReadForStudent(ctx context.Context, studentID, conversationID int64) (int64, error) {
	if studentID <= 0 || conversationID <= 0 {
		return 0, ErrInvalidInput
	}
	return r.markRead(ctx, conversationID, func(ctx context.Context, tx pgx.Tx) (int64, error) {
		var participantID int64
		err := tx.QueryRow(ctx, `
			SELECT participant.id FROM conversation_participants participant
			JOIN conversations c ON c.id = participant.conversation_id
			WHERE c.id = $1 AND c.student_id = $2 AND participant.student_id = $2
		`, conversationID, studentID).Scan(&participantID)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		if err != nil {
			return 0, mapDatabaseError("проверить участника-ученика", err)
		}
		return participantID, nil
	})
}

func (r *ConversationRepository) markRead(ctx context.Context, conversationID int64, resolve func(context.Context, pgx.Tx) (int64, error)) (int64, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, mapDatabaseError("начать отметку прочтения", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	participantID, err := resolve(ctx, tx)
	if err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO conversation_message_reads (message_id, participant_id, conversation_id)
		SELECT message.id, $2, message.conversation_id
		FROM conversation_messages message
		WHERE message.conversation_id = $1 AND message.sender_participant_id <> $2
		ON CONFLICT (message_id, participant_id) DO NOTHING
	`, conversationID, participantID)
	if err != nil {
		return 0, mapDatabaseError("отметить сообщения прочитанными", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE conversation_participants SET last_read_at = now() WHERE id = $1", participantID); err != nil {
		return 0, mapDatabaseError("обновить last_read_at участника", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, mapDatabaseError("зафиксировать отметку прочтения", err)
	}
	return tag.RowsAffected(), nil
}

func (r *ConversationRepository) UnreadCountForCurator(ctx context.Context, staffID int64) (int64, error) {
	if staffID <= 0 {
		return 0, ErrInvalidInput
	}
	var count int64
	err := r.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM conversations c
		JOIN conversation_participants participant
		  ON participant.conversation_id = c.id AND participant.staff_id = $1 AND participant.left_at IS NULL
		JOIN conversation_messages message ON message.conversation_id = c.id
		WHERE c.status = 'active' AND c.assigned_curator_staff_id = $1
		  AND message.sender_participant_id <> participant.id
		  AND NOT EXISTS (
		    SELECT 1 FROM conversation_message_reads read_mark
		    WHERE read_mark.message_id = message.id AND read_mark.participant_id = participant.id
		  )
		  AND EXISTS (
		    SELECT 1 FROM student_curator_assignments assignment
		    JOIN staff_accounts staff ON staff.id = assignment.curator_staff_id
		    JOIN staff_role_assignments ra ON ra.staff_id = staff.id AND ra.revoked_at IS NULL
		    JOIN staff_roles role ON role.id = ra.role_id AND role.role_code = 'curator'
		    WHERE assignment.student_id = c.student_id AND assignment.curator_staff_id = $1
		      AND assignment.status = 'active' AND staff.account_status = 'active'
		  )
	`, staffID).Scan(&count)
	if err != nil {
		return 0, mapDatabaseError("получить непрочитанные сообщения куратора", err)
	}
	return count, nil
}

func (r *ConversationRepository) UnreadCountForStudent(ctx context.Context, studentID int64) (int64, error) {
	if studentID <= 0 {
		return 0, ErrInvalidInput
	}
	var count int64
	err := r.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM conversations c
		JOIN conversation_participants participant
		  ON participant.conversation_id = c.id AND participant.student_id = $1
		JOIN conversation_messages message ON message.conversation_id = c.id
		WHERE c.student_id = $1 AND message.sender_participant_id <> participant.id
		  AND NOT EXISTS (
		    SELECT 1 FROM conversation_message_reads read_mark
		    WHERE read_mark.message_id = message.id AND read_mark.participant_id = participant.id
		  )
	`, studentID).Scan(&count)
	if err != nil {
		return 0, mapDatabaseError("получить непрочитанные сообщения ученика", err)
	}
	return count, nil
}

func normalizeConversationLimit(limit int) int {
	if limit <= 0 || limit > 100 {
		return 50
	}
	return limit
}
