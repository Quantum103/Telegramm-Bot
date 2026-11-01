package service

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

const maxConversationMessageRunes = 4000

type ConversationStore interface {
	GetOrCreateForStudent(context.Context, int64) (domain.Conversation, error)
	ListForCurator(context.Context, int64) ([]domain.ConversationSummary, error)
	ListMessagesForCurator(context.Context, int64, int64, int64, int) ([]domain.ConversationMessage, error)
	ListMessagesForStudent(context.Context, int64, int64, int64, int) ([]domain.ConversationMessage, error)
	CreateStudentMessage(context.Context, domain.CreateStudentConversationMessageParams) (domain.ConversationMessage, error)
	SendCuratorMessage(context.Context, int64, int64, string) (domain.ConversationMessage, error)
	MarkReadForCurator(context.Context, int64, int64) (int64, error)
	MarkReadForStudent(context.Context, int64, int64) (int64, error)
	UnreadCountForCurator(context.Context, int64) (int64, error)
	UnreadCountForStudent(context.Context, int64) (int64, error)
	SetStudentChatMode(context.Context, int64, bool) error
	IsStudentChatMode(context.Context, int64) (bool, error)
}

type ConversationService struct {
	store ConversationStore
}

func NewConversationService(store ConversationStore) *ConversationService {
	return &ConversationService{store: store}
}

func (s *ConversationService) GetOrCreateForStudent(ctx context.Context, studentID int64) (domain.Conversation, error) {
	if studentID <= 0 {
		return domain.Conversation{}, domain.ErrInvalidInput
	}
	return s.store.GetOrCreateForStudent(ctx, studentID)
}

func (s *ConversationService) ListForCurator(ctx context.Context, curatorStaffID int64) ([]domain.ConversationSummary, error) {
	if curatorStaffID <= 0 {
		return nil, domain.ErrInvalidInput
	}
	return s.store.ListForCurator(ctx, curatorStaffID)
}

func (s *ConversationService) ListMessagesForCurator(ctx context.Context, curatorStaffID, conversationID, beforeMessageID int64, limit int) ([]domain.ConversationMessage, error) {
	if curatorStaffID <= 0 || conversationID <= 0 || beforeMessageID < 0 {
		return nil, domain.ErrInvalidInput
	}
	return s.store.ListMessagesForCurator(ctx, curatorStaffID, conversationID, beforeMessageID, limit)
}

func (s *ConversationService) ListMessagesForStudent(ctx context.Context, studentID, conversationID, beforeMessageID int64, limit int) ([]domain.ConversationMessage, error) {
	if studentID <= 0 || conversationID <= 0 || beforeMessageID < 0 {
		return nil, domain.ErrInvalidInput
	}
	return s.store.ListMessagesForStudent(ctx, studentID, conversationID, beforeMessageID, limit)
}

func (s *ConversationService) CreateStudentMessage(ctx context.Context, params domain.CreateStudentConversationMessageParams) (domain.ConversationMessage, error) {
	params.BodyText = strings.TrimSpace(params.BodyText)
	if params.StudentID <= 0 || params.TelegramChatID <= 0 || params.TelegramMessageID <= 0 ||
		params.BodyText == "" || !utf8.ValidString(params.BodyText) ||
		utf8.RuneCountInString(params.BodyText) > maxConversationMessageRunes {
		return domain.ConversationMessage{}, domain.ErrInvalidInput
	}
	return s.store.CreateStudentMessage(ctx, params)
}

func (s *ConversationService) SendCuratorMessage(ctx context.Context, curatorStaffID, conversationID int64, bodyText string) (domain.ConversationMessage, error) {
	bodyText = strings.TrimSpace(bodyText)
	if curatorStaffID <= 0 || conversationID <= 0 || bodyText == "" ||
		!utf8.ValidString(bodyText) || utf8.RuneCountInString(bodyText) > maxConversationMessageRunes {
		return domain.ConversationMessage{}, domain.ErrInvalidInput
	}
	return s.store.SendCuratorMessage(ctx, curatorStaffID, conversationID, bodyText)
}

func (s *ConversationService) MarkReadForCurator(ctx context.Context, curatorStaffID, conversationID int64) (int64, error) {
	if curatorStaffID <= 0 || conversationID <= 0 {
		return 0, domain.ErrInvalidInput
	}
	return s.store.MarkReadForCurator(ctx, curatorStaffID, conversationID)
}

func (s *ConversationService) MarkReadForStudent(ctx context.Context, studentID, conversationID int64) (int64, error) {
	if studentID <= 0 || conversationID <= 0 {
		return 0, domain.ErrInvalidInput
	}
	return s.store.MarkReadForStudent(ctx, studentID, conversationID)
}

func (s *ConversationService) UnreadCountForCurator(ctx context.Context, curatorStaffID int64) (int64, error) {
	if curatorStaffID <= 0 {
		return 0, domain.ErrInvalidInput
	}
	return s.store.UnreadCountForCurator(ctx, curatorStaffID)
}

func (s *ConversationService) UnreadCountForStudent(ctx context.Context, studentID int64) (int64, error) {
	if studentID <= 0 {
		return 0, domain.ErrInvalidInput
	}
	return s.store.UnreadCountForStudent(ctx, studentID)
}


func (s *ConversationService) SetStudentChatMode(ctx context.Context, studentID int64, enabled bool) error {
	if studentID <= 0 {
		return domain.ErrInvalidInput
	}
	return s.store.SetStudentChatMode(ctx, studentID, enabled)
}

func (s *ConversationService) IsStudentChatMode(ctx context.Context, studentID int64) (bool, error) {
	if studentID <= 0 {
		return false, domain.ErrInvalidInput
	}
	return s.store.IsStudentChatMode(ctx, studentID)
}
