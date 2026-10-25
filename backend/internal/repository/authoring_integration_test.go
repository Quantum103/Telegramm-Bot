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

func TestCreateQuestionStoresStaffActorAndAuditEvent(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	prefix := fmt.Sprintf("staff-question-audit-%d", time.Now().UnixNano())
	var staffID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO staff_accounts (display_name, email, account_status)
		VALUES ('Question author', $1, 'active')
		RETURNING id
	`, prefix+"@example.test").Scan(&staffID); err != nil {
		t.Fatalf("create staff account: %v", err)
	}

	var courseID, lessonID, versionID, questionID int64
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if questionID > 0 {
			if _, err := pool.Exec(cleanupCtx, "DELETE FROM audit_events WHERE entity_type = 'question' AND entity_id = $1", questionID); err != nil {
				t.Errorf("cleanup question audit event: %v", err)
			}
			if _, err := pool.Exec(cleanupCtx, "DELETE FROM lesson_questions WHERE id = $1", questionID); err != nil {
				t.Errorf("cleanup question: %v", err)
			}
		}
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
			}
			if versionID > 0 {
				if _, err := pool.Exec(cleanupCtx, "DELETE FROM lesson_versions WHERE id = $1", versionID); err != nil {
					t.Errorf("cleanup lesson version: %v", err)
				}
			}
			if lessonID > 0 {
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

	repo := NewCatalogRepository(pool)
	authoring := service.NewAuthoringService(repo)
	course, err := authoring.CreateDraftCourse(ctx, domain.CreateCourseParams{
		Slug: prefix, Title: "Question audit course", CreatedByStaffID: staffID,
	})
	if err != nil {
		t.Fatalf("create audited course: %v", err)
	}
	courseID = course.ID

	lesson, err := authoring.CreateDraftLesson(ctx, domain.CreateLessonParams{
		CourseID: course.ID, Title: "Question audit lesson", CreatedByStaffID: staffID,
	})
	if err != nil {
		t.Fatalf("create audited lesson: %v", err)
	}
	lessonID, versionID = lesson.ID, lesson.LessonVersionID

	question, err := authoring.CreateQuestion(ctx, domain.CreateQuestionParams{
		LessonVersionID: versionID, QuestionNumber: 1, Prompt: "Question for auditing",
		ExpectedAnswer: "An internal expected answer", AnswerType: "text", CreatedByStaffID: staffID,
	})
	if err != nil {
		t.Fatalf("create audited question: %v", err)
	}
	questionID = question.ID

	var actorID, entityID int64
	var action, entityType, auditedVersionID, number, answerType string
	if err := pool.QueryRow(ctx, `
		SELECT actor_staff_id, action, entity_type, entity_id,
		       details->>'lesson_version_id', details->>'question_number', details->>'answer_type'
		FROM audit_events
		WHERE entity_type = 'question' AND entity_id = $1
		ORDER BY id DESC LIMIT 1
	`, question.ID).Scan(&actorID, &action, &entityType, &entityID, &auditedVersionID, &number, &answerType); err != nil {
		t.Fatalf("read question audit event: %v", err)
	}
	if actorID != staffID || action != "question.draft.create" || entityType != "question" ||
		entityID != question.ID || auditedVersionID != fmt.Sprint(versionID) || number != "1" || answerType != "text" {
		t.Fatalf("unexpected question audit event: actor=%d action=%q entity=%q id=%d version=%s number=%q type=%q",
			actorID, action, entityType, entityID, auditedVersionID, number, answerType)
	}
}

func TestListLessonsForStaffIncludesDraftVersion(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	repo := NewCatalogRepository(pool)
	authoring := service.NewAuthoringService(repo)
	suffix := fmt.Sprintf("staff-lesson-list-%d", time.Now().UnixNano())
	course, err := authoring.CreateDraftCourse(ctx, domain.CreateCourseParams{
		Slug: suffix, Title: "Staff list draft course",
	})
	if err != nil {
		t.Fatalf("create draft course: %v", err)
	}
	lesson, err := authoring.CreateDraftLesson(ctx, domain.CreateLessonParams{
		CourseID: course.ID, Title: "Staff list draft lesson",
	})
	if err != nil {
		t.Fatalf("create draft lesson: %v", err)
	}
	question, err := authoring.CreateQuestion(ctx, domain.CreateQuestionParams{
		LessonVersionID: lesson.LessonVersionID, QuestionNumber: 1,
		Prompt: "Public prompt", ExpectedAnswer: "Private expected answer", AnswerType: "text",
	})
	if err != nil {
		t.Fatalf("create draft question: %v", err)
	}
	if question.ID <= 0 {
		t.Fatal("draft question was not created")
	}

	lessons, err := repo.ListLessonsForStaffByCourseID(ctx, course.ID)
	if err != nil {
		t.Fatalf("list lessons for staff: %v", err)
	}
	if len(lessons) != 1 {
		t.Fatalf("staff lessons count = %d, want 1: %+v", len(lessons), lessons)
	}
	got := lessons[0]
	if got.ID != lesson.ID || got.CourseID != course.ID || got.CourseTitle != course.Title ||
		got.LessonVersionID != lesson.LessonVersionID || got.VersionNumber != 1 ||
		got.VersionStatus != "draft" || got.Title != lesson.Title || got.QuestionCount != 1 {
		t.Fatalf("staff listing missed draft/version metadata: %+v", got)
	}
	if _, err := repo.ListLessonsForStaffByCourseID(ctx, 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero course ID error = %v, want ErrInvalidInput", err)
	}
}

func TestListQuestionsForStaffIncludesDraftQuestionAndMaterials(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	repo := NewCatalogRepository(pool)
	authoring := service.NewAuthoringService(repo)
	suffix := fmt.Sprintf("staff-question-list-%d", time.Now().UnixNano())
	course, err := authoring.CreateDraftCourse(ctx, domain.CreateCourseParams{Slug: suffix, Title: "Staff question listing"})
	if err != nil {
		t.Fatalf("create draft course: %v", err)
	}
	lesson, err := authoring.CreateDraftLesson(ctx, domain.CreateLessonParams{CourseID: course.ID, Title: "Question lesson"})
	if err != nil {
		t.Fatalf("create draft lesson: %v", err)
	}
	correctRequired := true
	_, err = authoring.CreateQuestion(ctx, domain.CreateQuestionParams{
		LessonVersionID: lesson.LessonVersionID, QuestionNumber: 1,
		Prompt: "Draft question prompt", ExpectedAnswer: "Expected secret answer", AnswerType: "single_choice",
		ExpectedExplanation: stringPtr("Explain why the answer is correct"),
		Criteria:            []domain.QuestionCriterionInput{{Key: "core", Description: "Core meaning", Weight: 2, Required: &correctRequired}},
		Options:             []domain.QuestionOptionInput{{Key: "A", Text: "Correct", IsCorrect: true}, {Key: "B", Text: "Incorrect"}},
		CommonMistakes:      []domain.CommonMistake{{Text: "Common mistake", Explanation: "Correction"}},
		BiblePassages:       []domain.BiblePassageInput{{BookName: "John", Chapter: 3, StartVerse: 16, EndVerse: 17, PassageText: stringPtr("Passage text"), SourceNote: stringPtr("Source note")}},
	})
	if err != nil {
		t.Fatalf("create draft question: %v", err)
	}

	questions, err := authoring.ListQuestionsForStaffVersion(ctx, lesson.LessonVersionID)
	if err != nil {
		t.Fatalf("list draft questions for staff: %v", err)
	}
	if len(questions) != 1 {
		t.Fatalf("staff question count = %d, want 1: %+v", len(questions), questions)
	}
	got := questions[0]
	if got.LessonVersionID != lesson.LessonVersionID || got.QuestionNumber != 1 ||
		got.Prompt != "Draft question prompt" || got.ExpectedAnswer != "Expected secret answer" ||
		got.ExpectedExplanation == nil || *got.ExpectedExplanation != "Explain why the answer is correct" ||
		len(got.Criteria) != 1 || len(got.Options) != 2 || !got.Options[0].IsCorrect ||
		len(got.CommonMistakes) != 1 || len(got.BiblePassages) != 1 || got.BiblePassages[0].SourceNote == nil {
		t.Fatalf("staff question listing lost draft content: %+v", got)
	}

	emptyLesson, err := authoring.CreateDraftLesson(ctx, domain.CreateLessonParams{CourseID: course.ID, Title: "Empty lesson"})
	if err != nil {
		t.Fatalf("create empty draft lesson: %v", err)
	}
	emptyQuestions, err := authoring.ListQuestionsForStaffVersion(ctx, emptyLesson.LessonVersionID)
	if err != nil || emptyQuestions == nil || len(emptyQuestions) != 0 {
		t.Fatalf("empty draft questions = %#v, error=%v; want empty list without error", emptyQuestions, err)
	}
	if _, err := authoring.ListQuestionsForStaffVersion(ctx, 0); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("zero version ID error = %v, want ErrInvalidInput", err)
	}
}
