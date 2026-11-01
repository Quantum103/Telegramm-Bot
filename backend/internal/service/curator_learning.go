package service

import (
	"context"
	"strings"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type CuratorLearningStore interface {
	ListCourseLessons(context.Context, int64, int64, int64) (domain.CuratorCourseAccess, error)
	ListLessonAnswers(context.Context, int64, int64, int64, int64) ([]domain.CuratorAnswer, error)
	ReviewAttempt(context.Context, domain.ReviewAnswerAttemptParams) (domain.AnswerAttemptReview, error)
}

// CuratorLearningService делегирует авторизацию текущего назначения в репозитории,
// где она повторно проверяется внутри самих запросов/транзакций.
type CuratorLearningService struct {
	store CuratorLearningStore
}

func NewCuratorLearningService(store CuratorLearningStore) *CuratorLearningService {
	return &CuratorLearningService{store: store}
}

func (s *CuratorLearningService) ListCourseLessons(
	ctx context.Context,
	curatorStaffID, studentID, courseEnrollmentID int64,
) (domain.CuratorCourseAccess, error) {
	if curatorStaffID <= 0 || studentID <= 0 || courseEnrollmentID <= 0 {
		return domain.CuratorCourseAccess{}, domain.ErrInvalidInput
	}
	return s.store.ListCourseLessons(ctx, curatorStaffID, studentID, courseEnrollmentID)
}

func (s *CuratorLearningService) ListLessonAnswers(
	ctx context.Context,
	curatorStaffID, studentID, courseEnrollmentID, lessonID int64,
) ([]domain.CuratorAnswer, error) {
	if curatorStaffID <= 0 || studentID <= 0 || courseEnrollmentID <= 0 || lessonID <= 0 {
		return nil, domain.ErrInvalidInput
	}
	return s.store.ListLessonAnswers(ctx, curatorStaffID, studentID, courseEnrollmentID, lessonID)
}

func (s *CuratorLearningService) ReviewAttempt(
	ctx context.Context,
	curatorStaffID, attemptID int64,
	decision, feedback string,
) (domain.AnswerAttemptReview, error) {
	decision = strings.TrimSpace(decision)
	feedback = strings.TrimSpace(feedback)
	if curatorStaffID <= 0 || attemptID <= 0 ||
		(decision != "approved" && decision != "needs_revision") ||
		len(feedback) > 4000 || (decision == "needs_revision" && feedback == "") {
		return domain.AnswerAttemptReview{}, domain.ErrInvalidInput
	}
	return s.store.ReviewAttempt(ctx, domain.ReviewAnswerAttemptParams{
		CuratorStaffID: curatorStaffID,
		AttemptID: attemptID,
		Decision: decision,
		Feedback: feedback,
	})
}
