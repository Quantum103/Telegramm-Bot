package repository

import (
	"context"
	"encoding/json"
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
		WHERE ce.id = $3 AND ce.student_id = $2
		  AND (
		      ce.mode = 'curator'
		      OR EXISTS (
		          SELECT 1
		          FROM staff_role_assignments admin_assignment
		          JOIN staff_roles admin_role ON admin_role.id = admin_assignment.role_id
		          WHERE admin_assignment.staff_id = $1
		            AND admin_assignment.revoked_at IS NULL
		            AND admin_role.role_code = 'administrator'
		      )
		  )
		  AND EXISTS (
		      SELECT 1
		      FROM staff_accounts actor
		      WHERE actor.id = $1 AND actor.account_status = 'active'
		        AND (
		            EXISTS (
		                SELECT 1
		                FROM staff_role_assignments admin_assignment
		                JOIN staff_roles admin_role ON admin_role.id = admin_assignment.role_id
		                WHERE admin_assignment.staff_id = actor.id
		                  AND admin_assignment.revoked_at IS NULL
		                  AND admin_role.role_code = 'administrator'
		            )
		            OR (
		                ce.curator_staff_id = actor.id
		                AND EXISTS (
		                    SELECT 1
		                    FROM student_curator_assignments assignment
		                    JOIN staff_role_assignments curator_assignment
		                      ON curator_assignment.staff_id = actor.id
		                     AND curator_assignment.revoked_at IS NULL
		                    JOIN staff_roles curator_role
		                      ON curator_role.id = curator_assignment.role_id
		                     AND curator_role.role_code = 'curator'
		                    WHERE assignment.student_id = ce.student_id
		                      AND assignment.curator_staff_id = actor.id
		                      AND assignment.status = 'active'
		                )
		            )
		        )
		  )
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
		            SELECT aa.id, aa.processing_status
		            FROM answer_attempts aa
		            WHERE aa.enrollment_id = le.id AND aa.question_id = q.id
		            ORDER BY aa.created_at DESC, aa.id DESC
		            LIMIT 1
		        ) latest ON TRUE
		        LEFT JOIN answer_attempt_reviews review ON review.answer_attempt_id = latest.id
		        WHERE q.lesson_version_id = cel.lesson_version_id
		          AND $2 = 'curator'
		          AND latest.processing_status = 'pending'
		          AND review.id IS NULL),
		       (SELECT CASE WHEN EXISTS (
		            SELECT 1
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
		              AND $2 = 'curator'
		              AND review.decision = 'needs_revision'
		        ) THEN 1 ELSE 0 END),
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
		          AND $2 = 'curator'
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
	`, courseEnrollmentID, result.Enrollment.Mode)
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
			&item.PendingReviews, &item.NeedsRevision, &item.NeedsRevisionAnswers,
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
		       review.decision, review.feedback, review.reviewed_at, latest.created_at,
		       reviewer.id, reviewer.display_name,
		       COALESCE(history.attempts, '[]'::jsonb)
		FROM course_enrollments ce
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
		LEFT JOIN staff_accounts reviewer ON reviewer.id = review.curator_staff_id
		LEFT JOIN LATERAL (
		    SELECT COALESCE(
		        jsonb_agg(
		            jsonb_build_object(
		                'id', history_attempt.id,
		                'answer_id', history_attempt.answer_id,
		                'submitted_answer', history_attempt.submitted_answer_text,
		                'selected_option_ids', history_attempt.selected_option_ids,
		                'processing_status', history_attempt.processing_status,
		                'evaluation_result', history_attempt.evaluation_result,
		                'explanation', history_attempt.explanation,
		                'missing_points', history_attempt.missing_points,
		                'provider', history_attempt.provider,
		                'model', history_attempt.model,
		                'duration_ms', history_attempt.duration_ms,
		                'error_code', history_attempt.error_code,
		                'review_decision', history_attempt.review_decision,
		                'review_feedback', history_attempt.review_feedback,
		                'reviewed_at', history_attempt.reviewed_at,
		                'reviewer_staff_id', history_attempt.reviewer_staff_id,
		                'reviewer_display_name', history_attempt.reviewer_display_name,
		                'created_at', history_attempt.created_at,
		                'completed_at', history_attempt.completed_at
		            )
		            ORDER BY history_attempt.created_at DESC, history_attempt.id DESC
		        ),
		        '[]'::jsonb
		    ) AS attempts
		    FROM (
		        SELECT aa.id, aa.answer_id, aa.submitted_answer_text,
		               COALESCE((
		                   SELECT array_agg(option.option_id ORDER BY question_option.sort_order)
		                   FROM answer_attempt_options option
		                   JOIN question_options question_option
		                     ON question_option.id = option.option_id
		                    AND question_option.question_id = option.question_id
		                   WHERE option.attempt_id = aa.id
		               ), ARRAY[]::BIGINT[]) AS selected_option_ids,
		               aa.processing_status, aa.evaluation_result, aa.explanation,
		               aa.missing_points, aa.provider, aa.model, aa.duration_ms,
		               aa.error_code, aa.created_at, aa.completed_at,
		               review.decision AS review_decision, review.feedback AS review_feedback,
		               review.reviewed_at, review.curator_staff_id AS reviewer_staff_id,
		               reviewer.display_name AS reviewer_display_name
		        FROM answer_attempts aa
		        LEFT JOIN answer_attempt_reviews review ON review.answer_attempt_id = aa.id
		        LEFT JOIN staff_accounts reviewer ON reviewer.id = review.curator_staff_id
		        WHERE aa.enrollment_id = le.id AND aa.question_id = q.id
		    ) history_attempt
		) history ON TRUE
		WHERE ce.id = $3 AND ce.student_id = $2
		  AND (
		      ce.mode = 'curator'
		      OR EXISTS (
		          SELECT 1
		          FROM staff_role_assignments admin_assignment
		          JOIN staff_roles admin_role ON admin_role.id = admin_assignment.role_id
		          WHERE admin_assignment.staff_id = $1
		            AND admin_assignment.revoked_at IS NULL
		            AND admin_role.role_code = 'administrator'
		      )
		  )
		  AND EXISTS (
		      SELECT 1
		      FROM staff_accounts actor
		      WHERE actor.id = $1 AND actor.account_status = 'active'
		        AND (
		            EXISTS (
		                SELECT 1
		                FROM staff_role_assignments admin_assignment
		                JOIN staff_roles admin_role ON admin_role.id = admin_assignment.role_id
		                WHERE admin_assignment.staff_id = actor.id
		                  AND admin_assignment.revoked_at IS NULL
		                  AND admin_role.role_code = 'administrator'
		            )
		            OR (
		                ce.curator_staff_id = actor.id
		                AND EXISTS (
		                    SELECT 1
		                    FROM student_curator_assignments assignment
		                    JOIN staff_role_assignments curator_assignment
		                      ON curator_assignment.staff_id = actor.id
		                     AND curator_assignment.revoked_at IS NULL
		                    JOIN staff_roles curator_role
		                      ON curator_role.id = curator_assignment.role_id
		                     AND curator_role.role_code = 'curator'
		                    WHERE assignment.student_id = ce.student_id
		                      AND assignment.curator_staff_id = actor.id
		                      AND assignment.status = 'active'
		                )
		            )
		        )
		  )
		ORDER BY q.question_number, q.id
	`, curatorStaffID, studentID, courseEnrollmentID, lessonID)
	if err != nil {
		return nil, mapDatabaseError("получить ответы ученика для куратора", err)
	}
	defer rows.Close()
	answers := make([]domain.CuratorAnswer, 0)
	for rows.Next() {
		var answer domain.CuratorAnswer
		var attemptID, reviewerID pgtype.Int8
		var decision, feedback, reviewerName pgtype.Text
		var reviewedAt, submittedAt pgtype.Timestamptz
		var attemptsJSON []byte
		if err := rows.Scan(
			&answer.QuestionID, &answer.QuestionNumber, &answer.Prompt, &answer.AnswerType,
			&attemptID, &answer.SubmittedAnswer, &answer.SelectedOptionIDs,
			&answer.ProcessingStatus, &decision, &feedback, &reviewedAt, &submittedAt,
			&reviewerID, &reviewerName, &attemptsJSON,
		); err != nil {
			return nil, mapDatabaseError("прочитать ответ ученика для куратора", err)
		}
		if attemptID.Valid {
			value := attemptID.Int64
			answer.AttemptID = &value
			switch answer.ProcessingStatus {
			case "pending":
				answer.ReviewStatus = "pending_review"
			case "running":
				answer.ReviewStatus = "processing"
			case "failed":
				answer.ReviewStatus = "failed"
			default:
				answer.ReviewStatus = "unreviewed"
			}
		} else {
			answer.ReviewStatus = "not_submitted"
		}
		if decision.Valid {
			value := decision.String
			answer.ReviewDecision = &value
			answer.ReviewStatus = decision.String
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
		if reviewerID.Valid {
			value := reviewerID.Int64
			answer.ReviewerStaffID = &value
		}
		if reviewerName.Valid {
			value := reviewerName.String
			answer.ReviewerDisplayName = &value
		}
		answer.Attempts = make([]domain.AnswerAttempt, 0)
		if len(attemptsJSON) > 0 {
			if err := json.Unmarshal(attemptsJSON, &answer.Attempts); err != nil {
				return nil, mapDatabaseError("разобрать историю попыток ответа", err)
			}
		}
		answers = append(answers, answer)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("обойти ответы ученика для куратора", err)
	}
	if len(answers) == 0 {
		return nil, ErrNotFound
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

	var studentID int64
	if err := tx.QueryRow(ctx, `
		SELECT le.student_id
		FROM answer_attempts aa
		JOIN lesson_enrollments le ON le.id = aa.enrollment_id
		WHERE aa.id = $1
	`, params.AttemptID).Scan(&studentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AnswerAttemptReview{}, ErrNotFound
		}
		return domain.AnswerAttemptReview{}, mapDatabaseError("найти ученика по попытке ответа", err)
	}
	if err := tx.QueryRow(ctx, "SELECT id FROM students WHERE id = $1 FOR UPDATE", studentID).Scan(&studentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AnswerAttemptReview{}, ErrNotFound
		}
		return domain.AnswerAttemptReview{}, mapDatabaseError("заблокировать ученика перед проверкой ответа", err)
	}

	var enrollmentID, questionID, courseEnrollmentID, lessonVersionID int64
	var enrollmentStatus, courseStatus, mode, processingStatus string
	err = tx.QueryRow(ctx, `
		SELECT le.id, aa.question_id, ce.id, le.lesson_version_id,
		       le.status, ce.status, ce.mode, aa.processing_status
		FROM answer_attempts aa
		JOIN lesson_enrollments le
		  ON le.id = aa.enrollment_id AND le.lesson_version_id = aa.lesson_version_id
		JOIN course_enrollments ce ON ce.id = le.course_enrollment_id
		WHERE aa.id = $1
		FOR UPDATE OF aa, le, ce
	`, params.AttemptID).Scan(
		&enrollmentID, &questionID, &courseEnrollmentID, &lessonVersionID,
		&enrollmentStatus, &courseStatus, &mode, &processingStatus,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AnswerAttemptReview{}, ErrNotFound
		}
		return domain.AnswerAttemptReview{}, mapDatabaseError("найти попытку для проверки куратором", err)
	}
	if mode != "curator" {
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

	// Повтор того же запроса возвращает уже сохранённое решение. Другое
	// решение по одной попытке запрещено: исправленный ответ должен быть
	// новой попыткой ученика, а не перезаписью истории.
	var existing domain.AnswerAttemptReview
	existingErr := tx.QueryRow(ctx, `
		SELECT id, answer_attempt_id, curator_staff_id, decision, feedback, reviewed_at
		FROM answer_attempt_reviews
		WHERE answer_attempt_id = $1
	`, params.AttemptID).Scan(
		&existing.ID, &existing.AnswerAttemptID, &existing.CuratorStaffID,
		&existing.Decision, &existing.Feedback, &existing.ReviewedAt,
	)
	if existingErr == nil {
		if existing.CuratorStaffID != params.CuratorStaffID ||
			existing.Decision != params.Decision || existing.Feedback != params.Feedback {
			return domain.AnswerAttemptReview{}, ErrConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.AnswerAttemptReview{}, mapDatabaseError("зафиксировать повторную проверку ответа", err)
		}
		existing.Replayed = true
		return existing, nil
	}
	if !errors.Is(existingErr, pgx.ErrNoRows) {
		return domain.AnswerAttemptReview{}, mapDatabaseError("проверить существующее решение куратора", existingErr)
	}
	if courseStatus != "in_progress" || enrollmentStatus != "in_progress" || processingStatus != "pending" {
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
	if params.Decision == "approved" {
		if err := completeCuratorLessonAndCourseTx(
			ctx, tx, params.CuratorStaffID, studentID, courseEnrollmentID, enrollmentID, lessonVersionID,
		); err != nil {
			return domain.AnswerAttemptReview{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.AnswerAttemptReview{}, mapDatabaseError("зафиксировать решение куратора", err)
	}
	return review, nil
}

// completeCuratorLessonAndCourseTx completes a lesson only after every question
// has a saved answer and its latest attempt has been approved. Both transitions
// share the transaction that records the curator's decision.
func completeCuratorLessonAndCourseTx(
	ctx context.Context,
	tx pgx.Tx,
	curatorStaffID, studentID, courseEnrollmentID, enrollmentID, lessonVersionID int64,
) error {
	var totalQuestions, unapprovedQuestions int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::INTEGER,
		       count(*) FILTER (
		           WHERE sa.id IS NULL
		              OR latest.id IS NULL
		              OR review.decision IS DISTINCT FROM 'approved'
		       )::INTEGER
		FROM lesson_questions q
		LEFT JOIN student_answers sa
		  ON sa.enrollment_id = $1
		 AND sa.lesson_version_id = $2
		 AND sa.question_id = q.id
		LEFT JOIN LATERAL (
			SELECT aa.id
			FROM answer_attempts aa
			WHERE aa.enrollment_id = $1 AND aa.question_id = q.id
			ORDER BY aa.created_at DESC, aa.id DESC
			LIMIT 1
		) latest ON TRUE
		LEFT JOIN answer_attempt_reviews review
		  ON review.answer_attempt_id = latest.id
		WHERE q.lesson_version_id = $2
	`, enrollmentID, lessonVersionID).Scan(&totalQuestions, &unapprovedQuestions); err != nil {
		return mapDatabaseError("проверить все ответы перед автоматическим завершением урока", err)
	}
	if totalQuestions == 0 || unapprovedQuestions > 0 {
		return nil
	}

	tag, err := tx.Exec(ctx, `
		UPDATE lesson_enrollments
		SET status = 'completed',
		    completed_at = now(),
		    started_at = COALESCE(started_at, now()),
		    last_activity_at = now(),
		    updated_at = now()
		WHERE id = $1 AND student_id = $2
		  AND course_enrollment_id = $3
		  AND status = 'in_progress'
	`, enrollmentID, studentID, courseEnrollmentID)
	if err != nil {
		return mapDatabaseError("автоматически завершить проверенный урок", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidState
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_events (actor_staff_id, action, entity_type, entity_id, details)
		VALUES ($1, 'lesson.completed_after_curator_review', 'lesson_enrollment', $2,
		        jsonb_build_object('course_enrollment_id', $3::bigint))
	`, curatorStaffID, enrollmentID, courseEnrollmentID); err != nil {
		return mapDatabaseError("записать аудит автоматического завершения урока", err)
	}

	courseTag, err := tx.Exec(ctx, `
		UPDATE course_enrollments ce
		SET status = 'completed', completed_at = now(), updated_at = now()
		WHERE ce.id = $1
		  AND ce.student_id = $2
		  AND ce.status = 'in_progress'
		  AND EXISTS (
		      SELECT 1 FROM course_enrollment_lessons cel
		      WHERE cel.course_enrollment_id = ce.id
		  )
		  AND NOT EXISTS (
		      SELECT 1
		      FROM course_enrollment_lessons cel
		      LEFT JOIN lesson_enrollments lesson
		        ON lesson.course_enrollment_id = cel.course_enrollment_id
		       AND lesson.lesson_id = cel.lesson_id
		       AND lesson.lesson_version_id = cel.lesson_version_id
		      WHERE cel.course_enrollment_id = ce.id
		        AND (lesson.id IS NULL OR lesson.status <> 'completed')
		  )
	`, courseEnrollmentID, studentID)
	if err != nil {
		return mapDatabaseError("автоматически завершить курс после последнего урока", err)
	}
	if courseTag.RowsAffected() > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO audit_events (actor_staff_id, action, entity_type, entity_id, details)
			VALUES ($1, 'course.completed_after_curator_review', 'course_enrollment', $2,
			        jsonb_build_object('completed_by_lesson_enrollment_id', $3::bigint))
		`, curatorStaffID, courseEnrollmentID, enrollmentID); err != nil {
			return mapDatabaseError("записать аудит автоматического завершения курса", err)
		}
	}
	return nil
}
