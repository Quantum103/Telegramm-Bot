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
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		content    string
		wantStatus int
	}{
		{name: "login requires same origin", method: http.MethodPost, path: "/api/v1/staff/auth/login", body: `{"email":"a@example.test","password":"bad-password"}`, content: "application/json", wantStatus: http.StatusForbidden},
		{name: "me requires session", method: http.MethodGet, path: "/api/v1/staff/auth/me", wantStatus: http.StatusUnauthorized},
		{name: "logout requires same origin", method: http.MethodPost, path: "/api/v1/staff/auth/logout", wantStatus: http.StatusForbidden},
		{name: "admin namespace requires session before route resolution", method: http.MethodPost, path: "/api/v1/staff/admin/courses", wantStatus: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(tt.method, "https://admin.example.test"+tt.path, strings.NewReader(tt.body))
			if tt.content != "" {
				request.Header.Set("Content-Type", tt.content)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
		})
	}
	if api.loginCalls != 0 {
		t.Fatalf("login service invoked %d times despite missing Origin", api.loginCalls)
	}
	if api.logoutCalls != 0 {
		t.Fatalf("logout service invoked %d times despite missing Origin", api.logoutCalls)
	}
}

type fakeStaffAuthoringAPI struct {
	listCoursesCalls       int
	listCoursesResult      []domain.Course
	listCoursesErr         error
	listLessonsCalls       int
	listLessonsCourseID    int64
	listLessonsResult      []domain.StaffLesson
	listLessonsErr         error
	listQuestionsCalls     int
	listQuestionsVersionID int64
	listQuestionsResult    []domain.LessonQuestion
	listQuestionsErr       error
	createCourseCalls      int
	createCourseParams     domain.CreateCourseParams
	createCourseResult     domain.Course
	createCourseErr        error
	createLessonCalls      int
	createLessonParams     domain.CreateLessonParams
	createLessonResult     domain.Lesson
	createLessonErr        error
	createRevisionCalls    int
	createRevisionLessonID int64
	createRevisionStaffID  int64
	createRevisionResult   domain.Lesson
	createRevisionErr      error
	createQuestionCalls    int
	createQuestionParams   domain.CreateQuestionParams
	createQuestionResult   domain.LessonQuestion
	createQuestionErr      error
	publishVersionCalls    int
	publishVersionID       int64
	publishVersionStaffID  int64
	publishVersionErr      error
	publishCourseCalls     int
	publishCourseID        int64
	publishCourseStaffID   int64
	publishCourseErr       error
}

func (f *fakeStaffAuthoringAPI) ListCoursesForStaff(_ context.Context) ([]domain.Course, error) {
	f.listCoursesCalls++
	return f.listCoursesResult, f.listCoursesErr
}

func (f *fakeStaffAuthoringAPI) ListLessonsForStaffByCourseID(_ context.Context, courseID int64) ([]domain.StaffLesson, error) {
	f.listLessonsCalls++
	f.listLessonsCourseID = courseID
	return f.listLessonsResult, f.listLessonsErr
}

func (f *fakeStaffAuthoringAPI) ListQuestionsForStaffVersion(_ context.Context, versionID int64) ([]domain.LessonQuestion, error) {
	f.listQuestionsCalls++
	f.listQuestionsVersionID = versionID
	return f.listQuestionsResult, f.listQuestionsErr
}

func (f *fakeStaffAuthoringAPI) CreateDraftCourse(_ context.Context, params domain.CreateCourseParams) (domain.Course, error) {
	f.createCourseCalls++
	f.createCourseParams = params
	return f.createCourseResult, f.createCourseErr
}

func (f *fakeStaffAuthoringAPI) CreateDraftLesson(_ context.Context, params domain.CreateLessonParams) (domain.Lesson, error) {
	f.createLessonCalls++
	f.createLessonParams = params
	return f.createLessonResult, f.createLessonErr
}

func (f *fakeStaffAuthoringAPI) CreateDraftRevision(_ context.Context, lessonID, staffID int64) (domain.Lesson, error) {
	f.createRevisionCalls++
	f.createRevisionLessonID = lessonID
	f.createRevisionStaffID = staffID
	return f.createRevisionResult, f.createRevisionErr
}

