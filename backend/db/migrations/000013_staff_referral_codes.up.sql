CREATE TABLE staff_referral_codes (
    staff_id BIGINT PRIMARY KEY REFERENCES staff_accounts(id) ON DELETE RESTRICT,
    code TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (code ~ '^c_[A-Za-z0-9_-]{43}$')
);
