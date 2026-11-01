package domain

import "time"

type Conversation struct {
	ID                     int64     `json:"id"`
	StudentID              int64     `json:"student_id"`
	AssignedCuratorStaffID int64     `json:"assigned_curator_staff_id"`
	Status                 string    `json:"status"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

// ConversationMessage не содержит личные Telegram-контакты отправителя.
type ConversationMessage struct {
	ID                  int64     `json:"id"`
	ConversationID      int64     `json:"conversation_id"`
	SenderParticipantID int64     `json:"sender_participant_id"`
	SenderType          string    `json:"sender_type"`
	MessageType         string    `json:"message_type"`
	BodyText            *string   `json:"body_text,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	Replayed            bool      `json:"replayed,omitempty"`
	DeliveryStatus      string    `json:"delivery_status,omitempty"`
	DeliveryErrorCode   *string   `json:"delivery_error_code,omitempty"`
}

type ConversationSummary struct {
	ID                 int64                `json:"id"`
	StudentID          int64                `json:"student_id"`
	StudentDisplayName string               `json:"student_display_name"`
	Status             string               `json:"status"`
	LastMessage        *ConversationMessage `json:"last_message,omitempty"`
	UnreadCount        int64                `json:"unread_count"`
	LastActivityAt     time.Time            `json:"last_activity_at"`
}

// Параметры сообщения ученика заполняются доверенным Telegram-обработчиком.
type CreateStudentConversationMessageParams struct {
	StudentID         int64
	TelegramChatID    int64
	TelegramMessageID int64
	BodyText          string
}


// ConversationDelivery — задание на доставку ответа куратора в Telegram.
type ConversationDelivery struct {
	ID              int64
	MessageID       int64
	RecipientChatID int64
	BodyText        string
	AttemptCount    int
}