func (f *fakeStaffAuthoringAPI) CreateQuestion(_ context.Context, params domain.CreateQuestionParams) (domain.LessonQuestion, error) {
	f.createQuestionCalls++
	f.createQuestionParams = params
	return f.createQuestionResult, f.createQuestionErr
}

func (f *fakeStaffAuthoringAPI) PublishLessonVersion(_ context.Context, versionID, staffID int64) error {
	f.publishVersionCalls++
	f.publishVersionID = versionID
	f.publishVersionStaffID = staffID
	return f.publishVersionErr
}

func (f *fakeStaffAuthoringAPI) PublishCourse(_ context.Context, courseID, staffID int64) error {
	f.publishCourseCalls++
	f.publishCourseID = courseID
	f.publishCourseStaffID = staffID
	return f.publishCourseErr
}

func TestStaffCreateDraftCourseUsesAuthenticatedActorAndCSRF(t *testing.T) {
	csrfToken := "csrf-test-token-0123456789"
	identity := domain.StaffSessionIdentity{
		StaffID: 42, DisplayName: "Admin", Email: "admin@example.test",
		CSRFTokenHash: auth.HashToken(csrfToken), ExpiresAt: time.Now().Add(time.Hour),
	}
	authAPI := &fakeStaffAuthAPI{authIdentity: identity}
	createdAt := time.Now().UTC()
	authoring := &fakeStaffAuthoringAPI{createCourseResult: domain.Course{
		ID: 99, Slug: "new-course", Title: "New course", Status: "draft",
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}}
	handler := NewHandlerWithStaffAuthAndAuthoringAndLoginLimiter(
		fakePinger{}, testLogger(), nil, authAPI, authoring, newStaffLoginLimiter(), true,
	)

	makeRequest := func(includeCSRF bool) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "https://admin.example.test/api/v1/staff/admin/courses",
			strings.NewReader(`{"slug":"new-course","title":"New course","description":"Draft description"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "https://admin.example.test")
		request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: strings.Repeat("A", 43)})
		if includeCSRF {
			request.AddCookie(&http.Cookie{Name: staffCSRFCookieName, Value: csrfToken})
			request.Header.Set("X-CSRF-Token", csrfToken)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	withoutCSRF := makeRequest(false)
	if withoutCSRF.Code != http.StatusForbidden {
		t.Fatalf("request without CSRF status = %d, want %d; body=%s", withoutCSRF.Code, http.StatusForbidden, withoutCSRF.Body.String())
	}
	if authoring.createCourseCalls != 0 {
		t.Fatal("authoring service was called without a valid CSRF token")
	}

	response := makeRequest(true)
	if response.Code != http.StatusCreated {
		t.Fatalf("valid request status = %d, want %d; body=%s", response.Code, http.StatusCreated, response.Body.String())
	}
	if authoring.createCourseCalls != 1 {
		t.Fatalf("create course calls = %d, want 1", authoring.createCourseCalls)
	}
	if authoring.createCourseParams.CreatedByStaffID != identity.StaffID {
		t.Fatalf("CreatedByStaffID = %d, want authenticated staff ID %d", authoring.createCourseParams.CreatedByStaffID, identity.StaffID)
	}
	if strings.Contains(response.Body.String(), csrfToken) || strings.Contains(response.Body.String(), "staff_session") {
		t.Fatal("course creation response leaked authentication material")
	}
}

func TestStaffCreateDraftLessonUsesAuthenticatedActorAndCoursePath(t *testing.T) {
	csrfToken := "csrf-lesson-test-token-0123456789"
	identity := domain.StaffSessionIdentity{
		StaffID: 42, DisplayName: "Admin", Email: "admin@example.test",
		CSRFTokenHash: auth.HashToken(csrfToken), ExpiresAt: time.Now().Add(time.Hour),
	}
	authAPI := &fakeStaffAuthAPI{authIdentity: identity}
	authoring := &fakeStaffAuthoringAPI{createLessonResult: domain.Lesson{
		ID: 123, CourseID: 77, CourseTitle: "Draft course", Position: 1,
		LessonVersionID: 124, VersionNumber: 1, Title: "First lesson",
	}}
	handler := NewHandlerWithStaffAuthAndAuthoringAndLoginLimiter(
		fakePinger{}, testLogger(), nil, authAPI, authoring, newStaffLoginLimiter(), true,
	)

	makeRequest := func(body string, includeCSRF bool) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "https://admin.example.test/api/v1/staff/admin/courses/77/lessons",
			strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "https://admin.example.test")
		request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: strings.Repeat("A", 43)})
		if includeCSRF {
			request.AddCookie(&http.Cookie{Name: staffCSRFCookieName, Value: csrfToken})
			request.Header.Set("X-CSRF-Token", csrfToken)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	withoutCSRF := makeRequest(`{"title":"First lesson"}`, false)
	if withoutCSRF.Code != http.StatusForbidden {
		t.Fatalf("request without CSRF status = %d, want %d; body=%s", withoutCSRF.Code, http.StatusForbidden, withoutCSRF.Body.String())
	}
	if authoring.createLessonCalls != 0 {
		t.Fatal("authoring service was called without a valid CSRF token")
	}

	withUntrustedCourseID := makeRequest(`{"title":"First lesson","course_id":999}`, true)
	if withUntrustedCourseID.Code != http.StatusBadRequest {
		t.Fatalf("request with caller-supplied course_id status = %d, want %d; body=%s", withUntrustedCourseID.Code, http.StatusBadRequest, withUntrustedCourseID.Body.String())
	}
	if authoring.createLessonCalls != 0 {
		t.Fatal("request with unknown course_id field reached the authoring service")
	}

	response := makeRequest(`{"title":"First lesson","description":"Draft description"}`, true)
	if response.Code != http.StatusCreated {
		t.Fatalf("valid request status = %d, want %d; body=%s", response.Code, http.StatusCreated, response.Body.String())
	}
	if authoring.createLessonCalls != 1 {
		t.Fatalf("create lesson calls = %d, want 1", authoring.createLessonCalls)
	}
	if authoring.createLessonParams.CourseID != 77 {
		t.Fatalf("CourseID = %d, want path course ID 77", authoring.createLessonParams.CourseID)
	}
	if authoring.createLessonParams.CreatedByStaffID != identity.StaffID {
		t.Fatalf("CreatedByStaffID = %d, want authenticated staff ID %d", authoring.createLessonParams.CreatedByStaffID, identity.StaffID)
	}
	if strings.Contains(response.Body.String(), csrfToken) || strings.Contains(response.Body.String(), "staff_session") {
		t.Fatal("lesson creation response leaked authentication material")
	}
}

func TestStaffCreateQuestionUsesAuthenticatedActorAndVersionPath(t *testing.T) {
	csrfToken := "csrf-question-test-token-0123456789"
	identity := domain.StaffSessionIdentity{
		StaffID: 42, DisplayName: "Admin", Email: "admin@example.test",
		CSRFTokenHash: auth.HashToken(csrfToken), ExpiresAt: time.Now().Add(time.Hour),
	}
	authAPI := &fakeStaffAuthAPI{authIdentity: identity}
	createdAt := time.Now().UTC()
	authoring := &fakeStaffAuthoringAPI{createQuestionResult: domain.LessonQuestion{
		ID: 301, LessonVersionID: 124, QuestionNumber: 1, Prompt: "Question text",
		ExpectedAnswer: "Internal expected answer", AnswerType: "text", CreatedAt: createdAt, UpdatedAt: createdAt,
	}}
	handler := NewHandlerWithStaffAuthAndAuthoringAndLoginLimiter(
		fakePinger{}, testLogger(), nil, authAPI, authoring, newStaffLoginLimiter(), true,
	)

	makeRequest := func(body string, includeCSRF bool) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "https://admin.example.test/api/v1/staff/admin/lesson-versions/124/questions",
			strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "https://admin.example.test")
		request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: strings.Repeat("A", 43)})
		if includeCSRF {
			request.AddCookie(&http.Cookie{Name: staffCSRFCookieName, Value: csrfToken})
			request.Header.Set("X-CSRF-Token", csrfToken)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	payload := `{"question_number":1,"prompt":"Question text","expected_answer":"Internal expected answer","answer_type":"text"}`
	withoutCSRF := makeRequest(payload, false)
	if withoutCSRF.Code != http.StatusForbidden {
		t.Fatalf("request without CSRF status = %d, want %d; body=%s", withoutCSRF.Code, http.StatusForbidden, withoutCSRF.Body.String())
	}
	if authoring.createQuestionCalls != 0 {
		t.Fatal("authoring service was called without a valid CSRF token")
	}

	withUntrustedVersionID := makeRequest(`{"lesson_version_id":999,"question_number":1,"prompt":"Question text","expected_answer":"Answer","answer_type":"text"}`, true)
	if withUntrustedVersionID.Code != http.StatusBadRequest {
		t.Fatalf("request with caller-supplied lesson_version_id status = %d, want %d; body=%s", withUntrustedVersionID.Code, http.StatusBadRequest, withUntrustedVersionID.Body.String())
	}
	if authoring.createQuestionCalls != 0 {
		t.Fatal("request with caller-supplied lesson_version_id reached the authoring service")
	}

	response := makeRequest(payload, true)
	if response.Code != http.StatusCreated {
		t.Fatalf("valid request status = %d, want %d; body=%s", response.Code, http.StatusCreated, response.Body.String())
	}
	if authoring.createQuestionCalls != 1 {
		t.Fatalf("create question calls = %d, want 1", authoring.createQuestionCalls)
	}
	params := authoring.createQuestionParams
	if params.LessonVersionID != 124 {
		t.Fatalf("LessonVersionID = %d, want path version ID 124", params.LessonVersionID)
	}
	if params.CreatedByStaffID != identity.StaffID {
		t.Fatalf("CreatedByStaffID = %d, want authenticated staff ID %d", params.CreatedByStaffID, identity.StaffID)
	}
	if strings.Contains(response.Body.String(), "Internal expected answer") || strings.Contains(response.Body.String(), csrfToken) ||
		strings.Contains(response.Body.String(), "staff_session") {
		t.Fatal("question creation response leaked internal answer or authentication material")
	}
}

func TestStaffListLessonsForCourseRequiresSessionAndReturnsLatestVersionMetadata(t *testing.T) {
	identity := domain.StaffSessionIdentity{
		StaffID: 42, DisplayName: "Admin", Email: "admin@example.test",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	authAPI := &fakeStaffAuthAPI{authIdentity: identity}
	authoring := &fakeStaffAuthoringAPI{listLessonsResult: []domain.StaffLesson{{
		ID: 91, CourseID: 77, CourseTitle: "Draft course", Position: 1,
		LessonVersionID: 93, VersionNumber: 2, VersionStatus: "draft",
		Title: "Lesson under revision", QuestionCount: 3,
	}}}
	handler := NewHandlerWithStaffAuthAndAuthoringAndLoginLimiter(
		fakePinger{}, testLogger(), nil, authAPI, authoring, newStaffLoginLimiter(), true,
	)

	unauthenticated := httptest.NewRequest(http.MethodGet,
		"https://admin.example.test/api/v1/staff/admin/courses/77/lessons", nil)
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d; body=%s",
			unauthenticatedResponse.Code, http.StatusUnauthorized, unauthenticatedResponse.Body.String())
	}
	if authoring.listLessonsCalls != 0 {
		t.Fatal("authoring service was called without a staff session")
	}

	request := httptest.NewRequest(http.MethodGet,
		"https://admin.example.test/api/v1/staff/admin/courses/77/lessons", nil)
	request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: strings.Repeat("A", 43)})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("valid session status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if authoring.listLessonsCalls != 1 || authoring.listLessonsCourseID != 77 {
		t.Fatalf("list lessons calls/course ID = %d/%d, want 1/77",
			authoring.listLessonsCalls, authoring.listLessonsCourseID)
	}
	var body struct {
		Lessons []domain.StaffLesson `json:"lessons"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode lessons response: %v", err)
	}
	if len(body.Lessons) != 1 {
		t.Fatalf("lessons count = %d, want 1", len(body.Lessons))
	}
	lesson := body.Lessons[0]
	if lesson.ID != 91 || lesson.LessonVersionID != 93 || lesson.VersionNumber != 2 ||
		lesson.VersionStatus != "draft" || lesson.QuestionCount != 3 {
		t.Fatalf("staff lesson metadata is incomplete: %+v", lesson)
	}
	for _, forbidden := range []string{"expected_answer", "expected_explanation", "staff_session"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("staff lesson listing leaked forbidden field/material %q: %s", forbidden, response.Body.String())
		}
	}

	badID := httptest.NewRequest(http.MethodGet,
		"https://admin.example.test/api/v1/staff/admin/courses/0/lessons", nil)
	badID.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: strings.Repeat("A", 43)})
	badIDResponse := httptest.NewRecorder()
	handler.ServeHTTP(badIDResponse, badID)
	if badIDResponse.Code != http.StatusBadRequest {
		t.Fatalf("zero course ID status = %d, want %d; body=%s",
			badIDResponse.Code, http.StatusBadRequest, badIDResponse.Body.String())
	}
	if authoring.listLessonsCalls != 1 {
		t.Fatal("invalid course ID reached the authoring service")
	}
}

