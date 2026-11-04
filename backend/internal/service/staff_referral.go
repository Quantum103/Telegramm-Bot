package service

import (
	"context"
	"strconv"
	"strings"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

const (
	staffReferralCodePrefix = "c_"
)

type StaffReferralStore interface {
	GetOrCreateReferralCode(context.Context, int64, string) (domain.StaffReferralCode, error)
	AssignStudentByReferral(context.Context, int64, string) (domain.ReferralAssignmentResult, error)
}

type StaffReferralService struct {
	store  StaffReferralStore
	events ConversationEventPublisher
}

func NewStaffReferralService(store StaffReferralStore, eventPublishers ...ConversationEventPublisher) *StaffReferralService {
	service := &StaffReferralService{store: store}
	if len(eventPublishers) > 0 {
		service.events = eventPublishers[0]
	}
	return service
}

func (s *StaffReferralService) GetOrCreateReferralCode(ctx context.Context, staffID int64) (domain.StaffReferralCode, error) {
	if s == nil || s.store == nil || staffID <= 0 {
		return domain.StaffReferralCode{}, domain.ErrInvalidInput
	}
	candidate := staffReferralCodePrefix + strconv.FormatInt(staffID, 10)
	return s.store.GetOrCreateReferralCode(ctx, staffID, candidate)
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
	result, err := s.store.AssignStudentByReferral(ctx, studentID, code)
	if err != nil {
		return domain.ReferralAssignmentResult{}, err
	}
	if result.Assigned && s.events != nil {
		s.events.PublishConversationChanged()
	}
	return result, nil
}

func validStaffReferralCode(code string) bool {
	if !strings.HasPrefix(code, staffReferralCodePrefix) {
		return false
	}
	idText := strings.TrimPrefix(code, staffReferralCodePrefix)
	id, err := strconv.ParseInt(idText, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == idText
}
