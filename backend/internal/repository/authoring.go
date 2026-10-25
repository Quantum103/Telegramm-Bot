package repository

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (r *CatalogRepository) CreateDraftCourse(ctx context.Context, params domain.CreateCourseParams) (domain.Course, error) {
	params.Slug = strings.TrimSpace(params.Slug)
	params.Title = strings.TrimSpace(params.Title)
	if !validCourseSlug(params.Slug) || len(params.Slug) > 100 ||
		params.Title == "" || len(params.Title) > 200 ||
		params.CreatedByStaffID < 0 ||
		(params.Description != nil && len(*params.Description) > 4000) {
		return domain.Course{}, ErrInvalidInput
	}
	var description any
	if params.Description != nil && strings.TrimSpace(*params.Description) != "" {
		description = strings.TrimSpace(*params.Description)
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Course{}, mapDatabaseError("начать транзакцию создания курса", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	course, err := scanCourse(tx.QueryRow(ctx, `
		INSERT INTO courses (slug, title, description, status, created_by_staff_id)
		VALUES ($1, $2, $3, 'draft', NULLIF($4, 0))
		RETURNING id, slug, title, description, status, published_at,
		          created_at, updated_at, 0::INTEGER
	`, params.Slug, params.Title, description, params.CreatedByStaffID))
	if err != nil {
		return domain.Course{}, mapDatabaseError("создать черновик курса", err)
	}

	if params.CreatedByStaffID > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO audit_events (actor_staff_id, action, entity_type, entity_id, details)
			VALUES ($1, 'course.draft.create', 'course', $2, jsonb_build_object('slug', $3::text, 'status', 'draft'))
		`, params.CreatedByStaffID, course.ID, course.Slug); err != nil {
			return domain.Course{}, mapDatabaseError("записать аудит создания курса", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Course{}, mapDatabaseError("зафиксировать создание черновика курса", err)
	}
	return course, nil
}

func (r *CatalogRepository) CreateDraftLesson(ctx context.Context, params domain.CreateLessonParams) (domain.Lesson, error) {
	params.Title = strings.TrimSpace(params.Title)
	if params.CourseID <= 0 || params.Title == "" || len(params.Title) > 200 ||
		params.CreatedByStaffID < 0 ||
		(params.Description != nil && len(*params.Description) > 4000) {
		return domain.Lesson{}, ErrInvalidInput
	}
	var description any
	if params.Description != nil && strings.TrimSpace(*params.Description) != "" {
		value := strings.TrimSpace(*params.Description)
		description = value
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Lesson{}, mapDatabaseError("начать транзакцию создания урока", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var courseTitle, courseStatus string
	if err := tx.QueryRow(ctx, `
		SELECT title, status FROM courses WHERE id = $1 FOR UPDATE
	`, params.CourseID).Scan(&courseTitle, &courseStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Lesson{}, ErrNotFound
		}
		return domain.Lesson{}, mapDatabaseError("проверить курс для нового урока", err)
	}
	if courseStatus != "draft" {
		return domain.Lesson{}, ErrInvalidState
	}

	var position int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(position), 0) + 1
		FROM course_lessons WHERE course_id = $1
	`, params.CourseID).Scan(&position); err != nil {
		return domain.Lesson{}, mapDatabaseError("определить позицию нового урока", err)
	}

	var lessonID, versionID int64
	if err := tx.QueryRow(ctx, "INSERT INTO lessons DEFAULT VALUES RETURNING id").Scan(&lessonID); err != nil {
		return domain.Lesson{}, mapDatabaseError("создать урок", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO lesson_versions (lesson_id, version_number, title, description, status, created_by_staff_id)
		VALUES ($1, 1, $2, $3, 'draft', NULLIF($4, 0))
		RETURNING id
	`, lessonID, params.Title, description, params.CreatedByStaffID).Scan(&versionID); err != nil {
		return domain.Lesson{}, mapDatabaseError("создать черновую версию урока", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO course_lessons (course_id, lesson_id, position)
		VALUES ($1, $2, $3)
	`, params.CourseID, lessonID, position); err != nil {
		return domain.Lesson{}, mapDatabaseError("добавить урок в курс", err)
	}
	if params.CreatedByStaffID > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO audit_events (actor_staff_id, action, entity_type, entity_id, details)
			VALUES ($1, 'lesson.draft.create', 'lesson', $2,
			        jsonb_build_object('course_id', $3::bigint, 'version_number', 1, 'status', 'draft'))
		`, params.CreatedByStaffID, lessonID, params.CourseID); err != nil {
			return domain.Lesson{}, mapDatabaseError("записать аудит создания урока", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Lesson{}, mapDatabaseError("зафиксировать создание урока", err)
	}

	return domain.Lesson{
		ID: lessonID, CourseID: params.CourseID, CourseTitle: courseTitle,
		Position: position, LessonVersionID: versionID, VersionNumber: 1,
		Title: params.Title, Description: cleanOptionalString(params.Description),
		QuestionCount: 0,
	}, nil
}

func (r *CatalogRepository) ListQuestionsForStaffVersion(ctx context.Context, lessonVersionID int64) ([]domain.LessonQuestion, error) {
	if lessonVersionID <= 0 {
		return nil, ErrInvalidInput
	}
	return NewContentRepository(r.pool).ListQuestionsForStaffVersion(ctx, lessonVersionID)
}

func (r *CatalogRepository) CreateQuestionWithMaterials(ctx context.Context, params domain.CreateQuestionParams) (domain.LessonQuestion, error) {
	if params.CreatedByStaffID < 0 || len(params.Prompt) > 4000 || len(params.ExpectedAnswer) > 4000 ||
		len(params.Options) > 20 || len(params.Criteria) > 50 ||
		len(params.CommonMistakes) > 50 || len(params.BiblePassages) > 20 ||
		(params.ExpectedExplanation != nil && len(*params.ExpectedExplanation) > 4000) {
		return domain.LessonQuestion{}, ErrInvalidInput
	}
	if err := validateQuestionCreation(&params); err != nil {
		return domain.LessonQuestion{}, err
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.LessonQuestion{}, mapDatabaseError("начать транзакцию создания вопроса", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var versionStatus string
	if err := tx.QueryRow(ctx, `
		SELECT status FROM lesson_versions WHERE id = $1 FOR UPDATE
	`, params.LessonVersionID).Scan(&versionStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.LessonQuestion{}, ErrNotFound
		}
		return domain.LessonQuestion{}, mapDatabaseError("проверить версию для нового вопроса", err)
	}
	if versionStatus != "draft" {
		return domain.LessonQuestion{}, ErrInvalidState
	}

	var question domain.LessonQuestion
	if err := tx.QueryRow(ctx, `
		INSERT INTO lesson_questions (
			lesson_version_id, question_number, prompt, expected_answer,
			answer_type, expected_explanation
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at, updated_at
	`, params.LessonVersionID, params.QuestionNumber, strings.TrimSpace(params.Prompt),
		strings.TrimSpace(params.ExpectedAnswer), params.AnswerType, cleanOptional(params.ExpectedExplanation),
	).Scan(&question.ID, &question.CreatedAt, &question.UpdatedAt); err != nil {
		return domain.LessonQuestion{}, mapDatabaseError("создать вопрос урока", err)
	}
	question.LessonVersionID = params.LessonVersionID
	question.QuestionNumber = params.QuestionNumber
	question.Prompt = strings.TrimSpace(params.Prompt)
	question.ExpectedAnswer = strings.TrimSpace(params.ExpectedAnswer)
	question.AnswerType = params.AnswerType
	question.ExpectedExplanation = cleanOptionalString(params.ExpectedExplanation)

	batch := &pgx.Batch{}
	batchCount := len(params.Options) + len(params.Criteria) + len(params.CommonMistakes)
	for index, option := range params.Options {
		batch.Queue(`
			INSERT INTO question_options (question_id, option_key, option_text, sort_order, is_correct)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id
		`, question.ID, strings.TrimSpace(option.Key), strings.TrimSpace(option.Text), index+1, option.IsCorrect)
	}
	for index, criterion := range params.Criteria {
		weight := criterion.Weight
		if weight == 0 {
			weight = 1
		}
		required := true
		if criterion.Required != nil {
			required = *criterion.Required
		}
		batch.Queue(`
			INSERT INTO question_criteria (question_id, criterion_key, description, weight, is_required, sort_order)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, question.ID, strings.TrimSpace(criterion.Key), strings.TrimSpace(criterion.Description), weight, required, index+1)
	}
	for index, mistake := range params.CommonMistakes {
		batch.Queue(`
			INSERT INTO question_common_mistakes (question_id, mistake_text, explanation, sort_order)
			VALUES ($1, $2, $3, $4)
		`, question.ID, strings.TrimSpace(mistake.Text), strings.TrimSpace(mistake.Explanation), index+1)
	}
	if batchCount > 0 {
		results := tx.SendBatch(ctx, batch)
		for index := range params.Options {
			var optionID int64
			if err := results.QueryRow().Scan(&optionID); err != nil {
				_ = results.Close()
				return domain.LessonQuestion{}, mapDatabaseError("создать вариант ответа", err)
			}
			question.Options = append(question.Options, domain.QuestionOption{
				ID: optionID, Key: strings.TrimSpace(params.Options[index].Key),
				Text: strings.TrimSpace(params.Options[index].Text), IsCorrect: params.Options[index].IsCorrect,
			})
		}
		for range params.Criteria {
			if _, err := results.Exec(); err != nil {
				_ = results.Close()
				return domain.LessonQuestion{}, mapDatabaseError("создать критерий оценки", err)
			}
		}
		for range params.CommonMistakes {
			if _, err := results.Exec(); err != nil {
				_ = results.Close()
				return domain.LessonQuestion{}, mapDatabaseError("создать пояснение типичной ошибки", err)
			}
		}
		if err := results.Close(); err != nil {
			return domain.LessonQuestion{}, mapDatabaseError("завершить пакет создания материалов вопроса", err)
		}
	}
	question.Criteria = make([]domain.QuestionCriterion, 0, len(params.Criteria))
	for _, criterion := range params.Criteria {
		weight := criterion.Weight
		if weight == 0 {
			weight = 1
		}
		required := true
		if criterion.Required != nil {
			required = *criterion.Required
		}
		question.Criteria = append(question.Criteria, domain.QuestionCriterion{
			Key: strings.TrimSpace(criterion.Key), Description: strings.TrimSpace(criterion.Description),
			Weight: weight, Required: required,
		})
	}
	question.CommonMistakes = params.CommonMistakes
	question.BiblePassages = make([]domain.BiblePassage, 0, len(params.BiblePassages))
	for index, passage := range params.BiblePassages {
		var passageID int64
		var translationName, passageText, sourceNote any
		if value := cleanOptional(passage.TranslationName); value != nil {
			translationName = value
		}
		if value := cleanOptional(passage.PassageText); value != nil {
			passageText = value
		}
		if value := cleanOptional(passage.SourceNote); value != nil {
			sourceNote = value
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO bible_passages (
				book_name, chapter, start_verse, end_verse,
				translation_name, passage_text, source_note
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING id
		`, strings.TrimSpace(passage.BookName), passage.Chapter, passage.StartVerse,
			passage.EndVerse, translationName, passageText, sourceNote).Scan(&passageID); err != nil {
			return domain.LessonQuestion{}, mapDatabaseError("создать библейский отрывок", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO question_bible_passages (question_id, passage_id, sort_order)
			VALUES ($1, $2, $3)
		`, question.ID, passageID, index+1); err != nil {
			return domain.LessonQuestion{}, mapDatabaseError("связать вопрос с библейским отрывком", err)
		}
		question.BiblePassages = append(question.BiblePassages, domain.BiblePassage{
			ID: passageID, BookName: strings.TrimSpace(passage.BookName),
			Chapter: passage.Chapter, StartVerse: passage.StartVerse, EndVerse: passage.EndVerse,
			TranslationName: cleanOptionalString(passage.TranslationName),
			PassageText:     cleanOptionalString(passage.PassageText),
			SourceNote:      cleanOptionalString(passage.SourceNote),
		})
	}

	if params.CreatedByStaffID > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO audit_events (actor_staff_id, action, entity_type, entity_id, details)
			VALUES ($1, 'question.draft.create', 'question', $2,
			        jsonb_build_object('lesson_version_id', $3::bigint,
			                           'question_number', $4::integer,
			                           'answer_type', $5::text))
		`, params.CreatedByStaffID, question.ID, params.LessonVersionID, params.QuestionNumber, params.AnswerType); err != nil {
			return domain.LessonQuestion{}, mapDatabaseError("записать аудит создания вопроса", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.LessonQuestion{}, mapDatabaseError("зафиксировать создание вопроса и материалов", err)
	}
	return question, nil
}

// CreateDraftRevision создаёт новую неизменяемую версию на основе последнего опубликованного содержимого.
func (r *CatalogRepository) CreateDraftRevision(ctx context.Context, lessonID int64) (domain.Lesson, error) {
	if lessonID <= 0 {
		return domain.Lesson{}, ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Lesson{}, mapDatabaseError("начать создание версии урока", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lockedLessonID int64
	if err := tx.QueryRow(ctx, "SELECT id FROM lessons WHERE id = $1 FOR UPDATE", lessonID).Scan(&lockedLessonID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Lesson{}, ErrNotFound
		}
		return domain.Lesson{}, mapDatabaseError("заблокировать урок для создания версии", err)
	}

	var baseVersionID int64
	var baseTitle string
	var baseDescription pgtype.Text
	if err := tx.QueryRow(ctx, `
		SELECT id, title, description
		FROM lesson_versions
		WHERE lesson_id = $1 AND status IN ('published', 'archived')
		ORDER BY version_number DESC
		LIMIT 1
	`, lessonID).Scan(&baseVersionID, &baseTitle, &baseDescription); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Lesson{}, ErrInvalidState
		}
		return domain.Lesson{}, mapDatabaseError("найти исходную опубликованную версию", err)
	}

	var nextVersionNumber int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(version_number), 0) + 1
		FROM lesson_versions WHERE lesson_id = $1
	`, lessonID).Scan(&nextVersionNumber); err != nil {
		return domain.Lesson{}, mapDatabaseError("определить номер новой версии", err)
	}

	var newVersionID int64
	var description any
	if baseDescription.Valid {
		description = baseDescription.String
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO lesson_versions (lesson_id, version_number, title, description, status)
		VALUES ($1, $2, $3, $4, 'draft')
		RETURNING id
	`, lessonID, nextVersionNumber, baseTitle, description).Scan(&newVersionID); err != nil {
		return domain.Lesson{}, mapDatabaseError("создать черновую версию урока", err)
	}

	type questionCopy struct {
		oldID           int64
		newID           int64
		number          int
		prompt          string
		expectedAnswer  string
		answerType      string
		expectedExplain pgtype.Text
	}
	rows, err := tx.Query(ctx, `
		SELECT id, question_number, prompt, expected_answer, answer_type, expected_explanation
		FROM lesson_questions
		WHERE lesson_version_id = $1
		ORDER BY question_number
	`, baseVersionID)
	if err != nil {
		return domain.Lesson{}, mapDatabaseError("получить вопросы исходной версии", err)
	}
	questions := make([]questionCopy, 0)
	for rows.Next() {
		var item questionCopy
		if err := rows.Scan(&item.oldID, &item.number, &item.prompt, &item.expectedAnswer,
			&item.answerType, &item.expectedExplain); err != nil {
			rows.Close()
			return domain.Lesson{}, mapDatabaseError("прочитать вопрос исходной версии", err)
		}
		questions = append(questions, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.Lesson{}, mapDatabaseError("обойти вопросы исходной версии", err)
	}
	rows.Close()

	for index := range questions {
		item := &questions[index]
		var explanation any
		if item.expectedExplain.Valid {
			explanation = item.expectedExplain.String
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO lesson_questions (
				lesson_version_id, question_number, prompt, expected_answer, answer_type, expected_explanation
			)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id
		`, newVersionID, item.number, item.prompt, item.expectedAnswer, item.answerType, explanation).Scan(&item.newID); err != nil {
			return domain.Lesson{}, mapDatabaseError("скопировать вопрос в новую версию", err)
		}
		copyStatements := []struct {
			name string
			sql  string
		}{
			{
				name: "варианты ответа",
				sql: `INSERT INTO question_options (question_id, option_key, option_text, sort_order, is_correct)
				      SELECT $1, option_key, option_text, sort_order, is_correct
				      FROM question_options WHERE question_id = $2`,
			},
			{
				name: "критерии оценки",
				sql: `INSERT INTO question_criteria (question_id, criterion_key, description, weight, is_required, sort_order)
				      SELECT $1, criterion_key, description, weight, is_required, sort_order
				      FROM question_criteria WHERE question_id = $2`,
			},
			{
				name: "распространённые ошибки",
				sql: `INSERT INTO question_common_mistakes (question_id, mistake_text, explanation, sort_order)
				      SELECT $1, mistake_text, explanation, sort_order
				      FROM question_common_mistakes WHERE question_id = $2`,
			},
			{
				name: "библейские отрывки",
				sql: `INSERT INTO question_bible_passages (question_id, passage_id, sort_order)
				      SELECT $1, passage_id, sort_order
				      FROM question_bible_passages WHERE question_id = $2`,
			},
		}
		for _, statement := range copyStatements {
			if _, err := tx.Exec(ctx, statement.sql, item.newID, item.oldID); err != nil {
				return domain.Lesson{}, mapDatabaseError("скопировать "+statement.name, err)
			}
		}
	}

	var result domain.Lesson
	var publishedCourseTitle pgtype.Text
	err = tx.QueryRow(ctx, `
		SELECT cl.course_id, c.title, cl.position
		FROM course_lessons cl
		JOIN courses c ON c.id = cl.course_id
		WHERE cl.lesson_id = $1
		ORDER BY c.id
		LIMIT 1
	`, lessonID).Scan(&result.CourseID, &publishedCourseTitle, &result.Position)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return domain.Lesson{}, mapDatabaseError("получить курс урока", err)
	}
	result.ID = lessonID
	result.LessonVersionID = newVersionID
	result.VersionNumber = nextVersionNumber
	result.Title = baseTitle
	result.Description = cleanOptionalString(func() *string {
		if baseDescription.Valid {
			value := baseDescription.String
			return &value
		}
		return nil
	}())
	result.QuestionCount = len(questions)
	if publishedCourseTitle.Valid {
		result.CourseTitle = publishedCourseTitle.String
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Lesson{}, mapDatabaseError("зафиксировать новую версию урока", err)
	}
	return result, nil
}

