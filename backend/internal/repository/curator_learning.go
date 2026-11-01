package repository

import (
	"context"
	"errors"
	"strings"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CuratorLearningRepository даёт куратору доступ только к ученикам, закреплённым
// за ним прямо сейчас. Роль и назначение повторно проверяются в каждом запросе.
type CuratorLearningRepository struct {
	pool *pgxpool.Pool
}

func NewCuratorLearningRepository(pool *pgxpool.Pool) *CuratorLearningRepository {
	return &CuratorLearningRepository{pool: pool}
}

func (r *CuratorLearningRepository) ListCourseLessons(
	ctx context.Context,
	curatorStaffID, studentID, courseEnrollmentID int64,
) (domain.CuratorCourseAccess, error) {
	if curatorStaffID <= 0 || studentID <= 0 || courseEnrollmentID <= 0 {
		return domain.CuratorCourseAccess{}, ErrInvalidInput
	}

	var result domain.CuratorCourseAccess
	var curatorID pgtype.Int8
	var startedAt, completedAt pgtype.Timestamptz
	err := r.pool.QueryRow(ctx, `
		SELECT ce.id, ce.student_id, ce.course_id, c.title, ce.run_number,
		       ce.mode, ce.curator_staff_id, ce.status, ce.started_at,
		       ce.completed_at, ce.created_at, ce.updated_at
		FROM course_enrollments ce
		JOIN courses c ON c.id = ce.course_id
		JOIN student_curator_assignments assignment
		  ON assignment.student_id = ce.student_id
		 AND assignment.curator_staff_id = $1
		 AND assignment.status = 'active'
		JOIN staff_accounts staff ON staff.id = assignment.curator_staff_id
		JOIN staff_role_assignments role_assignment
		  ON role_assignment.staff_id = staff.id AND role_assignment.revoked_at IS NULL
		JOIN staff_roles role ON role.id = role_assignment.role_id AND role.role_code = 'curator'
		WHERE ce.id = $3 AND ce.student_id = $2
		  AND ce.mode = 'curator'
		  AND ce.curator_staff_id = $1
		  AND staff.account_status = 'active'
	`, curatorStaffID, studentID, courseEnrollmentID).Scan(
		&result.Enrollment.ID, &result.Enrollment.StudentID, &result.Enrollment.CourseID,
		&result.Enrollment.CourseTitle, &result.Enrollment.RunNumber, &result.Enrollment.Mode,
		&curatorID, &result.Enrollment.Status, &startedAt, &completedAt,
		&result.Enrollment.CreatedAt, &result.Enrollment.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CuratorCourseAccess{}, ErrNotFound
		}
		return domain.CuratorCourseAccess{}, mapDatabaseError("проверить доступ куратора к курсу", err)
	}
	if curatorID.Valid {
		value := curatorID.Int64
		result.Enrollment.CuratorStaffID = &value
	}
	if startedAt.Valid {
		value := startedAt.Time
		result.Enrollment.StartedAt = &value
	}
	if completedAt.Valid {
		value := completedAt.Time
		result.Enrollment.CompletedAt = &value
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
		            SELECT aa.id
		            FROM answer_attempts aa
		            WHERE aa.enrollment_id = le.id AND aa.question_id = q.id
		            ORDER BY aa.created_at DESC, aa.id DESC
		            LIMIT 1
		        ) latest ON TRUE
		        LEFT JOIN answer_attempt_reviews review ON review.answer_attempt_id = latest.id
		        WHERE q.lesson_version_id = cel.lesson_version_id
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
		FROM course_enrollment_lessons cel
		JOIN lesson_versions lv
		  ON lv.id = cel.lesson_version_id AND lv.lesson_id = cel.lesson_id
		LEFT JOIN lesson_enrollments le
		  ON le.course_enrollment_id = cel.course_enrollment_id
		 AND le.lesson_id = cel.lesson_id
		 AND le.lesson_version_id = cel.lesson_version_id
		WHERE cel.course_enrollment_id = $1
		ORDER BY cel.position, cel.lesson_id
	`, courseEnrollmentID)
	if err != nil {
		return domain.CuratorCourseAccess{}, mapDatabaseError("получить уроки закреплённого курса", err)
	}
	defer rows.Close()
	result.Lessons = make([]domain.CourseEnrollmentLesson, 0)
	for rows.Next() {
		var item domain.CourseEnrollmentLesson
		var enrollmentID pgtype.Int8
		if err := rows.Scan(
			&item.LessonID, &item.LessonVersionID, &item.Position, &item.Title,
			&enrollmentID, &item.Status, &item.TotalQuestions, &item.AnsweredQuestions,
			&item.PendingReviews, &item.NeedsRevision,
		); err != nil {
			return domain.CuratorCourseAccess{}, mapDatabaseError("прочитать урок закреплённого курса", err)
		}
		if enrollmentID.Valid {
			value := enrollmentID.Int64
			item.EnrollmentID = &value
		}
		result.Lessons = append(result.Lessons, item)
	}
	if err := rows.Err(); err != nil {
		return domain.CuratorCourseAccess{}, mapDatabaseError("обойти уроки закреплённого курса", err)
	}
	return result, nil
}

func (r *CuratorLearningRepository) ListLessonAnswers(
	ctx context.Context,
	curatorStaffID, studentID, courseEnrollmentID, lessonID int64,
) ([]domain.CuratorAnswer, error) {
	if curatorStaffID <= 0 || studentID <= 0 || courseEnrollmentID <= 0 || lessonID <= 0 {
		return nil, ErrInvalidInput
	}
	rows, err := r.pool.Query(ctx, `
		SELECT q.id, q.question_number, q.prompt, q.answer_type,
		       latest.id, COALESCE(latest.submitted_answer_text, ''),
		       COALESCE(latest.options, ARRAY[]::BIGINT[]),
		       COALESCE(latest.processing_status, ''),
		       review.decision, review.feedback, review.reviewed_at, latest.created_at
		FROM course_enrollments ce
		JOIN student_curator_assignments assignment
		  ON assignment.student_id = ce.student_id
		 AND assignment.curator_staff_id = $1
		 AND assignment.status = 'active'
		JOIN staff_accounts staff ON staff.id = assignment.curator_staff_id
		JOIN staff_role_assignments role_assignment
		  ON role_assignment.staff_id = staff.id AND role_assignment.revoked_at IS NULL
		JOIN staff_roles role ON role.id = role_assignment.role_id AND role.role_code = 'curator'
		JOIN course_enrollment_lessons cel
		  ON cel.course_enrollment_id = ce.id AND cel.lesson_id = $4
		JOIN lesson_questions q ON q.lesson_version_id = cel.lesson_version_id
		LEFT JOIN lesson_enrollments le
		  ON le.course_enrollment_id = ce.id
		 AND le.lesson_id = cel.lesson_id
		 AND le.lesson_version_id = cel.lesson_version_id
		LEFT JOIN LATERAL (
		    SELECT aa.id, aa.submitted_answer_text, aa.processing_status, aa.created_at,
		           COALESCE((
		               SELECT array_agg(option.option_id ORDER BY question_option.sort_order)
		               FROM answer_attempt_options option
		               JOIN question_options question_option
		                 ON question_option.id = option.option_id
		                AND question_option.question_id = option.question_id
		               WHERE option.attempt_id = aa.id
		           ), ARRAY[]::BIGINT[]) AS options
		    FROM answer_attempts aa
		    WHERE aa.enrollment_id = le.id AND aa.question_id = q.id
		    ORDER BY aa.created_at DESC, aa.id DESC
		    LIMIT 1
		) latest ON TRUE
		LEFT JOIN answer_attempt_reviews review ON review.answer_attempt_id = latest.id
		WHERE ce.id = $3 AND ce.student_id = $2
		  AND ce.mode = 'curator' AND ce.curator_staff_id = $1
		  AND staff.account_status = 'active'
		ORDER BY q.question_number, q.id
	`, curatorStaffID, studentID, courseEnrollmentID, lessonID)
	if err != nil {
		return nil, mapDatabaseError("получить ответы ученика для куратора", err)
	}
	defer rows.Close()
	answers := make([]domain.CuratorAnswer, 0)
	for rows.Next() {
		var answer domain.CuratorAnswer
		var attemptID pgtype.Int8
		var decision, feedback pgtype.Text
		var reviewedAt, submittedAt pgtype.Timestamptz
		if err := rows.Scan(
			&answer.QuestionID, &answer.QuestionNumber, &answer.Prompt, &answer.AnswerType,
			&attemptID, &answer.SubmittedAnswer, &answer.SelectedOptionIDs,
			&answer.ProcessingStatus, &decision, &feedback, &reviewedAt, &submittedAt,
		); err != nil {
			return nil, mapDatabaseError("прочитать ответ ученика для куратора", err)
		}
		if attemptID.Valid {
			value := attemptID.Int64
			answer.AttemptID = &value
		}
		if decision.Valid {
			value := decision.String
			answer.ReviewDecision = &value
		}
		if feedback.Valid {
			value := feedback.String
			answer.ReviewFeedback = &value
		}
		if reviewedAt.Valid {
			value := reviewedAt.Time
			answer.ReviewedAt = &value
		}
		if submittedAt.Valid {
			value := submittedAt.Time
			answer.SubmittedAt = &value
		}
		answers = append(answers, answer)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("обойти ответы ученика для куратора", err)
	}
	return answers, nil
}

// ReviewAttempt принимает одно окончательное решение по последней попытке вопроса.
// Новая отправка ученика создаст другую попытку и не затрёт прежнее решение.
func (r *CuratorLearningRepository) ReviewAttempt(
	ctx context.Context,
	params domain.ReviewAnswerAttemptParams,
) (domain.AnswerAttemptReview, error) {
	params.Feedback = strings.TrimSpace(params.Feedback)
	if params.CuratorStaffID <= 0 || params.AttemptID <= 0 ||
		(params.Decision != "approved" && params.Decision != "needs_revision") ||
		len(params.Feedback) > 4000 ||
		(params.Decision == "needs_revision" && params.Feedback == "") {
		return domain.AnswerAttemptReview{}, ErrInvalidInput
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.AnswerAttemptReview{}, mapDatabaseError("начать проверку ответа куратором", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var studentID, enrollmentID, questionID, courseEnrollmentID int64
	var enrollmentStatus, courseStatus, mode, processingStatus string
	err = tx.QueryRow(ctx, `
		SELECT le.student_id, le.id, aa.question_id, ce.id,
		       le.status, ce.status, ce.mode, aa.processing_status
		FROM answer_attempts aa
		JOIN lesson_enrollments le
		  ON le.id = aa.enrollment_id AND le.lesson_version_id = aa.lesson_version_id
		JOIN course_enrollments ce ON ce.id = le.course_enrollment_id
		WHERE aa.id = $1
		FOR UPDATE OF aa, le, ce
	`, params.AttemptID).Scan(
		&studentID, &enrollmentID, &questionID, &courseEnrollmentID,
		&enrollmentStatus, &courseStatus, &mode, &processingStatus,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AnswerAttemptReview{}, ErrNotFound
		}
		return domain.AnswerAttemptReview{}, mapDatabaseError("найти попытку для проверки куратором", err)
	}
	if mode != "curator" || courseStatus != "in_progress" ||
		enrollmentStatus != "in_progress" || processingStatus != "pending" {
		return domain.AnswerAttemptReview{}, ErrInvalidState
	}

	var authorized bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1
		    FROM course_enrollments ce
		    JOIN student_curator_assignments assignment
		      ON assignment.student_id = ce.student_id
		     AND assignment.curator_staff_id = $1
		     AND assignment.status = 'active'
		    JOIN staff_accounts staff ON staff.id = assignment.curator_staff_id
		    JOIN staff_role_assignments role_assignment
		      ON role_assignment.staff_id = staff.id AND role_assignment.revoked_at IS NULL
		    JOIN staff_roles role ON role.id = role_assignment.role_id AND role.role_code = 'curator'
		    WHERE ce.id = $2 AND ce.student_id = $3
		      AND ce.mode = 'curator' AND ce.curator_staff_id = $1
		      AND staff.account_status = 'active'
		)
	`, params.CuratorStaffID, courseEnrollmentID, studentID).Scan(&authorized); err != nil {
		return domain.AnswerAttemptReview{}, mapDatabaseError("проверить закрепление куратора для ответа", err)
	}
	if !authorized {
		return domain.AnswerAttemptReview{}, ErrInvalidState
	}

	var latestAttemptID int64
	if err := tx.QueryRow(ctx, `
		SELECT id FROM answer_attempts
		WHERE enrollment_id = $1 AND question_id = $2
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, enrollmentID, questionID).Scan(&latestAttemptID); err != nil {
		return domain.AnswerAttemptReview{}, mapDatabaseError("проверить актуальность попытки", err)
	}
	if latestAttemptID != params.AttemptID {
		return domain.AnswerAttemptReview{}, ErrInvalidState
	}

	var review domain.AnswerAttemptReview
	err = tx.QueryRow(ctx, `
		INSERT INTO answer_attempt_reviews (
		    answer_attempt_id, curator_staff_id, decision, feedback
		)
		VALUES ($1, $2, $3, $4)
		RETURNING id, answer_attempt_id, curator_staff_id, decision, feedback, reviewed_at
	`, params.AttemptID, params.CuratorStaffID, params.Decision, params.Feedback).Scan(
		&review.ID, &review.AnswerAttemptID, &review.CuratorStaffID,
		&review.Decision, &review.Feedback, &review.ReviewedAt,
	)
	if err != nil {
		return domain.AnswerAttemptReview{}, mapDatabaseError("сохранить решение куратора", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_events (actor_staff_id, action, entity_type, entity_id, details)
		VALUES ($1, 'answer_attempt.curator_review', 'answer_attempt', $2,
		        jsonb_build_object('decision', $3::text, 'student_id', $4::bigint))
	`, params.CuratorStaffID, params.AttemptID, params.Decision, studentID); err != nil {
		return domain.AnswerAttemptReview{}, mapDatabaseError("записать аудит проверки ответа куратором", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.AnswerAttemptReview{}, mapDatabaseError("зафиксировать решение куратора", err)
	}
	return review, nil
}
