-- XM-INV-POLICY-ANCHOR: explicitly model a third eligibility bootstrap
-- origin, POLICY_ANCHOR, for a baseline-member account whose signed cutover
-- row is unavailable (parked indefinitely on identity binding) but a
-- post-policy-start reconciliation checkpoint already proves its opening
-- balance. This is deliberately not a reuse of POST_CUTOVER_REPLAY:
-- bootstrap_kind is part of the trust contract every trigger/query on this
-- table keys off (see enforce_account_eligibility_cutover_contract below),
-- so a new anchor origin must be modeled explicitly, not disguised as an
-- existing one with different semantics.
--
-- The cutover-boundary trigger becomes a DEFERRABLE CONSTRAINT TRIGGER here
-- (was a plain, immediate BEFORE trigger). This is required, not
-- cosmetic: POLICY_ANCHOR's own boundary check (condition (b) below) reads
-- balance_reconciliation_checkpoints to confirm the anchor checkpoint it
-- claims exists, but that table's own pre-existing
-- balance_reconciliation_checkpoints_contract_guard trigger
-- (enforce_source_eligibility_fact_contract, migration 0009) requires a
-- *trusted* source_account_eligibility_state row to already exist before it
-- will accept a checkpoint insert. A POLICY_ANCHOR bootstrap inserts both
-- rows in one transaction; under immediate (BEFORE) trigger semantics,
-- whichever row is inserted first is rejected by the *other* table's
-- trigger, because each side's validation depends on the other row already
-- existing -- state-then-checkpoint satisfies the checkpoint's own trigger
-- but not the new condition (b) below (checked too early, before the
-- checkpoint exists); checkpoint-then-state satisfies condition (b) but
-- fails the checkpoint table's own trigger (no trusted state yet). Deferring
-- this table's check to COMMIT (the same pattern migration 0009 already uses
-- for the analogous mutual cross-row check between funding_lots and
-- funding_lot_consumption_state -- see funding_lots_consumption_state_guard
-- / funding_lot_consumption_mirror_guard) resolves the ordering deadlock
-- without weakening either check: insert state then checkpoint (satisfying
-- the checkpoint table's own trigger), and by COMMIT time both rows exist
-- regardless of statement order, so this trigger's own read of the
-- checkpoint table sees it. The function never modifies NEW (pure
-- validation), so moving it from BEFORE to AFTER (required for a
-- CONSTRAINT TRIGGER) changes nothing about what it can enforce, only when.

ALTER TABLE source_account_eligibility_state
    DROP CONSTRAINT IF EXISTS source_account_eligibility_state_bootstrap_kind_check,
    ADD CONSTRAINT source_account_eligibility_state_bootstrap_kind_check CHECK (
        bootstrap_kind IN ('SIGNED_CUTOVER','POST_CUTOVER_REPLAY','POLICY_ANCHOR')
    );

CREATE OR REPLACE FUNCTION enforce_account_eligibility_cutover_contract() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    manifest_cutover TIMESTAMPTZ;
    manifest_unit TEXT;
    account_source UUID;
    policy_start TIMESTAMPTZ;
    anchor_checkpoint_exists BOOLEAN;
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
    IF NEW.bootstrap_kind='POLICY_ANCHOR' THEN
        SELECT eligibility_start_at INTO STRICT policy_start
        FROM invoice_eligibility_policy WHERE singleton_id=1;
        SELECT EXISTS (
            SELECT 1 FROM balance_reconciliation_checkpoints
            WHERE external_account_id=NEW.external_account_id
              AND checkpoint_kind='reconciliation'
              AND as_of=NEW.cutover_at
              AND balance_service_units=NEW.cutover_balance_units
              AND unit_code=NEW.unit_code
        ) INTO anchor_checkpoint_exists;
        IF NEW.cutover_at<policy_start OR NOT anchor_checkpoint_exists THEN
            RAISE EXCEPTION 'policy anchor boundary is invalid';
        END IF;
    ELSIF (NEW.bootstrap_kind='SIGNED_CUTOVER' AND NEW.cutover_at<>manifest_cutover)
       OR (NEW.bootstrap_kind='POST_CUTOVER_REPLAY'
           AND (NEW.cutover_at<>manifest_cutover OR NEW.cutover_balance_units<>0)) THEN
        RAISE EXCEPTION 'account eligibility cutover boundary is invalid';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS source_account_eligibility_cutover_guard ON source_account_eligibility_state;
CREATE CONSTRAINT TRIGGER source_account_eligibility_cutover_guard
AFTER INSERT OR UPDATE OF source_instance_id,external_account_id,cutover_at,unit_code,
    cutover_balance_units,cutover_manifest_hash,bootstrap_kind,finalization_delay_seconds
    ON source_account_eligibility_state
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_account_eligibility_cutover_contract();

-- EVENT_DEAD: a dead source_ingest_events row with no freeze yet can now be
-- frozen so it stops holding its scan cycle open indefinitely (design 2.5).
-- POLICY_ANCHOR_BLOCKED: an account whose bootstrap predates the policy
-- start and needs re-anchoring (design 2.4), but whose existing
-- source_account_stream_watermarks / eligibility_projection_jobs rows make
-- the DELETE-and-reinsert re-anchor migration impossible under those
-- tables' own ON DELETE RESTRICT foreign keys (see the handoff doc) --
-- frozen for manual review instead of forcing the migration through.
ALTER TABLE eligibility_freezes
    DROP CONSTRAINT IF EXISTS eligibility_freezes_freeze_reason_check,
    ADD CONSTRAINT eligibility_freezes_freeze_reason_check CHECK (freeze_reason IN (
        'UNKNOWN_NEGATIVE_BALANCE',
        'LATE_FINALIZED_EVENT',
        'AMBIGUOUS_EVENT_ORDER',
        'EVENT_PAYLOAD_DRIFT',
        'UNIT_MISMATCH',
        'USAGE_EXCEEDS_LEDGER',
        'STREAM_WATERMARK_REGRESSION',
        'SOURCE_GAP',
        'SOURCE_REFUND',
        'EVENT_DEAD',
        'POLICY_ANCHOR_BLOCKED'
    ));
