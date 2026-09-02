DROP TABLE IF EXISTS core.staff_login_challenge;
DROP TABLE IF EXISTS core.staff_totp_recovery_code;

ALTER TABLE core.staff_session
    DROP COLUMN IF EXISTS amr,
    DROP COLUMN IF EXISTS mfa_at;

ALTER TABLE core.staff_account
    DROP COLUMN IF EXISTS must_enroll_totp,
    DROP COLUMN IF EXISTS totp_enrolled_at,
    DROP COLUMN IF EXISTS totp_secret_ref;
