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
	"github.com/Quantum103/Telegramm-Bot/backend/internal/service"
)

type fakeStaffAuthAPI struct {
	loginResult  service.StaffLoginResult
	loginErr     error
	loginCalls   int
	authIdentity domain.StaffSessionIdentity
	authErr      error
	authCalls    int
	authToken    string
	logoutToken  string
	logoutErr    error
	logoutCalls  int
}

func (f *fakeStaffAuthAPI) Login(_ context.Context, email, password string) (service.StaffLoginResult, error) {
	f.loginCalls++
	if email == "" || password == "" {
		return service.StaffLoginResult{}, service.ErrInvalidStaffCredentials
	}
	return f.loginResult, f.loginErr
}

func (f *fakeStaffAuthAPI) AuthenticateSession(_ context.Context, token string) (domain.StaffSessionIdentity, error) {
	f.authCalls++
	f.authToken = token
	return f.authIdentity, f.authErr
}

func (f *fakeStaffAuthAPI) Logout(_ context.Context, token string) error {
	f.logoutCalls++
	f.logoutToken = token
	return f.logoutErr
}

func TestStaffLoginSetsSecureCookiesWithoutReturningSecrets(t *testing.T) {
	expires := time.Now().Add(12 * time.Hour).UTC()
	api := &fakeStaffAuthAPI{loginResult: service.StaffLoginResult{
		StaffID: 7, DisplayName: "Admin", Email: "admin@example.test",
		SessionToken: "raw-session-secret", CSRFToken: "raw-csrf-secret", ExpiresAt: expires,
	}}
	handler := NewHandlerWithStaffAuth(fakePinger{}, testLogger(), nil, api, true)
	request := httptest.NewRequest(http.MethodPost, "http://admin.example.test/api/v1/staff/auth/login",
		strings.NewReader(`{"email":"admin@example.test","password":"correct horse battery staple"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://admin.example.test")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if api.loginCalls != 1 {
		t.Fatalf("login calls = %d, want 1", api.loginCalls)
	}
	if strings.Contains(recorder.Body.String(), "raw-session-secret") || strings.Contains(recorder.Body.String(), "raw-csrf-secret") {
		t.Fatal("login response body leaked an authentication secret")
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if _, exists := body["session_token"]; exists {
		t.Fatal("login response must not contain session_token")
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("Set-Cookie count = %d, want 2", len(cookies))
	}
	byName := make(map[string]*http.Cookie, len(cookies))
	for _, cookie := range cookies {
		byName[cookie.Name] = cookie
	}
	session := byName[staffSessionCookieName]
	csrf := byName[staffCSRFCookieName]
	if session == nil || csrf == nil {
		t.Fatalf("expected session and CSRF cookies, got names=%v", cookieNames(cookies))
	}
	if session.Value != "raw-session-secret" || !session.HttpOnly || !session.Secure ||
		session.Path != "/" || session.SameSite != http.SameSiteStrictMode {
		t.Errorf("session cookie has unsafe attributes: %+v", session)
	}
	if csrf.Value != "raw-csrf-secret" || csrf.HttpOnly || !csrf.Secure ||
		csrf.Path != "/" || csrf.SameSite != http.SameSiteStrictMode {
		t.Errorf("CSRF cookie attributes do not support the same-site CSRF-header pattern: %+v", csrf)
	}
}

func TestStaffLoginRejectsInvalidCredentialsGenerically(t *testing.T) {
	api := &fakeStaffAuthAPI{loginErr: service.ErrInvalidStaffCredentials}
	handler := NewHandlerWithStaffAuth(fakePinger{}, testLogger(), nil, api, true)
	request := httptest.NewRequest(http.MethodPost, "http://admin.example.test/api/v1/staff/auth/login",
		strings.NewReader(`{"email":"unknown@example.test","password":"wrong"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://admin.example.test")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "invalid_credentials") {
		t.Fatalf("status/body = %d/%q, want generic invalid_credentials", recorder.Code, recorder.Body.String())
	}
	if len(recorder.Result().Cookies()) != 0 {
		t.Fatal("failed login unexpectedly set authentication cookies")
	}
}

func TestStaffLoginRejectsMalformedOrCrossOriginRequests(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType string
		origin      string
		wantStatus  int
	}{
		{name: "malformed json", body: `{"email":`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "unknown field", body: `{"email":"a","password":"b","role":"administrator"}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "wrong content type", body: `{}`, contentType: "text/plain", wantStatus: http.StatusUnsupportedMediaType},
		{name: "cross origin", body: `{"email":"a","password":"b"}`, contentType: "application/json", origin: "https://attacker.example", wantStatus: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeStaffAuthAPI{}
			handler := NewHandlerWithStaffAuth(fakePinger{}, testLogger(), nil, api, true)
			request := httptest.NewRequest(http.MethodPost, "http://admin.example.test/api/v1/staff/auth/login", strings.NewReader(tt.body))
			request.Header.Set("Content-Type", tt.contentType)
			if tt.origin != "" {
				request.Header.Set("Origin", tt.origin)
			} else {
				request.Header.Set("Origin", "http://admin.example.test")
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
			if api.loginCalls != 0 {
				t.Fatal("invalid request reached authentication service")
			}
		})
	}
}

func TestStaffLogoutRevokesSessionAndClearsCookies(t *testing.T) {
	api := &fakeStaffAuthAPI{authIdentity: domain.StaffSessionIdentity{
		StaffID: 7, DisplayName: "Admin", Email: "admin@example.test",
		CSRFTokenHash: auth.HashToken("raw-csrf-secret"), ExpiresAt: time.Now().Add(time.Hour),
	}}
	handler := NewHandlerWithStaffAuth(fakePinger{}, testLogger(), nil, api, true)
	request := httptest.NewRequest(http.MethodPost, "https://admin.example.test/api/v1/staff/auth/logout", nil)
	request.Header.Set("Origin", "https://admin.example.test")
	request.Header.Set("X-CSRF-Token", "raw-csrf-secret")
	request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: "raw-session-secret"})
	request.AddCookie(&http.Cookie{Name: staffCSRFCookieName, Value: "raw-csrf-secret"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusNoContent, recorder.Body.String())
	}
	if api.logoutCalls != 1 || api.logoutToken != "raw-session-secret" {
		t.Fatalf("logout service call = (%d, %q), want (1, token)", api.logoutCalls, api.logoutToken)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("clear-cookie count = %d, want 2", len(cookies))
	}
	for _, cookie := range cookies {
		if cookie.MaxAge != -1 || cookie.Value != "" || !cookie.Secure || cookie.Path != "/" || cookie.SameSite != http.SameSiteStrictMode {
			t.Errorf("cookie not securely expired: %+v", cookie)
		}
		if cookie.Name == staffSessionCookieName && !cookie.HttpOnly {
			t.Error("cleared session cookie did not retain HttpOnly")
		}
	}
}

func TestStaffLogoutClearsCookiesWhenRevocationFails(t *testing.T) {
	api := &fakeStaffAuthAPI{
		logoutErr: errors.New("database unavailable"),
		authIdentity: domain.StaffSessionIdentity{
			StaffID: 7, Email: "admin@example.test",
			CSRFTokenHash: auth.HashToken("raw-csrf-secret"), ExpiresAt: time.Now().Add(time.Hour),
		},
	}
	handler := NewHandlerWithStaffAuth(fakePinger{}, testLogger(), nil, api, false)
	request := httptest.NewRequest(http.MethodPost, "http://admin.example.test/api/v1/staff/auth/logout", nil)
	request.Header.Set("Origin", "http://admin.example.test")
	request.Header.Set("X-CSRF-Token", "raw-csrf-secret")
	request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: "raw-session-secret"})
	request.AddCookie(&http.Cookie{Name: staffCSRFCookieName, Value: "raw-csrf-secret"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if len(recorder.Result().Cookies()) != 2 {
		t.Fatal("logout failure should still clear local browser cookies")
	}
}

func TestStaffLogoutRejectsCrossOriginBeforeRevoking(t *testing.T) {
	api := &fakeStaffAuthAPI{}
	handler := NewHandlerWithStaffAuth(fakePinger{}, testLogger(), nil, api, true)
	request := httptest.NewRequest(http.MethodPost, "https://admin.example.test/api/v1/staff/auth/logout", nil)
	request.Header.Set("Origin", "https://attacker.example")
	request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: "raw-session-secret"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden || api.logoutCalls != 0 {
		t.Fatalf("status/calls = %d/%d, want 403/0", recorder.Code, api.logoutCalls)
	}
}

func cookieNames(cookies []*http.Cookie) []string {
	names := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		names = append(names, cookie.Name)
	}
	return names
}

func TestStaffMeRequiresValidSession(t *testing.T) {
	api := &fakeStaffAuthAPI{authErr: service.ErrInvalidStaffSession}
	handler := NewHandlerWithStaffAuth(fakePinger{}, testLogger(), nil, api, true)
	request := httptest.NewRequest(http.MethodGet, "https://admin.example.test/api/v1/staff/auth/me", nil)
	request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: strings.Repeat("A", 43)})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "unauthorized") {
		t.Fatalf("status/body = %d/%q, want generic unauthorized", recorder.Code, recorder.Body.String())
	}
}

func TestStaffMeReturnsValidatedIdentityWithoutSecrets(t *testing.T) {
	api := &fakeStaffAuthAPI{authIdentity: domain.StaffSessionIdentity{
		StaffID: 12, DisplayName: "Curator", Email: "curator@example.test",
		CSRFTokenHash: auth.HashToken("csrf-secret"), ExpiresAt: time.Now().Add(time.Hour),
	}}
	handler := NewHandlerWithStaffAuth(fakePinger{}, testLogger(), nil, api, true)
	request := httptest.NewRequest(http.MethodGet, "https://admin.example.test/api/v1/staff/auth/me", nil)
	request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: strings.Repeat("A", 43)})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "curator@example.test") || strings.Contains(recorder.Body.String(), "csrf-secret") {
		t.Fatalf("unexpected /me response: %s", recorder.Body.String())
	}
	if api.authCalls != 1 {
		t.Fatalf("AuthenticateSession calls = %d, want 1", api.authCalls)
	}
}

func TestStaffSessionMiddlewareProtectsUnsafeMethodsWithOriginAndCSRF(t *testing.T) {
	csrf := "csrf-secret"
	validIdentity := domain.StaffSessionIdentity{
		StaffID: 12, DisplayName: "Curator", Email: "curator@example.test",
		CSRFTokenHash: auth.HashToken(csrf), ExpiresAt: time.Now().Add(time.Hour),
	}
	tests := []struct {
		name       string
		origin     string
		csrfCookie string
		csrfHeader string
		authErr    error
		wantStatus int
	}{
		{name: "valid request", origin: "https://admin.example.test", csrfCookie: csrf, csrfHeader: csrf, wantStatus: http.StatusNoContent},
		{name: "missing origin", csrfCookie: csrf, csrfHeader: csrf, wantStatus: http.StatusForbidden},
		{name: "cross origin", origin: "https://attacker.example", csrfCookie: csrf, csrfHeader: csrf, wantStatus: http.StatusForbidden},
		{name: "missing CSRF header", origin: "https://admin.example.test", csrfCookie: csrf, wantStatus: http.StatusForbidden},
		{name: "cookie/header mismatch", origin: "https://admin.example.test", csrfCookie: "other", csrfHeader: csrf, wantStatus: http.StatusForbidden},
		{name: "unknown session", origin: "https://admin.example.test", csrfCookie: csrf, csrfHeader: csrf, authErr: service.ErrInvalidStaffSession, wantStatus: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeStaffAuthAPI{authIdentity: validIdentity, authErr: tt.authErr}
			h := handlers{logger: testLogger(), staffAuth: api, cookieSecure: true}
			called := false
			protected := h.staffSessionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(http.MethodPost, "https://admin.example.test/api/v1/staff/protected", nil)
			if tt.origin != "" {
				request.Header.Set("Origin", tt.origin)
			}
			if tt.csrfHeader != "" {
				request.Header.Set("X-CSRF-Token", tt.csrfHeader)
			}
			request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: strings.Repeat("A", 43)})
			if tt.csrfCookie != "" {
				request.AddCookie(&http.Cookie{Name: staffCSRFCookieName, Value: tt.csrfCookie})
			}
			recorder := httptest.NewRecorder()
			protected.ServeHTTP(recorder, request)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
			if called != (tt.wantStatus == http.StatusNoContent) {
				t.Fatalf("protected handler called = %v for status %d", called, recorder.Code)
			}
		})
	}
}

func TestStaffLoginRateLimitsRepeatedFailures(t *testing.T) {
	api := &fakeStaffAuthAPI{loginErr: service.ErrInvalidStaffCredentials}
	handler := NewHandlerWithStaffAuth(fakePinger{}, testLogger(), nil, api, true)

	makeRequest := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "https://admin.example.test/api/v1/staff/auth/login",
			strings.NewReader(`{"email":"admin@example.test","password":"wrong"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "https://admin.example.test")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	for attempt := 0; attempt < staffLoginAccountFailureLimit; attempt++ {
		response := makeRequest()
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want %d; body=%s", attempt+1, response.Code, http.StatusUnauthorized, response.Body.String())
		}
	}
	response := makeRequest()
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked attempt status = %d, want %d; body=%s", response.Code, http.StatusTooManyRequests, response.Body.String())
	}
	if api.loginCalls != staffLoginAccountFailureLimit {
		t.Fatalf("authentication service calls = %d, want %d; blocked request reached service", api.loginCalls, staffLoginAccountFailureLimit)
	}
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("rate-limited response is missing Retry-After")
	}
	if !strings.Contains(response.Body.String(), "rate_limited") {
		t.Fatalf("rate-limit response body = %q, want generic rate_limited", response.Body.String())
	}
}

