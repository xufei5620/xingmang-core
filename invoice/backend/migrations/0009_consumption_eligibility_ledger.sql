-- Consumption-based invoice eligibility.  This migration deliberately makes
-- every pre-existing payment lot historical/non-invoiceable.  The invoice
-- service was not in production before this cutover, nevertheless pending
-- reservations are released defensively so an upgrade cannot carry the old
-- "paid means invoiceable" rule across the boundary.

ALTER TABLE source_ingest_batches
    ADD COLUMN schema_version TEXT NOT NULL DEFAULT '2.0',
    ADD COLUMN stream_watermark_at TIMESTAMPTZ,
    ADD COLUMN source_cursor TEXT,
    ADD COLUMN scan_ceiling_at TIMESTAMPTZ,
    ADD COLUMN scan_ceiling_cursor TEXT,
    ADD COLUMN scan_cycle_id UUID,
    ADD COLUMN scan_complete BOOLEAN NOT NULL DEFAULT FALSE,
	ADD COLUMN scan_snapshot_id CHAR(64),
	ADD COLUMN scan_snapshot_row_count BIGINT,
    ADD CONSTRAINT source_ingest_batches_economic_metadata CHECK (
        (schema_version <> '3.0' AND stream_watermark_at IS NULL AND source_cursor IS NULL
         AND scan_ceiling_at IS NULL AND scan_ceiling_cursor IS NULL AND scan_cycle_id IS NULL
		 AND scan_snapshot_id IS NULL AND scan_snapshot_row_count IS NULL
		 AND scan_complete=FALSE)
        OR
        (schema_version='3.0' AND stream_id IN ('payments','usage','credits','balances')
         AND stream_watermark_at IS NOT NULL AND btrim(source_cursor)<>''
         AND char_length(source_cursor)<=256 AND scan_ceiling_at IS NOT NULL
         AND btrim(scan_ceiling_cursor)<>'' AND char_length(scan_ceiling_cursor)<=256
         AND scan_cycle_id IS NOT NULL AND stream_watermark_at<=scan_ceiling_at
		 AND scan_ceiling_at<=source_captured_at+interval '5 minutes'
		 AND ((stream_id='balances' AND scan_snapshot_id~'^[0-9a-f]{64}$'
		       AND scan_snapshot_row_count BETWEEN 0 AND 2000000)
		      OR (stream_id<>'balances' AND scan_snapshot_id IS NULL
		          AND scan_snapshot_row_count IS NULL)))
    );

CREATE UNIQUE INDEX source_ingest_batches_scan_cycle_sequence
    ON source_ingest_batches(source_instance_id,stream_id,scan_cycle_id,sequence)
    WHERE scan_cycle_id IS NOT NULL;

CREATE TABLE source_economic_scan_cycles (
    source_instance_id UUID NOT NULL,
    stream_id          TEXT NOT NULL CHECK (stream_id IN ('payments','usage','credits','balances')),
    scan_cycle_id      UUID NOT NULL,
    stream_watermark_at TIMESTAMPTZ NOT NULL,
    source_cursor      TEXT NOT NULL CHECK (btrim(source_cursor)<>'' AND char_length(source_cursor)<=256),
    scan_ceiling_at    TIMESTAMPTZ NOT NULL,
    scan_ceiling_cursor TEXT NOT NULL CHECK (btrim(scan_ceiling_cursor)<>'' AND char_length(scan_ceiling_cursor)<=256),
	scan_snapshot_id   CHAR(64),
	scan_snapshot_row_count BIGINT,
    first_sequence     BIGINT NOT NULL CHECK (first_sequence>0),
    last_sequence      BIGINT NOT NULL CHECK (last_sequence>=first_sequence),
    final_sequence     BIGINT CHECK (final_sequence IS NULL OR final_sequence>=first_sequence),
    cycle_status       TEXT NOT NULL DEFAULT 'receiving'
        CHECK (cycle_status IN ('receiving','processing','published','blocked')),
    published_at       TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY(source_instance_id,stream_id,scan_cycle_id),
    FOREIGN KEY(source_instance_id,stream_id)
        REFERENCES source_ingest_state(source_instance_id,stream_id) ON DELETE RESTRICT,
    CHECK (stream_watermark_at<=scan_ceiling_at),
	CHECK ((stream_id='balances' AND scan_snapshot_id~'^[0-9a-f]{64}$'
	        AND scan_snapshot_row_count BETWEEN 0 AND 2000000)
	       OR (stream_id<>'balances' AND scan_snapshot_id IS NULL AND scan_snapshot_row_count IS NULL)),
    CHECK ((cycle_status='published')=(published_at IS NOT NULL)),
    CHECK (final_sequence IS NULL OR final_sequence=last_sequence)
);

CREATE TABLE source_economic_scan_cycle_events (
    source_instance_id UUID NOT NULL,
    stream_id          TEXT NOT NULL,
    scan_cycle_id      UUID NOT NULL,
    event_id           UUID NOT NULL,
    batch_id           UUID NOT NULL,
    payload_hash       CHAR(64) NOT NULL CHECK (payload_hash~'^[0-9a-f]{64}$'),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY(source_instance_id,stream_id,scan_cycle_id,event_id),
    FOREIGN KEY(source_instance_id,stream_id,scan_cycle_id)
        REFERENCES source_economic_scan_cycles(source_instance_id,stream_id,scan_cycle_id) ON DELETE RESTRICT,
    FOREIGN KEY(source_instance_id,stream_id,event_id)
        REFERENCES source_ingest_events(source_instance_id,stream_id,event_id) ON DELETE RESTRICT,
    FOREIGN KEY(source_instance_id,stream_id,batch_id)
        REFERENCES source_ingest_batches(source_instance_id,stream_id,batch_id) ON DELETE RESTRICT
);

CREATE INDEX source_economic_scan_cycle_events_status_idx
    ON source_economic_scan_cycle_events(source_instance_id,stream_id,scan_cycle_id,event_id);

CREATE UNIQUE INDEX source_economic_one_active_scan_cycle
    ON source_economic_scan_cycles(source_instance_id,stream_id)
    WHERE cycle_status IN ('receiving','processing');

