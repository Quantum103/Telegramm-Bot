package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type conversationEventStoreStub struct {
	ConversationStore
	createErr error
	sendErr   error
}

func (s *conversationEventStoreStub) CreateStudentMessage(_ context.Context, _ domain.CreateStudentConversationMessageParams) (domain.ConversationMessage, error) {
	if s.createErr != nil {
		return domain.ConversationMessage{}, s.createErr
	}
	return domain.ConversationMessage{ID: 101}, nil
}

func (s *conversationEventStoreStub) SendCuratorMessage(_ context.Context, _, _ int64, _ string) (domain.ConversationMessage, error) {
	if s.sendErr != nil {
		return domain.ConversationMessage{}, s.sendErr
	}
	return domain.ConversationMessage{ID: 202}, nil
}

func waitForConversationEvent(t *testing.T, events <-chan struct{}) {
	t.Helper()
	select {
	case _, ok := <-events:
		if !ok {
			t.Fatal("conversation event subscription closed unexpectedly")
		}
	case <-time.After(time.Second):
		t.Fatal("conversation change was not published")
	}
}

func TestConversationServicePublishesStudentAndCuratorMessages(t *testing.T) {
	store := &conversationEventStoreStub{}
	conversations := NewConversationService(store)
	events, unsubscribe := conversations.SubscribeConversationEvents()
	defer unsubscribe()

	if _, err := conversations.CreateStudentMessage(context.Background(), domain.CreateStudentConversationMessageParams{
		StudentID: 1, TelegramChatID: 11, TelegramMessageID: 22, BodyText: "Помогите с вопросом",
	}); err != nil {
		t.Fatalf("create student message: %v", err)
	}
	waitForConversationEvent(t, events)

	if _, err := conversations.SendCuratorMessage(context.Background(), 2, 3, "Здравствуйте"); err != nil {
		t.Fatalf("send curator message: %v", err)
	}
	waitForConversationEvent(t, events)
}

func TestConversationServiceDoesNotPublishFailedMessages(t *testing.T) {
	store := &conversationEventStoreStub{createErr: errors.New("database unavailable")}
	conversations := NewConversationService(store)
	events, unsubscribe := conversations.SubscribeConversationEvents()
	defer unsubscribe()

	_, err := conversations.CreateStudentMessage(context.Background(), domain.CreateStudentConversationMessageParams{
		StudentID: 1, TelegramChatID: 11, TelegramMessageID: 22, BodyText: "Сообщение не сохранено",
	})
	if err == nil {
		t.Fatal("expected persistence error")
	}
	select {
	case <-events:
		t.Fatal("failed message unexpectedly published a conversation event")
	default:
	}
}
