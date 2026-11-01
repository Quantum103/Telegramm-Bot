package repository

import (
	"context"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type EnrollmentRepository struct {
	pool *pgxpool.Pool
}

func NewEnrollmentRepository(pool *pgxpool.Pool) *EnrollmentRepository {
	return &EnrollmentRepository{pool: pool}
}

type Enrollment = domain.Enrollment
type StartEnrollmentParams = domain.StartEnrollmentParams

func (r *EnrollmentRepository) Start(ctx context.Context, params StartEnrollmentParams) (Enrollment, error) {
	if params.StudentID <= 0 || params.LessonID <= 0 || params.LessonVersionID <= 0 {
		return Enrollment{}, ErrInvalidInput
	}
	if params.Mode != "ai" && params.Mode != "curator" {
		return Enrollment{}, ErrInvalidInput
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Enrollment{}, mapDatabaseError("начать транзакцию прохождения урока", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if params.CourseEnrollmentID > 0 {
		var valid bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM course_enrollments ce
				JOIN course_enrollment_lessons cel ON cel.course_enrollment_id = ce.id
				WHERE ce.id = $1
				  AND ce.student_id = $2
				  AND ce.mode = $3
				  AND ce.status = 'in_progress'
				  AND cel.lesson_id = $4
				  AND cel.lesson_version_id = $5
			)
		`, params.CourseEnrollmentID, params.StudentID, params.Mode, params.LessonID, params.LessonVersionID).Scan(&valid); err != nil {
			return Enrollment{}, mapDatabaseError("проверить право начать урок в выбранном курсе", err)
		}
		if !valid {
			return Enrollment{}, ErrInvalidState
		}
	} else {
		var published bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM lesson_versions
				WHERE id = $1 AND lesson_id = $2 AND status = 'published'
			)
		`, params.LessonVersionID, params.LessonID).Scan(&published); err != nil {
			return Enrollment{}, mapDatabaseError("проверить публикацию версии урока", err)
		}
		if !published {
			return Enrollment{}, ErrNotFound
		}
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO lesson_enrollments (
			student_id, lesson_id, lesson_version_id, course_enrollment_id, mode,
			status, started_at, last_activity_at
		)
		VALUES ($1, $2, $3, NULLIF($4, 0), $5, 'in_progress', now(), now())
		ON CONFLICT DO NOTHING
	`, params.StudentID, params.LessonID, params.LessonVersionID, params.CourseEnrollmentID, params.Mode)
	if err != nil {
		return Enrollment{}, mapDatabaseError("создать прохождение урока", err)
	}

	var enrollment Enrollment
	if params.CourseEnrollmentID > 0 {
		enrollment, err = scanEnrollment(tx.QueryRow(ctx, `
			SELECT id, student_id, lesson_id, lesson_version_id, course_enrollment_id, mode,
			       current_question_id, status, started_at, completed_at,
			       last_activity_at, created_at, updated_at
			FROM lesson_enrollments
			WHERE student_id = $1 AND course_enrollment_id = $2 AND lesson_version_id = $3
			FOR UPDATE
		`, params.StudentID, params.CourseEnrollmentID, params.LessonVersionID))
	} else {
		enrollment, err = scanEnrollment(tx.QueryRow(ctx, `
			SELECT id, student_id, lesson_id, lesson_version_id, course_enrollment_id, mode,
			       current_question_id, status, started_at, completed_at,
			       last_activity_at, created_at, updated_at
			FROM lesson_enrollments
			WHERE student_id = $1 AND lesson_version_id = $2 AND mode = $3
			  AND course_enrollment_id IS NULL
			FOR UPDATE
		`, params.StudentID, params.LessonVersionID, params.Mode))
	}
	if err != nil {
		return Enrollment{}, mapDatabaseError("получить прохождение урока", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Enrollment{}, mapDatabaseError("зафиксировать прохождение урока", err)
	}
	return enrollment, nil
}

func (r *EnrollmentRepository) GetByStudentAndVersion(ctx context.Context, studentID, lessonVersionID int64, mode string) (Enrollment, error) {
	if studentID <= 0 || lessonVersionID <= 0 || (mode != "ai" && mode != "curator") {
		return Enrollment{}, ErrInvalidInput
	}
	enrollment, err := scanEnrollment(r.pool.QueryRow(ctx, `
		SELECT id, student_id, lesson_id, lesson_version_id, course_enrollment_id, mode,
		       current_question_id, status, started_at, completed_at,
		       last_activity_at, created_at, updated_at
		FROM lesson_enrollments
		WHERE student_id = $1 AND lesson_version_id = $2 AND mode = $3
	`, studentID, lessonVersionID, mode))
	if err != nil {
		if err == pgx.ErrNoRows {
			return Enrollment{}, ErrNotFound
		}
		return Enrollment{}, mapDatabaseError("получить сохранённый прогресс", err)
	}
	return enrollment, nil
}

func scanEnrollment(row pgx.Row) (Enrollment, error) {
	var enrollment Enrollment
	var courseEnrollmentID, currentQuestionID pgtype.Int8
	var startedAt, completedAt, lastActivityAt pgtype.Timestamptz

	err := row.Scan(
		&enrollment.ID, &enrollment.StudentID, &enrollment.LessonID,
		&enrollment.LessonVersionID, &courseEnrollmentID, &enrollment.Mode, &currentQuestionID,
		&enrollment.Status, &startedAt, &completedAt, &lastActivityAt,
		&enrollment.CreatedAt, &enrollment.UpdatedAt,
	)
	if err != nil {
		return Enrollment{}, err
	}
	if courseEnrollmentID.Valid {
		value := courseEnrollmentID.Int64
		enrollment.CourseEnrollmentID = &value
	}
	if currentQuestionID.Valid {
		value := currentQuestionID.Int64
		enrollment.CurrentQuestionID = &value
	}
	if startedAt.Valid {
		value := startedAt.Time
		enrollment.StartedAt = &value
	}
	if completedAt.Valid {
		value := completedAt.Time
		enrollment.CompletedAt = &value
	}
	if lastActivityAt.Valid {
		value := lastActivityAt.Time
		enrollment.LastActivityAt = &value
	}
	return enrollment, nil
}
