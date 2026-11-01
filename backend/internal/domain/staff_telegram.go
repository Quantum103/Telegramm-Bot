package domain

import "time"

type StaffTelegramAccount struct {
	StaffID       int64
	DisplayName   string
	AccountStatus string
	IsCurator       bool
	IsAdministrator bool
}

type StaffTelegramBindingStatus struct {
	Linked           bool    `json:"linked"`
	TelegramUsername *string `json:"telegram_username,omitempty"`
}

type StaffTelegramLinkToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type CuratorNotification struct {
	ID                 int64      `json:"id"`
	MessageID          int64      `json:"message_id"`
	ConversationID     int64      `json:"conversation_id"`
	StudentDisplayName string     `json:"student_display_name"`
	Channel             string     `json:"channel"`
	DeliveryStatus     string     `json:"delivery_status"`
	AttemptCount       int        `json:"attempt_count"`
	LastErrorCode      *string    `json:"last_error_code,omitempty"`
	LastErrorAt        *time.Time `json:"last_error_at,omitempty"`
	SentAt             *time.Time `json:"sent_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
}
