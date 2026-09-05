-- XM-INV-NEGATIVE-DEFICIT: a negative upstream balance now carries its magnitude.
--
-- The bridge reports a negative balance as zero service units plus
-- balance_negative=true; since this slice it also reports
-- deficit_service_units, the magnitude of that negative balance. Evidence
-- sealed before the bridge learned this (pending spools replayed byte for
-- byte, historical rows) arrives without it and is stored as NULL -- "unknown",
-- never zero. The zero-units invariant for a negative balance is unchanged.
--
-- A carry-forward proof restates its prior checkpoint's actual balance; it
-- must now restate the deficit as well, so the contract trigger compares it
-- (IS DISTINCT FROM: an unknown prior deficit carries forward as unknown).
ALTER TABLE public.balance_reconciliation_checkpoints
    ADD COLUMN deficit_service_units NUMERIC(78,0),
    ADD CONSTRAINT balance_reconciliation_checkpoints_deficit_nonnegative_check
        CHECK (deficit_service_units IS NULL OR deficit_service_units>=0),
    ADD CONSTRAINT balance_reconciliation_checkpoints_deficit_flag_check
        CHECK (deficit_service_units IS NULL OR (deficit_service_units>0)=balance_negative);

ALTER TABLE public.balance_carry_forward_proofs
    ADD COLUMN deficit_service_units NUMERIC(78,0),
    ADD CONSTRAINT balance_carry_forward_proofs_deficit_nonnegative_check
        CHECK (deficit_service_units IS NULL OR deficit_service_units>=0),
    ADD CONSTRAINT balance_carry_forward_proofs_deficit_flag_check
        CHECK (deficit_service_units IS NULL OR (deficit_service_units>0)=balance_negative);

CREATE OR REPLACE FUNCTION public.enforce_balance_carry_forward_proof_contract() RETURNS trigger
LANGUAGE plpgsql
SET search_path=pg_catalog,public
AS $$
DECLARE
    trusted_source UUID;
    trusted_catchup TEXT;
    trusted_status TEXT;
    cycle_snapshot CHAR(64);
    cycle_rows BIGINT;
    cycle_as_of TIMESTAMPTZ;
    cycle_watermark TIMESTAMPTZ;
    cycle_cursor TEXT;
    cycle_sequence BIGINT;
    batch_hash CHAR(64);
    batch_observed TIMESTAMPTZ;
    prior_source UUID;
    prior_account UUID;
    prior_as_of TIMESTAMPTZ;
    prior_sequence BIGINT;
    prior_balance NUMERIC(78,0);
    prior_negative BOOLEAN;
    prior_member BOOLEAN;
    prior_deficit NUMERIC(78,0);
BEGIN
    SELECT source_instance_id,catchup_key_hmac,eligibility_status
      INTO trusted_source,trusted_catchup,trusted_status
    FROM public.source_account_eligibility_state
    WHERE external_account_id=NEW.external_account_id;
    IF trusted_source IS NULL OR trusted_source<>NEW.source_instance_id
       OR trusted_catchup IS NOT NULL OR trusted_status='syncing' THEN
        RAISE EXCEPTION 'balance carry-forward account source is invalid';
    END IF;
    IF NEW.proof_key<>'carry-forward:'||NEW.scan_cycle_id::text||':'||lower(NEW.external_account_id::text) THEN
        RAISE EXCEPTION 'balance carry-forward proof key is invalid';
    END IF;

    SELECT c.scan_snapshot_id,c.scan_snapshot_row_count,c.scan_ceiling_at,
           c.stream_watermark_at,c.source_cursor,c.final_sequence,
           b.body_hash,b.source_captured_at
      INTO cycle_snapshot,cycle_rows,cycle_as_of,cycle_watermark,cycle_cursor,
           cycle_sequence,batch_hash,batch_observed
    FROM public.source_economic_scan_cycles c
    JOIN public.source_ingest_batches b
      ON b.source_instance_id=c.source_instance_id AND b.stream_id=c.stream_id
     AND b.scan_cycle_id=c.scan_cycle_id AND b.sequence=c.final_sequence
    WHERE c.source_instance_id=NEW.source_instance_id
      AND c.stream_id='balances' AND c.scan_cycle_id=NEW.scan_cycle_id
      AND c.cycle_status='published' AND b.batch_id=NEW.final_batch_id;
    IF cycle_snapshot IS NULL OR cycle_snapshot<>NEW.source_snapshot_id
       OR cycle_rows<>NEW.snapshot_row_count OR cycle_as_of<>NEW.as_of
       OR cycle_watermark<>NEW.stream_watermark_at OR cycle_cursor<>NEW.source_cursor
       OR cycle_sequence<>NEW.source_sequence OR batch_hash<>NEW.source_revision_hash
       OR batch_observed<>NEW.observed_at THEN
        RAISE EXCEPTION 'balance carry-forward signed cycle proof is invalid';
    END IF;

    SELECT source_instance_id,external_account_id,as_of,source_sequence,balance_service_units,
           balance_negative,baseline_member,deficit_service_units
      INTO prior_source,prior_account,prior_as_of,prior_sequence,prior_balance,prior_negative,prior_member,
           prior_deficit
    FROM public.balance_reconciliation_checkpoints WHERE id=NEW.prior_checkpoint_id;
    IF prior_source IS NULL OR prior_source<>NEW.source_instance_id
       OR prior_account<>NEW.external_account_id OR prior_as_of>NEW.as_of
       OR (prior_as_of=NEW.as_of AND prior_sequence>=NEW.source_sequence)
       OR prior_balance<>NEW.balance_service_units OR prior_negative<>NEW.balance_negative
       OR prior_member<>NEW.baseline_member
       OR prior_deficit IS DISTINCT FROM NEW.deficit_service_units THEN
        RAISE EXCEPTION 'balance carry-forward prior actual is invalid';
    END IF;
    IF EXISTS (
        SELECT 1 FROM public.balance_reconciliation_checkpoints newer
        WHERE newer.external_account_id=NEW.external_account_id
          AND (newer.as_of<NEW.as_of
               OR (newer.as_of=NEW.as_of AND newer.source_sequence<NEW.source_sequence))
          AND (newer.as_of>prior_as_of
               OR (newer.as_of=prior_as_of AND newer.source_sequence>prior_sequence)
               OR (newer.as_of=prior_as_of AND newer.source_sequence=prior_sequence
                   AND newer.id>NEW.prior_checkpoint_id))
    ) THEN
        RAISE EXCEPTION 'balance carry-forward prior actual is not latest';
    END IF;

    IF EXISTS (
        SELECT 1 FROM public.balance_reconciliation_checkpoints checkpoint
        JOIN public.source_economic_scan_cycle_events mapped
          ON mapped.source_instance_id=checkpoint.source_instance_id
         AND mapped.stream_id='balances'
         AND mapped.event_id::text=checkpoint.external_event_id
         AND mapped.payload_hash=checkpoint.source_revision_hash
        WHERE checkpoint.external_account_id=NEW.external_account_id
          AND mapped.scan_cycle_id=NEW.scan_cycle_id
    ) THEN
        RAISE EXCEPTION 'balance carry-forward cannot replace a real checkpoint';
    END IF;
    RETURN NEW;
END;
$$;
