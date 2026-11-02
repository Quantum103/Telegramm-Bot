package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

const (
	staffReferralCodePrefix  = "c_"
	staffReferralRandomSize  = 32
	staffReferralCodeRetries = 5
)

type StaffReferralStore interface {
	GetOrCreateReferralCode(context.Context, int64, string) (domain.StaffReferralCode, error)
	AssignStudentByReferral(context.Context, int64, string) (domain.ReferralAssignmentResult, error)
}

type StaffReferralService struct {
	store StaffReferralStore
}

func NewStaffReferralService(store StaffReferralStore) *StaffReferralService {
	return &StaffReferralService{store: store}
}

func (s *StaffReferralService) GetOrCreateReferralCode(ctx context.Context, staffID int64) (domain.StaffReferralCode, error) {
	if s == nil || s.store == nil || staffID <= 0 {
		return domain.StaffReferralCode{}, domain.ErrInvalidInput
	}
	for attempt := 0; attempt < staffReferralCodeRetries; attempt++ {
		raw := make([]byte, staffReferralRandomSize)
		if _, err := rand.Read(raw); err != nil {
			return domain.StaffReferralCode{}, errors.New("не удалось создать реферальный код")
		}
		candidate := staffReferralCodePrefix + base64.RawURLEncoding.EncodeToString(raw)
		referral, err := s.store.GetOrCreateReferralCode(ctx, staffID, candidate)
		if errors.Is(err, domain.ErrConflict) {
			continue
		}
		return referral, err
	}
	return domain.StaffReferralCode{}, domain.ErrConflict
}

func (s *StaffReferralService) AssignStudentByReferral(
	ctx context.Context,
	studentID int64,
	code string,
) (domain.ReferralAssignmentResult, error) {
	code = strings.TrimSpace(code)
	if s == nil || s.store == nil || studentID <= 0 || !validStaffReferralCode(code) {
		return domain.ReferralAssignmentResult{}, domain.ErrInvalidInput
	}
	return s.store.AssignStudentByReferral(ctx, studentID, code)
}

func validStaffReferralCode(code string) bool {
	if len(code) != len(staffReferralCodePrefix)+base64.RawURLEncoding.EncodedLen(staffReferralRandomSize) ||
		!strings.HasPrefix(code, staffReferralCodePrefix) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, staffReferralCodePrefix))
	return err == nil && len(raw) == staffReferralRandomSize &&
		base64.RawURLEncoding.EncodeToString(raw) == strings.TrimPrefix(code, staffReferralCodePrefix)
}
