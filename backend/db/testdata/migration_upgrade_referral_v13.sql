-- Simulate a legacy long referral code created while schema version 13 was active.
INSERT INTO staff_referral_codes (staff_id, code)
SELECT id, 'c_' || repeat('A', 43)
FROM staff_accounts
WHERE email = 'migration-upgrade-staff@example.test';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM staff_referral_codes
        WHERE legacy_code = 'c_' || repeat('A', 43)
    ) THEN
        RAISE EXCEPTION 'migration 000014 did not preserve the legacy referral code';
    END IF;
END $$;
