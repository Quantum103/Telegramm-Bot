package httpserver

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"strings"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/service"
)

// StaffManagementAPI exposes only the operations needed by the staff/admin UI.
type StaffManagementAPI interface {
	ListStaffAccounts(context.Context, int64) ([]domain.StaffAccountSummary, error)
	CreateStaffAccount(context.Context, int64, string, string, string) (domain.CreatedStaffAccount, error)
	ListStudents(context.Context, int64) ([]domain.AdminStudent, error)
	AssignCurator(context.Context, int64, int64, int64) error
	EndCuratorAssignment(context.Context, int64, int64) error
	SetAccountStatus(context.Context, int64, int64, string) error
	ResetPassword(context.Context, int64, int64) (string, error)
}

type staffPasswordAPI interface {
	ChangePassword(context.Context, int64, string, string) error
}

type staffCreateAccountRequest struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	RoleCode    string `json:"role_code"`
}

type staffCreateAccountResponse struct {
	Account           domain.StaffAccountSummary `json:"account"`
	TemporaryPassword string                    `json:"temporary_password"`
}

type staffAccountsResponse struct {
	Accounts []domain.StaffAccountSummary `json:"accounts"`
}

type adminStudentsResponse struct {
	Students []domain.AdminStudent `json:"students"`
}

type assignStudentCuratorRequest struct {
	CuratorStaffID int64 `json:"curator_staff_id"`
}

type changeStaffPasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type setStaffStatusRequest struct {
	AccountStatus string `json:"account_status"`
}

type resetStaffPasswordResponse struct {
	TemporaryPassword string `json:"temporary_password"`
}

func (h handlers) staffChangePassword(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	if !ok || identity.StaffID <= 0 {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
		return
	}
	api, ok := h.staffAuth.(staffPasswordAPI)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, apiErrorResponse{Error: "temporarily_unavailable"})
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "content_type_must_be_json"})
		return
	}
	var input changeStaffPasswordRequest
	if err := decodeJSONBodyWithLimit(w, r, &input, maxStaffAdminBodyBytes); err != nil {
		writeInvalidAdminRequest(w, err)
		return
	}
	if err := api.ChangePassword(r.Context(), identity.StaffID, input.CurrentPassword, input.NewPassword); err != nil {
		if errors.Is(err, service.ErrInvalidStaffCredentials) {
			writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "invalid_current_password"})
			return
		}
		h.writeAPIError(w, err, "сменить пароль сотрудника")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h handlers) staffListAccounts(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	if !ok || identity.StaffID <= 0 {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
		return
	}
	if !staffHasRole(identity, "administrator") {
		writeJSON(w, http.StatusForbidden, apiErrorResponse{Error: "insufficient_permissions"})
		return
	}
	accounts, err := h.management.ListStaffAccounts(r.Context(), identity.StaffID)
	if err != nil {
		h.writeAPIError(w, err, "получить список сотрудников")
		return
	}
	if accounts == nil {
		accounts = []domain.StaffAccountSummary{}
	}
	writeJSON(w, http.StatusOK, staffAccountsResponse{Accounts: accounts})
}

func (h handlers) staffCreateAccount(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	if !ok || identity.StaffID <= 0 {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "content_type_must_be_json"})
		return
	}
	var input staffCreateAccountRequest
	if err := decodeJSONBodyWithLimit(w, r, &input, maxStaffAdminBodyBytes); err != nil {
		writeInvalidAdminRequest(w, err)
		return
	}
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Email = strings.TrimSpace(input.Email)
	input.RoleCode = strings.TrimSpace(input.RoleCode)
	created, err := h.management.CreateStaffAccount(r.Context(), identity.StaffID, input.DisplayName, input.Email, input.RoleCode)
	if err != nil {
		h.writeAPIError(w, err, "создать учётную запись сотрудника")
		return
	}
	// Временный пароль возвращается только этому администратору и только в ответе создания.
	writeJSON(w, http.StatusCreated, staffCreateAccountResponse{
		Account: created.Account, TemporaryPassword: created.TemporaryPassword,
	})
}

func (h handlers) staffSetAccountStatus(w http.ResponseWriter, r *http.Request) {
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
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "self_status_change_forbidden"})
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "content_type_must_be_json"})
		return
	}
	var input setStaffStatusRequest
	if err := decodeJSONBodyWithLimit(w, r, &input, maxStaffAdminBodyBytes); err != nil {
		writeInvalidAdminRequest(w, err)
		return
	}
	if err := h.management.SetAccountStatus(r.Context(), identity.StaffID, targetID, strings.TrimSpace(input.AccountStatus)); err != nil {
		h.writeAPIError(w, err, "изменить статус сотрудника")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h handlers) staffResetPassword(w http.ResponseWriter, r *http.Request) {
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
	temporaryPassword, err := h.management.ResetPassword(r.Context(), identity.StaffID, targetID)
	if err != nil {
		h.writeAPIError(w, err, "сбросить пароль сотрудника")
		return
	}
	writeJSON(w, http.StatusOK, resetStaffPasswordResponse{TemporaryPassword: temporaryPassword})
}

func (h handlers) staffListStudents(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	if !ok || identity.StaffID <= 0 {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
		return
	}
	students, err := h.management.ListStudents(r.Context(), identity.StaffID)
	if err != nil {
		h.writeAPIError(w, err, "получить список учеников")
		return
	}
	if students == nil {
		students = []domain.AdminStudent{}
	}
	writeJSON(w, http.StatusOK, adminStudentsResponse{Students: students})
}

func (h handlers) staffAssignStudentCurator(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	if !ok || identity.StaffID <= 0 {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
		return
	}
	studentID, ok := parsePositiveID(w, r.PathValue("studentID"))
	if !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "content_type_must_be_json"})
		return
	}
	var input assignStudentCuratorRequest
	if err := decodeJSONBodyWithLimit(w, r, &input, maxStaffAdminBodyBytes); err != nil {
		writeInvalidAdminRequest(w, err)
		return
	}
	if input.CuratorStaffID <= 0 {
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_input"})
		return
	}
	if err := h.management.AssignCurator(r.Context(), identity.StaffID, studentID, input.CuratorStaffID); err != nil {
		h.writeAPIError(w, err, "назначить куратора ученику")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h handlers) staffEndStudentCuratorAssignment(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	if !ok || identity.StaffID <= 0 {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
		return
	}
	studentID, ok := parsePositiveID(w, r.PathValue("studentID"))
	if !ok {
		return
	}
	if err := h.management.EndCuratorAssignment(r.Context(), identity.StaffID, studentID); err != nil {
		h.writeAPIError(w, err, "завершить назначение куратора")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeInvalidAdminRequest(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeJSON(w, http.StatusRequestEntityTooLarge, apiErrorResponse{Error: "request_too_large"})
		return
	}
	writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_request"})
}