ALTER TABLE source_ingest_events
    DROP CONSTRAINT source_ingest_events_processing_status_check,
	DROP CONSTRAINT source_ingest_events_dependency_pair,
	ADD COLUMN catchup_key_hmac TEXT CHECK (catchup_key_hmac IS NULL OR catchup_key_hmac~'^h1:[0-9a-f]{64}$'),
	ADD CONSTRAINT source_ingest_events_processing_status_check CHECK (
		processing_status IN ('queued','processing','failed','waiting_dependency','parked_identity','processed','dead')
	),
	ADD CONSTRAINT source_ingest_events_dependency_pair CHECK (
		(processing_status IN ('waiting_dependency','parked_identity')
		 AND dependency_kind IN ('invoice_oidc_user','source_external_account','source_funding_lot','source_eligibility_cutover','source_cutover_manifest')
		 AND dependency_key_hmac~'^h1:[0-9a-f]{64}$')
		OR
		(processing_status NOT IN ('waiting_dependency','parked_identity')
		 AND dependency_kind IS NULL AND dependency_key_hmac IS NULL)
	);

DROP INDEX IF EXISTS source_ingest_events_dependency_wait_idx;
CREATE INDEX source_ingest_events_dependency_wait_idx
    ON source_ingest_events(dependency_kind,dependency_key_hmac,next_attempt_at,event_id)
    WHERE processing_status IN ('waiting_dependency','parked_identity');

UPDATE funding_lots fl
SET reserved_minor = fl.reserved_minor - released.amount_minor,
    updated_at = now()
FROM (
    SELECT ia.funding_lot_id, sum(ia.amount_minor)::bigint AS amount_minor
    FROM invoice_allocations ia
    JOIN invoice_requests ir ON ir.id = ia.invoice_request_id
    WHERE ia.allocation_state = 'reserved'
      AND ir.status IN ('pending_review','needs_changes','approved','manual_issuing')
    GROUP BY ia.funding_lot_id
) released
WHERE fl.id = released.funding_lot_id;

UPDATE invoice_allocations ia
SET allocation_state = 'released', updated_at = now()
FROM invoice_requests ir
WHERE ir.id = ia.invoice_request_id
  AND ia.allocation_state = 'reserved'
  AND ir.status IN ('pending_review','needs_changes','approved','manual_issuing');

UPDATE invoice_requests
SET status = 'rejected',
    review_note = 'consumption eligibility cutover invalidated this pre-cutover request',
    version = version + 1,
    updated_at = now()
WHERE status IN ('pending_review','needs_changes','approved','manual_issuing');

ALTER TABLE funding_lots
    ADD COLUMN eligibility_kind TEXT NOT NULL DEFAULT 'LEGACY_NON_INVOICEABLE',
    ADD COLUMN eligibility_cutover_at TIMESTAMPTZ,
    ADD COLUMN verified_cash_minor BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN consumed_cash_minor BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN refund_frozen BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN eligibility_revision BIGINT NOT NULL DEFAULT 1,
    ADD CONSTRAINT funding_lots_eligibility_kind_check CHECK (
        eligibility_kind IN (
            'LEGACY_NON_INVOICEABLE',
            'WALLET_CASH',
            'SUBSCRIPTION_CASH',
            'NON_CASH'
        )
    ),
    ADD CONSTRAINT funding_lots_consumed_cash_nonnegative CHECK (consumed_cash_minor >= 0),
	ADD CONSTRAINT funding_lots_verified_cash_nonnegative CHECK (verified_cash_minor >= 0),
	ADD CONSTRAINT funding_lots_verified_cash_candidate_bound CHECK (verified_cash_minor <= original_minor),
	ADD CONSTRAINT funding_lots_consumed_cash_verified_bound CHECK (consumed_cash_minor <= verified_cash_minor),
    ADD CONSTRAINT funding_lots_eligibility_revision_positive CHECK (eligibility_revision > 0),
    ADD CONSTRAINT funding_lots_cutover_pair CHECK (
        (eligibility_kind = 'LEGACY_NON_INVOICEABLE' AND eligibility_cutover_at IS NULL)
        OR
        (eligibility_kind <> 'LEGACY_NON_INVOICEABLE' AND eligibility_cutover_at IS NOT NULL)
    ),
    ADD CONSTRAINT funding_lots_wallet_subscription_verified CHECK (
        eligibility_kind NOT IN ('WALLET_CASH','SUBSCRIPTION_CASH')
        OR verification_state IN ('verified','frozen')
        OR consumed_cash_minor = 0
    );

-- Existing issued invoices remain represented for accounting consistency, but
-- their historical lots can never be selected for a new request.
UPDATE funding_lots
SET verified_cash_minor = issued_minor,
	consumed_cash_minor = issued_minor,
    eligibility_revision = eligibility_revision + 1,
    updated_at = now();

ALTER TABLE funding_lots
    ADD CONSTRAINT funding_lots_consumed_cash_allocation_bound CHECK (
		reserved_minor + issued_minor <= consumed_cash_minor
    );

CREATE TABLE source_account_eligibility_state (
    external_account_id       UUID PRIMARY KEY REFERENCES external_accounts(id) ON DELETE RESTRICT,
    source_instance_id        UUID NOT NULL REFERENCES source_instances(id) ON DELETE RESTRICT,
    cutover_at                TIMESTAMPTZ NOT NULL,
    unit_code                 TEXT NOT NULL CHECK (unit_code ~ '^[A-Z0-9_:-]{1,32}$'),
    cutover_balance_units     NUMERIC(78,0) NOT NULL CHECK (cutover_balance_units >= 0),
    cutover_manifest_hash     CHAR(64) NOT NULL CHECK (cutover_manifest_hash ~ '^[0-9a-f]{64}$'),
	bootstrap_kind            TEXT NOT NULL DEFAULT 'SIGNED_CUTOVER'
		CHECK (bootstrap_kind IN ('SIGNED_CUTOVER','POST_CUTOVER_CONSERVATIVE')),
    finalized_through         TIMESTAMPTZ NOT NULL,
    finalization_delay_seconds INTEGER NOT NULL DEFAULT 900
        CHECK (finalization_delay_seconds BETWEEN 60 AND 86400),
	catchup_key_hmac          TEXT CHECK (catchup_key_hmac IS NULL OR catchup_key_hmac~'^h1:[0-9a-f]{64}$'),
    eligibility_status        TEXT NOT NULL DEFAULT 'active'
		CHECK (eligibility_status IN ('active','syncing','frozen')),
    projection_version        BIGINT NOT NULL DEFAULT 1 CHECK (projection_version > 0),
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_instance_id, external_account_id),
    CHECK (finalized_through >= cutover_at)
);

