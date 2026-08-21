CREATE TABLE source_instances (
    id                  UUID PRIMARY KEY,
    source_type         TEXT NOT NULL CHECK (source_type IN ('sub2api', 'newapi')),
    name                TEXT NOT NULL,
    runtime_version     TEXT NOT NULL DEFAULT '',
    enabled             BOOLEAN NOT NULL DEFAULT TRUE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE invoice_users (
    id                  UUID PRIMARY KEY,
    oidc_issuer         TEXT NOT NULL,
    oidc_subject        TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'disabled')),
    email_ciphertext    BYTEA,
    email_verified      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (oidc_issuer, oidc_subject)
);

CREATE TABLE external_accounts (
    id                  UUID PRIMARY KEY,
    invoice_user_id     UUID NOT NULL REFERENCES invoice_users(id),
    source_instance_id  UUID NOT NULL REFERENCES source_instances(id),
    external_user_id    TEXT NOT NULL,
    external_subject_hmac TEXT,
    binding_method      TEXT NOT NULL,
    binding_status      TEXT NOT NULL DEFAULT 'verified' CHECK (binding_status IN ('pending', 'verified', 'frozen', 'revoked')),
    verified_at         TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_instance_id, external_user_id),
    UNIQUE NULLS NOT DISTINCT (source_instance_id, external_subject_hmac)
);

CREATE TABLE invoice_profiles (
    id                  UUID PRIMARY KEY,
    invoice_user_id     UUID NOT NULL REFERENCES invoice_users(id),
    profile_type        TEXT NOT NULL CHECK (profile_type IN ('personal', 'enterprise')),
    title_ciphertext    BYTEA NOT NULL,
    tax_id_ciphertext   BYTEA,
    tax_id_hmac         TEXT,
    email_ciphertext    BYTEA NOT NULL,
    address_ciphertext  BYTEA,
    phone_ciphertext    BYTEA,
    bank_name_ciphertext BYTEA,
    bank_account_ciphertext BYTEA,
    is_default          BOOLEAN NOT NULL DEFAULT FALSE,
    revision            BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX invoice_profiles_one_default_per_user
    ON invoice_profiles(invoice_user_id) WHERE is_default;

CREATE TABLE source_events (
    id                  UUID PRIMARY KEY,
    source_instance_id  UUID NOT NULL REFERENCES source_instances(id),
    external_user_id    TEXT NOT NULL,
    event_kind          TEXT NOT NULL CHECK (event_kind IN ('payment', 'refund', 'identity', 'usage_summary', 'tombstone')),
    external_event_id   TEXT NOT NULL,
    source_status       TEXT NOT NULL,
    schema_version      TEXT NOT NULL,
    source_revision_hash TEXT NOT NULL,
    payload_ciphertext  BYTEA,
    source_created_at   TIMESTAMPTZ,
    source_updated_at   TIMESTAMPTZ,
    observed_at         TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_instance_id, event_kind, external_event_id, source_revision_hash)
);

CREATE TABLE funding_lots (
    id                  UUID PRIMARY KEY,
    invoice_user_id     UUID NOT NULL REFERENCES invoice_users(id),
    external_account_id UUID NOT NULL REFERENCES external_accounts(id),
    source_instance_id  UUID NOT NULL REFERENCES source_instances(id),
    source_event_id     UUID REFERENCES source_events(id),
    external_order_id   TEXT NOT NULL,
    trade_no            TEXT NOT NULL DEFAULT '',
    currency            CHAR(3) NOT NULL CHECK (currency = 'CNY'),
    original_minor      BIGINT NOT NULL CHECK (original_minor >= 0),
    current_cap_minor   BIGINT NOT NULL CHECK (current_cap_minor >= 0),
    reserved_minor      BIGINT NOT NULL DEFAULT 0 CHECK (reserved_minor >= 0),
    issued_minor        BIGINT NOT NULL DEFAULT 0 CHECK (issued_minor >= 0),
    verification_state  TEXT NOT NULL CHECK (verification_state IN ('pending', 'verified', 'frozen')),
    source_status       TEXT NOT NULL,
    source_revision_hash TEXT NOT NULL,
    completed_at        TIMESTAMPTZ,
    observed_at         TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_instance_id, external_order_id),
    CHECK (current_cap_minor <= original_minor),
    CHECK (reserved_minor + issued_minor <= original_minor)
);

