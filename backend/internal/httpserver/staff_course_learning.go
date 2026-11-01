package httpserver

import (
	"context"
	"mime"
	"net/http"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type CuratorLearningAPI interface {
	ListCourseLessons(context.Context, int64, int64, int64) (domain.CuratorCourseAccess, error)
	ListLessonAnswers(context.Context, int64, int64, int64, int64) ([]domain.CuratorAnswer, error)
	ReviewAttempt(context.Context, int64, int64, string, string) (domain.AnswerAttemptReview, error)
}

type curatorReviewRequest struct {
	Decision string `json:"decision"`
	Feedback string `json:"feedback"`
}

func (h handlers) staffCuratorCourseLessons(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity := staffIdentityFromRequest(r)
	studentID, ok := parsePositiveID(w, r.PathValue("studentID"))
	if !ok {
		return
	}
	courseEnrollmentID, ok := parsePositiveID(w, r.PathValue("courseEnrollmentID"))
	if !ok {
		return
	}
	access, err := h.courseLearning.ListCourseLessons(r.Context(), identity.StaffID, studentID, courseEnrollmentID)
	if err != nil {
		h.writeAPIError(w, err, "получить уроки курса закреплённого ученика")
		return
	}
	if access.Lessons == nil {
		access.Lessons = []domain.CourseEnrollmentLesson{}
	}
	writeJSON(w, http.StatusOK, access)
}

func (h handlers) staffCuratorLessonAnswers(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity := staffIdentityFromRequest(r)
	studentID, ok := parsePositiveID(w, r.PathValue("studentID"))
	if !ok {
		return
	}
	courseEnrollmentID, ok := parsePositiveID(w, r.PathValue("courseEnrollmentID"))
	if !ok {
		return
	}
	lessonID, ok := parsePositiveID(w, r.PathValue("lessonID"))
	if !ok {
		return
	}
	answers, err := h.courseLearning.ListLessonAnswers(
		r.Context(), identity.StaffID, studentID, courseEnrollmentID, lessonID,
	)
	if err != nil {
		h.writeAPIError(w, err, "получить ответы ученика по уроку")
		return
	}
	if answers == nil {
		answers = []domain.CuratorAnswer{}
	}
	writeJSON(w, http.StatusOK, struct {
		Answers []domain.CuratorAnswer `json:"answers"`
	}{Answers: answers})
}

func (h handlers) staffCuratorReviewAttempt(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity := staffIdentityFromRequest(r)
	attemptID, ok := parsePositiveID(w, r.PathValue("attemptID"))
	if !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "content_type_must_be_json"})
		return
	}
	var input curatorReviewRequest
	if err := decodeJSONBodyWithLimit(w, r, &input, maxStaffAdminBodyBytes); err != nil {
		writeInvalidAdminRequest(w, err)
		return
	}
	review, err := h.courseLearning.ReviewAttempt(
		r.Context(), identity.StaffID, attemptID, input.Decision, input.Feedback,
	)
	if err != nil {
		h.writeAPIError(w, err, "проверить ответ ученика")
		return
	}
	writeJSON(w, http.StatusCreated, review)
}