-- One signed source-level manifest is the trust root for every account
-- baseline.  Per-user baseline rows may arrive later (for example after OIDC
-- binding), but cannot silently select a different cutover/configuration.
CREATE TABLE source_cutover_manifests (
    source_instance_id       UUID PRIMARY KEY REFERENCES source_instances(id) ON DELETE RESTRICT,
    manifest_hash            CHAR(64) NOT NULL UNIQUE CHECK (manifest_hash ~ '^[0-9a-f]{64}$'),
    cutover_at               TIMESTAMPTZ NOT NULL,
    database_clock           TIMESTAMPTZ NOT NULL,
    source_runtime_version   TEXT NOT NULL CHECK (btrim(source_runtime_version) <> '' AND char_length(source_runtime_version) <= 128),
	projection_contract      TEXT NOT NULL CHECK (btrim(projection_contract) <> '' AND char_length(projection_contract) <= 128),
    configuration_hash       CHAR(64) NOT NULL CHECK (configuration_hash ~ '^[0-9a-f]{64}$'),
    unit_code                TEXT NOT NULL CHECK (unit_code ~ '^[A-Z0-9_:-]{1,32}$'),
    payments_ceiling         TEXT NOT NULL CHECK (btrim(payments_ceiling) <> '' AND char_length(payments_ceiling) <= 256),
    usage_ceiling            TEXT NOT NULL CHECK (btrim(usage_ceiling) <> '' AND char_length(usage_ceiling) <= 256),
    credits_ceiling          TEXT NOT NULL CHECK (btrim(credits_ceiling) <> '' AND char_length(credits_ceiling) <= 256),
    balances_ceiling         TEXT NOT NULL CHECK (btrim(balances_ceiling) <> '' AND char_length(balances_ceiling) <= 256),
	baseline_snapshot_id      CHAR(64) NOT NULL CHECK (baseline_snapshot_id ~ '^[0-9a-f]{64}$'),
	baseline_snapshot_hash    CHAR(64) NOT NULL CHECK (baseline_snapshot_hash ~ '^[0-9a-f]{64}$'),
	baseline_row_count        BIGINT NOT NULL CHECK (baseline_row_count BETWEEN 0 AND 2000000),
    signing_key_id           TEXT NOT NULL CHECK (btrim(signing_key_id) <> '' AND char_length(signing_key_id) <= 128),
    accepted_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_instance_id,manifest_hash),
	CHECK (baseline_snapshot_id = baseline_snapshot_hash),
    CHECK (cutover_at <= database_clock + interval '5 minutes')
);

ALTER TABLE source_account_eligibility_state
    ADD CONSTRAINT source_account_cutover_manifest_fk
    FOREIGN KEY (source_instance_id,cutover_manifest_hash)
    REFERENCES source_cutover_manifests(source_instance_id,manifest_hash) ON DELETE RESTRICT;

CREATE FUNCTION enforce_account_eligibility_cutover_contract() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    manifest_cutover TIMESTAMPTZ;
    manifest_unit TEXT;
    account_source UUID;
BEGIN
	IF TG_OP='UPDATE' AND (
		NEW.external_account_id IS DISTINCT FROM OLD.external_account_id
		OR NEW.source_instance_id IS DISTINCT FROM OLD.source_instance_id
		OR NEW.cutover_at IS DISTINCT FROM OLD.cutover_at
		OR NEW.unit_code IS DISTINCT FROM OLD.unit_code
		OR NEW.cutover_balance_units IS DISTINCT FROM OLD.cutover_balance_units
		OR NEW.cutover_manifest_hash IS DISTINCT FROM OLD.cutover_manifest_hash
		OR NEW.bootstrap_kind IS DISTINCT FROM OLD.bootstrap_kind
		OR NEW.finalization_delay_seconds IS DISTINCT FROM OLD.finalization_delay_seconds
	) THEN
		RAISE EXCEPTION 'account eligibility trust boundary is immutable';
	END IF;
    SELECT cutover_at,unit_code INTO manifest_cutover,manifest_unit
    FROM source_cutover_manifests
    WHERE source_instance_id=NEW.source_instance_id AND manifest_hash=NEW.cutover_manifest_hash;
    SELECT source_instance_id INTO account_source FROM external_accounts WHERE id=NEW.external_account_id;
    IF manifest_cutover IS NULL OR account_source<>NEW.source_instance_id OR manifest_unit<>NEW.unit_code THEN
        RAISE EXCEPTION 'account eligibility state does not match source cutover manifest';
    END IF;
    IF (NEW.bootstrap_kind='SIGNED_CUTOVER' AND NEW.cutover_at<>manifest_cutover)
       OR (NEW.bootstrap_kind='POST_CUTOVER_CONSERVATIVE' AND NEW.cutover_at<=manifest_cutover) THEN
        RAISE EXCEPTION 'account eligibility cutover boundary is invalid';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER source_account_eligibility_cutover_guard
BEFORE INSERT OR UPDATE OF source_instance_id,external_account_id,cutover_at,unit_code,
	cutover_balance_units,cutover_manifest_hash,bootstrap_kind,finalization_delay_seconds
	ON source_account_eligibility_state
FOR EACH ROW EXECUTE FUNCTION enforce_account_eligibility_cutover_contract();

