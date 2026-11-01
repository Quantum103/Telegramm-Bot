package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/auth"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

const staffTelegramLinkTTL = 10 * time.Minute

type StaffTelegramStore interface {
	GetBindingStatus(context.Context, int64) (domain.StaffTelegramBindingStatus, error)
	IssueLinkToken(context.Context, int64, [32]byte, time.Time) error
	BindTelegramAccount(context.Context, int64, string, [32]byte) (domain.StaffTelegramAccount, error)
	FindByTelegramID(context.Context, int64) (domain.StaffTelegramAccount, error)
	RestoreNotifications(context.Context, int64) error
	ListNotifications(context.Context, int64, int) ([]domain.CuratorNotification, error)
}

type StaffTelegramService struct {
	store StaffTelegramStore
	now   func() time.Time
}

func NewStaffTelegramService(store StaffTelegramStore) *StaffTelegramService {
	return &StaffTelegramService{store: store, now: time.Now}
}

func (s *StaffTelegramService) GetBindingStatus(ctx context.Context, staffID int64) (domain.StaffTelegramBindingStatus, error) {
	if staffID <= 0 {
		return domain.StaffTelegramBindingStatus{}, domain.ErrInvalidInput
	}
	return s.store.GetBindingStatus(ctx, staffID)
}

func (s *StaffTelegramService) IssueLinkToken(ctx context.Context, staffID int64) (domain.StaffTelegramLinkToken, error) {
	if staffID <= 0 {
		return domain.StaffTelegramLinkToken{}, domain.ErrInvalidInput
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return domain.StaffTelegramLinkToken{}, errors.New("не удалось создать код Telegram-привязки")
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expiresAt := s.now().Add(staffTelegramLinkTTL)
	if err := s.store.IssueLinkToken(ctx, staffID, auth.HashToken(token), expiresAt); err != nil {
		return domain.StaffTelegramLinkToken{}, err
	}
	return domain.StaffTelegramLinkToken{Token: token, ExpiresAt: expiresAt}, nil
}

func (s *StaffTelegramService) BindTelegramAccount(ctx context.Context, telegramID int64, username, token string) (domain.StaffTelegramAccount, error) {
	if telegramID <= 0 || len(token) != base64.RawURLEncoding.EncodedLen(32) {
		return domain.StaffTelegramAccount{}, domain.ErrInvalidInput
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
		return domain.StaffTelegramAccount{}, domain.ErrInvalidInput
	}
	if len(username) > 64 {
		username = username[:64]
	}
	return s.store.BindTelegramAccount(ctx, telegramID, username, auth.HashToken(token))
}

func (s *StaffTelegramService) FindByTelegramID(ctx context.Context, telegramID int64) (domain.StaffTelegramAccount, error) {
	if telegramID <= 0 {
		return domain.StaffTelegramAccount{}, domain.ErrInvalidInput
	}
	return s.store.FindByTelegramID(ctx, telegramID)
}

func (s *StaffTelegramService) RestoreNotifications(ctx context.Context, staffID int64) error {
	if staffID <= 0 {
		return domain.ErrInvalidInput
	}
	return s.store.RestoreNotifications(ctx, staffID)
}

func (s *StaffTelegramService) ListNotifications(ctx context.Context, staffID int64) ([]domain.CuratorNotification, error) {
	if staffID <= 0 {
		return nil, domain.ErrInvalidInput
	}
	return s.store.ListNotifications(ctx, staffID, 50)
}
