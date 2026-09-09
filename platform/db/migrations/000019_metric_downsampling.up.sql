-- XM-C-DS1: immutable raw metadata, UTC daily accumulators, receipts and
-- per-stream rollup state.  This migration is forward-only in deployment;
-- the down file exists solely for disposable/local resets.

CREATE SCHEMA IF NOT EXISTS ops;

ALTER TABLE ops.metric_observation_sample
    ADD COLUMN rollup_policy_version smallint NOT NULL DEFAULT 1,
    ADD COLUMN expected_interval_seconds integer NULL;

ALTER TABLE ops.metric_observation_sample
    ADD CONSTRAINT metric_observation_sample_policy_version_positive
        CHECK (rollup_policy_version > 0),
    ADD CONSTRAINT metric_observation_sample_expected_interval_positive
        CHECK (expected_interval_seconds IS NULL OR expected_interval_seconds > 0);

-- Existing callers may still write legacy rows with cadence NULL.  Runtime
-- identities receive append-only UPDATE/TRUNCATE ACLs in the later DBR/DS4
-- slice; DS1 deliberately creates no new routines or triggers so the schema
-- object inventory remains within the approved DBR1 boundary.

CREATE INDEX metric_observation_sample_environment_metric_id_idx
    ON ops.metric_observation_sample (environment, metric_key, id);
CREATE INDEX metric_observation_sample_synced_id_idx
    ON ops.metric_observation_sample (synced_at, id);

CREATE TABLE ops.metric_observation_daily (
    environment                 text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    metric_key                  text NOT NULL,
    source                      text NOT NULL,
    bucket_day                  date NOT NULL,
    bucket_timezone             text NOT NULL DEFAULT 'UTC',
    bucket_start_at             timestamptz NOT NULL,
    bucket_end_at               timestamptz NOT NULL,
    policy_version              smallint NOT NULL,
    policy_hash                 text NOT NULL,
    value_kind                  text NOT NULL,
    primary_kind                text NOT NULL,
    sum_mode                    text NOT NULL,
    unit                        text NOT NULL,
    scale                       bigint NOT NULL,
    currency                    text NULL,
    currency_set                jsonb NOT NULL DEFAULT '[]'::jsonb,
    first_numeric               numeric(39,0) NULL,
    last_numeric                numeric(39,0) NULL,
    min_numeric                 numeric(39,0) NULL,
    max_numeric                 numeric(39,0) NULL,
    sum_numeric                 numeric(39,0) NULL,
    numeric_count               bigint NOT NULL,
    first_full_value_json       jsonb NULL,
    last_full_value_json        jsonb NULL,
    last_partial_value_json     jsonb NULL,
    first_full_sample_id        bigint NULL,
    last_full_sample_id         bigint NULL,
    last_partial_sample_id      bigint NULL,
    first_synced_at             timestamptz NULL,
    last_synced_at              timestamptz NULL,
    first_observed_at           timestamptz NULL,
    last_observed_at            timestamptz NULL,
    first_watermark              text NULL,
    last_watermark               text NULL,
    sample_count                bigint NOT NULL,
    full_success_count          bigint NOT NULL,
    partial_success_count       bigint NOT NULL,
    failed_count                bigint NOT NULL,
    expected_slot_count         bigint NULL,
    covered_slot_count          bigint NULL,
    duplicate_count             bigint NOT NULL,
    coverage_ppm                integer NULL,
    coverage_unknown_reason     text NULL,
    error_counts                jsonb NOT NULL DEFAULT '{}'::jsonb,
    min_sample_id               bigint NOT NULL,
    max_sample_id               bigint NOT NULL,
    aggregated_at               timestamptz NOT NULL,
    PRIMARY KEY (environment, metric_key, source, bucket_day, policy_version),
    CONSTRAINT metric_observation_daily_bucket_utc
        CHECK (
            bucket_timezone = 'UTC'
            AND bucket_start_at = (bucket_day::timestamp AT TIME ZONE 'UTC')
            AND bucket_end_at = bucket_start_at + interval '1 day'
        ),
    CONSTRAINT metric_observation_daily_policy_shape
        CHECK (
            policy_version > 0
            AND scale > 0
            AND policy_hash ~ '^[0-9a-f]{64}$'
            AND value_kind IN ('gauge', 'daily_snapshot', 'additive_delta', 'document_status')
            AND primary_kind IN ('count', 'money_minor', 'fixed_point', 'none')
            AND sum_mode IN ('forbidden', 'additive')
            AND unit <> ''
        ),
    CONSTRAINT metric_observation_daily_count_shape
        CHECK (
            numeric_count >= 0
            AND sample_count >= 0
            AND full_success_count >= 0
            AND partial_success_count >= 0
            AND failed_count >= 0
            AND duplicate_count >= 0
            AND sample_count = full_success_count + partial_success_count + failed_count
            AND numeric_count <= full_success_count
            AND min_sample_id > 0
            AND max_sample_id >= min_sample_id
        ),
    CONSTRAINT metric_observation_daily_representative_shape
        CHECK (
            (first_full_sample_id IS NULL) = (first_synced_at IS NULL AND first_full_value_json IS NULL)
            AND (last_full_sample_id IS NULL) = (last_synced_at IS NULL AND last_full_value_json IS NULL)
            AND (last_partial_sample_id IS NULL) = (last_partial_value_json IS NULL)
        ),
    CONSTRAINT metric_observation_daily_coverage_shape
        CHECK (
            ((expected_slot_count IS NULL) = (covered_slot_count IS NULL))
            AND ((expected_slot_count IS NULL) = (coverage_ppm IS NULL))
            AND (expected_slot_count IS NULL OR (expected_slot_count > 0 AND covered_slot_count >= 0 AND covered_slot_count <= expected_slot_count AND coverage_ppm BETWEEN 0 AND 1000000))
            AND (expected_slot_count IS NOT NULL OR nullif(coverage_unknown_reason, '') IS NOT NULL)
        ),
    CONSTRAINT metric_observation_daily_currency_shape
        CHECK (
            jsonb_typeof(currency_set) = 'array'
            AND (
                primary_kind <> 'money_minor'
                OR jsonb_array_length(currency_set) = 0
                OR (jsonb_array_length(currency_set) = 1 AND currency IS NOT NULL AND currency = currency_set->>0)
                OR (jsonb_array_length(currency_set) > 1 AND currency IS NULL
                    AND first_numeric IS NULL AND last_numeric IS NULL
                    AND min_numeric IS NULL AND max_numeric IS NULL AND sum_numeric IS NULL)
            )
        ),
    CONSTRAINT metric_observation_daily_sum_shape
        CHECK ((sum_mode = 'forbidden' AND sum_numeric IS NULL) OR sum_mode = 'additive'),
    CONSTRAINT metric_observation_daily_error_shape
        CHECK (jsonb_typeof(error_counts) = 'object')
);

