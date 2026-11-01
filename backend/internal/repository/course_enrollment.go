package repository

import (
	"context"
	"errors"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CourseEnrollmentRepository struct {
	pool *pgxpool.Pool
}

func NewCourseEnrollmentRepository(pool *pgxpool.Pool) *CourseEnrollmentRepository {
	return &CourseEnrollmentRepository{pool: pool}
}

// Start возвращает существующий запуск того же курса или создаёт новый.
// Блокировка строки students сериализует запросы одного ученика; частичный
// уникальный индекс остаётся последней защитой на уровне PostgreSQL.
func (r *CourseEnrollmentRepository) Start(
	ctx context.Context,
	studentID, courseID int64,
	mode string,
) (domain.CourseEnrollmentProgress, error) {
	if studentID <= 0 || courseID <= 0 || (mode != "ai" && mode != "curator") {
		return domain.CourseEnrollmentProgress{}, ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.CourseEnrollmentProgress{}, mapDatabaseError("начать запуск курса", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var studentStatus string
	if err := tx.QueryRow(ctx, `
		SELECT account_status FROM students WHERE id = $1 FOR UPDATE
	`, studentID).Scan(&studentStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CourseEnrollmentProgress{}, ErrNotFound
		}
		return domain.CourseEnrollmentProgress{}, mapDatabaseError("заблокировать ученика перед запуском курса", err)
	}
	if studentStatus != "active" {
		return domain.CourseEnrollmentProgress{}, ErrInvalidState
	}

	active, activeErr := scanCourseEnrollment(tx.QueryRow(ctx, courseEnrollmentSelect+`
		WHERE ce.student_id = $1
		  AND ce.status IN ('in_progress', 'awaiting_curator')
		ORDER BY ce.updated_at DESC, ce.id DESC
		LIMIT 1
		FOR UPDATE OF ce
	`, studentID))
	if activeErr == nil {
		progress, progressErr := r.getProgress(ctx, tx, studentID, active.ID)
		if progressErr != nil {
			return domain.CourseEnrollmentProgress{}, progressErr
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.CourseEnrollmentProgress{}, mapDatabaseError("зафиксировать восстановление активного курса", err)
		}
		if active.CourseID != courseID {
			return progress, domain.ErrActiveCourse
		}
		if active.Status == "awaiting_curator" {
			return progress, domain.ErrNoCurator
		}
		return progress, nil
	}
	if !errors.Is(activeErr, pgx.ErrNoRows) && !errors.Is(activeErr, ErrNotFound) {
		return domain.CourseEnrollmentProgress{}, activeErr
	}

	paused, pausedErr := scanCourseEnrollment(tx.QueryRow(ctx, courseEnrollmentSelect+`
		WHERE ce.student_id = $1 AND ce.status = 'paused'
		ORDER BY ce.updated_at DESC, ce.id DESC
		LIMIT 1
		FOR UPDATE OF ce
	`, studentID))
	if pausedErr == nil {
		if paused.CourseID != courseID {
			progress, progressErr := r.getProgress(ctx, tx, studentID, paused.ID)
			if progressErr != nil {
				return domain.CourseEnrollmentProgress{}, progressErr
			}
			if err := tx.Commit(ctx); err != nil {
				return domain.CourseEnrollmentProgress{}, mapDatabaseError("зафиксировать приостановленное прохождение", err)
			}
			return progress, domain.ErrActiveCourse
		}
		curatorID := int64(0)
		if paused.Mode == "curator" {
			curatorID, err = currentActiveCuratorTx(ctx, tx, studentID)
			if err != nil {
				return domain.CourseEnrollmentProgress{}, err
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE course_enrollments
			SET status = 'in_progress',
			    curator_staff_id = NULLIF($2, 0),
			    started_at = COALESCE(started_at, now()),
			    updated_at = now()
			WHERE id = $1
		`, paused.ID, curatorID); err != nil {
			return domain.CourseEnrollmentProgress{}, mapDatabaseError("возобновить приостановленный курс", err)
		}
		progress, progressErr := r.getProgress(ctx, tx, studentID, paused.ID)
		if progressErr != nil {
			return domain.CourseEnrollmentProgress{}, progressErr
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.CourseEnrollmentProgress{}, mapDatabaseError("зафиксировать возобновление курса", err)
		}
		return progress, nil
	}
	if !errors.Is(pausedErr, pgx.ErrNoRows) && !errors.Is(pausedErr, ErrNotFound) {
		return domain.CourseEnrollmentProgress{}, pausedErr
	}

	var hasUnmappedLegacy bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM lesson_enrollments
			WHERE student_id = $1
			  AND course_enrollment_id IS NULL
			  AND status IN ('not_started', 'in_progress', 'paused')
		)
	`, studentID).Scan(&hasUnmappedLegacy); err != nil {
		return domain.CourseEnrollmentProgress{}, mapDatabaseError("проверить старый незавершённый прогресс", err)
	}
	if hasUnmappedLegacy {
		return domain.CourseEnrollmentProgress{}, domain.ErrLegacyProgress
	}

	var lessonCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(lv.id)::INTEGER
		FROM courses c
		LEFT JOIN course_lessons cl ON cl.course_id = c.id
		LEFT JOIN LATERAL (
			SELECT id FROM lesson_versions
			WHERE lesson_id = cl.lesson_id AND status = 'published'
			ORDER BY version_number DESC, id DESC
			LIMIT 1
		) lv ON TRUE
		WHERE c.id = $1 AND c.status = 'published'
		GROUP BY c.id
	`, courseID).Scan(&lessonCount); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CourseEnrollmentProgress{}, ErrNotFound
		}
		return domain.CourseEnrollmentProgress{}, mapDatabaseError("проверить опубликованный курс", err)
	}
	if lessonCount == 0 {
		return domain.CourseEnrollmentProgress{}, ErrInvalidState
	}

	curatorID := int64(0)
	if mode == "curator" {
		curatorID, err = currentActiveCuratorTx(ctx, tx, studentID)
		if err != nil {
			return domain.CourseEnrollmentProgress{}, err
		}
	}

	var runNumber int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(max(run_number), 0) + 1
		FROM course_enrollments
		WHERE student_id = $1 AND course_id = $2
	`, studentID, courseID).Scan(&runNumber); err != nil {
		return domain.CourseEnrollmentProgress{}, mapDatabaseError("определить номер запуска курса", err)
	}

	var enrollmentID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO course_enrollments (
			student_id, course_id, run_number, mode, curator_staff_id,
			status, started_at
		)
		VALUES ($1, $2, $3, $4, NULLIF($5, 0), 'in_progress', now())
		RETURNING id
	`, studentID, courseID, runNumber, mode, curatorID).Scan(&enrollmentID); err != nil {
		return domain.CourseEnrollmentProgress{}, mapDatabaseError("создать прохождение курса", err)
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO course_enrollment_lessons (
			course_enrollment_id, lesson_id, lesson_version_id, position
		)
		SELECT $1, cl.lesson_id, lv.id, cl.position
		FROM course_lessons cl
		JOIN LATERAL (
			SELECT id FROM lesson_versions
			WHERE lesson_id = cl.lesson_id AND status = 'published'
			ORDER BY version_number DESC, id DESC
			LIMIT 1
		) lv ON TRUE
		WHERE cl.course_id = $2
	`, enrollmentID, courseID)
	if err != nil {
		return domain.CourseEnrollmentProgress{}, mapDatabaseError("зафиксировать обязательные уроки курса", err)
	}
	if int(tag.RowsAffected()) != lessonCount {
		return domain.CourseEnrollmentProgress{}, ErrConflict
	}

	progress, err := r.getProgress(ctx, tx, studentID, enrollmentID)
	if err != nil {
		return domain.CourseEnrollmentProgress{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.CourseEnrollmentProgress{}, mapDatabaseError("зафиксировать запуск курса", err)
	}
	return progress, nil
}

func (r *CourseEnrollmentRepository) GetActiveForStudent(
	ctx context.Context,
	studentID int64,
) (domain.CourseEnrollmentProgress, error) {
	if studentID <= 0 {
		return domain.CourseEnrollmentProgress{}, ErrInvalidInput
	}
	return r.getProgress(ctx, r.pool, studentID, 0)
}

func (r *CourseEnrollmentRepository) GetForStudent(
	ctx context.Context,
	studentID, courseEnrollmentID int64,
) (domain.CourseEnrollmentProgress, error) {
	if studentID <= 0 || courseEnrollmentID <= 0 {
		return domain.CourseEnrollmentProgress{}, ErrInvalidInput
	}
	return r.getProgress(ctx, r.pool, studentID, courseEnrollmentID)
}

type courseEnrollmentQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (r *CourseEnrollmentRepository) getProgress(
	ctx context.Context,
	q courseEnrollmentQuerier,
	studentID, courseEnrollmentID int64,
) (domain.CourseEnrollmentProgress, error) {
	var progress domain.CourseEnrollmentProgress
	var curatorID pgtype.Int8
	var startedAt, completedAt pgtype.Timestamptz
	err := q.QueryRow(ctx, `
		SELECT ce.id, ce.student_id, ce.course_id, c.title, ce.run_number,
		       ce.mode, ce.curator_staff_id, ce.status, ce.started_at,
		       ce.completed_at, ce.created_at, ce.updated_at,
		       count(cel.lesson_id)::INTEGER,
		       count(cel.lesson_id) FILTER (WHERE le.status = 'completed')::INTEGER
		FROM course_enrollments ce
		JOIN courses c ON c.id = ce.course_id
		LEFT JOIN course_enrollment_lessons cel ON cel.course_enrollment_id = ce.id
		LEFT JOIN lesson_enrollments le
		  ON le.course_enrollment_id = ce.id
		 AND le.lesson_id = cel.lesson_id
		 AND le.lesson_version_id = cel.lesson_version_id
		WHERE ce.student_id = $1
		  AND ($2::bigint = 0 OR ce.id = $2)
		  AND ($2::bigint <> 0 OR ce.status IN ('in_progress', 'awaiting_curator'))
		GROUP BY ce.id, c.title
		ORDER BY ce.updated_at DESC, ce.id DESC
		LIMIT 1
	`, studentID, courseEnrollmentID).Scan(
		&progress.Enrollment.ID,
		&progress.Enrollment.StudentID,
		&progress.Enrollment.CourseID,
		&progress.Enrollment.CourseTitle,
		&progress.Enrollment.RunNumber,
		&progress.Enrollment.Mode,
		&curatorID,
		&progress.Enrollment.Status,
		&startedAt,
		&completedAt,
		&progress.Enrollment.CreatedAt,
		&progress.Enrollment.UpdatedAt,
		&progress.TotalLessons,
		&progress.CompletedLessons,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CourseEnrollmentProgress{}, ErrNotFound
		}
		return domain.CourseEnrollmentProgress{}, mapDatabaseError("получить прогресс курса", err)
	}
	if curatorID.Valid {
		value := curatorID.Int64
		progress.Enrollment.CuratorStaffID = &value
	}
	if startedAt.Valid {
		value := startedAt.Time
		progress.Enrollment.StartedAt = &value
	}
	if completedAt.Valid {
		value := completedAt.Time
		progress.Enrollment.CompletedAt = &value
	}
	progress.RemainingLessons = progress.TotalLessons - progress.CompletedLessons
	progress.IsComplete = progress.Enrollment.Status == "completed"
	return progress, nil
}

func (r *CourseEnrollmentRepository) GetLessonForRun(
	ctx context.Context,
	studentID, courseEnrollmentID, lessonID int64,
) (domain.Lesson, error) {
	if studentID <= 0 || courseEnrollmentID <= 0 || lessonID <= 0 {
		return domain.Lesson{}, ErrInvalidInput
	}
	var lesson domain.Lesson
	var description pgtype.Text
	var publishedAt pgtype.Timestamptz
	err := r.pool.QueryRow(ctx, `
		SELECT c.id, c.title, cel.position, l.id, lv.id, lv.version_number,
		       lv.title, lv.description, lv.published_at,
		       (SELECT count(*)::INTEGER FROM lesson_questions q WHERE q.lesson_version_id = lv.id)
		FROM course_enrollments ce
		JOIN courses c ON c.id = ce.course_id
		JOIN course_enrollment_lessons cel ON cel.course_enrollment_id = ce.id
		JOIN lessons l ON l.id = cel.lesson_id
		JOIN lesson_versions lv ON lv.id = cel.lesson_version_id AND lv.lesson_id = l.id
		WHERE ce.id = $1 AND ce.student_id = $2
		  AND ce.status IN ('in_progress', 'awaiting_curator')
		  AND cel.lesson_id = $3
	`, courseEnrollmentID, studentID, lessonID).Scan(
		&lesson.CourseID, &lesson.CourseTitle, &lesson.Position, &lesson.ID,
		&lesson.LessonVersionID, &lesson.VersionNumber, &lesson.Title,
		&description, &publishedAt, &lesson.QuestionCount,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Lesson{}, ErrNotFound
		}
		return domain.Lesson{}, mapDatabaseError("получить урок из зафиксированного состава курса", err)
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

func (r *CourseEnrollmentRepository) ListLessons(
	ctx context.Context,
	studentID, courseEnrollmentID int64,
) ([]domain.CourseEnrollmentLesson, error) {
	if studentID <= 0 || courseEnrollmentID <= 0 {
		return nil, ErrInvalidInput
	}
	rows, err := r.pool.Query(ctx, `
		SELECT cel.lesson_id, cel.lesson_version_id, cel.position, lv.title,
		       le.id, COALESCE(le.status, 'not_started'),
		       (SELECT count(*)::INTEGER FROM lesson_questions q
		        WHERE q.lesson_version_id = cel.lesson_version_id),
		       (SELECT count(*)::INTEGER FROM student_answers sa
		        WHERE sa.enrollment_id = le.id AND sa.lesson_version_id = cel.lesson_version_id),
		       (SELECT count(*)::INTEGER
		        FROM lesson_questions q
		        JOIN LATERAL (
		            SELECT aa.id, aa.processing_status
		            FROM answer_attempts aa
		            WHERE aa.enrollment_id = le.id AND aa.question_id = q.id
		            ORDER BY aa.created_at DESC, aa.id DESC
		            LIMIT 1
		        ) latest ON TRUE
		        LEFT JOIN answer_attempt_reviews review ON review.answer_attempt_id = latest.id
		        WHERE q.lesson_version_id = cel.lesson_version_id
		          AND latest.processing_status = 'pending'
		          AND review.id IS NULL),
		       (SELECT count(*)::INTEGER
		        FROM lesson_questions q
		        JOIN LATERAL (
		            SELECT aa.id
		            FROM answer_attempts aa
		            WHERE aa.enrollment_id = le.id AND aa.question_id = q.id
		            ORDER BY aa.created_at DESC, aa.id DESC
		            LIMIT 1
		        ) latest ON TRUE
		        JOIN answer_attempt_reviews review ON review.answer_attempt_id = latest.id
		        WHERE q.lesson_version_id = cel.lesson_version_id
		          AND review.decision = 'needs_revision')
		FROM course_enrollments ce
		JOIN course_enrollment_lessons cel ON cel.course_enrollment_id = ce.id
		JOIN lesson_versions lv ON lv.id = cel.lesson_version_id AND lv.lesson_id = cel.lesson_id
		LEFT JOIN lesson_enrollments le
		  ON le.course_enrollment_id = ce.id
		 AND le.lesson_id = cel.lesson_id
		 AND le.lesson_version_id = cel.lesson_version_id
		WHERE ce.id = $1 AND ce.student_id = $2
		ORDER BY cel.position, cel.lesson_id
	`, courseEnrollmentID, studentID)
	if err != nil {
		return nil, mapDatabaseError("получить обязательные уроки курса", err)
	}
	defer rows.Close()
	lessons := make([]domain.CourseEnrollmentLesson, 0)
	for rows.Next() {
		var lesson domain.CourseEnrollmentLesson
		var enrollmentID pgtype.Int8
		if err := rows.Scan(
			&lesson.LessonID, &lesson.LessonVersionID, &lesson.Position, &lesson.Title,
			&enrollmentID, &lesson.Status, &lesson.TotalQuestions, &lesson.AnsweredQuestions,
			&lesson.PendingReviews, &lesson.NeedsRevision,
		); err != nil {
			return nil, mapDatabaseError("прочитать обязательный урок курса", err)
		}
		if enrollmentID.Valid {
			value := enrollmentID.Int64
			lesson.EnrollmentID = &value
		}
		lessons = append(lessons, lesson)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("обойти обязательные уроки курса", err)
	}
	return lessons, nil
}

func (r *CourseEnrollmentRepository) RefreshCompletion(
	ctx context.Context,
	studentID, courseEnrollmentID int64,
) error {
	if studentID <= 0 || courseEnrollmentID <= 0 {
		return ErrInvalidInput
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE course_enrollments ce
		SET status = 'completed', completed_at = now(), updated_at = now()
		WHERE ce.id = $2
		  AND ce.student_id = $1
		  AND ce.status = 'in_progress'
		  AND NOT EXISTS (
		      SELECT 1
		      FROM course_enrollment_lessons cel
		      LEFT JOIN lesson_enrollments le
		        ON le.course_enrollment_id = cel.course_enrollment_id
		       AND le.lesson_id = cel.lesson_id
		       AND le.lesson_version_id = cel.lesson_version_id
		      WHERE cel.course_enrollment_id = ce.id
		        AND (le.id IS NULL OR le.status <> 'completed')
		  )
		  AND EXISTS (
		      SELECT 1 FROM course_enrollment_lessons cel
		      WHERE cel.course_enrollment_id = ce.id
		  )
	`, studentID, courseEnrollmentID)
	if err != nil {
		return mapDatabaseError("проверить завершение курса", err)
	}
	return nil
}

func currentActiveCuratorTx(ctx context.Context, tx pgx.Tx, studentID int64) (int64, error) {
	var curatorID int64
	err := tx.QueryRow(ctx, `
		SELECT assignment.curator_staff_id
		FROM student_curator_assignments assignment
		JOIN staff_accounts curator ON curator.id = assignment.curator_staff_id
		JOIN staff_role_assignments role_assignment
		  ON role_assignment.staff_id = curator.id AND role_assignment.revoked_at IS NULL
		JOIN staff_roles role
		  ON role.id = role_assignment.role_id AND role.role_code = 'curator'
		WHERE assignment.student_id = $1
		  AND assignment.status = 'active'
		  AND curator.account_status = 'active'
		ORDER BY assignment.assigned_at DESC, assignment.id DESC
		LIMIT 1
		FOR UPDATE OF assignment, curator
	`, studentID).Scan(&curatorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, domain.ErrNoCurator
	}
	if err != nil {
		return 0, mapDatabaseError("получить назначенного активного куратора", err)
	}
	return curatorID, nil
}

const courseEnrollmentSelect = `
	SELECT ce.id, ce.student_id, ce.course_id, c.title, ce.run_number,
	       ce.mode, ce.curator_staff_id, ce.status, ce.started_at,
	       ce.completed_at, ce.created_at, ce.updated_at
	FROM course_enrollments ce
	JOIN courses c ON c.id = ce.course_id
`

func scanCourseEnrollment(row pgx.Row) (domain.CourseEnrollment, error) {
	var enrollment domain.CourseEnrollment
	var curatorID pgtype.Int8
	var startedAt, completedAt pgtype.Timestamptz
	err := row.Scan(
		&enrollment.ID, &enrollment.StudentID, &enrollment.CourseID,
		&enrollment.CourseTitle, &enrollment.RunNumber, &enrollment.Mode,
		&curatorID, &enrollment.Status, &startedAt, &completedAt,
		&enrollment.CreatedAt, &enrollment.UpdatedAt,
	)
	if err != nil {
		return domain.CourseEnrollment{}, err
	}
	if curatorID.Valid {
		value := curatorID.Int64
		enrollment.CuratorStaffID = &value
	}
	if startedAt.Valid {
		value := startedAt.Time
		enrollment.StartedAt = &value
	}
	if completedAt.Valid {
		value := completedAt.Time
		enrollment.CompletedAt = &value
	}
	return enrollment, nil
}
