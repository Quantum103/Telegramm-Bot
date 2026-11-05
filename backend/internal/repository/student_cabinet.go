package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GetStudentCabinet читает профильную дату, общую статистику и последние запуски курсов одним SQL-запросом.
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
		       latest.course_id,
		       latest.title,
		       latest.status,
		       latest.total_lessons,
		       latest.completed_lessons
		FROM students student
		LEFT JOIN LATERAL (
		    SELECT current_run.course_id,
		           current_run.title,
		           current_run.status,
		           count(course_lesson.lesson_id)::INTEGER AS total_lessons,
		           count(course_lesson.lesson_id) FILTER (
		               WHERE lesson.status = 'completed'
		           )::INTEGER AS completed_lessons,
		           current_run.updated_at,
		           current_run.id
		    FROM (
		        SELECT DISTINCT ON (enrollment.course_id)
		               enrollment.id,
		               enrollment.course_id,
		               course.title,
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
		    GROUP BY current_run.course_id, current_run.title, current_run.status,
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
		var completedCourses, completedLessons int
		var courseID pgtype.Int8
		var title, status pgtype.Text
		var totalLessons, courseCompletedLessons pgtype.Int4

		if err := rows.Scan(
			&registeredAt, &completedCourses, &completedLessons,
			&courseID, &title, &status, &totalLessons, &courseCompletedLessons,
		); err != nil {
			return domain.StudentCabinet{}, mapDatabaseError("прочитать данные личного кабинета", err)
		}

		cabinet.RegisteredAt = registeredAt
		cabinet.CompletedCourses = completedCourses
		cabinet.CompletedLessons = completedLessons
		if courseID.Valid && title.Valid && status.Valid {
			cabinet.Courses = append(cabinet.Courses, domain.StudentCabinetCourse{
				CourseID:         courseID.Int64,
				Title:            title.String,
				Status:           status.String,
				TotalLessons:     int(totalLessons.Int32),
				CompletedLessons: int(courseCompletedLessons.Int32),
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