-- Completeness watermarks are source-stream facts, not per-account facts.
-- Empty signed batches advance these rows and therefore finalize accounts that
-- legitimately had no facts in a stream.
CREATE TABLE source_economic_stream_watermarks (
    source_instance_id UUID NOT NULL REFERENCES source_cutover_manifests(source_instance_id) ON DELETE RESTRICT,
    stream_kind        TEXT NOT NULL CHECK (stream_kind IN ('payments','usage','credits','balances')),
    watermark_at       TIMESTAMPTZ NOT NULL,
    source_sequence    BIGINT NOT NULL CHECK (source_sequence >= 0),
    source_cursor      TEXT NOT NULL CHECK (btrim(source_cursor) <> '' AND char_length(source_cursor) <= 256),
    configuration_hash CHAR(64) NOT NULL CHECK (configuration_hash ~ '^[0-9a-f]{64}$'),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (source_instance_id,stream_kind)
);

CREATE TABLE source_account_stream_watermarks (
    external_account_id UUID NOT NULL REFERENCES source_account_eligibility_state(external_account_id) ON DELETE RESTRICT,
	stream_kind         TEXT NOT NULL CHECK (stream_kind IN ('payments','usage','credits','balances')),
    watermark_at        TIMESTAMPTZ NOT NULL,
    source_sequence     BIGINT NOT NULL CHECK (source_sequence >= 0),
    source_cursor       TEXT NOT NULL CHECK (btrim(source_cursor) <> '' AND char_length(source_cursor) <= 256),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (external_account_id, stream_kind)
);

CREATE TABLE source_usage_events (
    id                    UUID PRIMARY KEY,
    source_instance_id    UUID NOT NULL REFERENCES source_instances(id) ON DELETE RESTRICT,
    external_account_id   UUID NOT NULL REFERENCES external_accounts(id) ON DELETE RESTRICT,
    external_event_id     TEXT NOT NULL CHECK (btrim(external_event_id) <> '' AND char_length(external_event_id) <= 256),
    external_usage_id     TEXT NOT NULL CHECK (btrim(external_usage_id) <> '' AND char_length(external_usage_id) <= 256),
    event_time            TIMESTAMPTZ NOT NULL,
    service_units         NUMERIC(78,0) NOT NULL CHECK (service_units > 0),
    unit_code             TEXT NOT NULL CHECK (unit_code ~ '^[A-Z0-9_:-]{1,32}$'),
    cutover_manifest_hash CHAR(64) NOT NULL CHECK (cutover_manifest_hash ~ '^[0-9a-f]{64}$'),
    configuration_hash    CHAR(64) NOT NULL CHECK (configuration_hash ~ '^[0-9a-f]{64}$'),
    billing_scope         TEXT NOT NULL CHECK (billing_scope = 'wallet'),
    causal_domain         TEXT CHECK (causal_domain IS NULL OR (btrim(causal_domain) <> '' AND char_length(causal_domain) <= 128)),
    causal_order          NUMERIC(78,0) CHECK (causal_order IS NULL OR causal_order >= 0),
    source_sequence       BIGINT NOT NULL CHECK (source_sequence > 0),
    source_cursor         TEXT NOT NULL CHECK (btrim(source_cursor) <> '' AND char_length(source_cursor) <= 256),
    stream_watermark_at   TIMESTAMPTZ NOT NULL,
    source_revision_hash  CHAR(64) NOT NULL CHECK (source_revision_hash ~ '^[0-9a-f]{64}$'),
    observed_at           TIMESTAMPTZ NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_instance_id, external_event_id),
    UNIQUE (source_instance_id, external_usage_id),
	FOREIGN KEY (source_instance_id,cutover_manifest_hash)
		REFERENCES source_cutover_manifests(source_instance_id,manifest_hash) ON DELETE RESTRICT,
    CHECK ((causal_domain IS NULL) = (causal_order IS NULL)),
    CHECK (event_time <= stream_watermark_at),
    CHECK (stream_watermark_at <= observed_at + interval '5 minutes')
);

CREATE INDEX source_usage_events_replay_idx
    ON source_usage_events(external_account_id,event_time,id);

CREATE TABLE source_credit_events (
    id                    UUID PRIMARY KEY,
    source_instance_id    UUID NOT NULL REFERENCES source_instances(id) ON DELETE RESTRICT,
    external_account_id   UUID NOT NULL REFERENCES external_accounts(id) ON DELETE RESTRICT,
    external_event_id     TEXT NOT NULL CHECK (btrim(external_event_id) <> '' AND char_length(external_event_id) <= 256),
    external_credit_id    TEXT NOT NULL CHECK (btrim(external_credit_id) <> '' AND char_length(external_credit_id) <= 256),
    event_time            TIMESTAMPTZ NOT NULL,
    service_units         NUMERIC(78,0) NOT NULL CHECK (service_units >= 0),
    unit_code             TEXT NOT NULL CHECK (unit_code ~ '^[A-Z0-9_:-]{1,32}$'),
    cutover_manifest_hash CHAR(64) NOT NULL CHECK (cutover_manifest_hash ~ '^[0-9a-f]{64}$'),
    configuration_hash    CHAR(64) NOT NULL CHECK (configuration_hash ~ '^[0-9a-f]{64}$'),
    credit_kind           TEXT NOT NULL CHECK (credit_kind IN (
        'LEGACY_NON_INVOICEABLE','BONUS','REBATE','ADMIN','UNKNOWN_POSITIVE'
    )),
    causal_domain         TEXT CHECK (causal_domain IS NULL OR (btrim(causal_domain) <> '' AND char_length(causal_domain) <= 128)),
    causal_order          NUMERIC(78,0) CHECK (causal_order IS NULL OR causal_order >= 0),
    source_sequence       BIGINT NOT NULL CHECK (source_sequence >= 0),
    source_cursor         TEXT NOT NULL CHECK (btrim(source_cursor) <> '' AND char_length(source_cursor) <= 256),
    stream_watermark_at   TIMESTAMPTZ NOT NULL,
    source_revision_hash  CHAR(64) NOT NULL CHECK (source_revision_hash ~ '^[0-9a-f]{64}$'),
    observed_at           TIMESTAMPTZ NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_instance_id, external_event_id),
    UNIQUE (source_instance_id, external_credit_id),
	FOREIGN KEY (source_instance_id,cutover_manifest_hash)
		REFERENCES source_cutover_manifests(source_instance_id,manifest_hash) ON DELETE RESTRICT,
    CHECK ((causal_domain IS NULL) = (causal_order IS NULL)),
    CHECK (event_time <= stream_watermark_at),
    CHECK (stream_watermark_at <= observed_at + interval '5 minutes'),
    CHECK (credit_kind <> 'LEGACY_NON_INVOICEABLE' OR source_sequence = 0)
);

