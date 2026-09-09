-- XM-INV-ELIG-POLICY-START-ANCHOR (design section 3(D)): allow re-anchoring
-- an *already* POLICY_ANCHOR account's cutover_at back to the global policy
-- start. Migration 0016's guarded re-anchor exception only recognizes a
-- transition FROM a legacy bootstrap_kind (SIGNED_CUTOVER/POST_CUTOVER_REPLAY)
-- TO POLICY_ANCHOR -- a same-kind POLICY_ANCHOR-to-POLICY_ANCHOR re-anchor
-- (used by invoice-eligibility-repair --kind=policy-start-reanchor to fix the
-- production accounts bootstrapped before this slice shipped, whose
-- cutover_at was the triggering checkpoint's own as_of rather than the
-- policy start) is deliberately a separate GUC and, at the application layer,
-- a separate audit action -- reusing 0016's own invoice.policy_anchor_reanchor
-- GUC would blur which repair tool a given re-anchor came from, and 0016's
-- own semantics (a legacy trust boundary being replaced for the first time)
-- are conceptually distinct from this one (an already-anchored account's own
-- opening balance being recomputed over a wider window).
--
-- The guarded column set this exception permits to change is identical to
-- 0016's own (cutover_at, cutover_balance_units, bootstrap_kind,
-- finalized_through) -- bootstrap_kind stays 'POLICY_ANCHOR' on both sides
-- here, it is simply re-asserted so the UPDATE statement's column list can
-- stay uniform with 0016's guarded re-anchor UPDATE shape.
CREATE OR REPLACE FUNCTION enforce_account_eligibility_cutover_contract() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    manifest_cutover TIMESTAMPTZ;
    manifest_unit TEXT;
    account_source UUID;
    policy_start TIMESTAMPTZ;
    anchor_checkpoint_exists BOOLEAN;
    reanchoring BOOLEAN := FALSE;
    start_reanchoring BOOLEAN := FALSE;
BEGIN
    IF TG_OP='UPDATE' THEN
        reanchoring := COALESCE(current_setting('invoice.policy_anchor_reanchor', true), '') = 'on'
            AND OLD.bootstrap_kind IN ('SIGNED_CUTOVER','POST_CUTOVER_REPLAY')
            AND NEW.bootstrap_kind = 'POLICY_ANCHOR';
        start_reanchoring := COALESCE(current_setting('invoice.policy_anchor_start_reanchor', true), '') = 'on'
            AND OLD.bootstrap_kind = 'POLICY_ANCHOR'
            AND NEW.bootstrap_kind = 'POLICY_ANCHOR';
        IF reanchoring OR start_reanchoring THEN
            IF NEW.external_account_id IS DISTINCT FROM OLD.external_account_id
               OR NEW.source_instance_id IS DISTINCT FROM OLD.source_instance_id
               OR NEW.unit_code IS DISTINCT FROM OLD.unit_code
               OR NEW.cutover_manifest_hash IS DISTINCT FROM OLD.cutover_manifest_hash
               OR NEW.finalization_delay_seconds IS DISTINCT FROM OLD.finalization_delay_seconds THEN
                RAISE EXCEPTION 'policy anchor re-anchor may only change cutover_at, cutover_balance_units, bootstrap_kind and finalized_through';
            END IF;
        ELSIF (
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
    END IF;

    SELECT cutover_at,unit_code INTO manifest_cutover,manifest_unit
    FROM source_cutover_manifests
    WHERE source_instance_id=NEW.source_instance_id AND manifest_hash=NEW.cutover_manifest_hash;
    SELECT source_instance_id INTO account_source FROM external_accounts WHERE id=NEW.external_account_id;
    IF manifest_cutover IS NULL OR account_source<>NEW.source_instance_id OR manifest_unit<>NEW.unit_code THEN
        RAISE EXCEPTION 'account eligibility state does not match source cutover manifest';
    END IF;

    IF NEW.bootstrap_kind='POLICY_ANCHOR' THEN
        -- Unchanged from migration 0016: an anchor must be an observed (or,
        -- after this slice, derived-from-observed) checkpoint, never an
        -- invented number -- cutover_at/cutover_balance_units must exactly
        -- match a real reconciliation checkpoint row for this account.
        -- Applies identically to a fresh INSERT, 0016's legacy re-anchor
        -- UPDATE, and this migration's own POLICY_ANCHOR-to-POLICY_ANCHOR
        -- re-anchor UPDATE.
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
        IF NEW.cutover_at<policy_start
           OR NEW.cutover_at<=manifest_cutover
           OR NEW.cutover_at>now()
           OR NEW.finalized_through<>NEW.cutover_at
           OR NOT anchor_checkpoint_exists THEN
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
