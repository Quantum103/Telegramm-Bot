ALTER TABLE staff_referral_codes
    DROP CONSTRAINT staff_referral_codes_code_format_check;

ALTER TABLE staff_referral_codes
    DROP CONSTRAINT staff_referral_codes_short_code_key;

-- Восстанавливаем обязательное старое поле для кодов, созданных после миграции.
UPDATE staff_referral_codes
SET legacy_code = 'c_' || lpad(to_hex(staff_id), 43, '0')
WHERE legacy_code IS NULL;

ALTER TABLE staff_referral_codes
    ALTER COLUMN legacy_code SET NOT NULL;

ALTER TABLE staff_referral_codes
    DROP COLUMN code;

ALTER TABLE staff_referral_codes
    RENAME COLUMN legacy_code TO code;
