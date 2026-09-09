-- XM-C-AUD2：已提交审计归档段、预 Put receipt journal 与可重建 catalog。
--
-- 这张迁移只增加归档元数据；不删除、更新或压缩 audit.audit_event。
-- 对象正文/凭据永不进入 PostgreSQL。对象的 exact VersionID、保护元数据与
-- 摘要由冻结 wire 保存为 bytea/jsonb，RecoveryIndex 才是 terminal commit。

CREATE SCHEMA IF NOT EXISTS audit;

CREATE TABLE audit.archive_segment (
    id                         uuid PRIMARY KEY,
    format_version             smallint NOT NULL CHECK (format_version = 1),
    from_sequence              bigint NOT NULL CHECK (from_sequence >= 1),
    to_sequence                bigint NOT NULL CHECK (to_sequence >= from_sequence),
    row_count                  bigint NOT NULL CHECK (row_count > 0),
    first_prev_hash            text NOT NULL CHECK (first_prev_hash ~ '^[0-9a-f]{64}$'),
    last_event_hash            text NOT NULL CHECK (last_event_hash ~ '^[0-9a-f]{64}$'),
    canonical_version_counts   jsonb NOT NULL CHECK (jsonb_typeof(canonical_version_counts) = 'array'),
    environment_counts         jsonb NOT NULL CHECK (jsonb_typeof(environment_counts) = 'object'),
    payload_object_key         text NOT NULL CHECK (
        payload_object_key ~ '^audit/v1/payload/seq-[0-9]{19}-[0-9]{19}-[0-9a-f]{64}\.ndjson$'
        AND right(payload_object_key, 71) = payload_sha256 || '.ndjson'
        AND payload_object_key !~ '[[:cntrl:]]'
    ),
    payload_version_id         text NOT NULL CHECK (
        btrim(payload_version_id) <> '' AND lower(payload_version_id) <> 'latest'
        AND payload_version_id !~ '[[:cntrl:]]'
    ),
    payload_sha256             text NOT NULL CHECK (payload_sha256 ~ '^[0-9a-f]{64}$'),
    payload_size_bytes         bigint NOT NULL CHECK (payload_size_bytes >= 0),
    projections                jsonb NOT NULL CHECK (jsonb_typeof(projections) = 'array'),
    manifest_object_key        text NOT NULL CHECK (
        manifest_object_key ~ '^audit/v1/manifest/seq-[0-9]{19}-[0-9]{19}-[0-9a-f]{64}\.json$'
        AND right(manifest_object_key, 69) = manifest_sha256 || '.json'
        AND manifest_object_key !~ '[[:cntrl:]]'
    ),
    manifest_version_id        text NOT NULL CHECK (
        btrim(manifest_version_id) <> '' AND lower(manifest_version_id) <> 'latest'
        AND manifest_version_id !~ '[[:cntrl:]]'
    ),
    manifest_sha256            text NOT NULL CHECK (manifest_sha256 ~ '^[0-9a-f]{64}$'),
    manifest_signature         text NOT NULL CHECK (btrim(manifest_signature) <> ''),
    manifest_key_id            text NOT NULL CHECK (btrim(manifest_key_id) <> ''),
    -- Deliberately no FK to audit.chain_root: an isolated database-loss rebuild
    -- may have no source root rows; the signed manifest/checkpoint is authoritative.
    chain_root_id              uuid NOT NULL,
    chain_root_hash            text NOT NULL CHECK (chain_root_hash ~ '^[0-9a-f]{64}$'),
    checkpoint_sha256          text NOT NULL CHECK (checkpoint_sha256 ~ '^[0-9a-f]{64}$'),
    recovery_generation        bigint NOT NULL CHECK (recovery_generation >= 1),
    committed_at               timestamptz NOT NULL,
    verified_at                timestamptz NOT NULL,
    CONSTRAINT archive_segment_range_count CHECK (to_sequence - from_sequence + 1 = row_count),
    CONSTRAINT archive_segment_object_keys_are_archive_scoped CHECK (
        payload_object_key LIKE 'audit/v1/%' AND manifest_object_key LIKE 'audit/v1/%'
    )
);

CREATE UNIQUE INDEX archive_segment_from_sequence_key
    ON audit.archive_segment (from_sequence);
CREATE UNIQUE INDEX archive_segment_to_sequence_key
    ON audit.archive_segment (to_sequence);
CREATE UNIQUE INDEX archive_segment_payload_sha256_key
    ON audit.archive_segment (payload_sha256);
