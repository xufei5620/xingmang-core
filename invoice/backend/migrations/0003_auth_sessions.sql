ALTER TABLE audit_events
    ADD COLUMN severity TEXT NOT NULL DEFAULT 'info'
    CHECK (severity IN ('info', 'notice', 'warning', 'critical'));

CREATE TABLE auth_sessions (
    id                  UUID PRIMARY KEY,
    family_id           UUID NOT NULL,
    invoice_user_id     UUID NOT NULL REFERENCES invoice_users(id) ON DELETE CASCADE,
    token_hash          CHAR(64) NOT NULL UNIQUE CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    csrf_hash           CHAR(64) NOT NULL CHECK (csrf_hash ~ '^[0-9a-f]{64}$'),
    provider_sid_hash   CHAR(64) CHECK (provider_sid_hash IS NULL OR provider_sid_hash ~ '^[0-9a-f]{64}$'),
    roles               TEXT[] NOT NULL DEFAULT '{}',
    acr                 TEXT NOT NULL DEFAULT '' CHECK (char_length(acr) <= 512),
    amr                 TEXT[] NOT NULL DEFAULT '{}',
    auth_time           TIMESTAMPTZ,
    mfa_at              TIMESTAMPTZ,
    client_ip_hmac      CHAR(64) CHECK (client_ip_hmac IS NULL OR client_ip_hmac ~ '^[0-9a-f]{64}$'),
    user_agent_hmac     CHAR(64) CHECK (user_agent_hmac IS NULL OR user_agent_hmac ~ '^[0-9a-f]{64}$'),
    created_at          TIMESTAMPTZ NOT NULL,
    last_seen_at        TIMESTAMPTZ NOT NULL,
    idle_expires_at     TIMESTAMPTZ NOT NULL,
    absolute_expires_at TIMESTAMPTZ NOT NULL,
    rotated_from        UUID UNIQUE REFERENCES auth_sessions(id),
    revoked_at          TIMESTAMPTZ,
    revoked_reason      TEXT NOT NULL DEFAULT '' CHECK (char_length(revoked_reason) <= 500),
    CHECK (cardinality(roles) <= 100 AND cardinality(amr) <= 32),
    CHECK (last_seen_at >= created_at),
    CHECK (idle_expires_at > created_at AND idle_expires_at <= absolute_expires_at),
    CHECK (absolute_expires_at > created_at AND absolute_expires_at <= created_at + interval '7 days'),
    CHECK (mfa_at IS NULL OR (auth_time IS NOT NULL AND mfa_at >= auth_time - interval '2 minutes' AND mfa_at <= created_at + interval '5 minutes')),
    CHECK ((revoked_at IS NULL AND revoked_reason = '') OR (revoked_at IS NOT NULL AND revoked_reason <> ''))
);

CREATE INDEX auth_sessions_active_token_idx
    ON auth_sessions(token_hash)
    WHERE revoked_at IS NULL;
CREATE INDEX auth_sessions_user_active_idx
    ON auth_sessions(invoice_user_id, absolute_expires_at DESC)
    WHERE revoked_at IS NULL;
CREATE INDEX auth_sessions_family_active_idx
    ON auth_sessions(family_id)
    WHERE revoked_at IS NULL;
CREATE INDEX auth_sessions_provider_sid_idx
    ON auth_sessions(provider_sid_hash)
    WHERE provider_sid_hash IS NOT NULL AND revoked_at IS NULL;

