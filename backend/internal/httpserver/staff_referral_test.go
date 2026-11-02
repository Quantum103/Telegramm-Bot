package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type fakeStaffReferralLinkAPI struct {
	result   domain.StaffReferralCode
	err      error
	calls    int
	staffID  int64
}

func (f *fakeStaffReferralLinkAPI) GetOrCreateReferralCode(_ context.Context, staffID int64) (domain.StaffReferralCode, error) {
	f.calls++
	f.staffID = staffID
	return f.result, f.err
}

type fakeTelegramBotUsernameProvider struct {
	username string
	err      error
	calls    int
}

func (f *fakeTelegramBotUsernameProvider) GetBotUsername(context.Context) (string, error) {
	f.calls++
	return f.username, f.err
}

func referralLinkTestHandler(identity domain.StaffSessionIdentity, referrals *fakeStaffReferralLinkAPI, username *fakeTelegramBotUsernameProvider) http.Handler {
	authAPI := &fakeStaffAuthAPI{authIdentity: identity}
	return NewHandlerWithStaffAuthAndAuthoringAndManagementAndLoginLimiterAndConversationsAndTelegramAndCourseLearningAndReferralLinks(
		fakePinger{}, testLogger(), nil, authAPI, nil, nil, nil, nil, nil,
		referrals, username, newStaffLoginLimiter(), true,
	)
}

func referralLinkRequest(handler http.Handler) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "https://staff.example.test/api/v1/staff/referral-link", nil)
	request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: "session-token"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestCuratorCanGetPersonalTelegramReferralLink(t *testing.T) {
	referrals := &fakeStaffReferralLinkAPI{
		result: domain.StaffReferralCode{StaffID: 42, Code: "c_42"},
	}
	username := &fakeTelegramBotUsernameProvider{username: "BibleStudyBot"}
	handler := referralLinkTestHandler(domain.StaffSessionIdentity{
		StaffID: 42, DisplayName: "Куратор", Email: "curator@example.test",
		Roles: []string{"curator"}, ExpiresAt: time.Now().Add(time.Hour),
	}, referrals, username)

	recorder := referralLinkRequest(handler)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var body staffReferralLinkResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode referral link response: %v", err)
	}
	if body.Code != "c_42" {
		t.Fatalf("referral code = %q, want c_42", body.Code)
	}
	if body.URL != "https://t.me/BibleStudyBot?start=c_42" {
		t.Fatalf("referral URL = %q, want Telegram deep link", body.URL)
	}
	if referrals.calls != 1 || referrals.staffID != 42 {
		t.Fatalf("referral service calls=%d staffID=%d, want 1 call for session staff 42", referrals.calls, referrals.staffID)
	}
	if username.calls != 1 {
		t.Fatalf("bot username lookups = %d, want 1", username.calls)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("referral link response must not be cached")
	}
}

func TestAdministratorWithoutCuratorRoleCannotGetReferralLink(t *testing.T) {
	referrals := &fakeStaffReferralLinkAPI{
		result: domain.StaffReferralCode{StaffID: 42, Code: "c_42"},
	}
	username := &fakeTelegramBotUsernameProvider{username: "BibleStudyBot"}
	handler := referralLinkTestHandler(domain.StaffSessionIdentity{
		StaffID: 7, DisplayName: "Администратор", Email: "admin@example.test",
		Roles: []string{"administrator"}, ExpiresAt: time.Now().Add(time.Hour),
	}, referrals, username)

	recorder := referralLinkRequest(handler)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
	if referrals.calls != 0 || username.calls != 0 {
		t.Fatalf("unauthorized role triggered referral lookup: referrals=%d username=%d", referrals.calls, username.calls)
	}
}

func TestReferralCodeRemainsAvailableWhenTelegramUsernameLookupFails(t *testing.T) {
	referrals := &fakeStaffReferralLinkAPI{
		result: domain.StaffReferralCode{StaffID: 42, Code: "c_42"},
	}
	username := &fakeTelegramBotUsernameProvider{err: errors.New("Telegram temporarily unavailable")}
	handler := referralLinkTestHandler(domain.StaffSessionIdentity{
		StaffID: 42, DisplayName: "Куратор", Email: "curator@example.test",
		Roles: []string{"curator"}, ExpiresAt: time.Now().Add(time.Hour),
	}, referrals, username)

	recorder := referralLinkRequest(handler)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var body staffReferralLinkResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode referral link response: %v", err)
	}
	if body.Code != "c_42" || body.URL != "" {
		t.Fatalf("response = %+v, want code without URL while Telegram is unavailable", body)
	}
}