func TestStaffListQuestionsForVersionRequiresSessionAndReturnsInternalAuthoringData(t *testing.T) {
	identity := domain.StaffSessionIdentity{
		StaffID: 42, DisplayName: "Admin", Email: "admin@example.test",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	authAPI := &fakeStaffAuthAPI{authIdentity: identity}
	explanation := "Internal explanation"
	authoring := &fakeStaffAuthoringAPI{listQuestionsResult: []domain.LessonQuestion{{
		ID: 501, LessonVersionID: 93, QuestionNumber: 1, Prompt: "Question text",
		ExpectedAnswer: "Internal expected answer", AnswerType: "single_choice",
		ExpectedExplanation: &explanation,
		Criteria:            []domain.QuestionCriterion{{Key: "core", Description: "Core concept", Weight: 2, Required: true}},
		Options:             []domain.QuestionOption{{ID: 601, Key: "A", Text: "Correct option", IsCorrect: true}, {ID: 602, Key: "B", Text: "Other option"}},
		CommonMistakes:      []domain.CommonMistake{{Text: "Mistake", Explanation: "How to correct it"}},
		BiblePassages:       []domain.BiblePassage{{ID: 701, BookName: "John", Chapter: 3, StartVerse: 16, EndVerse: 17, PassageText: &explanation, SourceNote: &explanation}},
	}}}
	handler := NewHandlerWithStaffAuthAndAuthoringAndLoginLimiter(
		fakePinger{}, testLogger(), nil, authAPI, authoring, newStaffLoginLimiter(), true,
	)

	unauthenticated := httptest.NewRequest(http.MethodGet,
		"https://admin.example.test/api/v1/staff/admin/lesson-versions/93/questions", nil)
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d; body=%s", unauthenticatedResponse.Code, http.StatusUnauthorized, unauthenticatedResponse.Body.String())
	}
	if authoring.listQuestionsCalls != 0 {
		t.Fatal("authoring service was called without a staff session")
	}

	request := httptest.NewRequest(http.MethodGet,
		"https://admin.example.test/api/v1/staff/admin/lesson-versions/93/questions", nil)
	request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: strings.Repeat("A", 43)})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("valid session status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if authoring.listQuestionsCalls != 1 || authoring.listQuestionsVersionID != 93 {
		t.Fatalf("list questions calls/version ID = %d/%d, want 1/93", authoring.listQuestionsCalls, authoring.listQuestionsVersionID)
	}
	for _, expected := range []string{`"expected_answer":"Internal expected answer"`, `"expected_explanation":"Internal explanation"`, `"is_correct":true`, `"criteria"`, `"common_mistakes"`, `"bible_passages"`} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Errorf("staff response does not include %q: %s", expected, response.Body.String())
		}
	}
	if strings.Contains(response.Body.String(), "staff_session") {
		t.Fatal("staff questions response leaked a session cookie name")
	}

	badID := httptest.NewRequest(http.MethodGet,
		"https://admin.example.test/api/v1/staff/admin/lesson-versions/0/questions", nil)
	badID.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: strings.Repeat("A", 43)})
	badIDResponse := httptest.NewRecorder()
	handler.ServeHTTP(badIDResponse, badID)
	if badIDResponse.Code != http.StatusBadRequest {
		t.Fatalf("zero version ID status = %d, want %d; body=%s", badIDResponse.Code, http.StatusBadRequest, badIDResponse.Body.String())
	}
	if authoring.listQuestionsCalls != 1 {
		t.Fatal("invalid version ID reached the authoring service")
	}
}

