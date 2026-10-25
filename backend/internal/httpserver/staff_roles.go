package httpserver

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"strings"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type staffRoleAPI interface {
	AssignStaffRole(context.Context, int64, int64, string) error
	RevokeStaffRole(context.Context, int64, int64, string) error
}

type staffAssignRoleRequest struct {
	RoleCode string `json:"role_code"`
}

type staffRoleResponse struct {
	StaffID  int64  `json:"staff_id"`
	RoleCode string `json:"role_code"`
}

func (h handlers) staffAssignRole(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	if !ok || identity.StaffID <= 0 {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
		return
	}
	targetID, ok := parsePositiveID(w, r.PathValue("staffID"))
	if !ok {
		return
	}
	if targetID == identity.StaffID {
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "self_role_change_forbidden"})
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "content_type_must_be_json"})
		return
	}
	var input staffAssignRoleRequest
	if err := decodeJSONBodyWithLimit(w, r, &input, maxStaffAdminBodyBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiErrorResponse{Error: "request_too_large"})
		} else {
			writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_request"})
		}
		return
	}
	input.RoleCode = strings.TrimSpace(input.RoleCode)
	if input.RoleCode != "administrator" && input.RoleCode != "curator" {
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_role"})
		return
	}
	api, ok := h.authoring.(staffRoleAPI)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, apiErrorResponse{Error: "temporarily_unavailable"})
		return
	}
	if err := api.AssignStaffRole(r.Context(), identity.StaffID, targetID, input.RoleCode); err != nil {
		h.writeAPIError(w, err, "назначить роль сотруднику")
		return
	}
	writeJSON(w, http.StatusCreated, staffRoleResponse{StaffID: targetID, RoleCode: input.RoleCode})
}

func (h handlers) staffRevokeRole(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	if !ok || identity.StaffID <= 0 {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
		return
	}
	targetID, ok := parsePositiveID(w, r.PathValue("staffID"))
	if !ok {
		return
	}
	if targetID == identity.StaffID {
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "self_role_change_forbidden"})
		return
	}
	roleCode := strings.TrimSpace(r.PathValue("roleCode"))
	if roleCode != "administrator" && roleCode != "curator" {
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_role"})
		return
	}
	api, ok := h.authoring.(staffRoleAPI)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, apiErrorResponse{Error: "temporarily_unavailable"})
		return
	}
	if err := api.RevokeStaffRole(r.Context(), identity.StaffID, targetID, roleCode); err != nil {
		h.writeAPIError(w, err, "отозвать роль сотрудника")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
