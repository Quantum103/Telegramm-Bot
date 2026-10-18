package service

import (
	"context"
	"strings"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type AuthoringStore interface {
	CreateDraftCourse(context.Context, domain.CreateCourseParams) (domain.Course, error)
	CreateDraftLesson(context.Context, domain.CreateLessonParams) (domain.Lesson, error)
	CreateDraftRevision(context.Context, int64) (domain.Lesson, error)
	CreateQuestionWithMaterials(context.Context, domain.CreateQuestionParams) (domain.LessonQuestion, error)
	PublishLessonVersion(context.Context, int64) error
	PublishCourse(context.Context, int64) error
}

// AuthoringService — внутренний сервис для будущей панели и импорта DOCX.
// Он не открыт через незащищённые HTTP-маршруты.
type AuthoringService struct {
	store AuthoringStore
}

func NewAuthoringService(store AuthoringStore) *AuthoringService {
	return &AuthoringService{store: store}
}

func (s *AuthoringService) CreateDraftCourse(ctx context.Context, params domain.CreateCourseParams) (domain.Course, error) {
	params.Slug = strings.TrimSpace(params.Slug)
	params.Title = strings.TrimSpace(params.Title)
	if params.Slug == "" || params.Title == "" {
		return domain.Course{}, domain.ErrInvalidInput
	}
	return s.store.CreateDraftCourse(ctx, params)
}

func (s *AuthoringService) CreateDraftLesson(ctx context.Context, params domain.CreateLessonParams) (domain.Lesson, error) {
	params.Title = strings.TrimSpace(params.Title)
	if params.CourseID <= 0 || params.Title == "" {
		return domain.Lesson{}, domain.ErrInvalidInput
	}
	return s.store.CreateDraftLesson(ctx, params)
}


func (s *AuthoringService) CreateDraftRevision(ctx context.Context, lessonID int64) (domain.Lesson, error) {
	if lessonID <= 0 {
		return domain.Lesson{}, domain.ErrInvalidInput
	}
	return s.store.CreateDraftRevision(ctx, lessonID)
}

func (s *AuthoringService) CreateQuestion(ctx context.Context, params domain.CreateQuestionParams) (domain.LessonQuestion, error) {
	params.Prompt = strings.TrimSpace(params.Prompt)
	params.ExpectedAnswer = strings.TrimSpace(params.ExpectedAnswer)
	params.AnswerType = strings.TrimSpace(params.AnswerType)
	if params.LessonVersionID <= 0 || params.QuestionNumber <= 0 ||
		params.Prompt == "" || params.ExpectedAnswer == "" {
		return domain.LessonQuestion{}, domain.ErrInvalidInput
	}
	return s.store.CreateQuestionWithMaterials(ctx, params)
}

func (s *AuthoringService) PublishLessonVersion(ctx context.Context, versionID int64) error {
	if versionID <= 0 {
		return domain.ErrInvalidInput
	}
	return s.store.PublishLessonVersion(ctx, versionID)
}

func (s *AuthoringService) PublishCourse(ctx context.Context, courseID int64) error {
	if courseID <= 0 {
		return domain.ErrInvalidInput
	}
	return s.store.PublishCourse(ctx, courseID)
}
