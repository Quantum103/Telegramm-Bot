-- Seed representative legacy data at schema version 11.
-- This file is only applied to the isolated migration-upgrade CI database.
DO $$
DECLARE
    student_id BIGINT;
    staff_id BIGINT;
    course_id BIGINT;
    lesson_id BIGINT;
    version_id BIGINT;
    question_id BIGINT;
    enrollment_id BIGINT;
    ambiguous_student_id BIGINT;
    shared_course_one BIGINT;
    shared_course_two BIGINT;
    shared_lesson_id BIGINT;
    shared_version_id BIGINT;
    shared_question_id BIGINT;
BEGIN
    INSERT INTO students (telegram_id, display_name)
    VALUES (880000000000001, 'Migration upgrade learner')
    RETURNING id INTO student_id;

    INSERT INTO staff_accounts (display_name, email)
    VALUES ('Migration upgrade staff', 'migration-upgrade-staff@example.test')
    RETURNING id INTO staff_id;

    INSERT INTO courses (slug, title, status, published_at)
    VALUES ('migration-upgrade-course', 'Migration upgrade course', 'published', now())
    RETURNING id INTO course_id;

    INSERT INTO lessons DEFAULT VALUES RETURNING id INTO lesson_id;

    INSERT INTO course_lessons (course_id, lesson_id, position)
    VALUES (course_id, lesson_id, 1);

    INSERT INTO lesson_versions (lesson_id, version_number, title, status)
    VALUES (lesson_id, 1, 'Legacy published lesson', 'draft')
    RETURNING id INTO version_id;

    INSERT INTO lesson_questions (lesson_version_id, question_number, prompt, expected_answer)
    VALUES (version_id, 1, 'Legacy question', 'Legacy expected answer')
    RETURNING id INTO question_id;

    UPDATE lesson_versions
    SET status = 'published', published_at = now()
    WHERE id = version_id;

    INSERT INTO lesson_enrollments (
        student_id, lesson_id, lesson_version_id, mode, current_question_id,
        status, started_at, last_activity_at
    ) VALUES (
        student_id, lesson_id, version_id, 'ai', question_id,
        'in_progress', now(), now()
    ) RETURNING id INTO enrollment_id;

    INSERT INTO student_answers (
        enrollment_id, lesson_version_id, question_id, answer_text
    ) VALUES (
        enrollment_id, version_id, question_id, 'Saved before migration'
    );

    -- This learner's lesson belongs to two courses. Migration 000012 must not
    -- guess the course owner or attach the legacy enrollment to either course.
    INSERT INTO students (telegram_id, display_name)
    VALUES (880000000000002, 'Ambiguous legacy learner')
    RETURNING id INTO ambiguous_student_id;

    INSERT INTO courses (slug, title, status, published_at)
    VALUES ('migration-shared-course-one', 'Shared course one', 'published', now())
    RETURNING id INTO shared_course_one;

    INSERT INTO courses (slug, title, status, published_at)
    VALUES ('migration-shared-course-two', 'Shared course two', 'published', now())
    RETURNING id INTO shared_course_two;

    INSERT INTO lessons DEFAULT VALUES RETURNING id INTO shared_lesson_id;

    INSERT INTO course_lessons (course_id, lesson_id, position)
    VALUES
        (shared_course_one, shared_lesson_id, 1),
        (shared_course_two, shared_lesson_id, 1);

    INSERT INTO lesson_versions (lesson_id, version_number, title, status)
    VALUES (shared_lesson_id, 1, 'Shared legacy lesson', 'draft')
    RETURNING id INTO shared_version_id;

    INSERT INTO lesson_questions (lesson_version_id, question_number, prompt, expected_answer)
    VALUES (shared_version_id, 1, 'Shared legacy question', 'Expected answer')
    RETURNING id INTO shared_question_id;

    UPDATE lesson_versions
    SET status = 'published', published_at = now()
    WHERE id = shared_version_id;

    INSERT INTO lesson_enrollments (
        student_id, lesson_id, lesson_version_id, mode, current_question_id,
        status, started_at, last_activity_at
    ) VALUES (
        ambiguous_student_id, shared_lesson_id, shared_version_id, 'ai', shared_question_id,
        'in_progress', now(), now()
    );
END $$;
