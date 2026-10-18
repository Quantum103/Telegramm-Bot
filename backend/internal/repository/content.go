package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ContentRepository struct {
	pool *pgxpool.Pool
}

func NewContentRepository(pool *pgxpool.Pool) *ContentRepository {
	return &ContentRepository{pool: pool}
}

type PublishedLesson struct {
	CourseTitle     string
	LessonID        int64
	LessonVersionID int64
	VersionNumber   int
	Position        int
	Title           string
	PublishedAt     time.Time
}

type QuestionCriterion struct {
	Key         string  `json:"key"`
	Description string  `json:"description"`
	Weight      float64 `json:"weight"`
	Required    bool    `json:"required"`
}

type QuestionOption struct {
	ID        int64  `json:"id"`
	Key       string `json:"key"`
	Text      string `json:"text"`
	IsCorrect bool   `json:"-"`
}

type CommonMistake struct {
	Text        string `json:"text"`
	Explanation string `json:"explanation"`
}

type BiblePassage struct {
	ID              int64   `json:"id"`
	BookName        string  `json:"book_name"`
	Chapter         int     `json:"chapter"`
	StartVerse      int     `json:"start_verse"`
	EndVerse        int     `json:"end_verse"`
	TranslationName *string `json:"translation_name"`
	PassageText     *string `json:"passage_text"`
	SourceNote      *string `json:"source_note"`
}