CREATE UNIQUE INDEX archive_segment_manifest_sha256_key
    ON audit.archive_segment (manifest_sha256);
CREATE UNIQUE INDEX archive_segment_range_key
    ON audit.archive_segment (from_sequence, to_sequence);
CREATE INDEX archive_segment_committed_idx
    ON audit.archive_segment (to_sequence DESC);

-- The intent is durable before the first external Put. Canonical bytes are stored only
-- as an opaque, digest-pinned journal record; the provider token is never logged.
CREATE TABLE audit.archive_operation_intent (
    operation_id                 uuid PRIMARY KEY,
    approval_envelope_sha256     text NOT NULL CHECK (approval_envelope_sha256 ~ '^[0-9a-f]{64}$'),
    deterministic_bytes_digest   text NOT NULL CHECK (deterministic_bytes_digest ~ '^[0-9a-f]{64}$'),
    canonical_intent_bytes      bytea NOT NULL CHECK (octet_length(canonical_intent_bytes) > 0),
    created_at                   timestamptz NOT NULL
);

CREATE TABLE audit.archive_put_receipt (
    operation_id             uuid NOT NULL REFERENCES audit.archive_operation_intent (operation_id) ON DELETE RESTRICT,
    ordinal                  integer NOT NULL CHECK (ordinal >= 0),
    object_version_bytes     bytea NOT NULL CHECK (octet_length(object_version_bytes) > 0),
    object_version_sha256    text NOT NULL CHECK (object_version_sha256 ~ '^[0-9a-f]{64}$'),
    recorded_at              timestamptz NOT NULL,
    PRIMARY KEY (operation_id, ordinal)
);

CREATE TABLE audit.archive_terminal_receipt (
    operation_id             uuid PRIMARY KEY REFERENCES audit.archive_operation_intent (operation_id) ON DELETE RESTRICT,
    signed_result_bytes      bytea NOT NULL CHECK (octet_length(signed_result_bytes) > 0),
    terminal_result_digest   text NOT NULL CHECK (terminal_result_digest ~ '^[0-9a-f]{64}$'),
    optional_artifact_ref_bytes bytea,
    recorded_at              timestamptz NOT NULL
);

CREATE FUNCTION audit.reject_archive_catalog_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'audit archive metadata is append-only'
        USING ERRCODE = '55000';
END;
$$;

CREATE RULE archive_segment_no_update AS ON UPDATE TO audit.archive_segment DO INSTEAD NOTHING;
CREATE RULE archive_segment_no_delete AS ON DELETE TO audit.archive_segment DO INSTEAD NOTHING;
CREATE TRIGGER archive_segment_no_truncate
    BEFORE TRUNCATE ON audit.archive_segment
    FOR EACH STATEMENT EXECUTE FUNCTION audit.reject_archive_catalog_mutation();

CREATE RULE archive_operation_intent_no_update AS ON UPDATE TO audit.archive_operation_intent DO INSTEAD NOTHING;
CREATE RULE archive_operation_intent_no_delete AS ON DELETE TO audit.archive_operation_intent DO INSTEAD NOTHING;
CREATE TRIGGER archive_operation_intent_no_truncate
    BEFORE TRUNCATE ON audit.archive_operation_intent
    FOR EACH STATEMENT EXECUTE FUNCTION audit.reject_archive_catalog_mutation();

CREATE RULE archive_put_receipt_no_update AS ON UPDATE TO audit.archive_put_receipt DO INSTEAD NOTHING;
CREATE RULE archive_put_receipt_no_delete AS ON DELETE TO audit.archive_put_receipt DO INSTEAD NOTHING;
CREATE TRIGGER archive_put_receipt_no_truncate
    BEFORE TRUNCATE ON audit.archive_put_receipt
    FOR EACH STATEMENT EXECUTE FUNCTION audit.reject_archive_catalog_mutation();

CREATE RULE archive_terminal_receipt_no_update AS ON UPDATE TO audit.archive_terminal_receipt DO INSTEAD NOTHING;
CREATE RULE archive_terminal_receipt_no_delete AS ON DELETE TO audit.archive_terminal_receipt DO INSTEAD NOTHING;
CREATE TRIGGER archive_terminal_receipt_no_truncate
    BEFORE TRUNCATE ON audit.archive_terminal_receipt
    FOR EACH STATEMENT EXECUTE FUNCTION audit.reject_archive_catalog_mutation();
