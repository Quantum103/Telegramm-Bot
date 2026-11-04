package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type staffReferralTestStore struct {
	codes          map[int64]domain.StaffReferralCode
	getCalls       int
	assignment     domain.ReferralAssignmentResult
	assignmentCode string
	assignCalls    int
	assignErr      error
}

func (s *staffReferralTestStore) GetOrCreateReferralCode(_ context.Context, staffID int64, candidate string) (domain.StaffReferralCode, error) {
	s.getCalls++
	if existing, ok := s.codes[staffID]; ok {
		return existing, nil
	}
	if s.codes == nil {
		s.codes = make(map[int64]domain.StaffReferralCode)
	}
	value := domain.StaffReferralCode{StaffID: staffID, Code: candidate, CreatedAt: time.Now()}
	s.codes[staffID] = value
	return value, nil
}

func (s *staffReferralTestStore) AssignStudentByReferral(_ context.Context, _ int64, code string) (domain.ReferralAssignmentResult, error) {
	s.assignCalls++
	s.assignmentCode = code
	return s.assignment, s.assignErr
}

func TestGetOrCreateReferralCodeReturnsStableShortCode(t *testing.T) {
	store := &staffReferralTestStore{}
	service := NewStaffReferralService(store)
	first, err := service.GetOrCreateReferralCode(context.Background(), 42)
	if err != nil {
		t.Fatalf("first GetOrCreateReferralCode() error = %v", err)
	}
	second, err := service.GetOrCreateReferralCode(context.Background(), 42)
	if err != nil {
		t.Fatalf("second GetOrCreateReferralCode() error = %v", err)
	}
	if first.Code != second.Code || first.StaffID != 42 || second.StaffID != 42 {
		t.Fatalf("referral code must remain stable: first=%+v second=%+v", first, second)
	}
	if first.Code != "c_42" {
		t.Fatalf("referral code = %q, want c_42", first.Code)
	}
	if store.getCalls != 2 {
		t.Fatalf("store calls = %d, want 2", store.getCalls)
	}
}

func TestGetOrCreateReferralCodeRejectsInvalidStaffID(t *testing.T) {
	store := &staffReferralTestStore{}
	_, err := NewStaffReferralService(store).GetOrCreateReferralCode(context.Background(), 0)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("GetOrCreateReferralCode() error = %v, want ErrInvalidInput", err)
	}
	if store.getCalls != 0 {
		t.Fatal("invalid staff ID reached the store")
	}
}

func TestAssignStudentByReferralValidatesCodeAndReturnsStoreResult(t *testing.T) {
	store := &staffReferralTestStore{assignment: domain.ReferralAssignmentResult{CuratorStaffID: 42, Assigned: true}}
	publisher := &conversationEventPublisherTestStub{}
	service := NewStaffReferralService(store, publisher)
	if _, err := service.AssignStudentByReferral(context.Background(), 11, "c_invalid"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("invalid referral code error = %v, want ErrInvalidInput", err)
	}
	if store.assignCalls != 0 {
		t.Fatal("invalid referral code reached the store")
	}
	code := "c_42"
	result, err := service.AssignStudentByReferral(context.Background(), 11, code)
	if err != nil {
		t.Fatalf("AssignStudentByReferral() error = %v", err)
	}
	if store.assignCalls != 1 || store.assignmentCode != code || !result.Assigned || result.CuratorStaffID != 42 {
		t.Fatalf("unexpected referral assignment: result=%+v store=%+v", result, store)
	}
	if publisher.calls != 1 {
		t.Fatalf("a new referral assignment should publish one conversation event, got %d", publisher.calls)
	}

	store.assignment = domain.ReferralAssignmentResult{CuratorStaffID: 42, AlreadyAssigned: true}
	if _, err := service.AssignStudentByReferral(context.Background(), 11, code); err != nil {
		t.Fatalf("repeat referral assignment: %v", err)
	}
	store.assignErr = errors.New("database unavailable")
	if _, err := service.AssignStudentByReferral(context.Background(), 12, code); err == nil {
		t.Fatal("expected referral assignment persistence error")
	}
	if publisher.calls != 1 {
		t.Fatalf("idempotent or failed referral assignment must not publish an event, got %d", publisher.calls)
	}
}