func TestStaffCreateDraftRevisionUsesAuthenticatedActorAndCSRF(t *testing.T) {
	csrfToken := "csrf-revision-test-token-0123456789"
	identity := domain.StaffSessionIdentity{
		StaffID: 42, DisplayName: "Admin", Email: "admin@example.test",
		CSRFTokenHash: auth.HashToken(csrfToken), ExpiresAt: time.Now().Add(time.Hour),
	}
	authAPI := &fakeStaffAuthAPI{authIdentity: identity}
	authoring := &fakeStaffAuthoringAPI{createRevisionResult: domain.Lesson{
		ID: 77, CourseID: 88, CourseTitle: "Published course", Position: 1,
		LessonVersionID: 99, VersionNumber: 2, Title: "Lesson in revision", QuestionCount: 4,
	}}
	handler := NewHandlerWithStaffAuthAndAuthoringAndLoginLimiter(
		fakePinger{}, testLogger(), nil, authAPI, authoring, newStaffLoginLimiter(), true,
	)

	makeRequest := func(pathID, body string, includeCSRF bool) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost,
			"https://admin.example.test/api/v1/staff/admin/lessons/"+pathID+"/revisions",
			strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "https://admin.example.test")
		request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: strings.Repeat("A", 43)})
		if includeCSRF {
			request.AddCookie(&http.Cookie{Name: staffCSRFCookieName, Value: csrfToken})
			request.Header.Set("X-CSRF-Token", csrfToken)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	unauthenticated := httptest.NewRequest(http.MethodPost,
		"https://admin.example.test/api/v1/staff/admin/lessons/77/revisions",
		strings.NewReader(`{}`))
	unauthenticated.Header.Set("Content-Type", "application/json")
	unauthenticated.Header.Set("Origin", "https://admin.example.test")
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d; body=%s",
			unauthenticatedResponse.Code, http.StatusUnauthorized, unauthenticatedResponse.Body.String())
	}
	if authoring.createRevisionCalls != 0 {
		t.Fatal("revision creation service was called without a staff session")
	}

	withoutCSRF := makeRequest("77", `{}`, false)
	if withoutCSRF.Code != http.StatusForbidden {
		t.Fatalf("request without CSRF status = %d, want %d; body=%s",
			withoutCSRF.Code, http.StatusForbidden, withoutCSRF.Body.String())
	}
	if authoring.createRevisionCalls != 0 {
		t.Fatal("revision creation service was called without a valid CSRF token")
	}

	withUntrustedActor := makeRequest("77", `{"created_by_staff_id":999}`, true)
	if withUntrustedActor.Code != http.StatusBadRequest {
		t.Fatalf("request with caller-supplied actor ID status = %d, want %d; body=%s",
			withUntrustedActor.Code, http.StatusBadRequest, withUntrustedActor.Body.String())
	}
	if authoring.createRevisionCalls != 0 {
		t.Fatal("request with caller-supplied actor reached the authoring service")
	}

	response := makeRequest("77", `{}`, true)
	if response.Code != http.StatusCreated {
		t.Fatalf("valid request status = %d, want %d; body=%s",
			response.Code, http.StatusCreated, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if authoring.createRevisionCalls != 1 || authoring.createRevisionLessonID != 77 ||
		authoring.createRevisionStaffID != identity.StaffID {
		t.Fatalf("revision service calls/lesson/staff = %d/%d/%d, want 1/77/%d",
			authoring.createRevisionCalls, authoring.createRevisionLessonID,
			authoring.createRevisionStaffID, identity.StaffID)
	}
	if !strings.Contains(response.Body.String(), `"version_status":"draft"`) ||
		!strings.Contains(response.Body.String(), `"lesson_version_id":99`) ||
		!strings.Contains(response.Body.String(), `"question_count":4`) {
		t.Fatalf("revision response is missing metadata: %s", response.Body.String())
	}
	if strings.Contains(response.Body.String(), csrfToken) || strings.Contains(response.Body.String(), "staff_session") ||
		strings.Contains(response.Body.String(), "expected_answer") {
		t.Fatal("revision response leaked authentication material or internal question answers")
	}

	badID := makeRequest("0", `{}`, true)
	if badID.Code != http.StatusBadRequest {
		t.Fatalf("zero lesson ID status = %d, want %d; body=%s",
			badID.Code, http.StatusBadRequest, badID.Body.String())
	}
	if authoring.createRevisionCalls != 1 {
		t.Fatal("invalid lesson ID reached the authoring service")
	}
}

func TestStaffPublishLessonVersionRequiresSessionAndCSRF(t *testing.T) {
	csrfToken := "csrf-publish-test-token-0123456789"
	identity := domain.StaffSessionIdentity{
		StaffID: 42, DisplayName: "Admin", Email: "admin@example.test",
		CSRFTokenHash: auth.HashToken(csrfToken), ExpiresAt: time.Now().Add(time.Hour),
	}
	authAPI := &fakeStaffAuthAPI{authIdentity: identity}
	authoring := &fakeStaffAuthoringAPI{}
	handler := NewHandlerWithStaffAuthAndAuthoringAndLoginLimiter(
		fakePinger{}, testLogger(), nil, authAPI, authoring, newStaffLoginLimiter(), true,
	)

	makeRequest := func(pathID, body string, includeCSRF bool) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost,
			"https://admin.example.test/api/v1/staff/admin/lesson-versions/"+pathID+"/publish",
			strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "https://admin.example.test")
		request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: strings.Repeat("A", 43)})
		if includeCSRF {
			request.AddCookie(&http.Cookie{Name: staffCSRFCookieName, Value: csrfToken})
			request.Header.Set("X-CSRF-Token", csrfToken)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	unauthenticated := httptest.NewRequest(http.MethodPost,
		"https://admin.example.test/api/v1/staff/admin/lesson-versions/77/publish", strings.NewReader("{}"))
	unauthenticated.Header.Set("Content-Type", "application/json")
	unauthenticated.Header.Set("Origin", "https://admin.example.test")
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d; body=%s",
			unauthenticatedResponse.Code, http.StatusUnauthorized, unauthenticatedResponse.Body.String())
	}
	if authoring.publishVersionCalls != 0 {
		t.Fatal("publication service was called without a staff session")
	}

	withoutCSRF := makeRequest("77", "{}", false)
	if withoutCSRF.Code != http.StatusForbidden {
		t.Fatalf("request without CSRF status = %d, want %d; body=%s",
			withoutCSRF.Code, http.StatusForbidden, withoutCSRF.Body.String())
	}
	if authoring.publishVersionCalls != 0 {
		t.Fatal("publication service was called without a valid CSRF token")
	}

	withUntrustedActor := makeRequest("77", `{"published_by_staff_id":999}`, true)
	if withUntrustedActor.Code != http.StatusBadRequest {
		t.Fatalf("request with caller-supplied actor ID status = %d, want %d; body=%s",
			withUntrustedActor.Code, http.StatusBadRequest, withUntrustedActor.Body.String())
	}
	if authoring.publishVersionCalls != 0 {
		t.Fatal("caller-supplied actor reached the publication service")
	}

	response := makeRequest("77", "{}", true)
	if response.Code != http.StatusNoContent {
		t.Fatalf("valid request status = %d, want %d; body=%s",
			response.Code, http.StatusNoContent, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if authoring.publishVersionCalls != 1 || authoring.publishVersionID != 77 ||
		authoring.publishVersionStaffID != identity.StaffID {
		t.Fatalf("publication calls/version/staff = %d/%d/%d, want 1/77/%d",
			authoring.publishVersionCalls, authoring.publishVersionID,
			authoring.publishVersionStaffID, identity.StaffID)
	}

	badID := makeRequest("0", "{}", true)
	if badID.Code != http.StatusBadRequest {
		t.Fatalf("zero version ID status = %d, want %d; body=%s",
			badID.Code, http.StatusBadRequest, badID.Body.String())
	}
	if authoring.publishVersionCalls != 1 {
		t.Fatal("invalid version ID reached the publication service")
	}
}

func TestStaffPublishCourseRequiresSessionAndCSRF(t *testing.T) {
	csrfToken := "csrf-course-publish-test-token-012345"
	identity := domain.StaffSessionIdentity{
		StaffID: 42, DisplayName: "Admin", Email: "admin@example.test",
		CSRFTokenHash: auth.HashToken(csrfToken), ExpiresAt: time.Now().Add(time.Hour),
	}
	authAPI := &fakeStaffAuthAPI{authIdentity: identity}
	authoring := &fakeStaffAuthoringAPI{}
	handler := NewHandlerWithStaffAuthAndAuthoringAndLoginLimiter(
		fakePinger{}, testLogger(), nil, authAPI, authoring, newStaffLoginLimiter(), true,
	)

	makeRequest := func(pathID, body string, includeCSRF bool) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost,
			"https://admin.example.test/api/v1/staff/admin/courses/"+pathID+"/publish",
			strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "https://admin.example.test")
		request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: strings.Repeat("A", 43)})
		if includeCSRF {
			request.AddCookie(&http.Cookie{Name: staffCSRFCookieName, Value: csrfToken})
			request.Header.Set("X-CSRF-Token", csrfToken)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	unauthenticated := httptest.NewRequest(http.MethodPost,
		"https://admin.example.test/api/v1/staff/admin/courses/77/publish", strings.NewReader("{}"))
	unauthenticated.Header.Set("Content-Type", "application/json")
	unauthenticated.Header.Set("Origin", "https://admin.example.test")
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d; body=%s",
			unauthenticatedResponse.Code, http.StatusUnauthorized, unauthenticatedResponse.Body.String())
	}
	if authoring.publishCourseCalls != 0 {
		t.Fatal("course publication service was called without a staff session")
	}

	withoutCSRF := makeRequest("77", "{}", false)
	if withoutCSRF.Code != http.StatusForbidden {
		t.Fatalf("request without CSRF status = %d, want %d; body=%s",
			withoutCSRF.Code, http.StatusForbidden, withoutCSRF.Body.String())
	}
	if authoring.publishCourseCalls != 0 {
		t.Fatal("course publication service was called without a valid CSRF token")
	}

	withUntrustedActor := makeRequest("77", `{"published_by_staff_id":999}`, true)
	if withUntrustedActor.Code != http.StatusBadRequest {
		t.Fatalf("request with caller-supplied actor ID status = %d, want %d; body=%s",
			withUntrustedActor.Code, http.StatusBadRequest, withUntrustedActor.Body.String())
	}
	if authoring.publishCourseCalls != 0 {
		t.Fatal("caller-supplied actor reached the course publication service")
	}

	response := makeRequest("77", "{}", true)
	if response.Code != http.StatusNoContent {
		t.Fatalf("valid request status = %d, want %d; body=%s",
			response.Code, http.StatusNoContent, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if authoring.publishCourseCalls != 1 || authoring.publishCourseID != 77 ||
		authoring.publishCourseStaffID != identity.StaffID {
		t.Fatalf("course publication calls/course/staff = %d/%d/%d, want 1/77/%d",
			authoring.publishCourseCalls, authoring.publishCourseID,
			authoring.publishCourseStaffID, identity.StaffID)
	}

	badID := makeRequest("0", "{}", true)
	if badID.Code != http.StatusBadRequest {
		t.Fatalf("zero course ID status = %d, want %d; body=%s",
			badID.Code, http.StatusBadRequest, badID.Body.String())
	}
	if authoring.publishCourseCalls != 1 {
		t.Fatal("invalid course ID reached the publication service")
	}
}