CREATE INDEX source_credit_events_replay_idx
    ON source_credit_events(external_account_id,event_time,id);

CREATE TABLE funding_lot_consumption_state (
    funding_lot_id              UUID PRIMARY KEY REFERENCES funding_lots(id) ON DELETE RESTRICT,
    cash_service_units          NUMERIC(78,0) NOT NULL CHECK (cash_service_units >= 0),
    consumed_service_units      NUMERIC(78,0) NOT NULL DEFAULT 0 CHECK (consumed_service_units >= 0),
    cumulative_cash_numerator   NUMERIC(156,0) NOT NULL DEFAULT 0 CHECK (cumulative_cash_numerator >= 0),
    rounded_consumed_cash_minor BIGINT NOT NULL DEFAULT 0 CHECK (rounded_consumed_cash_minor >= 0),
    rounding_remainder_numerator NUMERIC(156,0) NOT NULL DEFAULT 0,
    causal_domain               TEXT CHECK (causal_domain IS NULL OR (btrim(causal_domain) <> '' AND char_length(causal_domain) <= 128)),
    causal_order                NUMERIC(78,0) CHECK (causal_order IS NULL OR causal_order >= 0),
    state_version               BIGINT NOT NULL DEFAULT 1 CHECK (state_version > 0),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (consumed_service_units <= cash_service_units),
    CHECK ((causal_domain IS NULL) = (causal_order IS NULL)),
    CHECK (
        (cash_service_units = 0 AND consumed_service_units = 0
         AND cumulative_cash_numerator = 0 AND rounded_consumed_cash_minor = 0
         AND rounding_remainder_numerator = 0)
        OR cash_service_units > 0
    )
);

CREATE TABLE consumption_allocations (
    id                    UUID PRIMARY KEY,
    usage_event_id        UUID NOT NULL REFERENCES source_usage_events(id) ON DELETE RESTRICT,
    funding_lot_id        UUID REFERENCES funding_lots(id) ON DELETE RESTRICT,
    credit_event_id       UUID REFERENCES source_credit_events(id) ON DELETE RESTRICT,
    allocation_order      INTEGER NOT NULL CHECK (allocation_order > 0),
    service_units         NUMERIC(78,0) NOT NULL CHECK (service_units > 0),
    cash_minor_delta      BIGINT NOT NULL DEFAULT 0 CHECK (cash_minor_delta >= 0),
    projection_version    BIGINT NOT NULL CHECK (projection_version > 0),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (usage_event_id, allocation_order),
    CHECK ((funding_lot_id IS NULL) <> (credit_event_id IS NULL)),
    CHECK ((credit_event_id IS NULL) OR cash_minor_delta = 0)
);

CREATE INDEX consumption_allocations_lot_idx
    ON consumption_allocations(funding_lot_id) WHERE funding_lot_id IS NOT NULL;
CREATE INDEX consumption_allocations_credit_idx
    ON consumption_allocations(credit_event_id) WHERE credit_event_id IS NOT NULL;

CREATE TABLE balance_reconciliation_checkpoints (
    id                    UUID PRIMARY KEY,
    source_instance_id    UUID NOT NULL REFERENCES source_instances(id) ON DELETE RESTRICT,
    external_account_id   UUID NOT NULL REFERENCES external_accounts(id) ON DELETE RESTRICT,
    external_event_id     TEXT NOT NULL CHECK (btrim(external_event_id) <> '' AND char_length(external_event_id) <= 256),
    checkpoint_id         TEXT NOT NULL CHECK (btrim(checkpoint_id) <> '' AND char_length(checkpoint_id) <= 256),
    checkpoint_kind       TEXT NOT NULL CHECK (checkpoint_kind IN ('cutover','reconciliation')),
	baseline_snapshot_id   CHAR(64) CHECK (baseline_snapshot_id IS NULL OR baseline_snapshot_id ~ '^[0-9a-f]{64}$'),
	baseline_member        BOOLEAN NOT NULL DEFAULT FALSE,
	source_snapshot_id     CHAR(64),
	snapshot_row_count     BIGINT,
    as_of                 TIMESTAMPTZ NOT NULL,
    balance_service_units NUMERIC(78,0) NOT NULL CHECK (balance_service_units >= 0),
	balance_negative      BOOLEAN NOT NULL DEFAULT FALSE,
    expected_service_units NUMERIC(78,0) CHECK (expected_service_units IS NULL OR expected_service_units >= 0),
    difference_service_units NUMERIC(78,0),
    unit_code             TEXT NOT NULL CHECK (unit_code ~ '^[A-Z0-9_:-]{1,32}$'),
    cutover_manifest_hash CHAR(64) NOT NULL CHECK (cutover_manifest_hash ~ '^[0-9a-f]{64}$'),
    configuration_hash    CHAR(64) NOT NULL CHECK (configuration_hash ~ '^[0-9a-f]{64}$'),
    reconciliation_status TEXT NOT NULL CHECK (reconciliation_status IN (
        'cutover_baseline','matched','positive_classified_non_cash','negative_frozen','pending_finalization'
    )),
    source_sequence       BIGINT NOT NULL CHECK (source_sequence > 0),
    source_cursor         TEXT NOT NULL CHECK (btrim(source_cursor) <> '' AND char_length(source_cursor) <= 256),
    stream_watermark_at   TIMESTAMPTZ NOT NULL,
    source_revision_hash  CHAR(64) NOT NULL CHECK (source_revision_hash ~ '^[0-9a-f]{64}$'),
    observed_at           TIMESTAMPTZ NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_instance_id, external_event_id),
    UNIQUE (source_instance_id, checkpoint_id),
	FOREIGN KEY (source_instance_id,cutover_manifest_hash)
		REFERENCES source_cutover_manifests(source_instance_id,manifest_hash) ON DELETE RESTRICT,
    CHECK (as_of <= stream_watermark_at),
    CHECK (stream_watermark_at <= observed_at + interval '5 minutes'),
	CHECK ((checkpoint_kind='cutover') = (baseline_snapshot_id IS NOT NULL)),
	CHECK (checkpoint_kind<>'cutover' OR baseline_member),
	CHECK ((source_snapshot_id IS NULL AND snapshot_row_count IS NULL)
	       OR (source_snapshot_id~'^[0-9a-f]{64}$' AND snapshot_row_count BETWEEN 0 AND 2000000)),
	CHECK (NOT balance_negative OR balance_service_units = 0),
    CHECK (
        (reconciliation_status IN ('cutover_baseline','pending_finalization')
         AND expected_service_units IS NULL AND difference_service_units IS NULL)
        OR
        (reconciliation_status NOT IN ('cutover_baseline','pending_finalization')
         AND expected_service_units IS NOT NULL AND difference_service_units IS NOT NULL)
    )
);

