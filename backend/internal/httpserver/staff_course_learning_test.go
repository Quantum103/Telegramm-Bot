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

type fakeCuratorLearningAPI struct {
	access        domain.CuratorCourseAccess
	answers       []domain.CuratorAnswer
	review         domain.AnswerAttemptReview
	listCourseCalls int
	listAnswerCalls int
	reviewCalls    int
	lastStaffID    int64
	lastStudentID  int64
	lastRunID      int64
	lastLessonID   int64
	lastAttemptID  int64
	lastDecision   string
	lastFeedback   string
}

func (f *fakeCuratorLearningAPI) ListCourseLessons(_ context.Context, staffID, studentID, runID int64) (domain.CuratorCourseAccess, error) {
	f.listCourseCalls++
	f.lastStaffID, f.lastStudentID, f.lastRunID = staffID, studentID, runID
	return f.access, nil
}

func (f *fakeCuratorLearningAPI) ListLessonAnswers(_ context.Context, staffID, studentID, runID, lessonID int64) ([]domain.CuratorAnswer, error) {
	f.listAnswerCalls++
	f.lastStaffID, f.lastStudentID, f.lastRunID, f.lastLessonID = staffID, studentID, runID, lessonID
	return f.answers, nil
}

func (f *fakeCuratorLearningAPI) ReviewAttempt(_ context.Context, staffID, attemptID int64, decision, feedback string) (domain.AnswerAttemptReview, error) {
	f.reviewCalls++
	f.lastStaffID, f.lastAttemptID, f.lastDecision, f.lastFeedback = staffID, attemptID, decision, feedback
	return f.review, nil
}

