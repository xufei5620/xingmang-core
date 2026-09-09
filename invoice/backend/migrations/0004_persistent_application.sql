-- A profile email is invoiceable only when it came from the verified OIDC
-- identity.  The application never trusts a browser supplied boolean.
ALTER TABLE invoice_profiles
    ADD COLUMN email_verified BOOLEAN NOT NULL DEFAULT FALSE;

-- Multiple delivery addresses may be verified over time.  The normalized
-- address is represented by a keyed blind index, not plaintext; ciphertext is
-- retained only for controlled administration/recovery.  Revocation is
-- explicit and immediately blocks future profile saves.
CREATE TABLE verified_emails (
    id                    UUID PRIMARY KEY,
    invoice_user_id       UUID NOT NULL REFERENCES invoice_users(id),
    email_ciphertext      BYTEA NOT NULL CHECK (octet_length(email_ciphertext) BETWEEN 16 AND 4096),
    normalized_email_hmac TEXT NOT NULL CHECK (normalized_email_hmac ~ '^h1:[0-9a-f]{64}$'),
    verification_source   TEXT NOT NULL CHECK (verification_source IN ('oidc', 'email_challenge')),
    verified_at           TIMESTAMPTZ NOT NULL,
    revoked_at            TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (invoice_user_id, normalized_email_hmac)
);

CREATE INDEX verified_emails_active_idx
    ON verified_emails(invoice_user_id, normalized_email_hmac)
    WHERE revoked_at IS NULL;

-- Preserve the issuer/settings revision that was active at the irreversible
-- manual-issue confirmation.  The encrypted payload includes issuer name,
-- issuer code and fixed service item and is never exposed by the user API.
ALTER TABLE invoice_requests
    ADD COLUMN issuer_setting_revision BIGINT,
    ADD COLUMN issue_snapshot_ciphertext BYTEA,
    ADD CONSTRAINT invoice_requests_issue_snapshot_pair CHECK (
        (issuer_setting_revision IS NULL AND issue_snapshot_ciphertext IS NULL)
        OR (issuer_setting_revision > 0 AND octet_length(issue_snapshot_ciphertext) > 0)
    ),
    ADD CONSTRAINT invoice_requests_issued_requires_snapshot CHECK (
        status NOT IN ('issued_awaiting_document', 'issued')
        OR (issuer_setting_revision > 0 AND octet_length(issue_snapshot_ciphertext) > 0)
    );

ALTER TABLE invoice_requests
    ADD CONSTRAINT invoice_requests_issue_snapshot_size CHECK (
        issue_snapshot_ciphertext IS NULL
        OR octet_length(issue_snapshot_ciphertext) BETWEEN 16 AND 8192
    );

CREATE FUNCTION enforce_invoice_request_immutability() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.invoice_user_id IS DISTINCT FROM OLD.invoice_user_id
       OR NEW.source_instance_id IS DISTINCT FROM OLD.source_instance_id
       OR NEW.profile_id IS DISTINCT FROM OLD.profile_id
       OR NEW.profile_snapshot_ciphertext IS DISTINCT FROM OLD.profile_snapshot_ciphertext
       OR NEW.currency IS DISTINCT FROM OLD.currency
       OR NEW.issuer_code IS DISTINCT FROM OLD.issuer_code
       OR NEW.service_item IS DISTINCT FROM OLD.service_item
       OR NEW.amount_minor IS DISTINCT FROM OLD.amount_minor
       OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key THEN
        RAISE EXCEPTION 'immutable invoice request fields cannot be changed';
    END IF;
    IF OLD.issue_snapshot_ciphertext IS NOT NULL
       AND (NEW.issue_snapshot_ciphertext IS DISTINCT FROM OLD.issue_snapshot_ciphertext
            OR NEW.issuer_setting_revision IS DISTINCT FROM OLD.issuer_setting_revision) THEN
        RAISE EXCEPTION 'issued invoice snapshot cannot be changed';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER invoice_requests_immutable_fields
BEFORE UPDATE ON invoice_requests
FOR EACH ROW EXECUTE FUNCTION enforce_invoice_request_immutability();

-- V1 permits one final PDF per invoice request.  This also makes an upload
-- retry idempotent instead of creating a second downloadable invoice.
ALTER TABLE invoice_documents
    ADD CONSTRAINT invoice_documents_one_per_request UNIQUE (invoice_request_id),
    ADD CONSTRAINT invoice_documents_sha256_hex CHECK (sha256 ~ '^[0-9a-fA-F]{64}$'),
    ADD CONSTRAINT invoice_documents_identity_lengths CHECK (
        char_length(invoice_number) BETWEEN 1 AND 128
        AND char_length(object_key) BETWEEN 1 AND 1024
        AND char_length(object_version) BETWEEN 1 AND 256
    );

