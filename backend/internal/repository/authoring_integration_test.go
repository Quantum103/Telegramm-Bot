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
	revision, err := authoring.CreateDraftRevision(ctx, lesson.ID)
	if err != nil {
		t.Fatalf("создать новую черновую версию опубликованного урока: %v", err)
	}
	if revision.LessonVersionID == lesson.LessonVersionID || revision.VersionNumber != 2 || revision.QuestionCount != 1 {
		t.Fatalf("новая версия создана некорректно: %+v", revision)
	}
	if err := authoring.PublishLessonVersion(ctx, revision.LessonVersionID); err != nil {
		t.Fatalf("опубликовать новую версию урока: %v", err)
	}

	currentLessons, err := repo.ListPublishedLessonsByCourseID(ctx, course.ID)
	if err != nil {
		t.Fatalf("получить уроки после обновления версии: %v", err)
	}
	if len(currentLessons) != 1 || currentLessons[0].LessonVersionID != revision.LessonVersionID ||
		currentLessons[0].VersionNumber != 2 {
		t.Fatalf("ученический каталог не переключился на новую версию: %+v", currentLessons)
	}
	archivedQuestions, err := NewContentRepository(pool).ListQuestionsForVersion(ctx, lesson.LessonVersionID)
	if err != nil {
		t.Fatalf("восстановить вопросы архивной версии: %v", err)
	}
	if len(archivedQuestions) != 1 || archivedQuestions[0].Prompt != "Какой вариант указан в тексте?" ||
		archivedQuestions[0].ExpectedAnswer != "Предполагаемый ответ для внутренней проверки" ||
		len(archivedQuestions[0].Options) != 2 || len(archivedQuestions[0].Criteria) != 1 ||
		len(archivedQuestions[0].BiblePassages) != 1 {
		t.Fatalf("архивная версия не сохранила прежние материалы: %+v", archivedQuestions)
	}
	if err := authoring.PublishCourse(ctx, course.ID); err != nil {
		t.Fatalf("повторная публикация курса должна быть идемпотентной: %v", err)
	}
}

func stringPtr(value string) *string { return &value }

