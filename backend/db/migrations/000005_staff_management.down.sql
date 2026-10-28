DROP INDEX IF EXISTS staff_accounts_email_normalized_uq;
ALTER TABLE staff_accounts DROP COLUMN IF EXISTS must_change_password;