CREATE TABLE oidc_authorization_flows (
    state_hash              CHAR(64) PRIMARY KEY CHECK (state_hash ~ '^[0-9a-f]{64}$'),
    nonce_hash              CHAR(64) NOT NULL CHECK (nonce_hash ~ '^[0-9a-f]{64}$'),
    browser_binding_hash    CHAR(64) NOT NULL CHECK (browser_binding_hash ~ '^[0-9a-f]{64}$'),
    code_verifier_ciphertext BYTEA NOT NULL CHECK (octet_length(code_verifier_ciphertext) BETWEEN 32 AND 4096),
    code_verifier_key_version TEXT NOT NULL CHECK (btrim(code_verifier_key_version) <> '' AND char_length(code_verifier_key_version) <= 128),
    purpose                 TEXT NOT NULL CHECK (purpose IN ('login', 'admin_step_up', 'account_binding')),
    expected_identity_hash  CHAR(64) CHECK (expected_identity_hash IS NULL OR expected_identity_hash ~ '^[0-9a-f]{64}$'),
    existing_session_id     UUID REFERENCES auth_sessions(id) ON DELETE CASCADE,
    return_path             TEXT NOT NULL DEFAULT '' CHECK (char_length(return_path) <= 2048),
    created_at              TIMESTAMPTZ NOT NULL,
    expires_at              TIMESTAMPTZ NOT NULL,
    consumed_at             TIMESTAMPTZ,
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '20 minutes'),
    CHECK (consumed_at IS NULL OR consumed_at >= created_at),
    CHECK (
        (purpose = 'login' AND expected_identity_hash IS NULL AND existing_session_id IS NULL)
        OR
        (purpose IN ('admin_step_up', 'account_binding') AND expected_identity_hash IS NOT NULL AND existing_session_id IS NOT NULL)
    )
);

CREATE INDEX oidc_authorization_flows_expiry_idx
    ON oidc_authorization_flows(expires_at);

CREATE TABLE external_account_binding_proofs (
    id                   UUID PRIMARY KEY,
    invoice_user_id      UUID NOT NULL REFERENCES invoice_users(id) ON DELETE CASCADE,
    source_instance_id   UUID NOT NULL REFERENCES source_instances(id) ON DELETE CASCADE,
    external_user_id     TEXT NOT NULL CHECK (btrim(external_user_id) <> '' AND char_length(external_user_id) <= 512),
    proof_method         TEXT NOT NULL CHECK (proof_method IN ('source_signed_challenge', 'oidc_subject_projection', 'admin_attested')),
    challenge_hash       CHAR(64) NOT NULL CHECK (challenge_hash ~ '^[0-9a-f]{64}$'),
    evidence_hash        CHAR(64) CHECK (evidence_hash IS NULL OR evidence_hash ~ '^[0-9a-f]{64}$'),
    source_revision_hash TEXT NOT NULL DEFAULT '' CHECK (char_length(source_revision_hash) <= 256),
    status               TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'verified', 'rejected', 'expired')),
    request_id           TEXT NOT NULL CHECK (btrim(request_id) <> '' AND char_length(request_id) <= 512),
    verifier_actor_id    TEXT NOT NULL DEFAULT '' CHECK (char_length(verifier_actor_id) <= 512),
    rejection_reason     TEXT NOT NULL DEFAULT '' CHECK (char_length(rejection_reason) <= 500),
    created_at           TIMESTAMPTZ NOT NULL,
    expires_at           TIMESTAMPTZ NOT NULL,
    verified_at          TIMESTAMPTZ,
    consumed_at          TIMESTAMPTZ,
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '15 minutes'),
    CHECK (
        (status = 'pending' AND evidence_hash IS NULL AND verified_at IS NULL AND consumed_at IS NULL AND rejection_reason = '')
        OR
        (status = 'verified' AND evidence_hash IS NOT NULL AND verified_at IS NOT NULL AND consumed_at IS NOT NULL AND rejection_reason = '')
        OR
        (status IN ('rejected', 'expired') AND verified_at IS NULL AND consumed_at IS NOT NULL AND rejection_reason <> '')
    )
);

CREATE UNIQUE INDEX external_binding_proofs_one_pending_idx
    ON external_account_binding_proofs(invoice_user_id, source_instance_id, external_user_id)
    WHERE status = 'pending';
CREATE INDEX external_binding_proofs_expiry_idx
    ON external_account_binding_proofs(expires_at)
    WHERE status = 'pending';
