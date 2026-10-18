package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/service"
)

func TestDraftAuthoringPublicationAndLearnerCatalog(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := NewCatalogRepository(pool)
	authoring := service.NewAuthoringService(repo)
	course, err := authoring.CreateDraftCourse(ctx, domain.CreateCourseParams{
		Slug:  fmt.Sprintf("authoring-%d", time.Now().UnixNano()),
		Title: "Черновой тестовый курс",
	})
	if err != nil {
		t.Fatalf("создать черновой курс: %v", err)
	}
	lesson, err := authoring.CreateDraftLesson(ctx, domain.CreateLessonParams{
		CourseID: course.ID, Title: "Первый тестовый урок",
	})
	if err != nil {
		t.Fatalf("создать черновой урок: %v", err)
	}
	required := true
	question, err := authoring.CreateQuestion(ctx, domain.CreateQuestionParams{
		LessonVersionID:     lesson.LessonVersionID,
		QuestionNumber:      1,
		Prompt:              "Какой вариант указан в тексте?",
		ExpectedAnswer:      "Предполагаемый ответ для внутренней проверки",
		AnswerType:          "single_choice",
		ExpectedExplanation: stringPtr("Внутреннее пояснение"),
		Criteria: []domain.QuestionCriterionInput{{
			Key: "central_point", Description: "Смысловой критерий", Weight: 1,
			Required: &required,
		}},
		Options: []domain.QuestionOptionInput{
			{Key: "A", Text: "Правильный вариант", IsCorrect: true},
			{Key: "B", Text: "Другой вариант"},
		},
		CommonMistakes: []domain.CommonMistake{{
			Text: "Типичная ошибка", Explanation: "Пояснение для куратора/проверки",
		}},
		BiblePassages: []domain.BiblePassageInput{{
			BookName: "Иоанна", Chapter: 3, StartVerse: 16, EndVerse: 17,
			TranslationName: stringPtr("Тестовый перевод"),
			PassageText:     stringPtr("Тестовый текст отрывка"),
		}},
	})
	if err != nil {
		t.Fatalf("создать вопрос с материалами: %v", err)
	}
	if question.ID <= 0 || len(question.Options) != 2 || len(question.Criteria) != 1 || len(question.BiblePassages) != 1 {
		t.Fatalf("не все связанные материалы были созданы: %+v", question)
	}

	courses, err := repo.ListPublishedCourses(ctx)
	if err != nil {
		t.Fatalf("прочитать курсы до публикации: %v", err)
	}
	for _, published := range courses {
		if published.ID == course.ID {
			t.Fatal("черновой курс виден в ученическом каталоге")
		}
	}
	if err := authoring.PublishLessonVersion(ctx, lesson.LessonVersionID); err != nil {
		t.Fatalf("опубликовать версию урока: %v", err)
	}
	if err := authoring.PublishCourse(ctx, course.ID); err != nil {
		t.Fatalf("опубликовать курс: %v", err)
	}
	courses, err = repo.ListPublishedCourses(ctx)
	if err != nil {
		t.Fatalf("получить каталог после публикации: %v", err)
	}
	var found bool
	for _, published := range courses {
		if published.ID == course.ID {
			found = true
			if published.LessonCount != 1 {
				t.Fatalf("число опубликованных уроков = %d, хотели 1", published.LessonCount)
			}
		}
	}
	if !found {
		t.Fatal("опубликованный курс не появился в ученическом каталоге")
	}

	questions, err := NewContentRepository(pool).ListQuestionsForVersion(ctx, lesson.LessonVersionID)
	if err != nil {
		t.Fatalf("прочитать опубликованные учебные материалы: %v", err)
	}
	if len(questions) != 1 || questions[0].ExpectedAnswer != "Предполагаемый ответ для внутренней проверки" ||
		len(questions[0].Criteria) != 1 || len(questions[0].CommonMistakes) != 1 ||
		len(questions[0].BiblePassages) != 1 || len(questions[0].Options) != 2 {
		t.Fatalf("опубликованные учебные материалы потеряли связанные данные: %+v", questions)
	}

	_, err = repo.CreateQuestionWithMaterials(ctx, domain.CreateQuestionParams{
		LessonVersionID: lesson.LessonVersionID, QuestionNumber: 2,
		Prompt: "Вопрос после публикации", ExpectedAnswer: "Ответ",
		AnswerType: "text",
	})
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("нельзя менять опубликованную версию, получено %v", err)
	}
	if err := authoring.PublishCourse(ctx, course.ID); err != nil {
		t.Fatalf("повторная публикация курса должна быть идемпотентной: %v", err)
	}
}

func stringPtr(value string) *string { return &value }
