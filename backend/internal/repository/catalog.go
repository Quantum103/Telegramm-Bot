package repository

import (
	"context"
	"errors"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CatalogRepository struct {
	pool *pgxpool.Pool
}

func NewCatalogRepository(pool *pgxpool.Pool) *CatalogRepository {
	return &CatalogRepository{pool: pool}
}

func (r *CatalogRepository) ListPublishedCourses(ctx context.Context) ([]domain.Course, error) {
	const query = `
		SELECT c.id, c.slug, c.title, c.description, c.status, c.published_at,
		       c.created_at, c.updated_at,
		       (
		           SELECT count(*)
		           FROM course_lessons cl
		           JOIN lesson_versions lv ON lv.lesson_id = cl.lesson_id AND lv.status = 'published'
		           WHERE cl.course_id = c.id
		       ) AS lesson_count
		FROM courses c
		WHERE c.status = 'published'
		ORDER BY c.title, c.id
	`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, mapDatabaseError("получить опубликованные курсы", err)
	}
	defer rows.Close()

	courses := make([]domain.Course, 0)
	for rows.Next() {
		course, err := scanCourse(rows)
		if err != nil {
			return nil, mapDatabaseError("прочитать опубликованный курс", err)
		}
		courses = append(courses, course)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("обойти список опубликованных курсов", err)
	}
	return courses, nil
}

func (r *CatalogRepository) GetPublishedCourseByID(ctx context.Context, courseID int64) (domain.Course, error) {
	if courseID <= 0 {
		return domain.Course{}, ErrInvalidInput
	}
	course, err := scanCourse(r.pool.QueryRow(ctx, `
		SELECT c.id, c.slug, c.title, c.description, c.status, c.published_at,
		       c.created_at, c.updated_at,
		       (
		           SELECT count(*)
		           FROM course_lessons cl
		           JOIN lesson_versions lv ON lv.lesson_id = cl.lesson_id AND lv.status = 'published'
		           WHERE cl.course_id = c.id
		       ) AS lesson_count
		FROM courses c
		WHERE c.id = $1 AND c.status = 'published'
	`, courseID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Course{}, ErrNotFound
		}
		return domain.Course{}, mapDatabaseError("получить опубликованный курс", err)
	}
	return course, nil
}

func (r *CatalogRepository) ListPublishedLessonsByCourseID(ctx context.Context, courseID int64) ([]domain.Lesson, error) {
	if courseID <= 0 {
		return nil, ErrInvalidInput
	}
	if _, err := r.GetPublishedCourseByID(ctx, courseID); err != nil {
		return nil, err
	}
	const query = `
		SELECT c.id, c.title, cl.position, l.id, lv.id, lv.version_number,
		       lv.title, lv.description, lv.published_at,
		       (
		           SELECT count(*)
		           FROM lesson_questions q
		           WHERE q.lesson_version_id = lv.id
		       ) AS question_count
		FROM courses c
		JOIN course_lessons cl ON cl.course_id = c.id
		JOIN lessons l ON l.id = cl.lesson_id
		JOIN lesson_versions lv ON lv.lesson_id = l.id AND lv.status = 'published'
		WHERE c.id = $1 AND c.status = 'published'
		ORDER BY cl.position, l.id
	`
	rows, err := r.pool.Query(ctx, query, courseID)
	if err != nil {
		return nil, mapDatabaseError("получить уроки курса", err)
	}
	defer rows.Close()

	lessons := make([]domain.Lesson, 0)
	for rows.Next() {
		lesson, err := scanLesson(rows)
		if err != nil {
			return nil, mapDatabaseError("прочитать урок курса", err)
		}
		lessons = append(lessons, lesson)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("обойти уроки курса", err)
	}
	return lessons, nil
}

func (r *CatalogRepository) GetPublishedLessonInCourse(ctx context.Context, courseID, lessonID int64) (domain.Lesson, error) {
	if courseID <= 0 || lessonID <= 0 {
		return domain.Lesson{}, ErrInvalidInput
	}
	lesson, err := scanLesson(r.pool.QueryRow(ctx, `
		SELECT c.id, c.title, cl.position, l.id, lv.id, lv.version_number,
		       lv.title, lv.description, lv.published_at,
		       (
		           SELECT count(*)
		           FROM lesson_questions q
		           WHERE q.lesson_version_id = lv.id
		       ) AS question_count
		FROM courses c
		JOIN course_lessons cl ON cl.course_id = c.id
		JOIN lessons l ON l.id = cl.lesson_id
		JOIN lesson_versions lv ON lv.lesson_id = l.id AND lv.status = 'published'
		WHERE c.id = $1 AND c.status = 'published' AND l.id = $2
	`, courseID, lessonID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Lesson{}, ErrNotFound
		}
		return domain.Lesson{}, mapDatabaseError("получить урок указанного курса", err)
	}
	return lesson, nil
}

func (r *CatalogRepository) GetPublishedLessonByID(ctx context.Context, lessonID int64) (domain.Lesson, error) {
	if lessonID <= 0 {
		return domain.Lesson{}, ErrInvalidInput
	}
	lesson, err := scanLesson(r.pool.QueryRow(ctx, `
		SELECT c.id, c.title, cl.position, l.id, lv.id, lv.version_number,
		       lv.title, lv.description, lv.published_at,
		       (
		           SELECT count(*)
		           FROM lesson_questions q
		           WHERE q.lesson_version_id = lv.id
		       ) AS question_count
		FROM courses c
		JOIN course_lessons cl ON cl.course_id = c.id
		JOIN lessons l ON l.id = cl.lesson_id
		JOIN lesson_versions lv ON lv.lesson_id = l.id AND lv.status = 'published'
		WHERE l.id = $1 AND c.status = 'published'
		ORDER BY c.id, cl.position
		LIMIT 1
	`, lessonID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Lesson{}, ErrNotFound
		}
		return domain.Lesson{}, mapDatabaseError("получить опубликованный урок", err)
	}
	return lesson, nil
}

func scanCourse(row interface{ Scan(...any) error }) (domain.Course, error) {
	var course domain.Course
	var description pgtype.Text
	var publishedTime pgtype.Timestamptz
	if err := row.Scan(
		&course.ID, &course.Slug, &course.Title, &description, &course.Status,
		&publishedTime, &course.CreatedAt, &course.UpdatedAt, &course.LessonCount,
	); err != nil {
		return domain.Course{}, err
	}
	if description.Valid {
		value := description.String
		course.Description = &value
	}
	if publishedTime.Valid {
		value := publishedTime.Time
		course.PublishedAt = &value
	}
	return course, nil
}

func scanLesson(row interface{ Scan(...any) error }) (domain.Lesson, error) {
	var lesson domain.Lesson
	var description pgtype.Text
	var publishedAt pgtype.Timestamptz
	if err := row.Scan(
		&lesson.CourseID, &lesson.CourseTitle, &lesson.Position, &lesson.ID,
		&lesson.LessonVersionID, &lesson.VersionNumber, &lesson.Title,
		&description, &publishedAt, &lesson.QuestionCount,
	); err != nil {
		return domain.Lesson{}, err
	}
	if description.Valid {
		value := description.String
		lesson.Description = &value
	}
	if publishedAt.Valid {
		value := publishedAt.Time
		lesson.PublishedAt = &value
	}
	return lesson, nil
}
