package httpserver

import (
	"context"
	"mime"
	"net/http"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type StaffTelegramAPI interface {
	GetBindingStatus(context.Context, int64) (domain.StaffTelegramBindingStatus, error)
	IssueLinkToken(context.Context, int64) (domain.StaffTelegramLinkToken, error)
	ListNotifications(context.Context, int64) ([]domain.CuratorNotification, error)
	GetNotificationPreferences(context.Context, int64) (domain.StaffNotificationPreferences, error)
	UpdateNotificationPreferences(context.Context, int64, domain.StaffNotificationPreferences) error
}

type staffTelegramLinkTokenRequest struct{}

type staffTelegramLinkTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type curatorNotificationsResponse struct {
	Notifications []domain.CuratorNotification `json:"notifications"`
}

func (h handlers) staffTelegramStatus(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity := staffIdentityFromRequest(r)
	status, err := h.staffTelegram.GetBindingStatus(r.Context(), identity.StaffID)
	if err != nil {
		h.writeAPIError(w, err, "получить состояние Telegram-привязки куратора")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h handlers) staffTelegramNotifications(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity := staffIdentityFromRequest(r)
	items, err := h.staffTelegram.ListNotifications(r.Context(), identity.StaffID)
	if err != nil {
		h.writeAPIError(w, err, "получить журнал Telegram-уведомлений")
		return
	}
	if items == nil {
		items = []domain.CuratorNotification{}
	}
	writeJSON(w, http.StatusOK, curatorNotificationsResponse{Notifications: items})
}

func (h handlers) staffTelegramPreferences(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity := staffIdentityFromRequest(r)
	switch r.Method {
	case http.MethodGet:
		prefs, err := h.staffTelegram.GetNotificationPreferences(r.Context(), identity.StaffID)
		if err != nil { h.writeAPIError(w, err, "получить настройки уведомлений"); return }
		writeJSON(w, http.StatusOK, prefs)
	case http.MethodPut:
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeJSON(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "content_type_must_be_json"})
			return
		}
		var prefs domain.StaffNotificationPreferences
		if err := decodeJSONBodyWithLimit(w, r, &prefs, maxStaffAdminBodyBytes); err != nil {
			writeInvalidAdminRequest(w, err); return
		}
		if err := h.staffTelegram.UpdateNotificationPreferences(r.Context(), identity.StaffID, prefs); err != nil {
			h.writeAPIError(w, err, "сохранить настройки уведомлений"); return
		}
		writeJSON(w, http.StatusOK, prefs)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, apiErrorResponse{Error: "method_not_allowed"})
	}
}

func (h handlers) staffTelegramLinkToken(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity := staffIdentityFromRequest(r)
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "content_type_must_be_json"})
		return
	}
	var input staffTelegramLinkTokenRequest
	if err := decodeJSONBodyWithLimit(w, r, &input, maxStaffAdminBodyBytes); err != nil {
		writeInvalidAdminRequest(w, err)
		return
	}
	result, err := h.staffTelegram.IssueLinkToken(r.Context(), identity.StaffID)
	if err != nil {
		h.writeAPIError(w, err, "создать одноразовый Telegram-код")
		return
	}
	writeJSON(w, http.StatusCreated, staffTelegramLinkTokenResponse{Token: result.Token, ExpiresAt: result.ExpiresAt})
}
