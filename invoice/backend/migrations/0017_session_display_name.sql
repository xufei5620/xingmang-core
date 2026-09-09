-- Platform login username display (XM-INV-OBS-BUNDLE follow-up): persist the
-- account's captured platform username on the session that carries it,
-- rather than on invoice_users -- CR-0003 makes identity strictly
-- per-platform-login, and one invoice_user can hold sessions from different
-- platforms with different captured names, so the session is the correct
-- owner, not the user record.
-- Usernames can themselves be email addresses (Sub2API accounts are
-- email-keyed), so this is encrypted at rest with the same
-- application-managed field-encryption keyring invoice_users.email_ciphertext
-- uses (securefields.Keyring, AES-256-GCM) -- never plaintext.
-- No blind index: this value is never queried by, only ever decrypted for,
-- its own session row.
ALTER TABLE auth_sessions
    ADD COLUMN display_name_ciphertext BYTEA CHECK (
        display_name_ciphertext IS NULL OR octet_length(display_name_ciphertext) BETWEEN 16 AND 4096
    );