CREATE INDEX balance_reconciliation_account_idx
    ON balance_reconciliation_checkpoints(external_account_id,as_of,id);

CREATE TABLE balance_checkpoint_evaluations (
    id                       UUID PRIMARY KEY,
    checkpoint_id            UUID NOT NULL REFERENCES balance_reconciliation_checkpoints(id) ON DELETE RESTRICT,
    projection_version       BIGINT NOT NULL CHECK (projection_version > 0),
    expected_service_units   NUMERIC(78,0) NOT NULL CHECK (expected_service_units >= 0),
    difference_service_units NUMERIC(78,0) NOT NULL,
    evaluation_status        TEXT NOT NULL CHECK (evaluation_status IN (
        'matched','positive_classified_non_cash','negative_frozen','source_gap_frozen'
    )),
    evaluated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (checkpoint_id,projection_version)
);

CREATE INDEX balance_checkpoint_evaluations_latest_idx
    ON balance_checkpoint_evaluations(checkpoint_id,projection_version DESC);

CREATE TABLE eligibility_projection_jobs (
    external_account_id UUID PRIMARY KEY REFERENCES source_account_eligibility_state(external_account_id) ON DELETE RESTRICT,
    requested_through   TIMESTAMPTZ NOT NULL,
    status              TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','processing','failed')),
    attempt_count       INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_token         TEXT,
    lease_expires_at    TIMESTAMPTZ,
    last_error_code     TEXT CHECK (last_error_code IS NULL OR char_length(last_error_code) <= 128),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (status='processing' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR (status<>'processing' AND lease_token IS NULL AND lease_expires_at IS NULL)
    )
);

CREATE INDEX eligibility_projection_jobs_due_idx
    ON eligibility_projection_jobs(next_attempt_at,external_account_id)
    WHERE status IN ('queued','failed','processing');

CREATE TABLE eligibility_freezes (
    id                    UUID PRIMARY KEY,
    external_account_id   UUID NOT NULL REFERENCES external_accounts(id) ON DELETE RESTRICT,
    funding_lot_id        UUID REFERENCES funding_lots(id) ON DELETE RESTRICT,
    freeze_reason         TEXT NOT NULL CHECK (freeze_reason IN (
        'UNKNOWN_NEGATIVE_BALANCE',
        'LATE_FINALIZED_EVENT',
        'AMBIGUOUS_EVENT_ORDER',
        'EVENT_PAYLOAD_DRIFT',
        'UNIT_MISMATCH',
        'USAGE_EXCEEDS_LEDGER',
        'STREAM_WATERMARK_REGRESSION',
		'SOURCE_GAP',
		'SOURCE_REFUND'
    )),
    trigger_object_type   TEXT NOT NULL CHECK (btrim(trigger_object_type) <> '' AND char_length(trigger_object_type) <= 64),
    trigger_object_id     TEXT NOT NULL CHECK (btrim(trigger_object_id) <> '' AND char_length(trigger_object_id) <= 256),
    source_revision_hash  CHAR(64) CHECK (source_revision_hash IS NULL OR source_revision_hash ~ '^[0-9a-f]{64}$'),
    status                TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved')),
    opened_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at           TIMESTAMPTZ,
    resolved_by           UUID,
    resolution_evidence_hash CHAR(64),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (status = 'open' AND resolved_at IS NULL AND resolved_by IS NULL AND resolution_evidence_hash IS NULL)
        OR
        (status = 'resolved' AND resolved_at IS NOT NULL AND resolved_by IS NOT NULL
         AND resolution_evidence_hash ~ '^[0-9a-f]{64}$')
    )
);

CREATE UNIQUE INDEX eligibility_freezes_one_open_trigger
    ON eligibility_freezes(external_account_id,freeze_reason,trigger_object_type,trigger_object_id)
    WHERE status = 'open';
CREATE INDEX eligibility_freezes_open_account_idx
    ON eligibility_freezes(external_account_id,opened_at) WHERE status = 'open';

CREATE FUNCTION reject_source_eligibility_fact_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'source eligibility facts are immutable';
END;
$$;

CREATE TRIGGER source_usage_events_immutable
BEFORE UPDATE OR DELETE ON source_usage_events
FOR EACH ROW EXECUTE FUNCTION reject_source_eligibility_fact_mutation();

CREATE TRIGGER source_cutover_manifests_immutable
BEFORE UPDATE OR DELETE ON source_cutover_manifests
FOR EACH ROW EXECUTE FUNCTION reject_source_eligibility_fact_mutation();

CREATE TRIGGER source_ingest_batches_immutable
BEFORE UPDATE OR DELETE ON source_ingest_batches
FOR EACH ROW EXECUTE FUNCTION reject_source_eligibility_fact_mutation();

CREATE TRIGGER payment_candidate_reviews_immutable
BEFORE UPDATE OR DELETE ON payment_candidate_reviews
FOR EACH ROW EXECUTE FUNCTION reject_source_eligibility_fact_mutation();