func TestRegisteredStaffRoutesHaveExpectedGuards(t *testing.T) {
	api := &fakeStaffAuthAPI{loginErr: service.ErrInvalidStaffCredentials, authErr: service.ErrInvalidStaffSession}
	handler := NewHandlerWithStaffAuth(fakePinger{}, testLogger(), nil, api, true)
	tests := []struct { name, method, path, body, content string; wantStatus int }{
		{name:"login requires same origin", method:http.MethodPost, path:"/api/v1/staff/auth/login", body:`{"email":"a@example.test","password":"bad-password"}`, content:"application/json", wantStatus:http.StatusForbidden},
		{name:"me requires session", method:http.MethodGet, path:"/api/v1/staff/auth/me", wantStatus:http.StatusUnauthorized},
		{name:"logout requires same origin", method:http.MethodPost, path:"/api/v1/staff/auth/logout", wantStatus:http.StatusForbidden},
		{name:"admin namespace requires session before route resolution", method:http.MethodPost, path:"/api/v1/staff/admin/courses", wantStatus:http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name,func(t *testing.T){
			request:=httptest.NewRequest(tt.method,"https://admin.example.test"+tt.path,strings.NewReader(tt.body))
			if tt.content!="" { request.Header.Set("Content-Type",tt.content) }
			recorder:=httptest.NewRecorder(); handler.ServeHTTP(recorder,request)
			if recorder.Code!=tt.wantStatus { t.Fatalf("status = %d, want %d; body=%s",recorder.Code,tt.wantStatus,recorder.Body.String()) }
		})
	}
	if api.loginCalls!=0 { t.Fatalf("login service invoked %d times despite missing Origin",api.loginCalls) }
	if api.logoutCalls!=0 { t.Fatalf("logout service invoked %d times despite missing Origin",api.logoutCalls) }
}