CREATE INDEX funding_lots_user_source_idx
    ON funding_lots(invoice_user_id, source_instance_id, completed_at DESC);

CREATE TABLE invoice_requests (
    id                  UUID PRIMARY KEY,
    request_no          TEXT NOT NULL UNIQUE,
    invoice_user_id     UUID NOT NULL REFERENCES invoice_users(id),
    source_instance_id  UUID NOT NULL REFERENCES source_instances(id),
    profile_id          UUID NOT NULL REFERENCES invoice_profiles(id),
    profile_snapshot_ciphertext BYTEA NOT NULL,
    currency            CHAR(3) NOT NULL CHECK (currency = 'CNY'),
    issuer_code         TEXT NOT NULL DEFAULT 'default',
    service_item        TEXT NOT NULL DEFAULT '技术服务' CHECK (service_item = '技术服务'),
    amount_minor        BIGINT NOT NULL CHECK (amount_minor >= 20000),
    status              TEXT NOT NULL,
    idempotency_key     TEXT NOT NULL,
    review_note         TEXT NOT NULL DEFAULT '',
    reviewed_by         UUID,
    issued_by           UUID,
    version             BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    submitted_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (invoice_user_id, idempotency_key),
    CHECK (status IN (
        'pending_review', 'needs_changes', 'approved', 'rejected', 'user_cancelled',
        'manual_issuing', 'issued_awaiting_document', 'issued', 'refund_attention'
    ))
);

CREATE TABLE invoice_allocations (
    id                  UUID PRIMARY KEY,
    invoice_request_id  UUID NOT NULL REFERENCES invoice_requests(id),
    funding_lot_id      UUID NOT NULL REFERENCES funding_lots(id),
    amount_minor        BIGINT NOT NULL CHECK (amount_minor > 0),
    source_revision_hash TEXT NOT NULL,
    allocation_state    TEXT NOT NULL CHECK (allocation_state IN ('reserved', 'issued', 'released', 'refund_attention')),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (invoice_request_id, funding_lot_id)
);

CREATE TABLE invoice_documents (
    id                  UUID PRIMARY KEY,
    invoice_request_id  UUID NOT NULL REFERENCES invoice_requests(id),
    invoice_number      TEXT NOT NULL UNIQUE,
    object_key          TEXT NOT NULL,
    object_version      TEXT NOT NULL,
    sha256              CHAR(64) NOT NULL,
    size_bytes          BIGINT NOT NULL CHECK (size_bytes > 0),
    mime_type           TEXT NOT NULL CHECK (mime_type = 'application/pdf'),
    scan_status         TEXT NOT NULL CHECK (scan_status IN ('quarantined', 'clean', 'rejected')),
    uploaded_by         UUID NOT NULL,
    issued_at           TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (object_key, object_version)
);

CREATE TABLE email_outbox (
    id                  UUID PRIMARY KEY,
    invoice_request_id  UUID NOT NULL REFERENCES invoice_requests(id),
    document_id         UUID NOT NULL REFERENCES invoice_documents(id),
    recipient_hash      TEXT NOT NULL,
    template_version    TEXT NOT NULL,
    status              TEXT NOT NULL CHECK (status IN ('queued', 'sending', 'sent', 'failed', 'dead')),
    attempt_count       INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    provider_message_id TEXT,
    last_error_code     TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (invoice_request_id, document_id, recipient_hash, template_version)
);

CREATE TABLE audit_events (
    id                  UUID PRIMARY KEY,
    actor_type          TEXT NOT NULL,
    actor_id            TEXT NOT NULL,
    action              TEXT NOT NULL,
    object_type         TEXT NOT NULL,
    object_id           TEXT NOT NULL,
    request_id          TEXT NOT NULL,
    source_ip_hmac      TEXT,
    before_hash         TEXT,
    after_hash          TEXT,
    reason              TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX audit_events_object_idx ON audit_events(object_type, object_id, created_at DESC);
