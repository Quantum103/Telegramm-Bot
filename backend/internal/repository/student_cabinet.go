package repository

import (
	"context"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5/pgtype"
)

// GetStudentCabinet читает профильную дату, статистику, непрочитанные сообщения и последние запуски курсов одним SQL-запросом.
func (r *CourseEnrollmentRepository) GetStudentCabinet(
	ctx context.Context,
	studentID int64,
) (domain.StudentCabinet, error) {
	if studentID <= 0 {
		return domain.StudentCabinet{}, ErrInvalidInput
	}

	rows, err := r.pool.Query(ctx, `
		SELECT student.registered_at,
		       (
		           SELECT count(DISTINCT completed_run.course_id)::INTEGER
		           FROM course_enrollments completed_run
		           WHERE completed_run.student_id = student.id
		             AND completed_run.status = 'completed'
		       ) AS completed_courses,
		       (
		           SELECT count(*)::INTEGER
		           FROM lesson_enrollments completed_lesson
		           WHERE completed_lesson.student_id = student.id
		             AND completed_lesson.status = 'completed'
		       ) AS completed_lessons,
		       (
		           SELECT count(*)::INTEGER
		           FROM conversations unread_conversation
		           JOIN conversation_participants student_participant
		             ON student_participant.conversation_id = unread_conversation.id
		            AND student_participant.student_id = student.id
		           JOIN conversation_messages unread_message
		             ON unread_message.conversation_id = unread_conversation.id
		           WHERE unread_conversation.student_id = student.id
		             AND unread_message.sender_participant_id <> student_participant.id
		             AND NOT EXISTS (
		                 SELECT 1
		                 FROM conversation_message_reads read_mark
		                 WHERE read_mark.message_id = unread_message.id
		                   AND read_mark.participant_id = student_participant.id
		             )
		       ) AS unread_conversation_messages,
		       latest.course_id,
		       latest.title,
		       latest.status,
		       latest.mode,
		       latest.total_lessons,
		       latest.completed_lessons,
		       latest.pending_reviews,
		       latest.needs_revision_answers
		FROM students student
		LEFT JOIN LATERAL (
		    SELECT current_run.course_id,
		           current_run.title,
		           current_run.mode,
		           current_run.status,
		           count(course_lesson.lesson_id)::INTEGER AS total_lessons,
		           count(course_lesson.lesson_id) FILTER (
		               WHERE lesson.status = 'completed'
		           )::INTEGER AS completed_lessons,
		           COALESCE(review_totals.pending_reviews, 0)::INTEGER AS pending_reviews,
		           COALESCE(review_totals.needs_revision_answers, 0)::INTEGER AS needs_revision_answers,
		           current_run.updated_at,
		           current_run.id
		    FROM (
		        SELECT DISTINCT ON (enrollment.course_id)
		               enrollment.id,
		               enrollment.course_id,
		               course.title,
		               enrollment.mode,
		               enrollment.status,
		               enrollment.updated_at
		        FROM course_enrollments enrollment
		        JOIN courses course ON course.id = enrollment.course_id
		        WHERE enrollment.student_id = student.id
		          AND enrollment.status NOT IN ('cancelled', 'superseded')
		        ORDER BY enrollment.course_id, enrollment.run_number DESC, enrollment.id DESC
		    ) current_run
		    LEFT JOIN course_enrollment_lessons course_lesson
		      ON course_lesson.course_enrollment_id = current_run.id
		    LEFT JOIN lesson_enrollments lesson
		      ON lesson.course_enrollment_id = current_run.id
		     AND lesson.lesson_id = course_lesson.lesson_id
		     AND lesson.lesson_version_id = course_lesson.lesson_version_id
		    LEFT JOIN LATERAL (
		        SELECT
		            count(*) FILTER (
		                WHERE review_enrollment.status = 'completed'
		                  AND latest_attempt.id IS NOT NULL
		                  AND latest_attempt.processing_status = 'pending'
		                  AND review.id IS NULL
		            )::INTEGER AS pending_reviews,
		            count(*) FILTER (
		                WHERE review_enrollment.status = 'completed'
		                  AND review.decision = 'needs_revision'
		            )::INTEGER AS needs_revision_answers
		        FROM course_enrollment_lessons review_lesson
		        JOIN lesson_questions question
		          ON question.lesson_version_id = review_lesson.lesson_version_id
		        LEFT JOIN lesson_enrollments review_enrollment
		          ON review_enrollment.course_enrollment_id = current_run.id
		         AND review_enrollment.lesson_id = review_lesson.lesson_id
		         AND review_enrollment.lesson_version_id = review_lesson.lesson_version_id
		        LEFT JOIN LATERAL (
		            SELECT attempt.id, attempt.processing_status
		            FROM answer_attempts attempt
		            WHERE attempt.enrollment_id = review_enrollment.id
		              AND attempt.question_id = question.id
		            ORDER BY attempt.created_at DESC, attempt.id DESC
		            LIMIT 1
		        ) latest_attempt ON TRUE
		        LEFT JOIN answer_attempt_reviews review
		          ON review.answer_attempt_id = latest_attempt.id
		        WHERE review_lesson.course_enrollment_id = current_run.id
		          AND current_run.mode = 'curator'
		    ) review_totals ON TRUE
		    GROUP BY current_run.course_id, current_run.title, current_run.mode, current_run.status,
		             review_totals.pending_reviews, review_totals.needs_revision_answers,
		             current_run.updated_at, current_run.id
		    ORDER BY current_run.updated_at DESC, current_run.id DESC
		) latest ON TRUE
		WHERE student.id = $1
		  AND student.account_status = 'active'
		ORDER BY latest.updated_at DESC NULLS LAST, latest.id DESC NULLS LAST
	`, studentID)
	if err != nil {
		return domain.StudentCabinet{}, mapDatabaseError("получить личный кабинет ученика", err)
	}
	defer rows.Close()

	cabinet := domain.StudentCabinet{Courses: make([]domain.StudentCabinetCourse, 0)}
	found := false
	for rows.Next() {
		found = true
		var registeredAt time.Time
		var completedCourses, completedLessons, unreadConversationMessages int
		var courseID pgtype.Int8
		var title, mode, status pgtype.Text
		var totalLessons, courseCompletedLessons pgtype.Int4
		var pendingReviews, needsRevisionAnswers pgtype.Int4

		if err := rows.Scan(
			&registeredAt, &completedCourses, &completedLessons, &unreadConversationMessages,
			&courseID, &title, &status, &mode, &totalLessons, &courseCompletedLessons,
			&pendingReviews, &needsRevisionAnswers,
		); err != nil {
			return domain.StudentCabinet{}, mapDatabaseError("прочитать данные личного кабинета", err)
		}

		cabinet.RegisteredAt = registeredAt
		cabinet.CompletedCourses = completedCourses
		cabinet.CompletedLessons = completedLessons
		cabinet.UnreadConversationMessages = unreadConversationMessages
		if courseID.Valid && title.Valid && status.Valid {
			cabinet.Courses = append(cabinet.Courses, domain.StudentCabinetCourse{
				CourseID:             courseID.Int64,
				Title:                title.String,
				Mode:                 mode.String,
				Status:               status.String,
				TotalLessons:         int(totalLessons.Int32),
				CompletedLessons:     int(courseCompletedLessons.Int32),
				PendingReviews:       int(pendingReviews.Int32),
				NeedsRevisionAnswers: int(needsRevisionAnswers.Int32),
			})
		}
	}
	if err := rows.Err(); err != nil {
		return domain.StudentCabinet{}, mapDatabaseError("обойти данные личного кабинета", err)
	}
	if !found {
		return domain.StudentCabinet{}, ErrNotFound
	}
	return cabinet, nil
}
