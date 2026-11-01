package service

import (
	"context"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type notificationPreferencesStoreStub struct {
	updated bool
	prefs domain.StaffNotificationPreferences
}

func (s *notificationPreferencesStoreStub) GetBindingStatus(context.Context, int64) (domain.StaffTelegramBindingStatus, error) {
	return domain.StaffTelegramBindingStatus{}, nil
}
func (s *notificationPreferencesStoreStub) IssueLinkToken(context.Context, int64, [32]byte, time.Time) error {
	return nil
}
func (s *notificationPreferencesStoreStub) BindTelegramAccount(context.Context, int64, string, [32]byte) (domain.StaffTelegramAccount, error) {
	return domain.StaffTelegramAccount{}, nil
}
func (s *notificationPreferencesStoreStub) FindByTelegramID(context.Context, int64) (domain.StaffTelegramAccount, error) {
	return domain.StaffTelegramAccount{}, nil
}
func (s *notificationPreferencesStoreStub) RestoreNotifications(context.Context, int64) error { return nil }
func (s *notificationPreferencesStoreStub) ListNotifications(context.Context, int64, int) ([]domain.CuratorNotification, error) {
	return nil, nil
}
func (s *notificationPreferencesStoreStub) GetNotificationPreferences(context.Context, int64) (domain.StaffNotificationPreferences, error) {
	return s.prefs, nil
}
func (s *notificationPreferencesStoreStub) UpdateNotificationPreferences(_ context.Context, _ int64, prefs domain.StaffNotificationPreferences) error {
	s.updated = true
	s.prefs = prefs
	return nil
}

func TestUpdateNotificationPreferencesValidatesTimeZoneAndQuietWindow(t *testing.T) {
	store := &notificationPreferencesStoreStub{}
	service := NewStaffTelegramService(store)
	valid := domain.StaffNotificationPreferences{
		RemindersEnabled: true, NotificationsEnabled: true,
		Timezone: "Europe/Amsterdam", QuietHoursEnabled: true,
		QuietStart: "22:00", QuietEnd: "08:00",
	}
	if err := service.UpdateNotificationPreferences(context.Background(), 42, valid); err != nil {
		t.Fatalf("valid IANA timezone and overnight quiet hours should be accepted: %v", err)
	}
	if !store.updated || store.prefs.Timezone != "Europe/Amsterdam" {
		t.Fatalf("valid preferences were not saved: %+v", store)
	}

	store.updated = false
	invalid := valid
	invalid.Timezone = "Mars/Olympus"
	if err := service.UpdateNotificationPreferences(context.Background(), 42, invalid); err != domain.ErrInvalidInput {
		t.Fatalf("unknown time zone should be rejected, got %v", err)
	}
	if store.updated { t.Fatal("invalid time zone must not be persisted") }

	store.updated = false
	invalid = valid
	invalid.QuietHoursEnabled = false
	invalid.QuietStart, invalid.QuietEnd = "22:00", "22:00"
	if err := service.UpdateNotificationPreferences(context.Background(), 42, invalid); err != domain.ErrInvalidInput {
		t.Fatalf("equal quiet-hour bounds should be rejected to match database constraints, got %v", err)
	}
	if store.updated { t.Fatal("equal quiet-hour bounds must not be persisted") }
}
