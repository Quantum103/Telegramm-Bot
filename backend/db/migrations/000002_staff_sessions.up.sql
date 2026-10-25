CREATE TABLE staff_sessions (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    staff_id BIGINT NOT NULL REFERENCES staff_accounts(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    csrf_token_hash BYTEA NOT NULL CHECK (octet_length(csrf_token_hash) = 32),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ,
    CHECK (expires_at > created_at),
    CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);

CREATE INDEX staff_sessions_active_staff_idx
    ON staff_sessions (staff_id, expires_at)
    WHERE revoked_at IS NULL;

CREATE INDEX staff_sessions_expired_idx
    ON staff_sessions (expires_at);
