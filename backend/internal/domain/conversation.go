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
	StudentID           int64
	TelegramChatID      int64
	TelegramMessageID   int64
	BodyText            string
	FirstReminderAfter  time.Duration
	SecondReminderAfter time.Duration
	EscalationAfter     time.Duration
}

// ConversationDelivery — задание на доставку ответа куратора в Telegram.
type ConversationDelivery struct {
	ID                       int64
	MessageID                int64
	Channel                  string
	RecipientChatID          int64
	RecipientStaffID         int64
	ConversationID           int64
	StudentDisplayName       string
	MessageSenderDisplayName string
	StudentInLessonMode       bool
	BodyText                 string
	AttemptCount             int
	PreparationErrorCode     string
	CuratorDisplayName       string
	WaitedFor                time.Duration
	LastCuratorStatus        string
	ReminderEnabled          bool
	NotificationsEnabled     bool
	QuietHoursEnabled        bool
	Timezone                 string
	QuietStart               string
	QuietEnd                 string
	DeliveryKind             string
	ResponseWaitID           int64
	ResponseWaitGeneration   int
}

type DeliveryAuthorization struct {
	Current              bool
	NotificationsEnabled bool
	RemindersEnabled     bool
	QuietHoursEnabled    bool
	Timezone             string
	QuietStart           string
	QuietEnd             string
}

type StaffNotificationPreferences struct {
	RemindersEnabled     bool   `json:"reminders_enabled"`
	NotificationsEnabled bool   `json:"notifications_enabled"`
	Timezone             string `json:"timezone"`
	QuietHoursEnabled    bool   `json:"quiet_hours_enabled"`
	QuietStart           string `json:"quiet_start"`
	QuietEnd             string `json:"quiet_end"`
}
