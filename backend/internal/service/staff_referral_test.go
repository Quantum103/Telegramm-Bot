package service

import (
	"context"
	"encoding/base64"
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

func TestGetOrCreateReferralCodeReturnsPermanentURLSafeCode(t *testing.T) {
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
	if len(first.Code) != 45 || first.Code[:2] != "c_" {
		t.Fatalf("unexpected referral code format: %q", first.Code)
	}
	raw, err := base64.RawURLEncoding.DecodeString(first.Code[2:])
	if err != nil || len(raw) != 32 {
		t.Fatalf("referral code must contain 32 URL-safe random bytes: len=%d err=%v", len(raw), err)
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
	service := NewStaffReferralService(store)
	if _, err := service.AssignStudentByReferral(context.Background(), 11, "c_invalid"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("invalid referral code error = %v, want ErrInvalidInput", err)
	}
	if store.assignCalls != 0 {
		t.Fatal("invalid referral code reached the store")
	}
	code := "c_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	result, err := service.AssignStudentByReferral(context.Background(), 11, code)
	if err != nil {
		t.Fatalf("AssignStudentByReferral() error = %v", err)
	}
	if store.assignCalls != 1 || store.assignmentCode != code || !result.Assigned || result.CuratorStaffID != 42 {
		t.Fatalf("unexpected referral assignment: result=%+v store=%+v", result, store)
	}
}
