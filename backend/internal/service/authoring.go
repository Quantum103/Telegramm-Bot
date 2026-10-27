package service

import (
	"context"
	"strings"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

type AuthoringStore interface {
	ListCoursesForStaff(context.Context) ([]domain.Course, error)
	ListLessonsForStaffByCourseID(context.Context, int64) ([]domain.StaffLesson, error)
	ListQuestionsForStaffVersion(context.Context, int64) ([]domain.LessonQuestion, error)
	CreateDraftCourse(context.Context, domain.CreateCourseParams) (domain.Course, error)
	CreateDraftLesson(context.Context, domain.CreateLessonParams) (domain.Lesson, error)
	UpdateDraftLessonVersion(context.Context, int64, string, *string, int64) error
	CreateDraftRevision(context.Context, int64, int64) (domain.Lesson, error)
	CreateQuestionWithMaterials(context.Context, domain.CreateQuestionParams) (domain.LessonQuestion, error)
	ImportDraftLesson(context.Context, domain.ImportDraftLessonParams) (domain.Lesson, error)
	UpdateQuestionWithMaterials(context.Context, int64, domain.CreateQuestionParams, int64) (domain.LessonQuestion, error)
	PublishLessonVersion(context.Context, int64, int64) error
	PublishCourse(context.Context, int64, int64) error
}

// AuthoringService — внутренний сервис для будущей панели и импорта DOCX.
// Он не открыт через незащищённые HTTP-маршруты.
type AuthoringService struct {
	store AuthoringStore
}

func NewAuthoringService(store AuthoringStore) *AuthoringService {
	return &AuthoringService{store: store}
}

func (s *AuthoringService) ListCoursesForStaff(ctx context.Context) ([]domain.Course, error) {
	return s.store.ListCoursesForStaff(ctx)
}

func (s *AuthoringService) ListLessonsForStaffByCourseID(ctx context.Context, courseID int64) ([]domain.StaffLesson, error) {
	if courseID <= 0 {
		return nil, domain.ErrInvalidInput
	}
	return s.store.ListLessonsForStaffByCourseID(ctx, courseID)
}

func (s *AuthoringService) ListQuestionsForStaffVersion(ctx context.Context, lessonVersionID int64) ([]domain.LessonQuestion, error) {
	if lessonVersionID <= 0 {
		return nil, domain.ErrInvalidInput
	}
	return s.store.ListQuestionsForStaffVersion(ctx, lessonVersionID)
}

func (s *AuthoringService) CreateDraftCourse(ctx context.Context, params domain.CreateCourseParams) (domain.Course, error) {
	params.Slug = strings.TrimSpace(params.Slug)
	params.Title = strings.TrimSpace(params.Title)
	if params.Slug == "" || len(params.Slug) > 100 || params.Title == "" || len(params.Title) > 200 ||
		params.CreatedByStaffID < 0 ||
		(params.Description != nil && len(*params.Description) > 4000) {
		return domain.Course{}, domain.ErrInvalidInput
	}
	return s.store.CreateDraftCourse(ctx, params)
}

func (s *AuthoringService) CreateDraftLesson(ctx context.Context, params domain.CreateLessonParams) (domain.Lesson, error) {
	params.Title = strings.TrimSpace(params.Title)
	if params.CourseID <= 0 || params.Title == "" || len(params.Title) > 200 ||
		params.CreatedByStaffID < 0 ||
		(params.Description != nil && len(*params.Description) > 4000) {
		return domain.Lesson{}, domain.ErrInvalidInput
	}
	return s.store.CreateDraftLesson(ctx, params)
}

func (s *AuthoringService) UpdateDraftLessonVersion(ctx context.Context, versionID int64, title string, description *string, updatedByStaffID int64) error {
	title = strings.TrimSpace(title)
	if versionID <= 0 || updatedByStaffID <= 0 || title == "" || len(title) > 200 ||
		(description != nil && len(*description) > 4000) {
		return domain.ErrInvalidInput
	}
	return s.store.UpdateDraftLessonVersion(ctx, versionID, title, description, updatedByStaffID)
}

func (s *AuthoringService) CreateDraftRevision(ctx context.Context, lessonID, createdByStaffID int64) (domain.Lesson, error) {
	if lessonID <= 0 || createdByStaffID < 0 {
		return domain.Lesson{}, domain.ErrInvalidInput
	}
	return s.store.CreateDraftRevision(ctx, lessonID, createdByStaffID)
}

func (s *AuthoringService) ImportDraftLesson(ctx context.Context, params domain.ImportDraftLessonParams) (domain.Lesson, error) {
	params.Title = strings.TrimSpace(params.Title)
	if params.CourseID <= 0 || params.CreatedByStaffID <= 0 || params.Title == "" ||
		len(params.Title) > 200 || len(params.Questions) == 0 || len(params.Questions) > 200 {
		return domain.Lesson{}, domain.ErrInvalidInput
	}
	if params.Description != nil {
		value := strings.TrimSpace(*params.Description)
		if len(value) > 4000 {
			return domain.Lesson{}, domain.ErrInvalidInput
		}
		if value == "" {
			params.Description = nil
		} else {
			params.Description = &value
		}
	}
	seenNumbers := make(map[int]struct{}, len(params.Questions))
	for index := range params.Questions {
		question := &params.Questions[index]
		question.Prompt = strings.TrimSpace(question.Prompt)
		question.AnswerType = strings.TrimSpace(question.AnswerType)
		question.ExpectedAnswer = strings.TrimSpace(question.ExpectedAnswer)
		if question.Number <= 0 || question.Prompt == "" || len(question.Prompt) > 4000 {
			return domain.Lesson{}, domain.ErrInvalidInput
		}
		if _, exists := seenNumbers[question.Number]; exists {
			return domain.Lesson{}, domain.ErrInvalidInput
		}
		seenNumbers[question.Number] = struct{}{}
		if question.ExpectedExplanation != nil {
			value := strings.TrimSpace(*question.ExpectedExplanation)
			if value == "" || len(value) > 4000 {
				return domain.Lesson{}, domain.ErrInvalidInput
			}
			question.ExpectedExplanation = &value
		}
		if question.Note != nil {
			value := strings.TrimSpace(*question.Note)
			if value == "" || len(value) > 4000 {
				return domain.Lesson{}, domain.ErrInvalidInput
			}
			question.Note = &value
		}
		switch question.AnswerType {
		case "text":
			if question.ExpectedAnswer == "" || len(question.ExpectedAnswer) > 4000 || len(question.Options) != 0 {
				return domain.Lesson{}, domain.ErrInvalidInput
			}
		case "single_choice", "multiple_choice":
			if question.ExpectedAnswer != "" || len(question.Options) < 2 || len(question.Options) > 20 {
				return domain.Lesson{}, domain.ErrInvalidInput
			}
		default:
			return domain.Lesson{}, domain.ErrInvalidInput
		}
		correctOptions := 0
		for optionIndex := range question.Options {
			option := &question.Options[optionIndex]
			option.Text = strings.TrimSpace(option.Text)
			if option.Text == "" || len(option.Text) > 1000 {
				return domain.Lesson{}, domain.ErrInvalidInput
			}
			if option.IsCorrect {
				correctOptions++
			}
		}
		if (question.AnswerType == "single_choice" && correctOptions != 1) ||
			(question.AnswerType == "multiple_choice" && correctOptions == 0) {
			return domain.Lesson{}, domain.ErrInvalidInput
		}
		if len(question.BiblePassages) > 20 {
			return domain.Lesson{}, domain.ErrInvalidInput
		}
		for passageIndex := range question.BiblePassages {
			passage := &question.BiblePassages[passageIndex]
			passage.BookName = strings.TrimSpace(passage.BookName)
			if passage.BookName == "" || len(passage.BookName) > 100 || passage.Chapter <= 0 ||
				passage.StartVerse <= 0 || passage.EndVerse < passage.StartVerse ||
				(passage.TranslationName != nil && len(strings.TrimSpace(*passage.TranslationName)) > 100) ||
				(passage.PassageText != nil && len(strings.TrimSpace(*passage.PassageText)) > 4000) ||
				(passage.SourceNote != nil && len(strings.TrimSpace(*passage.SourceNote)) > 1000) {
				return domain.Lesson{}, domain.ErrInvalidInput
			}
		}
	}
	return s.store.ImportDraftLesson(ctx, params)
}

func (s *AuthoringService) CreateQuestion(ctx context.Context, params domain.CreateQuestionParams) (domain.LessonQuestion, error) {
	params.Prompt = strings.TrimSpace(params.Prompt)
	params.ExpectedAnswer = strings.TrimSpace(params.ExpectedAnswer)
	params.AnswerType = strings.TrimSpace(params.AnswerType)
	if params.LessonVersionID <= 0 || params.QuestionNumber <= 0 ||
		params.Prompt == "" || len(params.Prompt) > 4000 ||
		(params.AnswerType == "text" && params.ExpectedAnswer == "") || len(params.ExpectedAnswer) > 4000 ||
		params.AnswerType == "" || params.CreatedByStaffID < 0 ||
		len(params.Options) > 20 || len(params.Criteria) > 50 ||
		len(params.CommonMistakes) > 50 || len(params.BiblePassages) > 20 ||
		(params.ExpectedExplanation != nil && len(strings.TrimSpace(*params.ExpectedExplanation)) > 4000) ||
		(params.Note != nil && len(strings.TrimSpace(*params.Note)) > 4000) {
		return domain.LessonQuestion{}, domain.ErrInvalidInput
	}
	return s.store.CreateQuestionWithMaterials(ctx, params)
}

func (s *AuthoringService) UpdateQuestion(ctx context.Context, questionID int64, params domain.CreateQuestionParams, updatedByStaffID int64) (domain.LessonQuestion, error) {
	params.Prompt = strings.TrimSpace(params.Prompt)
	params.ExpectedAnswer = strings.TrimSpace(params.ExpectedAnswer)
	params.AnswerType = strings.TrimSpace(params.AnswerType)
	if questionID <= 0 || params.LessonVersionID <= 0 || params.QuestionNumber <= 0 ||
		params.Prompt == "" || len(params.Prompt) > 4000 ||
		(params.AnswerType == "text" && params.ExpectedAnswer == "") || len(params.ExpectedAnswer) > 4000 ||
		params.AnswerType == "" || updatedByStaffID <= 0 ||
		len(params.Options) > 20 || len(params.Criteria) > 50 ||
		len(params.CommonMistakes) > 50 || len(params.BiblePassages) > 20 ||
		(params.ExpectedExplanation != nil && len(strings.TrimSpace(*params.ExpectedExplanation)) > 4000) ||
		(params.Note != nil && len(strings.TrimSpace(*params.Note)) > 4000) {
		return domain.LessonQuestion{}, domain.ErrInvalidInput
	}
	return s.store.UpdateQuestionWithMaterials(ctx, questionID, params, updatedByStaffID)
}

func (s *AuthoringService) PublishLessonVersion(ctx context.Context, versionID, publishedByStaffID int64) error {
	if versionID <= 0 || publishedByStaffID < 0 {
		return domain.ErrInvalidInput
	}
	return s.store.PublishLessonVersion(ctx, versionID, publishedByStaffID)
}

func (s *AuthoringService) PublishCourse(ctx context.Context, courseID, publishedByStaffID int64) error {
	if courseID <= 0 || publishedByStaffID < 0 {
		return domain.ErrInvalidInput
	}
	return s.store.PublishCourse(ctx, courseID, publishedByStaffID)
}

// StaffRoleStore is implemented by the repository that persists role assignments.
type StaffRoleStore interface {
	AssignStaffRole(context.Context, int64, int64, string) error
	RevokeStaffRole(context.Context, int64, int64, string) error
}

func (s *AuthoringService) AssignStaffRole(ctx context.Context, actorStaffID, targetStaffID int64, roleCode string) error {
	if actorStaffID <= 0 || targetStaffID <= 0 || actorStaffID == targetStaffID ||
		(roleCode != "administrator" && roleCode != "curator") {
		return domain.ErrInvalidInput
	}
	store, ok := s.store.(StaffRoleStore)
	if !ok {
		return domain.ErrInvalidState
	}
	return store.AssignStaffRole(ctx, actorStaffID, targetStaffID, roleCode)
}

func (s *AuthoringService) RevokeStaffRole(ctx context.Context, actorStaffID, targetStaffID int64, roleCode string) error {
	if actorStaffID <= 0 || targetStaffID <= 0 || actorStaffID == targetStaffID ||
		(roleCode != "administrator" && roleCode != "curator") {
		return domain.ErrInvalidInput
	}
	store, ok := s.store.(StaffRoleStore)
	if !ok {
		return domain.ErrInvalidState
	}
	return store.RevokeStaffRole(ctx, actorStaffID, targetStaffID, roleCode)
}
