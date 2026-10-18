package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type fakeCatalogAPI struct {
	courses []domain.Course
	course  domain.Course
	lessons []domain.Lesson
	detail  domain.LessonDetail
	err     error
}

func (f fakeCatalogAPI) ListCourses(context.Context) ([]domain.Course, error) {
	return f.courses, f.err
}
func (f fakeCatalogAPI) GetCourse(context.Context, int64) (domain.Course, error) {
	return f.course, f.err
}
func (f fakeCatalogAPI) ListCourseLessons(context.Context, int64) ([]domain.Lesson, error) {
	return f.lessons, f.err
}
func (f fakeCatalogAPI) GetLessonDetail(context.Context, int64) (domain.LessonDetail, error) {
	return f.detail, f.err
}

func TestLessonEndpointDoesNotExposeInternalGradingData(t *testing.T) {
	lesson := domain.Lesson{
		ID: 7, CourseID: 3, CourseTitle: "Открытый курс",
		LessonVersionID: 12, VersionNumber: 2, Title: "Урок", QuestionCount: 1,
	}
	catalog := fakeCatalogAPI{detail: domain.LessonDetail{
		Lesson: lesson,
		Questions: []domain.LessonQuestion{{
			ID: 21, LessonVersionID: 12, QuestionNumber: 1,
			Prompt: "Почему важно изучать Писание?", AnswerType: "single_choice",
			ExpectedAnswer:      "expected-answer-secret",
			ExpectedExplanation: stringPointer("explanation-secret"),
			Criteria:            []domain.QuestionCriterion{{Key: "secret", Description: "criterion-secret"}},
			CommonMistakes:      []domain.CommonMistake{{Text: "mistake-secret", Explanation: "mistake-explanation-secret"}},
			BiblePassages:       []domain.BiblePassage{{ID: 5, BookName: "Иоанна", Chapter: 3, StartVerse: 16, EndVerse: 16, SourceNote: stringPointer("source-note-secret")}},
			Options: []domain.QuestionOption{
				{ID: 30, Key: "A", Text: "Первый вариант", IsCorrect: true},
				{ID: 31, Key: "B", Text: "Второй вариант"},
			},
		}},
	}}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/lessons/7", nil)
	recorder := httptest.NewRecorder()
	NewHandler(fakePinger{}, testLogger(), catalog).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{
		"expected-answer-secret", "explanation-secret", "criterion-secret",
		"mistake-secret", "source-note-secret", "is_correct", "criteria",
		"expected_answer", "correct_answer",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("ответ ученику раскрывает внутренние данные %q: %s", forbidden, body)
		}
	}
	for _, expected := range []string{
		"\"question_number\":1", "\"answer_type\":\"single_choice\"",
		"\"text\":\"Первый вариант\"", "\"book_name\":\"Иоанна\"",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("в ответе нет безопасного поля %q: %s", expected, body)
		}
	}
}

func TestCatalogRoutesValidateIDsAndHideMissingResources(t *testing.T) {
	catalog := fakeCatalogAPI{
		err: domain.ErrNotFound,
	}
	handler := NewHandler(fakePinger{}, testLogger(), catalog)
	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{name: "bad course id", path: "/api/v1/courses/not-a-number", wantStatus: http.StatusBadRequest, wantBody: "invalid_id"},
		{name: "course not found", path: "/api/v1/courses/123", wantStatus: http.StatusNotFound, wantBody: "not_found"},
		{name: "lesson not found", path: "/api/v1/lessons/444", wantStatus: http.StatusNotFound, wantBody: "not_found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), test.wantBody) {
				t.Fatalf("body = %q, want %q", recorder.Body.String(), test.wantBody)
			}
			if strings.Contains(recorder.Body.String(), "SELECT") || strings.Contains(recorder.Body.String(), "private database") {
				t.Fatal("API exposed internal error details")
			}
		})
	}
}

func TestCourseListReturnsEmptyArrayRatherThanNull(t *testing.T) {
	handler := NewHandler(fakePinger{}, testLogger(), fakeCatalogAPI{courses: []domain.Course{}})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/courses", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := strings.TrimSpace(recorder.Body.String()); got != "{\"courses\":[]}" {
		t.Fatalf("body = %q, want an empty courses array", got)
	}
}

func stringPointer(value string) *string { return &value }
