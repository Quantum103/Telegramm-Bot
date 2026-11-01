CREATE TABLE course_enrollments (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    student_id BIGINT NOT NULL REFERENCES students(id) ON DELETE RESTRICT,
    course_id BIGINT NOT NULL REFERENCES courses(id) ON DELETE RESTRICT,
    run_number INTEGER NOT NULL CHECK (run_number > 0),
    mode TEXT NOT NULL CHECK (mode IN ('ai', 'curator')),
    curator_staff_id BIGINT REFERENCES staff_accounts(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'in_progress'
        CHECK (status IN ('awaiting_curator', 'in_progress', 'paused', 'completed', 'cancelled', 'superseded')),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (student_id, course_id, run_number),
    CHECK (
        (mode = 'ai' AND curator_staff_id IS NULL)
        OR mode = 'curator'
    ),
    CHECK (
        (status = 'completed' AND completed_at IS NOT NULL)
        OR (status <> 'completed' AND completed_at IS NULL)
    ),
    CHECK (completed_at IS NULL OR started_at IS NOT NULL)
);

CREATE UNIQUE INDEX course_enrollments_one_active_uq
    ON course_enrollments (student_id)
    WHERE status IN ('awaiting_curator', 'in_progress', 'paused');
CREATE INDEX course_enrollments_student_history_idx
    ON course_enrollments (student_id, created_at DESC, id DESC);
CREATE INDEX course_enrollments_curator_idx
    ON course_enrollments (curator_staff_id, status, updated_at DESC)
    WHERE mode = 'curator' AND curator_staff_id IS NOT NULL;

-- A course run freezes its required lessons and published versions. Later content
-- edits or course reordering cannot silently change an in-progress student's work.
CREATE TABLE course_enrollment_lessons (
    course_enrollment_id BIGINT NOT NULL
        REFERENCES course_enrollments(id) ON DELETE RESTRICT,
    lesson_id BIGINT NOT NULL REFERENCES lessons(id) ON DELETE RESTRICT,
    lesson_version_id BIGINT NOT NULL,
    position INTEGER NOT NULL CHECK (position > 0),
    PRIMARY KEY (course_enrollment_id, lesson_id),
    UNIQUE (course_enrollment_id, position),
    FOREIGN KEY (lesson_version_id, lesson_id)
        REFERENCES lesson_versions(id, lesson_id) ON DELETE RESTRICT,
    UNIQUE (course_enrollment_id, lesson_version_id)
);

ALTER TABLE lesson_enrollments
    ADD COLUMN course_enrollment_id BIGINT
        REFERENCES course_enrollments(id) ON DELETE RESTRICT;

-- The former key allowed two modes for one lesson and prevented a later course run.
-- Keep the old uniqueness rule only for legacy rows that have not been reconciled.
ALTER TABLE lesson_enrollments
    DROP CONSTRAINT IF EXISTS lesson_enrollments_student_id_lesson_version_id_mode_key;

CREATE UNIQUE INDEX lesson_enrollments_course_version_uq
    ON lesson_enrollments (course_enrollment_id, lesson_version_id)
    WHERE course_enrollment_id IS NOT NULL;
CREATE UNIQUE INDEX lesson_enrollments_legacy_version_mode_uq
    ON lesson_enrollments (student_id, lesson_version_id, mode)
    WHERE course_enrollment_id IS NULL;
CREATE INDEX lesson_enrollments_course_run_idx
    ON lesson_enrollments (course_enrollment_id, status, lesson_id)
    WHERE course_enrollment_id IS NOT NULL;

-- Existing learner progress is backfilled only when the lesson belongs to exactly
-- one course and the student's rows for that course have one unambiguous mode.
-- Ambiguous legacy rows are intentionally retained without a guessed course link.
WITH unique_lesson_course AS (
    SELECT lesson_id, min(course_id) AS course_id
    FROM course_lessons
    GROUP BY lesson_id
    HAVING count(*) = 1
),
legacy_groups AS (
    SELECT
        le.student_id,
        unique_lesson_course.course_id,
        le.mode,
        min(le.created_at) AS created_at,
        max(COALESCE(le.last_activity_at, le.updated_at, le.created_at)) AS last_activity_at,
        (
            bool_or(le.status IN ('not_started', 'in_progress', 'paused'))
            OR count(DISTINCT le.lesson_id) < (
                SELECT count(*)
                FROM course_lessons required_lesson
                WHERE required_lesson.course_id = unique_lesson_course.course_id
            )
        ) AS has_open_lessons,
        (
            bool_and(le.status = 'completed')
            AND count(DISTINCT le.lesson_id) = (
                SELECT count(*)
                FROM course_lessons required_lesson
                WHERE required_lesson.course_id = unique_lesson_course.course_id
            )
        ) AS all_lessons_completed,
        max(le.completed_at) AS completed_at
    FROM lesson_enrollments le
    JOIN unique_lesson_course ON unique_lesson_course.lesson_id = le.lesson_id
    WHERE le.status <> 'cancelled'
    GROUP BY le.student_id, unique_lesson_course.course_id, le.mode
),
preferred_groups AS (
    SELECT DISTINCT ON (student_id, course_id)
        student_id, course_id, mode, created_at, last_activity_at,
        has_open_lessons, all_lessons_completed, completed_at
    FROM legacy_groups
    ORDER BY student_id, course_id, last_activity_at DESC, mode DESC
),
ranked_groups AS (
    SELECT preferred_groups.*,
           row_number() OVER (
               PARTITION BY student_id
               ORDER BY has_open_lessons DESC, last_activity_at DESC, course_id DESC
           ) AS student_course_rank
    FROM preferred_groups
)
INSERT INTO course_enrollments (
    student_id, course_id, run_number, mode, curator_staff_id, status,
    started_at, completed_at, created_at, updated_at
)
SELECT
    ranked_groups.student_id,
    ranked_groups.course_id,
    1,
    ranked_groups.mode,
    CASE WHEN ranked_groups.mode = 'curator' THEN (
        SELECT assignment.curator_staff_id
        FROM student_curator_assignments assignment
        JOIN staff_accounts curator ON curator.id = assignment.curator_staff_id
        JOIN staff_role_assignments role_assignment
          ON role_assignment.staff_id = curator.id AND role_assignment.revoked_at IS NULL
        JOIN staff_roles role ON role.id = role_assignment.role_id AND role.role_code = 'curator'
        WHERE assignment.student_id = ranked_groups.student_id
          AND assignment.status = 'active'
          AND curator.account_status = 'active'
        ORDER BY assignment.assigned_at DESC, assignment.id DESC
        LIMIT 1
    ) ELSE NULL END,
    CASE
        WHEN ranked_groups.all_lessons_completed THEN 'completed'
        WHEN ranked_groups.student_course_rank > 1 THEN 'superseded'
        WHEN ranked_groups.mode = 'curator' AND NOT EXISTS (
            SELECT 1
            FROM student_curator_assignments assignment
            JOIN staff_accounts curator ON curator.id = assignment.curator_staff_id
            JOIN staff_role_assignments role_assignment
              ON role_assignment.staff_id = curator.id AND role_assignment.revoked_at IS NULL
            JOIN staff_roles role ON role.id = role_assignment.role_id AND role.role_code = 'curator'
            WHERE assignment.student_id = ranked_groups.student_id
              AND assignment.status = 'active'
              AND curator.account_status = 'active'
        ) THEN 'awaiting_curator'
        ELSE 'in_progress'
    END,
    ranked_groups.created_at,
    CASE WHEN ranked_groups.all_lessons_completed THEN COALESCE(ranked_groups.completed_at, now()) ELSE NULL END,
    ranked_groups.created_at,
    ranked_groups.last_activity_at
FROM ranked_groups;

-- Attach only the legacy lesson records that unambiguously match the selected
-- (student, course, mode) run.
UPDATE lesson_enrollments le
SET course_enrollment_id = ce.id
FROM course_enrollments ce
JOIN (
    SELECT lesson_id, min(course_id) AS course_id
    FROM course_lessons
    GROUP BY lesson_id
    HAVING count(*) = 1
) unique_lesson_course ON unique_lesson_course.course_id = ce.course_id
WHERE le.student_id = ce.student_id
  AND le.mode = ce.mode
  AND le.lesson_id = unique_lesson_course.lesson_id
  AND le.status <> 'cancelled'
  AND le.course_enrollment_id IS NULL;

-- Snapshot each linked legacy run using the version it already started, falling
-- back to the currently published version for lessons not yet opened.
INSERT INTO course_enrollment_lessons (course_enrollment_id, lesson_id, lesson_version_id, position)
SELECT ce.id, cl.lesson_id, COALESCE(existing.lesson_version_id, published.id), cl.position
FROM course_enrollments ce
JOIN course_lessons cl ON cl.course_id = ce.course_id
LEFT JOIN LATERAL (
    SELECT le.lesson_version_id
    FROM lesson_enrollments le
    WHERE le.course_enrollment_id = ce.id AND le.lesson_id = cl.lesson_id
    ORDER BY COALESCE(le.last_activity_at, le.updated_at, le.created_at) DESC, le.id DESC
    LIMIT 1
) existing ON TRUE
LEFT JOIN LATERAL (
    SELECT lv.id
    FROM lesson_versions lv
    WHERE lv.lesson_id = cl.lesson_id AND lv.status = 'published'
    ORDER BY lv.version_number DESC, lv.id DESC
    LIMIT 1
) published ON TRUE
WHERE COALESCE(existing.lesson_version_id, published.id) IS NOT NULL
ON CONFLICT DO NOTHING;

CREATE TABLE answer_attempt_reviews (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    answer_attempt_id BIGINT NOT NULL UNIQUE
        REFERENCES answer_attempts(id) ON DELETE RESTRICT,
    curator_staff_id BIGINT NOT NULL REFERENCES staff_accounts(id) ON DELETE RESTRICT,
    decision TEXT NOT NULL CHECK (decision IN ('approved', 'needs_revision')),
    feedback TEXT NOT NULL DEFAULT '',
    reviewed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX answer_attempt_reviews_curator_idx
    ON answer_attempt_reviews (curator_staff_id, reviewed_at DESC);

-- A learner cannot change the course, run number, or mode after choosing a mode.
CREATE FUNCTION prevent_course_enrollment_mode_change() RETURNS trigger AS $$
BEGIN
    IF NEW.student_id <> OLD.student_id
       OR NEW.course_id <> OLD.course_id
       OR NEW.run_number <> OLD.run_number
       OR NEW.mode <> OLD.mode THEN
        RAISE EXCEPTION 'course enrollment identity and mode are immutable'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER course_enrollments_immutable_selection_trg
    BEFORE UPDATE ON course_enrollments
    FOR EACH ROW EXECUTE FUNCTION prevent_course_enrollment_mode_change();

-- A lesson enrollment must belong to the same student and mode as its parent run,
-- and its version must be part of the frozen course curriculum.
CREATE FUNCTION validate_course_lesson_enrollment() RETURNS trigger AS $$
DECLARE
    parent_student_id BIGINT;
    parent_mode TEXT;
BEGIN
    IF NEW.course_enrollment_id IS NULL THEN
        RETURN NEW;
    END IF;

    SELECT student_id, mode
      INTO parent_student_id, parent_mode
    FROM course_enrollments
    WHERE id = NEW.course_enrollment_id;

    IF NOT FOUND
       OR parent_student_id <> NEW.student_id
       OR parent_mode <> NEW.mode
       OR NOT EXISTS (
            SELECT 1
            FROM course_enrollment_lessons curriculum
            WHERE curriculum.course_enrollment_id = NEW.course_enrollment_id
              AND curriculum.lesson_id = NEW.lesson_id
              AND curriculum.lesson_version_id = NEW.lesson_version_id
       ) THEN
        RAISE EXCEPTION 'lesson enrollment does not match its course enrollment'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER lesson_enrollments_course_consistency_trg
    BEFORE INSERT OR UPDATE OF course_enrollment_id, student_id, lesson_id, lesson_version_id, mode
    ON lesson_enrollments
    FOR EACH ROW EXECUTE FUNCTION validate_course_lesson_enrollment();
