package service

import (
	"context"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type CatalogStore interface {
	ListPublishedCourses(context.Context) ([]domain.Course, error)
	GetPublishedCourseByID(context.Context, int64) (domain.Course, error)
	ListPublishedLessonsByCourseID(context.Context, int64) ([]domain.Lesson, error)
	GetPublishedLessonByID(context.Context, int64) (domain.Lesson, error)
	GetPublishedLessonInCourse(context.Context, int64, int64) (domain.Lesson, error)
}

type QuestionStore interface {
	ListQuestionsForVersion(context.Context, int64) ([]domain.LessonQuestion, error)
}

type CatalogService struct {
	courses   CatalogStore
	questions QuestionStore
}

func NewCatalogService(courses CatalogStore, questions QuestionStore) *CatalogService {
	return &CatalogService{courses: courses, questions: questions}
}

func (s *CatalogService) ListCourses(ctx context.Context) ([]domain.Course, error) {
	return s.courses.ListPublishedCourses(ctx)
}

func (s *CatalogService) GetCourse(ctx context.Context, courseID int64) (domain.Course, error) {
	return s.courses.GetPublishedCourseByID(ctx, courseID)
}

func (s *CatalogService) ListCourseLessons(ctx context.Context, courseID int64) ([]domain.Lesson, error) {
	return s.courses.ListPublishedLessonsByCourseID(ctx, courseID)
}

func (s *CatalogService) GetLessonDetailInCourse(ctx context.Context, courseID, lessonID int64) (domain.LessonDetail, error) {
	lesson, err := s.courses.GetPublishedLessonInCourse(ctx, courseID, lessonID)
	if err != nil {
		return domain.LessonDetail{}, err
	}
	questions, err := s.questions.ListQuestionsForVersion(ctx, lesson.LessonVersionID)
	if err != nil {
		return domain.LessonDetail{}, err
	}
	return domain.LessonDetail{Lesson: lesson, Questions: questions}, nil
}

func (s *CatalogService) GetLessonDetail(ctx context.Context, lessonID int64) (domain.LessonDetail, error) {
	lesson, err := s.courses.GetPublishedLessonByID(ctx, lessonID)
	if err != nil {
		return domain.LessonDetail{}, err
	}
	questions, err := s.questions.ListQuestionsForVersion(ctx, lesson.LessonVersionID)
	if err != nil {
		return domain.LessonDetail{}, err
	}
	return domain.LessonDetail{Lesson: lesson, Questions: questions}, nil
}