func TestCreateDraftCourseStoresStaffActorAndAuditEvent(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	prefix := fmt.Sprintf("staff-course-audit-%d", time.Now().UnixNano())
	var staffID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO staff_accounts (display_name, email, account_status)
		VALUES ('Course author', $1, 'active')
		RETURNING id
	`, prefix+"@example.test").Scan(&staffID); err != nil {
		t.Fatalf("create staff account: %v", err)
	}

	var courseID int64
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if courseID > 0 {
			if _, err := pool.Exec(cleanupCtx, "DELETE FROM audit_events WHERE entity_type = 'course' AND entity_id = $1", courseID); err != nil {
				t.Errorf("cleanup course audit event: %v", err)
			}
			if _, err := pool.Exec(cleanupCtx, "DELETE FROM courses WHERE id = $1", courseID); err != nil {
				t.Errorf("cleanup course: %v", err)
			}
		}
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM staff_accounts WHERE id = $1", staffID); err != nil {
			t.Errorf("cleanup staff account: %v", err)
		}
	})

	course, err := service.NewAuthoringService(NewCatalogRepository(pool)).CreateDraftCourse(ctx, domain.CreateCourseParams{
		Slug: prefix, Title: "Staff-created draft", CreatedByStaffID: staffID,
	})
	if err != nil {
		t.Fatalf("create audited course: %v", err)
	}
	courseID = course.ID

	var createdBy int64
	if err := pool.QueryRow(ctx, "SELECT created_by_staff_id FROM courses WHERE id = $1", course.ID).Scan(&createdBy); err != nil {
		t.Fatalf("read course creator: %v", err)
	}
	if createdBy != staffID {
		t.Fatalf("course creator = %d, want %d", createdBy, staffID)
	}

	var actorID, entityID int64
	var action, entityType, auditedSlug string
	if err := pool.QueryRow(ctx, `
		SELECT actor_staff_id, action, entity_type, entity_id, details->>'slug'
		FROM audit_events
		WHERE entity_type = 'course' AND entity_id = $1
	`, course.ID).Scan(&actorID, &action, &entityType, &entityID, &auditedSlug); err != nil {
		t.Fatalf("read course audit event: %v", err)
	}
	if actorID != staffID || action != "course.draft.create" || entityType != "course" || entityID != course.ID || auditedSlug != prefix {
		t.Fatalf("unexpected audit event: actor=%d action=%q entity=%q id=%d slug=%q", actorID, action, entityType, entityID, auditedSlug)
	}
}

func TestCreateDraftLessonStoresStaffActorAndAuditEvent(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	prefix := fmt.Sprintf("staff-lesson-audit-%d", time.Now().UnixNano())
	var staffID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO staff_accounts (display_name, email, account_status)
		VALUES ('Lesson author', $1, 'active')
		RETURNING id
	`, prefix+"@example.test").Scan(&staffID); err != nil {
		t.Fatalf("create staff account: %v", err)
	}

	var courseID, lessonID int64
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if lessonID > 0 {
			if _, err := pool.Exec(cleanupCtx, "DELETE FROM audit_events WHERE entity_type = 'lesson' AND entity_id = $1", lessonID); err != nil {
				t.Errorf("cleanup lesson audit event: %v", err)
			}
		}
		if courseID > 0 {
			if _, err := pool.Exec(cleanupCtx, "DELETE FROM audit_events WHERE entity_type = 'course' AND entity_id = $1", courseID); err != nil {
				t.Errorf("cleanup course audit event: %v", err)
			}
			if lessonID > 0 {
				if _, err := pool.Exec(cleanupCtx, "DELETE FROM course_lessons WHERE course_id = $1", courseID); err != nil {
					t.Errorf("cleanup course lesson links: %v", err)
				}
				if _, err := pool.Exec(cleanupCtx, "DELETE FROM lesson_versions WHERE lesson_id = $1", lessonID); err != nil {
					t.Errorf("cleanup lesson versions: %v", err)
				}
				if _, err := pool.Exec(cleanupCtx, "DELETE FROM lessons WHERE id = $1", lessonID); err != nil {
					t.Errorf("cleanup lesson: %v", err)
				}
			}
			if _, err := pool.Exec(cleanupCtx, "DELETE FROM courses WHERE id = $1", courseID); err != nil {
				t.Errorf("cleanup course: %v", err)
			}
		}
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM staff_accounts WHERE id = $1", staffID); err != nil {
			t.Errorf("cleanup staff account: %v", err)
		}
	})

	course, err := service.NewAuthoringService(NewCatalogRepository(pool)).CreateDraftCourse(ctx, domain.CreateCourseParams{
		Slug: prefix, Title: "Lesson audit course", CreatedByStaffID: staffID,
	})
	if err != nil {
		t.Fatalf("create audited course: %v", err)
	}
	courseID = course.ID

	lesson, err := service.NewAuthoringService(NewCatalogRepository(pool)).CreateDraftLesson(ctx, domain.CreateLessonParams{
		CourseID: course.ID, Title: "Staff-created draft lesson", CreatedByStaffID: staffID,
	})
	if err != nil {
		t.Fatalf("create audited lesson: %v", err)
	}
	lessonID = lesson.ID

	var createdBy int64
	if err := pool.QueryRow(ctx, "SELECT created_by_staff_id FROM lesson_versions WHERE id = $1", lesson.LessonVersionID).Scan(&createdBy); err != nil {
		t.Fatalf("read lesson version creator: %v", err)
	}
	if createdBy != staffID {
		t.Fatalf("lesson version creator = %d, want %d", createdBy, staffID)
	}

	var actorID, entityID int64
	var action, entityType, auditedCourseID, versionNumber, status string
	if err := pool.QueryRow(ctx, `
		SELECT actor_staff_id, action, entity_type, entity_id,
		       details->>'course_id', details->>'version_number', details->>'status'
		FROM audit_events
		WHERE entity_type = 'lesson' AND entity_id = $1
		ORDER BY id DESC LIMIT 1
	`, lesson.ID).Scan(&actorID, &action, &entityType, &entityID, &auditedCourseID, &versionNumber, &status); err != nil {
		t.Fatalf("read lesson audit event: %v", err)
	}
	if actorID != staffID || action != "lesson.draft.create" || entityType != "lesson" ||
		entityID != lesson.ID || auditedCourseID != fmt.Sprint(course.ID) || versionNumber != "1" || status != "draft" {
		t.Fatalf("unexpected audit event: actor=%d action=%q entity=%q id=%d course=%q version=%q status=%q",
			actorID, action, entityType, entityID, auditedCourseID, versionNumber, status)
	}
}