CREATE FUNCTION enforce_scan_cycle_trust_immutability() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.source_instance_id IS DISTINCT FROM OLD.source_instance_id
	   OR NEW.stream_id IS DISTINCT FROM OLD.stream_id
	   OR NEW.scan_cycle_id IS DISTINCT FROM OLD.scan_cycle_id
	   OR NEW.scan_ceiling_at IS DISTINCT FROM OLD.scan_ceiling_at
	   OR NEW.scan_ceiling_cursor IS DISTINCT FROM OLD.scan_ceiling_cursor
	   OR NEW.scan_snapshot_id IS DISTINCT FROM OLD.scan_snapshot_id
	   OR NEW.scan_snapshot_row_count IS DISTINCT FROM OLD.scan_snapshot_row_count
	   OR NEW.first_sequence IS DISTINCT FROM OLD.first_sequence THEN
		RAISE EXCEPTION 'economic scan cycle trust metadata is immutable';
	END IF;
	RETURN NEW;
END;
$$;

CREATE TRIGGER source_economic_scan_cycles_trust_guard
BEFORE UPDATE ON source_economic_scan_cycles
FOR EACH ROW EXECUTE FUNCTION enforce_scan_cycle_trust_immutability();

CREATE TRIGGER source_credit_events_immutable
BEFORE UPDATE OR DELETE ON source_credit_events
FOR EACH ROW EXECUTE FUNCTION reject_source_eligibility_fact_mutation();

CREATE TRIGGER balance_reconciliation_checkpoints_immutable
BEFORE UPDATE OR DELETE ON balance_reconciliation_checkpoints
FOR EACH ROW EXECUTE FUNCTION reject_source_eligibility_fact_mutation();

CREATE FUNCTION enforce_source_eligibility_fact_contract() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    trusted_source UUID;
    trusted_unit TEXT;
    trusted_manifest CHAR(64);
    trusted_config CHAR(64);
BEGIN
    SELECT eas.source_instance_id,eas.unit_code,eas.cutover_manifest_hash,scm.configuration_hash
      INTO trusted_source,trusted_unit,trusted_manifest,trusted_config
    FROM source_account_eligibility_state eas
    JOIN source_cutover_manifests scm ON scm.source_instance_id=eas.source_instance_id
    WHERE eas.external_account_id=NEW.external_account_id;
    IF trusted_source IS NULL OR NEW.source_instance_id <> trusted_source
       OR NEW.unit_code <> trusted_unit
       OR NEW.cutover_manifest_hash <> trusted_manifest
       OR NEW.configuration_hash <> trusted_config THEN
        RAISE EXCEPTION 'source eligibility fact does not match trusted cutover contract';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER source_usage_events_contract_guard
BEFORE INSERT ON source_usage_events
FOR EACH ROW EXECUTE FUNCTION enforce_source_eligibility_fact_contract();

CREATE TRIGGER source_credit_events_contract_guard
BEFORE INSERT ON source_credit_events
FOR EACH ROW EXECUTE FUNCTION enforce_source_eligibility_fact_contract();

CREATE TRIGGER balance_reconciliation_checkpoints_contract_guard
BEFORE INSERT ON balance_reconciliation_checkpoints
FOR EACH ROW EXECUTE FUNCTION enforce_source_eligibility_fact_contract();

CREATE FUNCTION enforce_source_watermark_contract() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    trusted_config CHAR(64);
BEGIN
    SELECT configuration_hash INTO trusted_config FROM source_cutover_manifests
    WHERE source_instance_id=NEW.source_instance_id;
    IF trusted_config IS NULL OR NEW.configuration_hash <> trusted_config THEN
        RAISE EXCEPTION 'source watermark configuration does not match cutover manifest';
    END IF;
	IF TG_OP='UPDATE' AND (
		NEW.watermark_at < OLD.watermark_at OR NEW.source_sequence < OLD.source_sequence
		OR (NEW.watermark_at=OLD.watermark_at AND NEW.source_sequence=OLD.source_sequence
			AND NEW.source_cursor<>OLD.source_cursor)
	) THEN
		RAISE EXCEPTION 'source economic watermark cannot regress or drift';
	END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER source_economic_watermarks_contract_guard
BEFORE INSERT OR UPDATE ON source_economic_stream_watermarks
FOR EACH ROW EXECUTE FUNCTION enforce_source_watermark_contract();

-- Re-check the cross-column accounting invariant for every future update.  A
-- constraint trigger is deferrable so a single transaction may lower
-- reservations and consumed cash in either statement order, but cannot commit
-- a transiently hidden over-allocation.
CREATE FUNCTION enforce_funding_consumption_state() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    rounded_minor BIGINT;
	current_consumed BIGINT;
	current_verified BIGINT;
	current_reserved BIGINT;
	current_issued BIGINT;
BEGIN
	SELECT fl.consumed_cash_minor,fl.verified_cash_minor,fl.reserved_minor,fl.issued_minor,
		flcs.rounded_consumed_cash_minor
	INTO current_consumed,current_verified,current_reserved,current_issued,rounded_minor
	FROM funding_lots fl
	LEFT JOIN funding_lot_consumption_state flcs ON flcs.funding_lot_id=fl.id
	WHERE fl.id=NEW.id;
	IF rounded_minor IS NOT NULL AND rounded_minor <> current_consumed THEN
        RAISE EXCEPTION 'funding lot consumed cash mirror is inconsistent';
    END IF;
	IF current_reserved + current_issued > current_consumed
	   OR current_consumed > current_verified
	   OR current_verified > (SELECT original_minor FROM funding_lots WHERE id=NEW.id) THEN
        RAISE EXCEPTION 'funding lot consumed cash invariant violated';
    END IF;
    RETURN NEW;
END;
$$;

CREATE CONSTRAINT TRIGGER funding_lots_consumption_state_guard
AFTER INSERT OR UPDATE OF original_minor,verified_cash_minor,reserved_minor,issued_minor,consumed_cash_minor ON funding_lots
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_funding_consumption_state();

CREATE FUNCTION enforce_lot_consumption_mirror() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    lot_minor BIGINT;
    paid_minor BIGINT;
	current_cash_units NUMERIC(78,0);
	current_consumed_units NUMERIC(78,0);
	current_numerator NUMERIC(156,0);
	current_rounded BIGINT;
	current_remainder NUMERIC(156,0);
