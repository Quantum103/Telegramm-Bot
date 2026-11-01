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
	status := http.StatusCreated
	if review.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, review)
}

func (h handlers) staffCourseLearningMiddleware(next http.Handler) http.Handler {
	roleChecked := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := staffIdentityFromRequest(r)
		if identity.StaffID <= 0 {
			writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
			return
		}
		isCurator := staffHasRole(identity, "curator")
		isAdministrator := staffHasRole(identity, "administrator")
		if !isCurator && !isAdministrator {
			writeJSON(w, http.StatusForbidden, apiErrorResponse{Error: "insufficient_permissions"})
			return
		}
		// Administrators may inspect all learning progress. Only a curator
		// may submit a review decision; repository authorization independently
		// verifies the current student assignment.
		if isUnsafeMethod(r.Method) && !isCurator {
			writeJSON(w, http.StatusForbidden, apiErrorResponse{Error: "insufficient_permissions"})
			return
		}
		next.ServeHTTP(w, r)
	})
	return h.staffSessionMiddleware(roleChecked)
}
