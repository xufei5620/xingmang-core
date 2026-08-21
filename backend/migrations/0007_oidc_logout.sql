CREATE TABLE oidc_backchannel_logout_events (
    id                      UUID PRIMARY KEY,
    issuer_hash             CHAR(64) NOT NULL CHECK (issuer_hash ~ '^[0-9a-f]{64}$'),
    jti_hash                CHAR(64) NOT NULL CHECK (jti_hash ~ '^[0-9a-f]{64}$'),
    sid_hash                CHAR(64) CHECK (sid_hash IS NULL OR sid_hash ~ '^[0-9a-f]{64}$'),
    subject_hash            CHAR(64) CHECK (subject_hash IS NULL OR subject_hash ~ '^[0-9a-f]{64}$'),
    token_issued_at         TIMESTAMPTZ NOT NULL,
    token_expires_at        TIMESTAMPTZ NOT NULL,
    received_at             TIMESTAMPTZ NOT NULL,
    revoked_session_count   INTEGER NOT NULL CHECK (revoked_session_count >= 0),
    request_id              TEXT NOT NULL CHECK (btrim(request_id) <> '' AND char_length(request_id) <= 512),
    source_ip_hmac          CHAR(64) CHECK (source_ip_hmac IS NULL OR source_ip_hmac ~ '^[0-9a-f]{64}$'),
    UNIQUE (issuer_hash, jti_hash),
    CHECK (token_expires_at > token_issued_at),
    CHECK (received_at >= token_issued_at - interval '5 minutes'),
    CHECK (sid_hash IS NOT NULL OR subject_hash IS NOT NULL)
);

CREATE INDEX oidc_backchannel_logout_received_idx
    ON oidc_backchannel_logout_events(received_at DESC);