BEGIN
	SELECT fl.consumed_cash_minor,fl.verified_cash_minor,flcs.cash_service_units,
		flcs.consumed_service_units,flcs.cumulative_cash_numerator,
		flcs.rounded_consumed_cash_minor,flcs.rounding_remainder_numerator
	INTO lot_minor,paid_minor,current_cash_units,current_consumed_units,current_numerator,
		current_rounded,current_remainder
	FROM funding_lots fl JOIN funding_lot_consumption_state flcs ON flcs.funding_lot_id=fl.id
	WHERE fl.id=NEW.funding_lot_id FOR UPDATE OF fl,flcs;
	IF current_rounded <> lot_minor
	   OR current_rounded > paid_minor THEN
        RAISE EXCEPTION 'lot consumption state does not match funding lot';
    END IF;
	IF current_numerator <> paid_minor::numeric * current_consumed_units THEN
        RAISE EXCEPTION 'cumulative cash numerator is inconsistent';
    END IF;
	IF current_cash_units > 0 AND
	   current_remainder <>
		 current_numerator - current_rounded::numeric * current_cash_units THEN
        RAISE EXCEPTION 'cash rounding remainder is inconsistent';
    END IF;
	IF current_cash_units > 0 AND
	   current_rounded::numeric <>
		 floor((2*current_numerator + current_cash_units) /
			   (2*current_cash_units)) THEN
        RAISE EXCEPTION 'cash rounding is not exact half-up';
    END IF;
	IF current_cash_units > 0 AND current_consumed_units = current_cash_units
	   AND current_rounded <> paid_minor THEN
		RAISE EXCEPTION 'fully consumed lot must equal verified cash';
	END IF;
    RETURN NEW;
END;
$$;

CREATE CONSTRAINT TRIGGER funding_lot_consumption_mirror_guard
AFTER INSERT OR UPDATE ON funding_lot_consumption_state
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_lot_consumption_mirror();

CREATE INDEX funding_lots_invoice_eligibility_idx
    ON funding_lots(invoice_user_id,source_instance_id,completed_at,id)
    WHERE eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH')
      AND verification_state = 'verified' AND refund_frozen = FALSE;

CREATE OR REPLACE FUNCTION enforce_newapi_verified_dual_control() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  source_kind TEXT;
  decision_ok BOOLEAN;
BEGIN
  IF NEW.verification_state <> 'verified' THEN
    RETURN NEW;
  END IF;
  SELECT source_type INTO source_kind FROM source_instances WHERE id=NEW.source_instance_id;
  IF source_kind <> 'newapi' THEN
    RETURN NEW;
  END IF;
  SELECT EXISTS (
    SELECT 1 FROM payment_candidate_decisions d
    WHERE d.funding_lot_id=NEW.id AND d.state='approved'
      AND d.paid_minor=NEW.current_cap_minor
      AND d.paid_minor=NEW.verified_cash_minor
      AND d.currency=NEW.currency AND d.proposed_by<>d.approved_by
  ) INTO decision_ok;
  IF NOT decision_ok THEN
    RAISE EXCEPTION 'New API funding lot requires approved two-person payment decision matching verified cash';
  END IF;
  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS newapi_verified_dual_control_guard ON funding_lots;
CREATE CONSTRAINT TRIGGER newapi_verified_dual_control_guard
AFTER INSERT OR UPDATE OF verification_state,current_cap_minor,verified_cash_minor,currency ON funding_lots
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_newapi_verified_dual_control();

CREATE FUNCTION enforce_payment_candidate_decision_evidence_chain() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    proposed_ok BOOLEAN;
    approved_ok BOOLEAN;
BEGIN
    IF TG_OP='DELETE' THEN
        IF OLD.state <> 'proposed' THEN
            RAISE EXCEPTION 'approved payment decision is immutable';
        END IF;
        RETURN OLD;
    END IF;

    SELECT EXISTS(
        SELECT 1 FROM payment_candidate_reviews r
        WHERE r.id=NEW.proposed_review_id AND r.funding_lot_id=NEW.funding_lot_id
          AND r.review_action='verify_propose' AND r.admin_id=NEW.proposed_by
          AND r.paid_minor=NEW.paid_minor AND r.currency=NEW.currency
          AND r.evidence_hash=NEW.evidence_hash
    ) INTO proposed_ok;
    IF NOT proposed_ok THEN
        RAISE EXCEPTION 'payment proposal does not match immutable review evidence';
    END IF;

    IF TG_OP='INSERT' THEN
        IF NEW.state <> 'proposed' OR NEW.approved_by IS NOT NULL
           OR NEW.approved_review_id IS NOT NULL OR NEW.approved_at IS NOT NULL THEN
            RAISE EXCEPTION 'payment decision must begin as a proposal';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.state <> 'proposed' OR NEW.state <> 'approved'
       OR NEW.funding_lot_id IS DISTINCT FROM OLD.funding_lot_id
       OR NEW.paid_minor IS DISTINCT FROM OLD.paid_minor
       OR NEW.currency IS DISTINCT FROM OLD.currency
       OR NEW.evidence_hash IS DISTINCT FROM OLD.evidence_hash
       OR NEW.proposed_by IS DISTINCT FROM OLD.proposed_by
       OR NEW.proposed_review_id IS DISTINCT FROM OLD.proposed_review_id
       OR NEW.proposed_at IS DISTINCT FROM OLD.proposed_at THEN
        RAISE EXCEPTION 'payment decision permits only proposed to approved transition';
    END IF;
    SELECT EXISTS(
        SELECT 1 FROM payment_candidate_reviews r
        WHERE r.id=NEW.approved_review_id AND r.funding_lot_id=NEW.funding_lot_id
          AND r.review_action='verify_approve' AND r.admin_id=NEW.approved_by
          AND r.paid_minor=NEW.paid_minor AND r.currency=NEW.currency
          AND r.evidence_hash=NEW.evidence_hash AND r.admin_id<>NEW.proposed_by
    ) INTO approved_ok;
    IF NOT approved_ok THEN
        RAISE EXCEPTION 'payment approval does not match immutable second review evidence';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER payment_candidate_decision_evidence_chain_guard
BEFORE INSERT OR UPDATE OR DELETE ON payment_candidate_decisions
FOR EACH ROW EXECUTE FUNCTION enforce_payment_candidate_decision_evidence_chain();
