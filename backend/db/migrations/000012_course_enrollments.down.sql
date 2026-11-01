DROP TRIGGER IF EXISTS lesson_enrollments_course_consistency_trg ON lesson_enrollments;
DROP FUNCTION IF EXISTS validate_course_lesson_enrollment();

DROP TRIGGER IF EXISTS course_enrollments_immutable_selection_trg ON course_enrollments;
DROP FUNCTION IF EXISTS prevent_course_enrollment_mode_change();

DROP TABLE IF EXISTS answer_attempt_reviews;
DROP INDEX IF EXISTS lesson_enrollments_course_run_idx;
DROP INDEX IF EXISTS lesson_enrollments_legacy_version_mode_uq;
DROP INDEX IF EXISTS lesson_enrollments_course_version_uq;
ALTER TABLE lesson_enrollments DROP COLUMN IF EXISTS course_enrollment_id;

-- This downgrade can fail after a learner has repeated a completed course because
-- the legacy uniqueness key cannot represent multiple runs of the same lesson.
ALTER TABLE lesson_enrollments
    ADD CONSTRAINT lesson_enrollments_student_id_lesson_version_id_mode_key
    UNIQUE (student_id, lesson_version_id, mode);

DROP TABLE IF EXISTS course_enrollment_lessons;
DROP INDEX IF EXISTS course_enrollments_curator_idx;
DROP INDEX IF EXISTS course_enrollments_student_history_idx;
DROP INDEX IF EXISTS course_enrollments_one_active_uq;
DROP TABLE IF EXISTS course_enrollments;
