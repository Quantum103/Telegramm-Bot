package httpserver

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type ConversationAPI interface {
	ListForCurator(context.Context, int64) ([]domain.ConversationSummary, error)
	ListMessagesForCurator(context.Context, int64, int64, int64, int) ([]domain.ConversationMessage, error)
	SendCuratorMessage(context.Context, int64, int64, string) (domain.ConversationMessage, error)
	MarkReadForCurator(context.Context, int64, int64) (int64, error)
	UnreadCountForCurator(context.Context, int64) (int64, error)
}

type staffConversationMessageRequest struct {
	BodyText string `json:"body_text"`
}

type staffConversationListResponse struct {
	Conversations []domain.ConversationSummary `json:"conversations"`
}

type staffConversationMessagesResponse struct {
	Messages []domain.ConversationMessage `json:"messages"`
}

type staffConversationUnreadResponse struct {
	UnreadCount int64 `json:"unread_count"`
}

type staffConversationReadResponse struct {
	MarkedRead int64 `json:"marked_read"`
}

func (h handlers) staffCuratorMiddleware(next http.Handler) http.Handler {
	roleChecked := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := staffIdentityFromRequest(r)
		if identity.StaffID <= 0 {
			writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
			return
		}
		if !staffHasRole(identity, "curator") {
			writeJSON(w, http.StatusForbidden, apiErrorResponse{Error: "insufficient_permissions"})
			return
		}
		next.ServeHTTP(w, r)
	})
	return h.staffSessionMiddleware(roleChecked)
}

func (h handlers) staffListConversations(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity := staffIdentityFromRequest(r)
	items, err := h.conversations.ListForCurator(r.Context(), identity.StaffID)
	if err != nil {
		h.writeAPIError(w, err, "получить диалоги куратора")
		return
	}
	if items == nil {
		items = []domain.ConversationSummary{}
	}
	writeJSON(w, http.StatusOK, staffConversationListResponse{Conversations: items})
}

func (h handlers) staffListConversationMessages(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity := staffIdentityFromRequest(r)
	conversationID, ok := parsePositiveID(w, r.PathValue("conversationID"))
	if !ok {
		return
	}
	beforeID, err := parseNonNegativeQueryID(r.URL.Query().Get("before_message_id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_before_message_id"})
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 100 {
			writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_limit"})
			return
		}
		limit = parsed
	}
	messages, err := h.conversations.ListMessagesForCurator(r.Context(), identity.StaffID, conversationID, beforeID, limit)
	if err != nil {
		h.writeAPIError(w, err, "получить историю диалога")
		return
	}
	if messages == nil {
		messages = []domain.ConversationMessage{}
	}
	writeJSON(w, http.StatusOK, staffConversationMessagesResponse{Messages: messages})
}

func (h handlers) staffSendConversationMessage(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity := staffIdentityFromRequest(r)
	conversationID, ok := parsePositiveID(w, r.PathValue("conversationID"))
	if !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "content_type_must_be_json"})
		return
	}
	var input staffConversationMessageRequest
	if err := decodeJSONBodyWithLimit(w, r, &input, maxStaffAdminBodyBytes); err != nil {
		writeInvalidAdminRequest(w, err)
		return
	}
	message, err := h.conversations.SendCuratorMessage(r.Context(), identity.StaffID, conversationID, strings.TrimSpace(input.BodyText))
	if err != nil {
		h.writeAPIError(w, err, "сохранить ответ куратора")
		return
	}
	writeJSON(w, http.StatusCreated, message)
}

func (h handlers) staffMarkConversationRead(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity := staffIdentityFromRequest(r)
	conversationID, ok := parsePositiveID(w, r.PathValue("conversationID"))
	if !ok {
		return
	}
	count, err := h.conversations.MarkReadForCurator(r.Context(), identity.StaffID, conversationID)
	if err != nil {
		h.writeAPIError(w, err, "отметить сообщения прочитанными")
		return
	}
	writeJSON(w, http.StatusOK, staffConversationReadResponse{MarkedRead: count})
}

func (h handlers) staffConversationUnreadCount(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity := staffIdentityFromRequest(r)
	count, err := h.conversations.UnreadCountForCurator(r.Context(), identity.StaffID)
	if err != nil {
		h.writeAPIError(w, err, "получить число непрочитанных сообщений")
		return
	}
	writeJSON(w, http.StatusOK, staffConversationUnreadResponse{UnreadCount: count})
}

func staffIdentityFromRequest(r *http.Request) domain.StaffSessionIdentity {
	identity, _ := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	return identity
}

func parseNonNegativeQueryID(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 0 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}
