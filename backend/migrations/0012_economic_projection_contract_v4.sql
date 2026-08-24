-- Replace the pre-launch economic projection allowlist with the normalized
-- financial-semantics v4 contract. This is deliberately an empty-ledger-only
-- transition: an accepted manifest or financial fact must retain its original
-- immutable contract and requires a separate epoch migration instead.

LOCK TABLE source_cutover_manifests,source_ingest_state,source_ingest_batches,
    source_account_eligibility_state,source_economic_stream_watermarks,
    source_account_stream_watermarks,funding_lots,source_usage_events,
    source_credit_events,consumption_allocations,invoice_requests IN SHARE MODE;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM source_cutover_manifests)
       OR EXISTS (SELECT 1 FROM source_ingest_state
                  WHERE sequence<>0 OR last_batch_hash IS NOT NULL)
       OR EXISTS (SELECT 1 FROM source_ingest_batches)
       OR EXISTS (SELECT 1 FROM source_account_eligibility_state)
       OR EXISTS (SELECT 1 FROM source_economic_stream_watermarks)
       OR EXISTS (SELECT 1 FROM source_account_stream_watermarks)
       OR EXISTS (SELECT 1 FROM funding_lots)
       OR EXISTS (SELECT 1 FROM source_usage_events)
       OR EXISTS (SELECT 1 FROM source_credit_events)
       OR EXISTS (SELECT 1 FROM consumption_allocations)
       OR EXISTS (SELECT 1 FROM invoice_requests) THEN
        RAISE EXCEPTION 'economic projection v4 migration requires an empty pre-launch financial ledger';
    END IF;
END
$$;

CREATE OR REPLACE FUNCTION enforce_source_manifest_projection_contract() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    source_kind TEXT;
    owner_name NAME;
    policy_start TIMESTAMPTZ;
BEGIN
    SELECT source_type INTO STRICT source_kind FROM source_instances WHERE id=NEW.source_instance_id;
    SELECT r.rolname INTO STRICT owner_name
    FROM pg_database d JOIN pg_roles r ON r.oid=d.datdba
    WHERE d.datname=current_database();
    SELECT eligibility_start_at INTO STRICT policy_start
    FROM invoice_eligibility_policy WHERE singleton_id=1;
    IF (NEW.cutover_at>=policy_start OR NEW.database_clock>=policy_start)
       AND NOT (current_user=owner_name AND NEW.projection_contract='fixture-v3'
           AND NEW.signing_key_id='fixture' AND NEW.source_runtime_version='fixture-runtime') THEN
        RAISE EXCEPTION 'source cutover must be strictly before invoice eligibility start';
    END IF;
    IF (source_kind='sub2api' AND NEW.projection_contract='sub2api-economic-v4')
       OR (source_kind='newapi' AND NEW.projection_contract='newapi-economic-rc25-v4') THEN
        RETURN NEW;
    END IF;
    IF current_user=owner_name AND NEW.projection_contract='fixture-v3'
       AND NEW.signing_key_id='fixture' AND NEW.source_runtime_version='fixture-runtime' THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'source projection contract does not match source type';
END;
$$;
