package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/auth"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type fakeStudentLabelManagement struct {
	*fakeCourseLearningStaffManagement
	actorID   int64
	studentID int64
	label     string
	err       error
	calls     int
}

func (f *fakeStudentLabelManagement) SetStudentLabel(_ context.Context, actorID, studentID int64, label string) error {
	f.calls++
	f.actorID, f.studentID, f.label = actorID, studentID, label
	return f.err
}

func TestStaffSetStudentLabelRequiresStaffRoleAndCSRF(t *testing.T) {
	const csrfToken = "staff-student-label-csrf-token"
	for _, tt := range []struct {
		name       string
		roles      []string
		csrf       bool
		wantStatus int
		wantCalls  int
	}{
		{name: "administrator", roles: []string{"administrator"}, csrf: true, wantStatus: http.StatusNoContent, wantCalls: 1},
		{name: "curator", roles: []string{"curator"}, csrf: true, wantStatus: http.StatusNoContent, wantCalls: 1},
		{name: "no role", roles: []string{"student"}, csrf: true, wantStatus: http.StatusForbidden},
		{name: "missing csrf", roles: []string{"curator"}, wantStatus: http.StatusForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			identity := domain.StaffSessionIdentity{
				StaffID: 42, DisplayName: "Staff", Email: "staff@example.test",
				Roles: tt.roles, CSRFTokenHash: auth.HashToken(csrfToken),
				ExpiresAt: time.Now().Add(time.Hour),
			}
			authAPI := &fakeStaffAuthAPI{authIdentity: identity}
			management := &fakeStudentLabelManagement{
				fakeCourseLearningStaffManagement: &fakeCourseLearningStaffManagement{},
			}
			handler := NewHandlerWithStaffAuthAndAuthoringAndManagementAndLoginLimiterAndConversationsAndTelegramAndCourseLearning(
				fakePinger{}, testLogger(), nil, authAPI, nil, management, nil, nil, nil, newStaffLoginLimiter(), true,
			)
			request := httptest.NewRequest(http.MethodPut,
				"https://staff.example.test/api/v1/staff/students/81/label",
				strings.NewReader(`{"label":"  Имя для панели  "}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", "https://staff.example.test")
			request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: "valid-session-token"})
			if tt.csrf {
				request.AddCookie(&http.Cookie{Name: staffCSRFCookieName, Value: csrfToken})
				request.Header.Set("X-CSRF-Token", csrfToken)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
			if management.calls != tt.wantCalls {
				t.Fatalf("setter calls = %d, want %d", management.calls, tt.wantCalls)
			}
			if tt.wantCalls > 0 && (management.actorID != identity.StaffID ||
				management.studentID != 81 || management.label != "  Имя для панели  ") {
				t.Fatalf("unexpected setter arguments: %+v", management)
			}
		})
	}
}

func TestStaffSetStudentLabelRejectsMissingLabelAndInvalidStudentID(t *testing.T) {
	const csrfToken = "staff-student-label-csrf-token"
	identity := domain.StaffSessionIdentity{
		StaffID: 42, DisplayName: "Staff", Email: "staff@example.test",
		Roles: []string{"curator"}, CSRFTokenHash: auth.HashToken(csrfToken),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	management := &fakeStudentLabelManagement{
		fakeCourseLearningStaffManagement: &fakeCourseLearningStaffManagement{},
	}
	handler := NewHandlerWithStaffAuthAndAuthoringAndManagementAndLoginLimiterAndConversationsAndTelegramAndCourseLearning(
		fakePinger{}, testLogger(), nil, &fakeStaffAuthAPI{authIdentity: identity},
		nil, management, nil, nil, nil, newStaffLoginLimiter(), true,
	)
	for _, tt := range []struct {
		path, body string
		wantStatus int
	}{
		{path: "/api/v1/staff/students/not-id/label", body: `{"label":"Имя"}`, wantStatus: http.StatusBadRequest},
		{path: "/api/v1/staff/students/81/label", body: `{}`, wantStatus: http.StatusBadRequest},
	} {
		request := httptest.NewRequest(http.MethodPut, "https://staff.example.test"+tt.path, strings.NewReader(tt.body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "https://staff.example.test")
		request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: "valid-session-token"})
		request.AddCookie(&http.Cookie{Name: staffCSRFCookieName, Value: csrfToken})
		request.Header.Set("X-CSRF-Token", csrfToken)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != tt.wantStatus {
			t.Fatalf("%s status = %d, want %d; body=%s", tt.path, recorder.Code, tt.wantStatus, recorder.Body.String())
		}
	}
	if management.calls != 0 {
		t.Fatalf("invalid request called store %d times", management.calls)
	}
}