-- Multiple worker replicas claim mail with SKIP LOCKED.  A lease allows work
-- abandoned by a crashed replica to be reclaimed safely; the token prevents a
-- stale worker from acknowledging a newer claim.
ALTER TABLE email_outbox
    ADD COLUMN lease_token TEXT,
    ADD COLUMN lease_expires_at TIMESTAMPTZ;

-- A pre-migration worker cannot safely retain a claim because it has no lease
-- token.  Return such work to the retry queue before enforcing the pair.
UPDATE email_outbox
SET status='failed', next_attempt_at=now(), updated_at=now()
WHERE status='sending';

ALTER TABLE email_outbox
    ADD CONSTRAINT email_outbox_recipient_hash_format CHECK (
        recipient_hash ~ '^h1:[0-9a-f]{64}$'
    ),
    ADD CONSTRAINT email_outbox_lease_pair CHECK (
        (status='sending' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR (status<>'sending' AND lease_token IS NULL AND lease_expires_at IS NULL)
    );

CREATE INDEX email_outbox_due_idx
    ON email_outbox(next_attempt_at, created_at)
    WHERE status IN ('queued', 'failed', 'sending');

CREATE INDEX invoice_requests_user_submitted_idx
    ON invoice_requests(invoice_user_id, submitted_at DESC, id DESC);

CREATE INDEX invoice_requests_admin_submitted_idx
    ON invoice_requests(submitted_at DESC, id DESC);

CREATE INDEX invoice_requests_admin_status_submitted_idx
    ON invoice_requests(status, submitted_at DESC, id DESC);

CREATE INDEX invoice_requests_admin_source_submitted_idx
    ON invoice_requests(source_instance_id, submitted_at DESC, id DESC);

CREATE INDEX invoice_allocations_lot_active_idx
    ON invoice_allocations(funding_lot_id, invoice_request_id)
    WHERE allocation_state IN ('reserved', 'issued', 'refund_attention');

CREATE INDEX funding_lots_newapi_candidate_idx
    ON funding_lots(verification_state, observed_at DESC, id DESC)
    WHERE verification_state IN ('pending', 'frozen');

CREATE TABLE payment_candidate_reviews (
    id                      UUID PRIMARY KEY,
    funding_lot_id          UUID NOT NULL REFERENCES funding_lots(id) ON DELETE RESTRICT,
    review_action           TEXT NOT NULL CHECK (review_action IN ('verify', 'reject', 'freeze')),
    admin_id                UUID NOT NULL,
    evidence_ref_ciphertext BYTEA NOT NULL CHECK (octet_length(evidence_ref_ciphertext) BETWEEN 16 AND 8192),
    review_reason_ciphertext BYTEA NOT NULL CHECK (octet_length(review_reason_ciphertext) BETWEEN 16 AND 4096),
    review_reason_hash      CHAR(64) NOT NULL CHECK (review_reason_hash ~ '^[0-9a-f]{64}$'),
    evidence_hash           CHAR(64) NOT NULL CHECK (evidence_hash ~ '^[0-9a-f]{64}$'),
    paid_minor              BIGINT CHECK (paid_minor IS NULL OR paid_minor > 0),
    currency                CHAR(3) CHECK (currency IS NULL OR currency = 'CNY'),
    request_id              TEXT NOT NULL CHECK (btrim(request_id) <> '' AND char_length(request_id) <= 512),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (funding_lot_id, review_action, evidence_hash)
);

CREATE INDEX payment_candidate_reviews_lot_idx
    ON payment_candidate_reviews(funding_lot_id, created_at DESC);

CREATE TABLE refund_cases (
    id                            UUID PRIMARY KEY,
    invoice_request_id            UUID NOT NULL REFERENCES invoice_requests(id) ON DELETE RESTRICT,
    funding_lot_id                UUID NOT NULL REFERENCES funding_lots(id) ON DELETE RESTRICT,
    source_revision_hash          TEXT NOT NULL CHECK (btrim(source_revision_hash) <> '' AND char_length(source_revision_hash) <= 256),
    observed_refund_minor         BIGINT NOT NULL CHECK (observed_refund_minor > 0),
    issued_exposure_minor         BIGINT NOT NULL CHECK (issued_exposure_minor > 0),
    observed_cap_minor            BIGINT NOT NULL CHECK (observed_cap_minor >= 0),
    status                        TEXT NOT NULL DEFAULT 'open'
                                  CHECK (status IN ('open','resolved_red_letter','resolved_no_action')),
    resolution_evidence_ciphertext BYTEA,
    resolution_evidence_hash      CHAR(64),
    resolution_note_ciphertext     BYTEA,
    resolution_note_hash           CHAR(64),
    resolved_by                    UUID,
    opened_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                     TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at                    TIMESTAMPTZ,
    CHECK (
        (status='open' AND resolution_evidence_ciphertext IS NULL
                       AND resolution_evidence_hash IS NULL
                       AND resolution_note_ciphertext IS NULL
                       AND resolution_note_hash IS NULL
                       AND resolved_by IS NULL AND resolved_at IS NULL)
        OR
        (status<>'open' AND octet_length(resolution_evidence_ciphertext) BETWEEN 16 AND 8192
                        AND resolution_evidence_hash ~ '^[0-9a-f]{64}$'
                        AND octet_length(resolution_note_ciphertext) BETWEEN 16 AND 8192
                        AND resolution_note_hash ~ '^[0-9a-f]{64}$'
                        AND resolved_by IS NOT NULL AND resolved_at IS NOT NULL)
    )
);

CREATE UNIQUE INDEX refund_cases_one_open_per_request_lot
    ON refund_cases(invoice_request_id,funding_lot_id) WHERE status='open';

CREATE INDEX refund_cases_admin_queue_idx
    ON refund_cases(status,opened_at DESC,id DESC);

-- Durable receiver-side progress.  Source agents never receive this database
-- DSN; their cursors live on their own volume and data flows outbound via mTLS.
CREATE TABLE source_ingest_state (
    source_instance_id UUID NOT NULL REFERENCES source_instances(id) ON DELETE CASCADE,
    stream_id          TEXT NOT NULL CHECK (stream_id ~ '^[a-z0-9][a-z0-9._-]{0,62}$'),
    sequence           BIGINT NOT NULL DEFAULT 0 CHECK (sequence >= 0),
    last_batch_hash    CHAR(64),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (source_instance_id, stream_id),
    CHECK ((sequence=0 AND last_batch_hash IS NULL)
           OR (sequence>0 AND last_batch_hash ~ '^[0-9a-f]{64}$'))
);

CREATE TABLE source_ingest_batches (
    source_instance_id UUID NOT NULL,
    stream_id          TEXT NOT NULL,
    batch_id           UUID NOT NULL,
    sequence           BIGINT NOT NULL CHECK (sequence > 0),
    body_hash          CHAR(64) NOT NULL CHECK (body_hash ~ '^[0-9a-f]{64}$'),
    previous_batch_hash CHAR(64),
    signing_key_id     TEXT NOT NULL CHECK (btrim(signing_key_id) <> '' AND char_length(signing_key_id) <= 128),
    record_count       INTEGER NOT NULL CHECK (record_count BETWEEN 0 AND 500),
    received_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (source_instance_id, stream_id, batch_id),
    UNIQUE (source_instance_id, stream_id, sequence),
    FOREIGN KEY (source_instance_id, stream_id)
        REFERENCES source_ingest_state(source_instance_id, stream_id) ON DELETE CASCADE,
    CHECK ((sequence=1 AND previous_batch_hash IS NULL)
           OR (sequence>1 AND previous_batch_hash ~ '^[0-9a-f]{64}$'))
);

CREATE TABLE source_ingest_events (
    source_instance_id UUID NOT NULL,
    stream_id          TEXT NOT NULL,
    event_id           UUID NOT NULL,
    first_batch_id     UUID NOT NULL,
    entity_type        TEXT NOT NULL CHECK (btrim(entity_type) <> '' AND char_length(entity_type) <= 128),
    operation          TEXT NOT NULL CHECK (operation IN ('upsert','tombstone')),
    payload_hash       CHAR(64) NOT NULL CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    payload_ciphertext BYTEA NOT NULL CHECK (octet_length(payload_ciphertext) BETWEEN 16 AND 1048576),
    observed_at        TIMESTAMPTZ NOT NULL,
    processing_status  TEXT NOT NULL DEFAULT 'queued'
                       CHECK (processing_status IN ('queued','processing','failed','processed','dead')),
    attempt_count      INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_token        TEXT,
    lease_expires_at   TIMESTAMPTZ,
    processed_at       TIMESTAMPTZ,
    processing_error   TEXT CHECK (processing_error IS NULL OR char_length(processing_error) <= 512),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (source_instance_id, stream_id, event_id),
    FOREIGN KEY (source_instance_id, stream_id, first_batch_id)
        REFERENCES source_ingest_batches(source_instance_id, stream_id, batch_id) ON DELETE RESTRICT,
    CHECK (
        (processing_status='processing' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR (processing_status<>'processing' AND lease_token IS NULL AND lease_expires_at IS NULL)
    ),
    CHECK ((processing_status='processed' AND processed_at IS NOT NULL)
           OR (processing_status<>'processed' AND processed_at IS NULL))
);

CREATE INDEX source_ingest_events_unprocessed_idx
    ON source_ingest_events(next_attempt_at, created_at, event_id)
    WHERE processing_status IN ('queued','failed','processing');
