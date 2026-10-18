CREATE TABLE students (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    telegram_id BIGINT NOT NULL UNIQUE CHECK (telegram_id > 0),
    display_name TEXT NOT NULL CHECK (length(btrim(display_name)) > 0),
    telegram_username TEXT CHECK (telegram_username IS NULL OR length(btrim(telegram_username)) > 0),
    preferred_name TEXT CHECK (preferred_name IS NULL OR length(btrim(preferred_name)) > 0),
    account_status TEXT NOT NULL DEFAULT 'active'
        CHECK (account_status IN ('active', 'inactive', 'blocked')),
    registered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_active_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE staff_accounts (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    telegram_id BIGINT UNIQUE CHECK (telegram_id IS NULL OR telegram_id > 0),
    display_name TEXT NOT NULL CHECK (length(btrim(display_name)) > 0),
    telegram_username TEXT CHECK (telegram_username IS NULL OR length(btrim(telegram_username)) > 0),
    email TEXT UNIQUE CHECK (email IS NULL OR length(btrim(email)) > 0),
    password_hash TEXT,
    account_status TEXT NOT NULL DEFAULT 'active'
        CHECK (account_status IN ('active', 'inactive', 'locked')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE staff_roles (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    role_code TEXT NOT NULL UNIQUE
        CHECK (role_code IN ('administrator', 'curator')),
    title TEXT NOT NULL CHECK (length(btrim(title)) > 0)
);

INSERT INTO staff_roles (role_code, title) VALUES
    ('administrator', 'Администратор'),
    ('curator', 'Куратор');

CREATE TABLE staff_role_assignments (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    staff_id BIGINT NOT NULL REFERENCES staff_accounts(id) ON DELETE RESTRICT,
    role_id BIGINT NOT NULL REFERENCES staff_roles(id) ON DELETE RESTRICT,
    granted_by_staff_id BIGINT REFERENCES staff_accounts(id) ON DELETE SET NULL,
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ,
    CHECK (revoked_at IS NULL OR revoked_at >= granted_at)
);

CREATE UNIQUE INDEX staff_role_assignments_active_uq
    ON staff_role_assignments (staff_id, role_id)
    WHERE revoked_at IS NULL;
CREATE INDEX staff_role_assignments_role_idx
    ON staff_role_assignments (role_id, staff_id)
    WHERE revoked_at IS NULL;

CREATE TABLE courses (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    slug TEXT NOT NULL UNIQUE CHECK (length(btrim(slug)) > 0),
    title TEXT NOT NULL CHECK (length(btrim(title)) > 0),
    description TEXT,
    status TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'published', 'archived')),
    created_by_staff_id BIGINT REFERENCES staff_accounts(id) ON DELETE SET NULL,
    published_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX courses_status_idx ON courses (status, title);

CREATE TABLE lessons (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE course_lessons (
    course_id BIGINT NOT NULL REFERENCES courses(id) ON DELETE RESTRICT,
    lesson_id BIGINT NOT NULL REFERENCES lessons(id) ON DELETE RESTRICT,
    position INTEGER NOT NULL CHECK (position > 0),
    added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (course_id, lesson_id),
    UNIQUE (course_id, position)
);

CREATE INDEX course_lessons_order_idx ON course_lessons (course_id, position);

CREATE TABLE lesson_versions (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    lesson_id BIGINT NOT NULL REFERENCES lessons(id) ON DELETE RESTRICT,
    version_number INTEGER NOT NULL CHECK (version_number > 0),
    title TEXT NOT NULL CHECK (length(btrim(title)) > 0),
    description TEXT,
    status TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'published', 'archived')),
    created_by_staff_id BIGINT REFERENCES staff_accounts(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    UNIQUE (lesson_id, version_number),
    UNIQUE (id, lesson_id),
    CHECK (
        (status = 'draft' AND published_at IS NULL)
        OR (status IN ('published', 'archived') AND published_at IS NOT NULL)
    )
);

CREATE UNIQUE INDEX lesson_versions_one_published_uq
    ON lesson_versions (lesson_id)
    WHERE status = 'published';
CREATE INDEX lesson_versions_status_idx
    ON lesson_versions (status, lesson_id, version_number DESC);

CREATE TABLE lesson_questions (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    lesson_version_id BIGINT NOT NULL REFERENCES lesson_versions(id) ON DELETE RESTRICT,
    question_number INTEGER NOT NULL CHECK (question_number > 0),
    prompt TEXT NOT NULL CHECK (length(btrim(prompt)) > 0),
    expected_answer TEXT NOT NULL CHECK (length(btrim(expected_answer)) > 0),
    answer_type TEXT NOT NULL DEFAULT 'text'
        CHECK (answer_type IN ('text', 'single_choice', 'multiple_choice')),
    expected_explanation TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (lesson_version_id, question_number),
    UNIQUE (id, lesson_version_id)
);

CREATE INDEX lesson_questions_order_idx
    ON lesson_questions (lesson_version_id, question_number);

CREATE TABLE question_options (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    question_id BIGINT NOT NULL REFERENCES lesson_questions(id) ON DELETE RESTRICT,
    option_key TEXT NOT NULL CHECK (length(btrim(option_key)) > 0),
    option_text TEXT NOT NULL CHECK (length(btrim(option_text)) > 0),
    sort_order INTEGER NOT NULL CHECK (sort_order > 0),
    is_correct BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE (question_id, option_key),
    UNIQUE (question_id, sort_order),
    UNIQUE (id, question_id)
);

CREATE TABLE question_criteria (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    question_id BIGINT NOT NULL REFERENCES lesson_questions(id) ON DELETE RESTRICT,
    criterion_key TEXT NOT NULL CHECK (length(btrim(criterion_key)) > 0),
    description TEXT NOT NULL CHECK (length(btrim(description)) > 0),
    weight NUMERIC(7, 3) NOT NULL DEFAULT 1.000 CHECK (weight > 0),
    is_required BOOLEAN NOT NULL DEFAULT TRUE,
    sort_order INTEGER NOT NULL CHECK (sort_order > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (question_id, criterion_key),
    UNIQUE (question_id, sort_order)
);

CREATE INDEX question_criteria_order_idx
    ON question_criteria (question_id, sort_order);

CREATE TABLE question_common_mistakes (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    question_id BIGINT NOT NULL REFERENCES lesson_questions(id) ON DELETE RESTRICT,
    mistake_text TEXT NOT NULL CHECK (length(btrim(mistake_text)) > 0),
    explanation TEXT NOT NULL CHECK (length(btrim(explanation)) > 0),
    sort_order INTEGER NOT NULL CHECK (sort_order > 0),
    UNIQUE (question_id, sort_order)
);

CREATE TABLE bible_passages (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    book_name TEXT NOT NULL CHECK (length(btrim(book_name)) > 0),
    chapter INTEGER NOT NULL CHECK (chapter > 0),
    start_verse INTEGER NOT NULL CHECK (start_verse > 0),
    end_verse INTEGER NOT NULL CHECK (end_verse >= start_verse),
    translation_name TEXT CHECK (translation_name IS NULL OR length(btrim(translation_name)) > 0),
    passage_text TEXT,
    source_note TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX bible_passages_reference_idx
    ON bible_passages (book_name, chapter, start_verse, end_verse);

CREATE TABLE question_bible_passages (
    question_id BIGINT NOT NULL REFERENCES lesson_questions(id) ON DELETE RESTRICT,
    passage_id BIGINT NOT NULL REFERENCES bible_passages(id) ON DELETE RESTRICT,
    sort_order INTEGER NOT NULL CHECK (sort_order > 0),
    PRIMARY KEY (question_id, passage_id),
    UNIQUE (question_id, sort_order)
);

CREATE TABLE lesson_enrollments (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    student_id BIGINT NOT NULL REFERENCES students(id) ON DELETE RESTRICT,
    lesson_id BIGINT NOT NULL,
    lesson_version_id BIGINT NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('ai', 'curator')),
    current_question_id BIGINT,
    status TEXT NOT NULL DEFAULT 'not_started'
        CHECK (status IN ('not_started', 'in_progress', 'paused', 'completed', 'cancelled')),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    last_activity_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (lesson_version_id, lesson_id)
        REFERENCES lesson_versions (id, lesson_id) ON DELETE RESTRICT,
    FOREIGN KEY (current_question_id, lesson_version_id)
        REFERENCES lesson_questions (id, lesson_version_id) ON DELETE RESTRICT,
    UNIQUE (student_id, lesson_version_id, mode),
    UNIQUE (id, lesson_version_id),
    UNIQUE (id, student_id),
    CHECK (
        (status = 'completed' AND completed_at IS NOT NULL)
        OR (status <> 'completed' AND completed_at IS NULL)
    ),
    CHECK (completed_at IS NULL OR started_at IS NOT NULL)
);

CREATE INDEX lesson_enrollments_student_progress_idx
    ON lesson_enrollments (student_id, status, updated_at DESC);
CREATE INDEX lesson_enrollments_version_idx
    ON lesson_enrollments (lesson_version_id, status);

CREATE TABLE student_answers (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    enrollment_id BIGINT NOT NULL,
    lesson_version_id BIGINT NOT NULL,
    question_id BIGINT NOT NULL,
    answer_text TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (enrollment_id, lesson_version_id)
        REFERENCES lesson_enrollments (id, lesson_version_id) ON DELETE RESTRICT,
    FOREIGN KEY (question_id, lesson_version_id)
        REFERENCES lesson_questions (id, lesson_version_id) ON DELETE RESTRICT,
    UNIQUE (enrollment_id, question_id),
    UNIQUE (id, question_id)
);

CREATE INDEX student_answers_enrollment_idx
    ON student_answers (enrollment_id, updated_at DESC);

CREATE TABLE student_answer_options (
    answer_id BIGINT NOT NULL,
    question_id BIGINT NOT NULL,
    option_id BIGINT NOT NULL,
    selected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (answer_id, option_id),
    FOREIGN KEY (answer_id, question_id)
        REFERENCES student_answers (id, question_id) ON DELETE RESTRICT,
    FOREIGN KEY (option_id, question_id)
        REFERENCES question_options (id, question_id) ON DELETE RESTRICT
);

CREATE TABLE answer_attempts (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    answer_id BIGINT NOT NULL,
    enrollment_id BIGINT NOT NULL,
    lesson_version_id BIGINT NOT NULL,
    question_id BIGINT NOT NULL,
    idempotency_key UUID,
    submitted_answer_text TEXT NOT NULL DEFAULT '',
    processing_status TEXT NOT NULL DEFAULT 'pending'
        CHECK (processing_status IN ('pending', 'running', 'completed', 'failed')),
    evaluation_result TEXT
        CHECK (evaluation_result IS NULL OR evaluation_result IN ('correct', 'partial', 'incorrect', 'ambiguous')),
    explanation TEXT,
    missing_points JSONB NOT NULL DEFAULT '[]'::JSONB
        CHECK (jsonb_typeof(missing_points) = 'array'),
    provider TEXT,
    model TEXT,
    duration_ms INTEGER CHECK (duration_ms IS NULL OR duration_ms >= 0),
    error_code TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    FOREIGN KEY (answer_id, question_id)
        REFERENCES student_answers (id, question_id) ON DELETE RESTRICT,
    FOREIGN KEY (enrollment_id, lesson_version_id)
        REFERENCES lesson_enrollments (id, lesson_version_id) ON DELETE RESTRICT,
    FOREIGN KEY (question_id, lesson_version_id)
        REFERENCES lesson_questions (id, lesson_version_id) ON DELETE RESTRICT,
    UNIQUE (enrollment_id, idempotency_key),
    UNIQUE (id, question_id),
    CHECK (
        (processing_status = 'completed'
            AND evaluation_result IS NOT NULL
            AND completed_at IS NOT NULL
            AND error_code IS NULL)
        OR (processing_status = 'failed'
            AND evaluation_result IS NULL
            AND completed_at IS NOT NULL
            AND error_code IS NOT NULL)
        OR (processing_status IN ('pending', 'running')
            AND evaluation_result IS NULL
            AND completed_at IS NULL
            AND error_code IS NULL)
    )
);

CREATE INDEX answer_attempts_history_idx
    ON answer_attempts (enrollment_id, question_id, created_at DESC);
CREATE INDEX answer_attempts_processing_idx
    ON answer_attempts (processing_status, created_at)
    WHERE processing_status IN ('pending', 'running');

CREATE TABLE answer_attempt_options (
    attempt_id BIGINT NOT NULL,
    question_id BIGINT NOT NULL,
    option_id BIGINT NOT NULL,
    PRIMARY KEY (attempt_id, option_id),
    FOREIGN KEY (attempt_id, question_id)
        REFERENCES answer_attempts (id, question_id) ON DELETE RESTRICT,
    FOREIGN KEY (option_id, question_id)
        REFERENCES question_options (id, question_id) ON DELETE RESTRICT
);

CREATE TABLE student_curator_assignments (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    student_id BIGINT NOT NULL REFERENCES students(id) ON DELETE RESTRICT,
    curator_staff_id BIGINT NOT NULL REFERENCES staff_accounts(id) ON DELETE RESTRICT,
    assigned_by_staff_id BIGINT REFERENCES staff_accounts(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'ended')),
    assigned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at TIMESTAMPTZ,
    CHECK (
        (status = 'active' AND ended_at IS NULL)
        OR (status = 'ended' AND ended_at IS NOT NULL)
    ),
    CHECK (ended_at IS NULL OR ended_at >= assigned_at)
);

CREATE UNIQUE INDEX student_curator_one_active_uq
    ON student_curator_assignments (student_id)
    WHERE status = 'active';
CREATE INDEX student_curator_lookup_idx
    ON student_curator_assignments (curator_staff_id, status, assigned_at DESC);

CREATE TABLE conversations (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    student_id BIGINT NOT NULL REFERENCES students(id) ON DELETE RESTRICT,
    assigned_curator_staff_id BIGINT REFERENCES staff_accounts(id) ON DELETE SET NULL,
    enrollment_id BIGINT,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'closed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at TIMESTAMPTZ,
    FOREIGN KEY (enrollment_id, student_id)
        REFERENCES lesson_enrollments (id, student_id) ON DELETE RESTRICT,
    UNIQUE (id, student_id),
    CHECK (
        (status = 'active' AND closed_at IS NULL)
        OR (status = 'closed' AND closed_at IS NOT NULL)
    )
);

CREATE INDEX conversations_student_idx
    ON conversations (student_id, status, updated_at DESC);
CREATE INDEX conversations_curator_idx
    ON conversations (assigned_curator_staff_id, status, updated_at DESC);

CREATE TABLE conversation_participants (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    conversation_id BIGINT NOT NULL REFERENCES conversations(id) ON DELETE RESTRICT,
    student_id BIGINT REFERENCES students(id) ON DELETE RESTRICT,
    staff_id BIGINT REFERENCES staff_accounts(id) ON DELETE RESTRICT,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    left_at TIMESTAMPTZ,
    last_read_at TIMESTAMPTZ,
    FOREIGN KEY (conversation_id, student_id)
        REFERENCES conversations (id, student_id) ON DELETE RESTRICT,
    CHECK (
        (student_id IS NOT NULL AND staff_id IS NULL)
        OR (student_id IS NULL AND staff_id IS NOT NULL)
    ),
    CHECK (left_at IS NULL OR left_at >= joined_at),
    UNIQUE (id, conversation_id)
);

CREATE UNIQUE INDEX conversation_participants_student_uq
    ON conversation_participants (conversation_id, student_id)
    WHERE student_id IS NOT NULL;
CREATE UNIQUE INDEX conversation_participants_staff_uq
    ON conversation_participants (conversation_id, staff_id)
    WHERE staff_id IS NOT NULL;
CREATE INDEX conversation_participants_staff_lookup_idx
    ON conversation_participants (staff_id, conversation_id)
    WHERE staff_id IS NOT NULL AND left_at IS NULL;

CREATE TABLE conversation_messages (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    conversation_id BIGINT NOT NULL REFERENCES conversations(id) ON DELETE RESTRICT,
    sender_participant_id BIGINT NOT NULL,
    message_type TEXT NOT NULL
        CHECK (message_type IN ('text', 'voice', 'photo', 'document', 'other')),
    body_text TEXT,
    telegram_file_id TEXT,
    media_mime_type TEXT,
    media_duration_seconds INTEGER CHECK (media_duration_seconds IS NULL OR media_duration_seconds >= 0),
    media_size_bytes BIGINT CHECK (media_size_bytes IS NULL OR media_size_bytes >= 0),
    telegram_chat_id BIGINT,
    telegram_message_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    edited_at TIMESTAMPTZ,
    FOREIGN KEY (sender_participant_id, conversation_id)
        REFERENCES conversation_participants (id, conversation_id) ON DELETE RESTRICT,
    UNIQUE (id, conversation_id),
    CHECK (message_type <> 'text' OR body_text IS NOT NULL),
    CHECK (message_type <> 'voice' OR telegram_file_id IS NOT NULL),
    CHECK (
        (telegram_chat_id IS NULL AND telegram_message_id IS NULL)
        OR (telegram_chat_id IS NOT NULL AND telegram_message_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX conversation_messages_telegram_uq
    ON conversation_messages (telegram_chat_id, telegram_message_id)
    WHERE telegram_chat_id IS NOT NULL AND telegram_message_id IS NOT NULL;
CREATE INDEX conversation_messages_history_idx
    ON conversation_messages (conversation_id, created_at, id);

CREATE TABLE conversation_message_reads (
    message_id BIGINT NOT NULL,
    participant_id BIGINT NOT NULL,
    conversation_id BIGINT NOT NULL,
    read_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (message_id, participant_id),
    FOREIGN KEY (message_id, conversation_id)
        REFERENCES conversation_messages (id, conversation_id) ON DELETE RESTRICT,
    FOREIGN KEY (participant_id, conversation_id)
        REFERENCES conversation_participants (id, conversation_id) ON DELETE RESTRICT
);

CREATE INDEX conversation_message_reads_participant_idx
    ON conversation_message_reads (participant_id, read_at DESC);

CREATE TABLE ai_operations (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    answer_attempt_id BIGINT REFERENCES answer_attempts(id) ON DELETE SET NULL,
    provider TEXT NOT NULL CHECK (length(btrim(provider)) > 0),
    model TEXT NOT NULL CHECK (length(btrim(model)) > 0),
    operation_type TEXT NOT NULL CHECK (length(btrim(operation_type)) > 0),
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'succeeded', 'failed')),
    input_tokens BIGINT CHECK (input_tokens IS NULL OR input_tokens >= 0),
    output_tokens BIGINT CHECK (output_tokens IS NULL OR output_tokens >= 0),
    cost_amount NUMERIC(14, 6) CHECK (cost_amount IS NULL OR cost_amount >= 0),
    cost_currency TEXT CHECK (cost_currency IS NULL OR length(btrim(cost_currency)) > 0),
    duration_ms INTEGER CHECK (duration_ms IS NULL OR duration_ms >= 0),
    error_code TEXT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    CHECK (
        (status = 'pending' AND completed_at IS NULL)
        OR (status IN ('succeeded', 'failed') AND completed_at IS NOT NULL)
    )
);

CREATE INDEX ai_operations_created_idx
    ON ai_operations (started_at DESC);
CREATE INDEX ai_operations_provider_idx
    ON ai_operations (provider, started_at DESC);
CREATE INDEX ai_operations_attempt_idx
    ON ai_operations (answer_attempt_id)
    WHERE answer_attempt_id IS NOT NULL;

CREATE TABLE audit_events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_staff_id BIGINT REFERENCES staff_accounts(id) ON DELETE SET NULL,
    action TEXT NOT NULL CHECK (length(btrim(action)) > 0),
    entity_type TEXT NOT NULL CHECK (length(btrim(entity_type)) > 0),
    entity_id BIGINT,
    details JSONB NOT NULL DEFAULT '{}'::JSONB
        CHECK (jsonb_typeof(details) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX audit_events_created_idx ON audit_events (created_at DESC);
CREATE INDEX audit_events_entity_idx
    ON audit_events (entity_type, entity_id, created_at DESC);
CREATE INDEX audit_events_actor_idx
    ON audit_events (actor_staff_id, created_at DESC)
    WHERE actor_staff_id IS NOT NULL;
