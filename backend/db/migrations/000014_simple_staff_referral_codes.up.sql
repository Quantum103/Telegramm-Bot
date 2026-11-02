ALTER TABLE staff_referral_codes
    RENAME COLUMN code TO legacy_code;

ALTER TABLE staff_referral_codes
    ALTER COLUMN legacy_code DROP NOT NULL;

ALTER TABLE staff_referral_codes
    ADD COLUMN code TEXT;

UPDATE staff_referral_codes
SET code = 'c_' || staff_id::TEXT;

ALTER TABLE staff_referral_codes
    ALTER COLUMN code SET NOT NULL;

ALTER TABLE staff_referral_codes
    ADD CONSTRAINT staff_referral_codes_short_code_key UNIQUE (code);

ALTER TABLE staff_referral_codes
    ADD CONSTRAINT staff_referral_codes_code_format_check
    CHECK (code ~ '^c_[1-9][0-9]*$');