type LessonQuestion struct {
	ID                  int64
	LessonVersionID     int64
	QuestionNumber      int
	Prompt              string
	ExpectedAnswer      string             `json:"-"`
	AnswerType          string
	ExpectedExplanation *string            `json:"-"`
	Criteria            []QuestionCriterion `json:"-"`
	CommonMistakes      []CommonMistake    `json:"-"`
	BiblePassages       []BiblePassage     `json:"-"`
	Options             []QuestionOption
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func (r *ContentRepository) ListPublishedLessonsByCourse(ctx context.Context, courseSlug string) ([]PublishedLesson, error) {
	const query = `
		SELECT c.title, l.id, lv.id, lv.version_number, cl.position, lv.title, lv.published_at
		FROM courses c
		JOIN course_lessons cl ON cl.course_id = c.id
		JOIN lessons l ON l.id = cl.lesson_id
		JOIN lesson_versions lv ON lv.lesson_id = l.id AND lv.status = 'published'
		WHERE c.slug = $1 AND c.status = 'published'
		ORDER BY cl.position
	`

	rows, err := r.pool.Query(ctx, query, courseSlug)
	if err != nil {
		return nil, mapDatabaseError("получить опубликованные уроки курса", err)
	}
	defer rows.Close()

	lessons := make([]PublishedLesson, 0)
	for rows.Next() {
		var lesson PublishedLesson
		var publishedAt pgtype.Timestamptz
		if err := rows.Scan(
			&lesson.CourseTitle, &lesson.LessonID, &lesson.LessonVersionID,
			&lesson.VersionNumber, &lesson.Position, &lesson.Title, &publishedAt,
		); err != nil {
			return nil, mapDatabaseError("прочитать урок курса", err)
		}
		if publishedAt.Valid {
			lesson.PublishedAt = publishedAt.Time
		}
		lessons = append(lessons, lesson)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("обойти список уроков курса", err)
	}
	return lessons, nil
}

func (r *ContentRepository) ListQuestionsForVersion(ctx context.Context, lessonVersionID int64) ([]LessonQuestion, error) {
	if lessonVersionID <= 0 {
		return nil, ErrInvalidInput
	}

	const query = `
		SELECT
			q.id,
			q.lesson_version_id,
			q.question_number,
			q.prompt,
			q.expected_answer,
			q.answer_type,
			q.expected_explanation,
			COALESCE((
				SELECT jsonb_agg(
					jsonb_build_object(
						'key', c.criterion_key,
						'description', c.description,
						'weight', c.weight::DOUBLE PRECISION,
						'required', c.is_required
					) ORDER BY c.sort_order
				)
				FROM question_criteria c
				WHERE c.question_id = q.id
			), '[]'::JSONB) AS criteria,
			COALESCE((
				SELECT jsonb_agg(
					jsonb_build_object(
						'id', o.id,
						'key', o.option_key,
						'text', o.option_text,
						'is_correct', o.is_correct
					) ORDER BY o.sort_order
				)
				FROM question_options o
				WHERE o.question_id = q.id
			), '[]'::JSONB) AS options,
			COALESCE((
				SELECT jsonb_agg(
					jsonb_build_object(
						'text', mistake.mistake_text,
						'explanation', mistake.explanation
					) ORDER BY mistake.sort_order
				)
				FROM question_common_mistakes mistake
				WHERE mistake.question_id = q.id
			), '[]'::JSONB) AS common_mistakes,
			COALESCE((
				SELECT jsonb_agg(
					jsonb_build_object(
						'id', passage.id,
						'book_name', passage.book_name,
						'chapter', passage.chapter,
						'start_verse', passage.start_verse,
						'end_verse', passage.end_verse,
						'translation_name', passage.translation_name,
						'passage_text', passage.passage_text,
						'source_note', passage.source_note
					) ORDER BY link.sort_order
				)
				FROM question_bible_passages link
				JOIN bible_passages passage ON passage.id = link.passage_id
				WHERE link.question_id = q.id
			), '[]'::JSONB) AS bible_passages,
			q.created_at,
			q.updated_at
		FROM lesson_questions q
		JOIN lesson_versions lv ON lv.id = q.lesson_version_id
		WHERE q.lesson_version_id = $1 AND lv.status IN ('published', 'archived')
		ORDER BY q.question_number
	`

	rows, err := r.pool.Query(ctx, query, lessonVersionID)
	if err != nil {
		return nil, mapDatabaseError("получить вопросы версии урока", err)
	}
	defer rows.Close()

	questions := make([]LessonQuestion, 0)
	for rows.Next() {
		var question LessonQuestion
		var explanation pgtype.Text
		var criteriaJSON, optionsJSON, mistakesJSON, passagesJSON []byte
		if err := rows.Scan(
			&question.ID, &question.LessonVersionID, &question.QuestionNumber,
			&question.Prompt, &question.ExpectedAnswer, &question.AnswerType,
			&explanation, &criteriaJSON, &optionsJSON, &mistakesJSON, &passagesJSON,
			&question.CreatedAt, &question.UpdatedAt,
		); err != nil {
			return nil, mapDatabaseError("прочитать вопрос урока", err)
		}
		if explanation.Valid {
			value := explanation.String
			question.ExpectedExplanation = &value
		}
		if err := json.Unmarshal(criteriaJSON, &question.Criteria); err != nil {
			return nil, mapDatabaseError("разобрать критерии вопроса", err)
		}
		if err := json.Unmarshal(optionsJSON, &question.Options); err != nil {
			return nil, mapDatabaseError("разобрать варианты ответа", err)
		}
		if err := json.Unmarshal(mistakesJSON, &question.CommonMistakes); err != nil {
			return nil, mapDatabaseError("разобрать распространённые ошибки", err)
		}
		if err := json.Unmarshal(passagesJSON, &question.BiblePassages); err != nil {
			return nil, mapDatabaseError("разобрать библейские отрывки", err)
		}
		questions = append(questions, question)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("обойти вопросы урока", err)
	}
	if len(questions) == 0 {
		var published bool
		if err := r.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM lesson_versions WHERE id = $1 AND status IN ('published', 'archived'))`,
			lessonVersionID,
		).Scan(&published); err != nil {
			return nil, mapDatabaseError("проверить версию урока", err)
		}
		if !published {
			return nil, ErrNotFound
		}
	}
	return questions, nil
}