func (r *CatalogRepository) PublishLessonVersion(ctx context.Context, versionID int64) error {
	if versionID <= 0 {
		return ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapDatabaseError("начать публикацию версии урока", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lessonID int64
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT lesson_id, status FROM lesson_versions WHERE id = $1 FOR UPDATE
	`, versionID).Scan(&lessonID, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return mapDatabaseError("проверить версию для публикации", err)
	}
	if status == "published" {
		return tx.Commit(ctx)
	}
	if status != "draft" {
		return ErrInvalidState
	}
	var lockedLessonID int64
	if err := tx.QueryRow(ctx, "SELECT id FROM lessons WHERE id = $1 FOR UPDATE", lessonID).Scan(&lockedLessonID); err != nil {
		return mapDatabaseError("заблокировать урок для публикации", err)
	}

	var questionCount, invalidChoiceCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::INTEGER,
		       count(*) FILTER (
		           WHERE q.answer_type IN ('single_choice', 'multiple_choice')
		             AND (SELECT count(*) FROM question_options o WHERE o.question_id = q.id) < 2
		       )::INTEGER
		FROM lesson_questions q
		WHERE q.lesson_version_id = $1
	`, versionID).Scan(&questionCount, &invalidChoiceCount); err != nil {
		return mapDatabaseError("проверить готовность урока к публикации", err)
	}
	if questionCount == 0 || invalidChoiceCount > 0 {
		return ErrInvalidState
	}
	if _, err := tx.Exec(ctx, `
		UPDATE lesson_versions SET status = 'archived'
		WHERE lesson_id = $1 AND status = 'published'
	`, lessonID); err != nil {
		return mapDatabaseError("архивировать предыдущую опубликованную версию", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE lesson_versions SET status = 'published', published_at = now()
		WHERE id = $1 AND status = 'draft'
	`, versionID); err != nil {
		return mapDatabaseError("опубликовать новую версию урока", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return mapDatabaseError("зафиксировать публикацию версии урока", err)
	}
	return nil
}

func (r *CatalogRepository) PublishCourse(ctx context.Context, courseID int64) error {
	if courseID <= 0 {
		return ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapDatabaseError("начать публикацию курса", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	if err := tx.QueryRow(ctx, "SELECT status FROM courses WHERE id = $1 FOR UPDATE", courseID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return mapDatabaseError("проверить курс для публикации", err)
	}
	if status == "published" {
		return tx.Commit(ctx)
	}
	if status != "draft" {
		return ErrInvalidState
	}
	var hasPublishedLesson bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM course_lessons cl
			JOIN lesson_versions lv ON lv.lesson_id = cl.lesson_id AND lv.status = 'published'
			WHERE cl.course_id = $1
		)
	`, courseID).Scan(&hasPublishedLesson); err != nil {
		return mapDatabaseError("проверить уроки курса перед публикацией", err)
	}
	if !hasPublishedLesson {
		return ErrInvalidState
	}
	if _, err := tx.Exec(ctx, `
		UPDATE courses SET status = 'published', published_at = now(), updated_at = now()
		WHERE id = $1 AND status = 'draft'
	`, courseID); err != nil {
		return mapDatabaseError("опубликовать курс", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return mapDatabaseError("зафиксировать публикацию курса", err)
	}
	return nil
}

func validateQuestionCreation(params *domain.CreateQuestionParams) error {
	if params.LessonVersionID <= 0 || params.QuestionNumber <= 0 ||
		strings.TrimSpace(params.Prompt) == "" || strings.TrimSpace(params.ExpectedAnswer) == "" {
		return ErrInvalidInput
	}
	params.Prompt = strings.TrimSpace(params.Prompt)
	params.ExpectedAnswer = strings.TrimSpace(params.ExpectedAnswer)
	params.AnswerType = strings.TrimSpace(params.AnswerType)
	switch params.AnswerType {
	case "text":
		if len(params.Options) != 0 {
			return ErrInvalidInput
		}
	case "single_choice", "multiple_choice":
		if len(params.Options) < 2 {
			return ErrInvalidInput
		}
	default:
		return ErrInvalidInput
	}

	optionKeys := make(map[string]struct{}, len(params.Options))
	correctOptions := 0
	for index := range params.Options {
		params.Options[index].Key = strings.TrimSpace(params.Options[index].Key)
		params.Options[index].Text = strings.TrimSpace(params.Options[index].Text)
		if params.Options[index].Key == "" || params.Options[index].Text == "" {
			return ErrInvalidInput
		}
		if _, exists := optionKeys[params.Options[index].Key]; exists {
			return ErrInvalidInput
		}
		optionKeys[params.Options[index].Key] = struct{}{}
		if params.Options[index].IsCorrect {
			correctOptions++
		}
	}
	if params.AnswerType == "single_choice" && correctOptions != 1 {
		return ErrInvalidInput
	}
	if params.AnswerType == "multiple_choice" && correctOptions == 0 {
		return ErrInvalidInput
	}

	criterionKeys := make(map[string]struct{}, len(params.Criteria))
	for index := range params.Criteria {
		params.Criteria[index].Key = strings.TrimSpace(params.Criteria[index].Key)
		params.Criteria[index].Description = strings.TrimSpace(params.Criteria[index].Description)
		weight := params.Criteria[index].Weight
		if weight == 0 {
			weight = 1
			params.Criteria[index].Weight = weight
		}
		if params.Criteria[index].Key == "" || params.Criteria[index].Description == "" ||
			weight < 0 || math.IsNaN(weight) || math.IsInf(weight, 0) {
			return ErrInvalidInput
		}
		if _, exists := criterionKeys[params.Criteria[index].Key]; exists {
			return ErrInvalidInput
		}
		criterionKeys[params.Criteria[index].Key] = struct{}{}
	}
	for index := range params.CommonMistakes {
		params.CommonMistakes[index].Text = strings.TrimSpace(params.CommonMistakes[index].Text)
		params.CommonMistakes[index].Explanation = strings.TrimSpace(params.CommonMistakes[index].Explanation)
		if params.CommonMistakes[index].Text == "" || params.CommonMistakes[index].Explanation == "" {
			return ErrInvalidInput
		}
	}
	for index := range params.BiblePassages {
		passage := &params.BiblePassages[index]
		passage.BookName = strings.TrimSpace(passage.BookName)
		if passage.BookName == "" || passage.Chapter <= 0 ||
			passage.StartVerse <= 0 || passage.EndVerse < passage.StartVerse {
			return ErrInvalidInput
		}
	}
	return nil
}

func validCourseSlug(slug string) bool {
	if slug == "" || strings.HasPrefix(slug, "-") || strings.HasSuffix(slug, "-") {
		return false
	}
	previousHyphen := false
	for _, char := range slug {
		isLetter := char >= 'a' && char <= 'z'
		isDigit := char >= '0' && char <= '9'
		if char == '-' {
			if previousHyphen {
				return false
			}
			previousHyphen = true
			continue
		}
		if !isLetter && !isDigit {
			return false
		}
		previousHyphen = false
	}
	return true
}

func cleanOptional(value *string) any {
	if value == nil {
		return nil
	}
	cleaned := strings.TrimSpace(*value)
	if cleaned == "" {
		return nil
	}
	return cleaned
}

func cleanOptionalString(value *string) *string {
	if cleaned, ok := cleanOptional(value).(string); ok {
		return &cleaned
	}
	return nil
}
