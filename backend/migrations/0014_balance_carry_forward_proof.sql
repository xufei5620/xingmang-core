-- Delta balance snapshots omit accounts whose signed actual balance did not
-- change. Preserve that unchanged proof separately from source checkpoints so
-- receiver-derived evidence can never masquerade as a source event.
CREATE TABLE public.balance_carry_forward_proofs (
    id                       UUID PRIMARY KEY,
    source_instance_id       UUID NOT NULL REFERENCES public.source_instances(id) ON DELETE RESTRICT,
    external_account_id      UUID NOT NULL REFERENCES public.external_accounts(id) ON DELETE RESTRICT,
    proof_key                TEXT NOT NULL CHECK (btrim(proof_key)<>'' AND char_length(proof_key)<=256),
    prior_checkpoint_id      UUID NOT NULL REFERENCES public.balance_reconciliation_checkpoints(id) ON DELETE RESTRICT,
    stream_kind              TEXT NOT NULL DEFAULT 'balances' CHECK (stream_kind='balances'),
    scan_cycle_id            UUID NOT NULL,
    final_batch_id           UUID NOT NULL,
    as_of                    TIMESTAMPTZ NOT NULL,
    balance_service_units    NUMERIC(78,0) NOT NULL CHECK (balance_service_units>=0),
    balance_negative         BOOLEAN NOT NULL DEFAULT FALSE,
    baseline_member          BOOLEAN NOT NULL,
    source_snapshot_id       CHAR(64) NOT NULL CHECK (source_snapshot_id~'^[0-9a-f]{64}$'),
    snapshot_row_count       BIGINT NOT NULL CHECK (snapshot_row_count BETWEEN 0 AND 2000000),
    source_sequence          BIGINT NOT NULL CHECK (source_sequence>0),
    source_cursor            TEXT NOT NULL CHECK (btrim(source_cursor)<>'' AND char_length(source_cursor)<=256),
    stream_watermark_at      TIMESTAMPTZ NOT NULL,
    source_revision_hash     CHAR(64) NOT NULL CHECK (source_revision_hash~'^[0-9a-f]{64}$'),
    observed_at              TIMESTAMPTZ NOT NULL,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (external_account_id,scan_cycle_id),
    UNIQUE (source_instance_id,proof_key),
    FOREIGN KEY (source_instance_id,stream_kind,scan_cycle_id)
        REFERENCES public.source_economic_scan_cycles(source_instance_id,stream_id,scan_cycle_id) ON DELETE RESTRICT,
    FOREIGN KEY (source_instance_id,stream_kind,final_batch_id)
        REFERENCES public.source_ingest_batches(source_instance_id,stream_id,batch_id) ON DELETE RESTRICT,
    CHECK (NOT balance_negative OR balance_service_units=0)
);

CREATE INDEX balance_carry_forward_account_idx
    ON public.balance_carry_forward_proofs(external_account_id,as_of,id);

CREATE TABLE public.balance_carry_forward_evaluations (
    id                       UUID PRIMARY KEY,
    proof_id                 UUID NOT NULL REFERENCES public.balance_carry_forward_proofs(id) ON DELETE RESTRICT,
    projection_version       BIGINT NOT NULL CHECK (projection_version>0),
    expected_service_units   NUMERIC(78,0) NOT NULL CHECK (expected_service_units>=0),
    difference_service_units NUMERIC(78,0) NOT NULL,
    evaluation_status        TEXT NOT NULL CHECK (evaluation_status IN (
        'matched','positive_classified_non_cash','negative_frozen','source_gap_frozen'
    )),
    evaluated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (proof_id,projection_version)
);

CREATE INDEX balance_carry_forward_evaluations_latest_idx
    ON public.balance_carry_forward_evaluations(proof_id,projection_version DESC);

CREATE FUNCTION public.enforce_balance_carry_forward_proof_contract() RETURNS trigger
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
           balance_negative,baseline_member
      INTO prior_source,prior_account,prior_as_of,prior_sequence,prior_balance,prior_negative,prior_member
    FROM public.balance_reconciliation_checkpoints WHERE id=NEW.prior_checkpoint_id;
    IF prior_source IS NULL OR prior_source<>NEW.source_instance_id
       OR prior_account<>NEW.external_account_id OR prior_as_of>NEW.as_of
       OR (prior_as_of=NEW.as_of AND prior_sequence>=NEW.source_sequence)
       OR prior_balance<>NEW.balance_service_units OR prior_negative<>NEW.balance_negative
       OR prior_member<>NEW.baseline_member THEN
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

CREATE TRIGGER balance_carry_forward_proofs_contract_guard
BEFORE INSERT ON public.balance_carry_forward_proofs
FOR EACH ROW EXECUTE FUNCTION public.enforce_balance_carry_forward_proof_contract();

CREATE FUNCTION public.reject_real_checkpoint_after_carry_forward() RETURNS trigger
LANGUAGE plpgsql
SET search_path=pg_catalog,public
AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM public.source_economic_scan_cycle_events mapped
        JOIN public.balance_carry_forward_proofs proof
          ON proof.source_instance_id=mapped.source_instance_id
         AND proof.scan_cycle_id=mapped.scan_cycle_id
         AND proof.external_account_id=NEW.external_account_id
        WHERE mapped.source_instance_id=NEW.source_instance_id
          AND mapped.stream_id='balances'
          AND mapped.event_id::text=NEW.external_event_id
          AND mapped.payload_hash=NEW.source_revision_hash
    ) THEN
        RAISE EXCEPTION 'real balance checkpoint conflicts with carry-forward proof';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER balance_checkpoint_carry_forward_exclusion_guard
BEFORE INSERT ON public.balance_reconciliation_checkpoints
FOR EACH ROW EXECUTE FUNCTION public.reject_real_checkpoint_after_carry_forward();

CREATE TRIGGER balance_carry_forward_proofs_immutable
BEFORE UPDATE OR DELETE ON public.balance_carry_forward_proofs
FOR EACH ROW EXECUTE FUNCTION public.reject_source_eligibility_fact_mutation();

CREATE TRIGGER balance_carry_forward_evaluations_immutable
BEFORE UPDATE OR DELETE ON public.balance_carry_forward_evaluations
FOR EACH ROW EXECUTE FUNCTION public.reject_source_eligibility_fact_mutation();