func TestStaffCourseLearningReadRolesAndReviewIdempotencyStatus(t *testing.T) {
	const csrfToken = "csrf-course-learning-test-token-123456"
	identity := domain.StaffSessionIdentity{
		StaffID: 42, DisplayName: "Staff", Email: "staff@example.test",
		CSRFTokenHash: auth.HashToken(csrfToken), ExpiresAt: time.Now().Add(time.Hour),
	}
	authAPI := &fakeStaffAuthAPI{authIdentity: identity}
	learning := &fakeCuratorLearningAPI{
		access: domain.CuratorCourseAccess{
			Enrollment: domain.CourseEnrollment{ID: 77, StudentID: 88, Mode: "curator"},
			Lessons: []domain.CourseEnrollmentLesson{{LessonID: 99, Status: "in_progress"}},
		},
		answers: []domain.CuratorAnswer{{QuestionID: 100, ReviewStatus: "pending_review"}},
		review: domain.AnswerAttemptReview{
			ID: 1, AnswerAttemptID: 2, CuratorStaffID: 42,
			Decision: "approved", Feedback: "Принято.", ReviewedAt: time.Now(),
		},
	}
	handler := NewHandlerWithStaffAuthAndAuthoringAndManagementAndLoginLimiterAndConversationsAndTelegramAndCourseLearning(
		fakePinger{}, testLogger(), nil, authAPI, nil, nil, nil, nil, learning, newStaffLoginLimiter(), true,
	)

	makeRequest := func(roles []string, method, path, body string, withCSRF bool) *httptest.ResponseRecorder {
		t.Helper()
		current := identity
		current.Roles = append([]string(nil), roles...)
		api := &fakeStaffAuthAPI{authIdentity: current}
		// The request uses the identity currently exposed by this per-request
		// authentication fake; all service actor IDs still come from that session.
		request := httptest.NewRequest(method, "https://staff.example.test"+path, strings.NewReader(body))
		if method == http.MethodPost {
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", "https://staff.example.test")
		}
		request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: "valid-session-token"})
		if withCSRF {
			request.AddCookie(&http.Cookie{Name: staffCSRFCookieName, Value: csrfToken})
			request.Header.Set("X-CSRF-Token", csrfToken)
		}
		_ = api
		// The handler's middleware uses the auth service attached at creation.
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	// This handler is tied to the administrator identity above.
	adminCourse := makeRequest([]string{"administrator"}, http.MethodGet,
		"/api/v1/staff/course-learning/students/88/courses/77/lessons", "", false)
	if adminCourse.Code != http.StatusOK || learning.listCourseCalls != 1 ||
		learning.lastStaffID != identity.StaffID || learning.lastStudentID != 88 || learning.lastRunID != 77 {
		t.Fatalf("administrator course read status/calls/actor = %d/%d/%d; body=%s",
			adminCourse.Code, learning.listCourseCalls, learning.lastStaffID, adminCourse.Body.String())
	}

	adminAnswers := makeRequest([]string{"administrator"}, http.MethodGet,
		"/api/v1/staff/course-learning/students/88/courses/77/lessons/99/answers", "", false)
	if adminAnswers.Code != http.StatusOK || learning.listAnswerCalls != 1 ||
		learning.lastStudentID != 88 || learning.lastRunID != 77 || learning.lastLessonID != 99 {
		t.Fatalf("administrator answer read status/calls = %d/%d; body=%s",
			adminAnswers.Code, learning.listAnswerCalls, adminAnswers.Body.String())
	}

	adminReview := makeRequest([]string{"administrator"}, http.MethodPost,
		"/api/v1/staff/course-learning/attempts/2/review", `{"decision":"approved","feedback":"Принято."}`, true)
	if adminReview.Code != http.StatusForbidden || learning.reviewCalls != 0 {
		t.Fatalf("administrator must not submit curator review: status=%d calls=%d body=%s",
			adminReview.Code, learning.reviewCalls, adminReview.Body.String())
	}

	curatorReview := makeRequest([]string{"curator"}, http.MethodPost,
		"/api/v1/staff/course-learning/attempts/2/review", `{"decision":"approved","feedback":"Принято."}`, true)
	// The bound handler still authenticates the configured admin identity, so
	// this call must remain forbidden; the next dedicated handler validates curator writes.
	if curatorReview.Code != http.StatusForbidden || learning.reviewCalls != 0 {
		t.Fatalf("administrator session unexpectedly acquired curator write permission: status=%d calls=%d",
			curatorReview.Code, learning.reviewCalls)
	}

	curatorIdentity := identity
	curatorIdentity.Roles = []string{"curator"}
	curatorAuth := &fakeStaffAuthAPI{authIdentity: curatorIdentity}
	curatorLearning := &fakeCuratorLearningAPI{review: domain.AnswerAttemptReview{
		ID: 9, AnswerAttemptID: 2, CuratorStaffID: curatorIdentity.StaffID,
		Decision: "approved", Feedback: "Принято.", ReviewedAt: time.Now(),
	}}
	curatorHandler := NewHandlerWithStaffAuthAndAuthoringAndManagementAndLoginLimiterAndConversationsAndTelegramAndCourseLearning(
		fakePinger{}, testLogger(), nil, curatorAuth, nil, nil, nil, nil, curatorLearning, newStaffLoginLimiter(), true,
	)
	post := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost,
			"https://staff.example.test/api/v1/staff/course-learning/attempts/2/review",
			strings.NewReader(`{"decision":"approved","feedback":"Принято."}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "https://staff.example.test")
		request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: "valid-session-token"})
		request.AddCookie(&http.Cookie{Name: staffCSRFCookieName, Value: csrfToken})
		request.Header.Set("X-CSRF-Token", csrfToken)
		recorder := httptest.NewRecorder()
		curatorHandler.ServeHTTP(recorder, request)
		return recorder
	}
	first := post()
	if first.Code != http.StatusCreated || curatorLearning.reviewCalls != 1 ||
		curatorLearning.lastStaffID != curatorIdentity.StaffID || curatorLearning.lastAttemptID != 2 ||
		curatorLearning.lastDecision != "approved" || curatorLearning.lastFeedback != "Принято." {
		t.Fatalf("first curator review status/call = %d/%d; body=%s",
			first.Code, curatorLearning.reviewCalls, first.Body.String())
	}
	curatorLearning.review.Replayed = true
	replay := post()
	if replay.Code != http.StatusOK || curatorLearning.reviewCalls != 2 {
		t.Fatalf("replayed curator review status/calls = %d/%d; body=%s",
			replay.Code, curatorLearning.reviewCalls, replay.Body.String())
	}
}
