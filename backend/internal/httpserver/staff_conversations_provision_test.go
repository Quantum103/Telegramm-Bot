package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/auth"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type fakeCuratorConversationProvisioner struct {
	*fakeRealtimeConversationAPI
	conversation domain.Conversation
	err          error
	calls        int
	curatorID    int64
	studentID    int64
}

func (f *fakeCuratorConversationProvisioner) EnsureForCurator(
	_ context.Context, curatorID, studentID int64,
) (domain.Conversation, error) {
	f.calls++
	f.curatorID = curatorID
	f.studentID = studentID
	return f.conversation, f.err
}

func curatorConversationProvisionHandler(
	identity domain.StaffSessionIdentity,
	conversations *fakeCuratorConversationProvisioner,
) http.Handler {
	authAPI := &fakeStaffAuthAPI{authIdentity: identity}
	return newHandlerWithManagementAndConversations(
		fakePinger{}, testLogger(), nil, authAPI, nil, nil, conversations, false, nil,
	)
}

func curatorConversationProvisionRequest(
	handler http.Handler, path, csrfToken string,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost,
		"https://staff.example.test"+path, strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://staff.example.test")
	request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: "valid-session-token"})
	if csrfToken != "" {
		request.AddCookie(&http.Cookie{Name: staffCSRFCookieName, Value: csrfToken})
		request.Header.Set("X-CSRF-Token", csrfToken)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestCuratorCanEnsureAssignedStudentConversation(t *testing.T) {
	const csrfToken = "curator-conversation-provision-csrf-token"
	identity := domain.StaffSessionIdentity{
		StaffID: 42, DisplayName: "Куратор", Email: "curator@example.test",
		Roles: []string{"curator"}, CSRFTokenHash: auth.HashToken(csrfToken),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	provisioner := &fakeCuratorConversationProvisioner{
		fakeRealtimeConversationAPI: &fakeRealtimeConversationAPI{events: make(chan struct{}, 1)},
		conversation: domain.Conversation{
			ID: 301, StudentID: 88, AssignedCuratorStaffID: identity.StaffID,
			Status: "active", CreatedAt: time.Now(), UpdatedAt: time.Now(),
		},
	}
	handler := curatorConversationProvisionHandler(identity, provisioner)

	recorder := curatorConversationProvisionRequest(
		handler, "/api/v1/staff/students/88/conversation", csrfToken,
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response domain.Conversation
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode conversation response: %v", err)
	}
	if response.ID != 301 || response.StudentID != 88 ||
		response.AssignedCuratorStaffID != identity.StaffID || response.Status != "active" {
		t.Fatalf("unexpected conversation response: %+v", response)
	}
	if provisioner.calls != 1 || provisioner.curatorID != identity.StaffID || provisioner.studentID != 88 {
		t.Fatalf("provisioner received wrong actor/student: calls=%d curator=%d student=%d",
			provisioner.calls, provisioner.curatorID, provisioner.studentID)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("conversation response must not be cached")
	}
}

func TestConversationProvisionEndpointRejectsUnauthorizedRequests(t *testing.T) {
	const csrfToken = "curator-conversation-provision-csrf-token"
	tests := []struct {
		name       string
		roles      []string
		csrf       string
		path       string
		wantStatus int
		wantCalls  int
	}{
		{
			name: "administrator without curator role", roles: []string{"administrator"},
			csrf: csrfToken, path: "/api/v1/staff/students/88/conversation",
			wantStatus: http.StatusForbidden,
		},
		{
			name: "missing csrf token", roles: []string{"curator"},
			path: "/api/v1/staff/students/88/conversation",
			wantStatus: http.StatusForbidden,
		},
		{
			name: "invalid student id", roles: []string{"curator"},
			csrf: csrfToken, path: "/api/v1/staff/students/not-a-number/conversation",
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "student belongs to another curator", roles: []string{"curator"},
			csrf: csrfToken, path: "/api/v1/staff/students/88/conversation",
			wantStatus: http.StatusNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identity := domain.StaffSessionIdentity{
				StaffID: 42, DisplayName: "Сотрудник", Email: "staff@example.test",
				Roles: tt.roles, CSRFTokenHash: auth.HashToken(csrfToken),
				ExpiresAt: time.Now().Add(time.Hour),
			}
			provisioner := &fakeCuratorConversationProvisioner{
				fakeRealtimeConversationAPI: &fakeRealtimeConversationAPI{events: make(chan struct{}, 1)},
				err: errors.New("unused"),
			}
			if tt.name == "student belongs to another curator" {
				provisioner.err = domain.ErrNotFound
			}
			handler := curatorConversationProvisionHandler(identity, provisioner)
			recorder := curatorConversationProvisionRequest(handler, tt.path, tt.csrf)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
			if provisioner.calls != tt.wantCalls {
				t.Fatalf("provisioner calls = %d, want %d", provisioner.calls, tt.wantCalls)
			}
		})
	}
}
