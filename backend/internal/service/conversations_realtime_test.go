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
	createErr      error
	sendErr        error
	leaveCalls     int
	leaveStudentID int64
	leaveErr       error
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

func (s *conversationEventStoreStub) LeaveStudentInteraction(_ context.Context, studentID int64) error {
	s.leaveCalls++
	s.leaveStudentID = studentID
	return s.leaveErr
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

func TestConversationServiceLeavesStudentInteractionThroughCombinedStoreOperation(t *testing.T) {
	store := &conversationEventStoreStub{}
	conversations := NewConversationService(store)

	if err := conversations.LeaveStudentInteraction(context.Background(), 42); err != nil {
		t.Fatalf("leave student interaction: %v", err)
	}
	if store.leaveCalls != 1 || store.leaveStudentID != 42 {
		t.Fatalf("combined store operation was not called exactly once: calls=%d student_id=%d",
			store.leaveCalls, store.leaveStudentID)
	}
	if err := conversations.LeaveStudentInteraction(context.Background(), 0); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("invalid student ID should be rejected: %v", err)
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

func TestConversationServiceCanPublishExternalAssignmentChanges(t *testing.T) {
	conversations := NewConversationService(&conversationEventStoreStub{})
	events, unsubscribe := conversations.SubscribeConversationEvents()
	defer unsubscribe()

	conversations.PublishConversationChanged()
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

type provisionableConversationEventStoreStub struct {
	*conversationEventStoreStub
	conversation       domain.Conversation
	provisionErr       error
	provisionCalls     int
	provisionCuratorID int64
	provisionStudentID int64
}

func (s *provisionableConversationEventStoreStub) GetOrCreateForCurator(
	_ context.Context, curatorStaffID, studentID int64,
) (domain.Conversation, error) {
	s.provisionCalls++
	s.provisionCuratorID = curatorStaffID
	s.provisionStudentID = studentID
	return s.conversation, s.provisionErr
}

func TestConversationServiceEnsuresConversationAndPublishesOnlyOnSuccess(t *testing.T) {
	store := &provisionableConversationEventStoreStub{
		conversationEventStoreStub: &conversationEventStoreStub{},
		conversation: domain.Conversation{
			ID: 301, StudentID: 42, AssignedCuratorStaffID: 17, Status: "active",
		},
	}
	conversations := NewConversationService(store)
	events, unsubscribe := conversations.SubscribeConversationEvents()
	defer unsubscribe()

	got, err := conversations.EnsureForCurator(context.Background(), 17, 42)
	if err != nil || got.ID != 301 {
		t.Fatalf("ensure curator conversation: conversation=%+v err=%v", got, err)
	}
	if store.provisionCalls != 1 || store.provisionCuratorID != 17 || store.provisionStudentID != 42 {
		t.Fatalf("provision call mismatch: calls=%d curator=%d student=%d",
			store.provisionCalls, store.provisionCuratorID, store.provisionStudentID)
	}
	waitForConversationEvent(t, events)

	store.provisionErr = domain.ErrNotFound
	if _, err := conversations.EnsureForCurator(context.Background(), 17, 43); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("wrong assignment should be rejected: %v", err)
	}
	select {
	case <-events:
		t.Fatal("failed provisioning unexpectedly published a conversation event")
	default:
	}

	if _, err := conversations.EnsureForCurator(context.Background(), 0, 42); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("invalid curator ID should be rejected: %v", err)
	}
}
