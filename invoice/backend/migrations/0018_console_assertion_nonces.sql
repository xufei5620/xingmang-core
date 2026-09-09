-- CR-0006 (XM-INV-CONSOLE-ASSERT): single-use replay protection for the
-- console-assertion admin login exchange endpoint. One row per successfully
-- verified assertion's nonce, inserted with ON CONFLICT DO NOTHING so a
-- replayed assertion's second exchange attempt observes zero rows affected
-- (see backend/internal/auth/postgres.go's ConsumeNonce). expires_at mirrors
-- the assertion's own exp claim (structurally capped at iat+5 minutes) and
-- exists only so the retention sweep below has something to compare against
-- -- an expired assertion can never pass signature/claim verification again,
-- so keeping the row past its own expiry serves no security purpose, only a
-- brief troubleshooting window (see cleanup comment).
CREATE TABLE console_assertion_nonces (
    nonce_hash  CHAR(64) PRIMARY KEY CHECK (nonce_hash ~ '^[0-9a-f]{64}$'),
    consumed_at TIMESTAMPTZ NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL
);

-- Retention: internal/auth's PostgresConsoleAssertionNonceStore.DeleteExpired
-- deletes rows with expires_at < now() - 1 hour, called from the existing
-- hourly auth-cleanup worker (cmd/api/runtime.go) alongside
-- oidc_authorization_flows/auth_sessions cleanup -- the extra hour past
-- expires_at is purely a troubleshooting window, not a security requirement.
CREATE INDEX console_assertion_nonces_expiry_idx
    ON console_assertion_nonces(expires_at);