CREATE INDEX metric_observation_daily_lookup_idx
    ON ops.metric_observation_daily (environment, metric_key, bucket_day, source);
CREATE INDEX metric_observation_daily_availability_idx
    ON ops.metric_observation_daily (environment, metric_key, bucket_start_at);

CREATE TABLE ops.metric_rollup_receipt (
    rollup_name       text NOT NULL,
    sample_id         bigint NOT NULL,
    environment       text NOT NULL,
    metric_key        text NOT NULL,
    source            text NOT NULL,
    bucket_day        date NOT NULL,
    policy_version    smallint NOT NULL,
    policy_hash       text NOT NULL,
    processed_at      timestamptz NOT NULL,
    PRIMARY KEY (rollup_name, sample_id),
    CONSTRAINT metric_rollup_receipt_sample_positive CHECK (sample_id > 0),
    CONSTRAINT metric_rollup_receipt_policy_shape CHECK (policy_version > 0 AND policy_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT metric_rollup_receipt_identity_shape CHECK (rollup_name <> '' AND environment <> '' AND metric_key <> '' AND source <> '')
);

CREATE INDEX metric_rollup_receipt_stream_idx
    ON ops.metric_rollup_receipt (rollup_name, environment, metric_key, source, bucket_day);

CREATE TABLE ops.metric_rollup_state (
    rollup_name                 text NOT NULL,
    environment                 text NOT NULL,
    metric_key                  text NOT NULL,
    policy_hash                 text NOT NULL,
    highest_receipt_sample_id   bigint NOT NULL DEFAULT 0,
    processed_sample_count      bigint NOT NULL DEFAULT 0,
    last_attempt_started_at     timestamptz NULL,
    last_success_at             timestamptz NULL,
    last_failure_at             timestamptz NULL,
    status                      text NOT NULL DEFAULT 'idle',
    last_error_code             text NOT NULL DEFAULT '',
    updated_at                  timestamptz NOT NULL,
    PRIMARY KEY (rollup_name, environment, metric_key),
    CONSTRAINT metric_rollup_state_identity_shape CHECK (rollup_name <> '' AND environment <> '' AND metric_key <> ''),
    CONSTRAINT metric_rollup_state_policy_shape CHECK (policy_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT metric_rollup_state_counts_nonnegative CHECK (highest_receipt_sample_id >= 0 AND processed_sample_count >= 0),
    CONSTRAINT metric_rollup_state_status_allowed CHECK (status IN ('idle', 'running', 'success', 'failed'))
);

CREATE INDEX metric_rollup_state_policy_idx
    ON ops.metric_rollup_state (policy_hash, updated_at);
