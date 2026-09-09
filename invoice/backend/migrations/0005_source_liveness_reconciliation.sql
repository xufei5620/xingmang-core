-- The configured source runtime is an approval boundary.  A receiver records
-- the exact runtime and agent versions carried by every signed batch so a
-- source upgrade cannot silently continue against an unreviewed contract.
ALTER TABLE source_instances
    ADD COLUMN runtime_version_revision BIGINT NOT NULL DEFAULT 1 CHECK (runtime_version_revision > 0);

ALTER TABLE source_ingest_state
    ADD COLUMN source_runtime_version TEXT,
    ADD COLUMN source_agent_version TEXT,
    ADD COLUMN projection_status TEXT,
    ADD COLUMN last_accepted_at TIMESTAMPTZ,
    ADD COLUMN last_nonempty_batch_at TIMESTAMPTZ,
    ADD CONSTRAINT source_ingest_state_runtime_pair CHECK (
        (last_accepted_at IS NULL AND source_runtime_version IS NULL AND source_agent_version IS NULL AND projection_status IS NULL)
        OR
        (last_accepted_at IS NOT NULL
         AND btrim(source_runtime_version) <> '' AND char_length(source_runtime_version) <= 128
         AND btrim(source_agent_version) <> '' AND char_length(source_agent_version) <= 128
         AND projection_status IS NOT NULL)
    ),
    ADD CONSTRAINT source_ingest_state_projection_status CHECK (
        projection_status IS NULL OR projection_status IN ('healthy','blocked','unknown')
    ),
    ADD CONSTRAINT source_ingest_state_nonempty_before_accepted CHECK (
        last_nonempty_batch_at IS NULL OR
        (last_accepted_at IS NOT NULL AND last_nonempty_batch_at <= last_accepted_at)
    );

ALTER TABLE source_ingest_batches
    ADD COLUMN source_runtime_version TEXT NOT NULL DEFAULT 'legacy-unrecorded',
    ADD COLUMN source_agent_version TEXT NOT NULL DEFAULT 'legacy-unrecorded',
    ADD COLUMN projection_status TEXT NOT NULL DEFAULT 'unknown'
        CHECK (projection_status IN ('healthy','blocked','unknown')),
    ADD COLUMN source_captured_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD CONSTRAINT source_ingest_batches_runtime_version CHECK (
        btrim(source_runtime_version) <> '' AND char_length(source_runtime_version) <= 128
    ),
    ADD CONSTRAINT source_ingest_batches_agent_version CHECK (
        btrim(source_agent_version) <> '' AND char_length(source_agent_version) <= 128
    );

ALTER TABLE external_accounts
    ADD COLUMN source_last_observed_at TIMESTAMPTZ NOT NULL DEFAULT 'epoch'::timestamptz,
    ADD COLUMN source_last_sequence BIGINT NOT NULL DEFAULT 0 CHECK (source_last_sequence >= 0);

ALTER TABLE funding_lots
    ADD COLUMN source_last_observed_at TIMESTAMPTZ NOT NULL DEFAULT 'epoch'::timestamptz,
    ADD COLUMN source_last_sequence BIGINT NOT NULL DEFAULT 0 CHECK (source_last_sequence >= 0);

ALTER TABLE source_events
    ADD COLUMN source_sequence BIGINT NOT NULL DEFAULT 0 CHECK (source_sequence >= 0);

CREATE INDEX source_ingest_state_freshness_idx
    ON source_ingest_state(last_accepted_at, source_instance_id, stream_id);

CREATE INDEX source_ingest_events_source_pending_idx
    ON source_ingest_events(source_instance_id, stream_id, processing_status)
    WHERE processing_status <> 'processed';

-- Source rows commonly arrive before the corresponding invoice OIDC user has
-- logged in.  This is a recoverable dependency, not a corrupt event.  Store a
-- keyed blind index only; waiting rows retry without consuming the dead-letter
-- attempt budget and do not make unrelated users or global readiness fail.
ALTER TABLE source_ingest_events
    DROP CONSTRAINT source_ingest_events_processing_status_check,
    DROP CONSTRAINT source_ingest_events_check,
    ADD COLUMN dependency_kind TEXT,
    ADD COLUMN dependency_key_hmac TEXT,
    ADD CONSTRAINT source_ingest_events_processing_status_check CHECK (
        processing_status IN ('queued','processing','failed','waiting_dependency','processed','dead')
    ),
    ADD CONSTRAINT source_ingest_events_dependency_pair CHECK (
        (processing_status='waiting_dependency'
         AND dependency_kind IN ('invoice_oidc_user','source_external_account','source_funding_lot')
         AND dependency_key_hmac ~ '^h1:[0-9a-f]{64}$')
        OR
        (processing_status<>'waiting_dependency'
         AND dependency_kind IS NULL AND dependency_key_hmac IS NULL)
    ),
    ADD CONSTRAINT source_ingest_events_lease_state_check CHECK (
        (processing_status='processing' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR (processing_status<>'processing' AND lease_token IS NULL AND lease_expires_at IS NULL)
    ),
    ADD CONSTRAINT source_ingest_events_processed_state_check CHECK (
        (processing_status='processed' AND processed_at IS NOT NULL)
        OR (processing_status<>'processed' AND processed_at IS NULL)
    );

CREATE INDEX source_ingest_events_dependency_wait_idx
    ON source_ingest_events(dependency_kind,dependency_key_hmac,next_attempt_at,event_id)
    WHERE processing_status='waiting_dependency';
