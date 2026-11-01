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
	access          domain.CuratorCourseAccess
	answers         []domain.CuratorAnswer
	review          domain.AnswerAttemptReview
	listCourseCalls int
	listAnswerCalls int
	reviewCalls     int
	lastStaffID     int64
	lastStudentID   int64
	lastRunID       int64
	lastLessonID    int64
	lastAttemptID   int64
	lastDecision    string
	lastFeedback    string
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

type fakeCourseLearningStaffManagement struct {
	students   []domain.AdminStudent
	listCalls  int
	actorStaff int64
}

func (f *fakeCourseLearningStaffManagement) ListStaffAccounts(context.Context, int64) ([]domain.StaffAccountSummary, error) {
	return []domain.StaffAccountSummary{}, nil
}
func (f *fakeCourseLearningStaffManagement) CreateStaffAccount(context.Context, int64, string, string, string) (domain.CreatedStaffAccount, error) {
	return domain.CreatedStaffAccount{}, nil
}
func (f *fakeCourseLearningStaffManagement) ListStudents(_ context.Context, actorID int64) ([]domain.AdminStudent, error) {
	f.listCalls++
	f.actorStaff = actorID
	return f.students, nil
}
func (f *fakeCourseLearningStaffManagement) AssignCurator(context.Context, int64, int64, int64) error {
	return nil
}
func (f *fakeCourseLearningStaffManagement) EndCuratorAssignment(context.Context, int64, int64) error {
	return nil
}
func (f *fakeCourseLearningStaffManagement) SetAccountStatus(context.Context, int64, int64, string) error {
	return nil
}
func (f *fakeCourseLearningStaffManagement) ResetPassword(context.Context, int64, int64) (string, error) {
	return "", nil
}

func TestStaffCourseLearningStudentListIncludesActiveCourse(t *testing.T) {
	const csrfToken = "csrf-course-learning-students-test-token"
	identity := domain.StaffSessionIdentity{
		StaffID: 51, DisplayName: "Admin", Email: "admin@example.test",
		Roles:         []string{"administrator"},
		CSRFTokenHash: auth.HashToken(csrfToken), ExpiresAt: time.Now().Add(time.Hour),
	}
	authAPI := &fakeStaffAuthAPI{authIdentity: identity}
	management := &fakeCourseLearningStaffManagement{
		students: []domain.AdminStudent{{
			ID: 88, DisplayName: "Ученик",
			ActiveCourse: &domain.StudentActiveCourse{
				CourseEnrollmentID: 77, CourseID: 10, CourseTitle: "Курс",
				Mode: "curator", Status: "in_progress", TotalLessons: 4, CompletedLessons: 1,
			},
		}},
	}
	learning := &fakeCuratorLearningAPI{}
	handler := NewHandlerWithStaffAuthAndAuthoringAndManagementAndLoginLimiterAndConversationsAndTelegramAndCourseLearning(
		fakePinger{}, testLogger(), nil, authAPI, nil, management, nil, nil, learning, newStaffLoginLimiter(), true,
	)
	request := httptest.NewRequest(http.MethodGet, "https://staff.example.test/api/v1/staff/course-learning/students", nil)
	request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: "valid-session-token"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || management.listCalls != 1 || management.actorStaff != identity.StaffID {
		t.Fatalf("course-learning student list status/calls/actor = %d/%d/%d; body=%s",
			recorder.Code, management.listCalls, management.actorStaff, recorder.Body.String())
	}
	for _, expected := range []string{
		"\"active_course\"",
		"\"course_enrollment_id\":77",
		"\"course_title\":\"Курс\"",
		"\"mode\":\"curator\"",
		"\"completed_lessons\":1",
	} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Fatalf("student list response is missing %q: %s", expected, recorder.Body.String())
		}
	}
}

func TestStaffCourseLearningReadRolesAndReviewIdempotencyStatus(t *testing.T) {
	const csrfToken = "csrf-course-learning-test-token-123456"
	identity := domain.StaffSessionIdentity{
		StaffID: 42, DisplayName: "Staff", Email: "staff@example.test",
		Roles:         []string{"administrator"},
		CSRFTokenHash: auth.HashToken(csrfToken), ExpiresAt: time.Now().Add(time.Hour),
	}
	authAPI := &fakeStaffAuthAPI{authIdentity: identity}
	learning := &fakeCuratorLearningAPI{
		access: domain.CuratorCourseAccess{
			Enrollment: domain.CourseEnrollment{ID: 77, StudentID: 88, Mode: "curator"},
			Lessons:    []domain.CourseEnrollmentLesson{{LessonID: 99, Status: "in_progress"}},
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

	makeAdminRequest := func(method, path, body string, withCSRF bool) *httptest.ResponseRecorder {
		t.Helper()
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
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	// This handler is tied to the administrator identity above.
	adminCourse := makeAdminRequest(http.MethodGet,
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
