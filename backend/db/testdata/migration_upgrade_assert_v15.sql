-- Assertions after all migrations have been applied.
DO $$
DECLARE
    learner_id BIGINT;
    course_id BIGINT;
    lesson_id BIGINT;
    version_id BIGINT;
    question_id BIGINT;
    enrollment_id BIGINT;
    ambiguous_learner_id BIGINT;
    ambiguous_enrollment_count INTEGER;
    referral_staff_id BIGINT;
BEGIN
    SELECT id INTO learner_id
    FROM students WHERE telegram_id = 880000000000001;
    IF learner_id IS NULL THEN
        RAISE EXCEPTION 'legacy learner was lost during migration';
    END IF;

    SELECT id INTO course_id
    FROM courses WHERE slug = 'migration-upgrade-course';
    SELECT id INTO lesson_id
    FROM lessons
    WHERE id = (
        SELECT lesson_id FROM course_lessons WHERE course_id = course_id
    );
    -- Resolve exact legacy IDs from the preserved lesson enrollment and its
    -- frozen curriculum snapshot instead of relying on identity sequence values.
    SELECT id, lesson_id, lesson_version_id, current_question_id
    INTO enrollment_id, lesson_id, version_id, question_id
    FROM lesson_enrollments
    WHERE student_id = learner_id AND mode = 'ai' AND status = 'in_progress';

    IF enrollment_id IS NULL OR version_id IS NULL OR question_id IS NULL THEN
        RAISE EXCEPTION 'legacy lesson enrollment or current question was lost';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM student_answers answer
        WHERE answer.enrollment_id = enrollment_id
          AND answer.lesson_version_id = version_id
          AND answer.question_id = question_id
          AND answer.answer_text = 'Saved before migration'
    ) THEN
        RAISE EXCEPTION 'saved learner answer was lost during migration';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM course_enrollments ce
        JOIN course_enrollment_lessons cel
          ON cel.course_enrollment_id = ce.id
        WHERE ce.student_id = learner_id
          AND ce.course_id = course_id
          AND ce.run_number = 1
          AND ce.mode = 'ai'
          AND ce.status = 'in_progress'
          AND cel.lesson_id = lesson_id
          AND cel.lesson_version_id = version_id
          AND cel.position = 1
    ) THEN
        RAISE EXCEPTION 'legacy learner progress was not backfilled into a frozen course run';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM lesson_enrollments le
        JOIN course_enrollments ce ON ce.id = le.course_enrollment_id
        WHERE le.id = enrollment_id
          AND ce.student_id = learner_id
          AND ce.course_id = course_id
          AND ce.mode = 'ai'
    ) THEN
        RAISE EXCEPTION 'legacy lesson enrollment was not linked to its migrated course run';
    END IF;

    SELECT id INTO ambiguous_learner_id
    FROM students WHERE telegram_id = 880000000000002;
    SELECT count(*) INTO ambiguous_enrollment_count
    FROM course_enrollments WHERE student_id = ambiguous_learner_id;
    IF ambiguous_enrollment_count <> 0 THEN
        RAISE EXCEPTION 'ambiguous legacy history was guessed into a course run';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM lesson_enrollments le
        WHERE le.student_id = ambiguous_learner_id
          AND le.course_enrollment_id IS NULL
          AND le.status = 'in_progress'
          AND le.current_question_id IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'ambiguous legacy progress was not preserved as an unlinked legacy row';
    END IF;

    SELECT id INTO referral_staff_id
    FROM staff_accounts WHERE email = 'migration-upgrade-staff@example.test';
    IF NOT EXISTS (
        SELECT 1
        FROM staff_referral_codes
        WHERE staff_id = referral_staff_id
          AND legacy_code = 'c_' || repeat('A', 43)
          AND code = 'c_' || referral_staff_id::TEXT
    ) THEN
        RAISE EXCEPTION 'legacy referral code was not transformed and preserved';
    END IF;

    IF to_regclass('public.bible_chapter_telegraph_jobs') IS NULL THEN
        RAISE EXCEPTION 'latest Telegram chapter preparation queue table is missing';
    END IF;

    INSERT INTO bible_chapter_telegraph_jobs (translation_code, book_id, chapter_number)
    VALUES ('synodal', 40, 1);
END $$;
