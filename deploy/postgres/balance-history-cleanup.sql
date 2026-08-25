\set ON_ERROR_STOP on
\if :{?apply_cleanup}
\else
  \echo 'apply_cleanup psql variable is required'
  \quit 2
\endif
\if :apply_cleanup
  \if :{?expected_purge_sha256}
  \else
    \echo 'expected_purge_sha256 psql variable is required'
    \quit 2
  \endif
  \if :{?expected_keep_sha256}
  \else
    \echo 'expected_keep_sha256 psql variable is required'
    \quit 2
  \endif
\endif
\if :{?keep_rows_path}
\else
  \echo 'keep_rows_path psql variable is required'
  \quit 2
\endif
\if :{?purge_rows_path}
\else
  \echo 'purge_rows_path psql variable is required'
  \quit 2
\endif

-- PostgreSQL read-only transactions reject every CREATE/ALTER command, even
-- for temporary objects.  Define the session-local work tables in a short,
-- explicit read-write transaction, then run the entire plan path under a
-- SERIALIZABLE READ ONLY transaction.  The apply path uses the same tables but
-- explicitly opts into READ WRITE below.
BEGIN READ WRITE;
CREATE TEMP TABLE cleanup_target(
  source_instance_id uuid NOT NULL,
  stream_id text NOT NULL,
  event_id uuid NOT NULL,
  dependency_key_hmac text NOT NULL,
  scan_cycle_id uuid NOT NULL,
  batch_id uuid NOT NULL,
  scan_ceiling_at timestamptz NOT NULL,
  scan_snapshot_id char(64),
  baseline_snapshot_hash char(64) NOT NULL,
  cutover_at timestamptz NOT NULL,
  sequence bigint NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY(source_instance_id,stream_id,event_id),
  UNIQUE(source_instance_id,stream_id,scan_cycle_id,event_id)
) ON COMMIT PRESERVE ROWS;
CREATE TEMP TABLE cleanup_ranked(
  source_instance_id uuid NOT NULL,
  stream_id text NOT NULL,
  event_id uuid NOT NULL,
  dependency_key_hmac text NOT NULL,
  scan_cycle_id uuid NOT NULL,
  batch_id uuid NOT NULL,
  scan_ceiling_at timestamptz NOT NULL,
  scan_snapshot_id char(64),
  baseline_snapshot_hash char(64) NOT NULL,
  cutover_at timestamptz NOT NULL,
  batch_sequence bigint NOT NULL,
  created_at timestamptz NOT NULL,
  first_rank bigint NOT NULL,
  last_rank bigint NOT NULL
) ON COMMIT PRESERVE ROWS;
CREATE TEMP TABLE cleanup_keep(
  source_instance_id uuid NOT NULL,
  stream_id text NOT NULL,
  event_id uuid NOT NULL,
  PRIMARY KEY(source_instance_id,stream_id,event_id)
) ON COMMIT PRESERVE ROWS;
CREATE TEMP TABLE cleanup_purge(
  source_instance_id uuid NOT NULL,
  stream_id text NOT NULL,
  event_id uuid NOT NULL,
  scan_cycle_id uuid NOT NULL,
  PRIMARY KEY(source_instance_id,stream_id,event_id),
  UNIQUE(source_instance_id,stream_id,scan_cycle_id,event_id)
) ON COMMIT PRESERVE ROWS;
CREATE TEMP TABLE cleanup_guard_before(
  object_name text PRIMARY KEY,
  row_count bigint NOT NULL
) ON COMMIT PRESERVE ROWS;
CREATE TEMP TABLE cleanup_status_before(
  processing_status text NOT NULL,
  row_count bigint NOT NULL
) ON COMMIT PRESERVE ROWS;
CREATE TEMP TABLE cleanup_baseline_metrics(
  non_target_events bigint NOT NULL,
  non_target_mappings bigint NOT NULL
) ON COMMIT PRESERVE ROWS;
CREATE TEMP TABLE cleanup_delete_counts(
  kind text PRIMARY KEY,
  row_count bigint NOT NULL
) ON COMMIT PRESERVE ROWS;
CREATE TEMP TABLE cleanup_guard_after(
  object_name text PRIMARY KEY,
  row_count bigint NOT NULL
) ON COMMIT PRESERVE ROWS;
COMMIT;

\if :apply_cleanup
BEGIN ISOLATION LEVEL SERIALIZABLE READ WRITE;
\else
BEGIN ISOLATION LEVEL SERIALIZABLE READ ONLY;
\endif
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='2h';
SET LOCAL idle_in_transaction_session_timeout='10min';
SET LOCAL transaction_timeout='2h';
SET LOCAL synchronous_commit=on;
\if :apply_cleanup
DO $$
BEGIN
  IF (SELECT count(*) FROM pg_catalog.pg_index i
      JOIN pg_catalog.pg_class idx ON idx.oid=i.indexrelid
      JOIN pg_catalog.pg_namespace ns ON ns.oid=idx.relnamespace
      JOIN pg_catalog.pg_am am ON am.oid=idx.relam
      WHERE ns.nspname='public'
        AND idx.relname='source_economic_scan_cycle_events_event_fk_idx'
        AND idx.relkind='i' AND idx.relpersistence='p' AND am.amname='btree'
        AND i.indrelid='public.source_economic_scan_cycle_events'::regclass
        AND i.indnatts=3 AND i.indnkeyatts=3 AND i.indexprs IS NULL AND i.indpred IS NULL
        AND pg_get_indexdef(idx.oid,1,true)='source_instance_id'
        AND pg_get_indexdef(idx.oid,2,true)='stream_id'
        AND pg_get_indexdef(idx.oid,3,true)='event_id'
        AND NOT i.indisunique AND NOT i.indisprimary AND NOT i.indisexclusion
        AND i.indisvalid AND i.indisready AND i.indislive)<>1 THEN
    RAISE EXCEPTION 'exact valid/ready/live event FK lookup index is required';
  END IF;
  IF (SELECT count(*) FROM pg_catalog.pg_constraint constraint_row
      WHERE constraint_row.contype='f'
        AND constraint_row.conrelid='public.source_economic_scan_cycle_events'::regclass
        AND constraint_row.confrelid='public.source_ingest_events'::regclass
        AND constraint_row.conkey=ARRAY[1,2,4]::smallint[]
        AND constraint_row.confkey=ARRAY[1,2,3]::smallint[]
        AND constraint_row.confdeltype='r')<>1
     OR (SELECT count(*) FROM pg_catalog.pg_constraint constraint_row
         WHERE constraint_row.contype='f'
           AND constraint_row.confrelid='public.source_ingest_events'::regclass)<>1
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_constraint constraint_row
                WHERE constraint_row.contype='f'
                  AND constraint_row.confrelid='public.source_economic_scan_cycle_events'::regclass)
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_trigger trigger_row
                WHERE trigger_row.tgrelid IN (
                  'public.source_ingest_events'::regclass,
                  'public.source_economic_scan_cycle_events'::regclass)
                  AND NOT trigger_row.tgisinternal AND (trigger_row.tgtype & 8)=8) THEN
    RAISE EXCEPTION 'balance history cleanup FK/delete-trigger contract mismatch';
  END IF;
END;
$$;
LOCK TABLE public.source_ingest_state,public.source_ingest_batches,
  public.source_economic_scan_cycles IN SHARE MODE;
LOCK TABLE public.source_ingest_events,public.source_economic_scan_cycle_events
  IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE public.source_economic_stream_watermarks,
  public.source_cutover_manifests,public.source_events,public.external_accounts,
  public.external_account_binding_proofs,public.funding_lots,public.source_usage_events,
  public.source_credit_events,public.balance_reconciliation_checkpoints,
  public.balance_checkpoint_evaluations,public.balance_carry_forward_proofs,
  public.balance_carry_forward_evaluations,public.source_account_eligibility_state,
  public.eligibility_projection_jobs,public.invoice_requests,public.invoice_allocations,
  public.invoice_documents,public.audit_events,public.schema_migrations IN SHARE MODE;
\else
LOCK TABLE public.source_ingest_state,public.source_ingest_batches,
  public.source_economic_scan_cycles,public.source_ingest_events,
  public.source_economic_scan_cycle_events,public.source_economic_stream_watermarks,
  public.source_cutover_manifests,public.source_events,public.external_accounts,
  public.external_account_binding_proofs,public.funding_lots,public.source_usage_events,
  public.source_credit_events,public.balance_reconciliation_checkpoints,
  public.balance_checkpoint_evaluations,public.balance_carry_forward_proofs,
  public.balance_carry_forward_evaluations,public.source_account_eligibility_state,
  public.eligibility_projection_jobs,public.invoice_requests,public.invoice_allocations,
  public.invoice_documents,public.audit_events,public.schema_migrations IN ACCESS SHARE MODE;
\endif

DO $$
BEGIN
  IF (SELECT count(*) FROM public.external_accounts)<>0
     OR (SELECT count(*) FROM public.external_account_binding_proofs)<>0
     OR (SELECT count(*) FROM public.source_account_eligibility_state)<>0
     OR (SELECT count(*) FROM public.balance_reconciliation_checkpoints)<>0
     OR (SELECT count(*) FROM public.balance_checkpoint_evaluations)<>0
     OR (SELECT count(*) FROM public.balance_carry_forward_proofs)<>0
     OR (SELECT count(*) FROM public.balance_carry_forward_evaluations)<>0
     OR (SELECT count(*) FROM public.eligibility_projection_jobs)<>0 THEN
    RAISE EXCEPTION 'pre-policy identity/economic zero-state contract mismatch';
  END IF;
END;
$$;

INSERT INTO cleanup_target
SELECT event.source_instance_id,event.stream_id,event.event_id,event.dependency_key_hmac,
       mapped.scan_cycle_id,mapped.batch_id,cycle.scan_ceiling_at,
       cycle.scan_snapshot_id,manifest.baseline_snapshot_hash,manifest.cutover_at,
       batch.sequence,event.created_at
FROM public.source_ingest_events event
JOIN public.source_economic_scan_cycle_events mapped
  ON mapped.source_instance_id=event.source_instance_id
 AND mapped.stream_id=event.stream_id AND mapped.event_id=event.event_id
JOIN public.source_economic_scan_cycles cycle
  ON cycle.source_instance_id=mapped.source_instance_id
 AND cycle.stream_id=mapped.stream_id AND cycle.scan_cycle_id=mapped.scan_cycle_id
JOIN public.source_ingest_batches batch
  ON batch.source_instance_id=mapped.source_instance_id
 AND batch.stream_id=mapped.stream_id AND batch.batch_id=mapped.batch_id
 AND batch.scan_cycle_id=mapped.scan_cycle_id
JOIN public.source_cutover_manifests manifest
  ON manifest.source_instance_id=event.source_instance_id
WHERE event.stream_id='balances'
  AND event.entity_type='balance_checkpoint'
  AND event.operation='upsert'
  AND event.processing_status='parked_identity'
  AND event.dependency_kind='source_external_account'
  AND event.dependency_key_hmac IS NOT NULL
  AND mapped.payload_hash=event.payload_hash
  AND cycle.cycle_status='published'
  AND batch.schema_version='3.0'
  AND cycle.scan_ceiling_at<'2026-09-01T00:00:00+08:00'::timestamptz;

INSERT INTO cleanup_ranked
SELECT target.*,
       row_number() OVER (
         PARTITION BY source_instance_id,dependency_key_hmac
         ORDER BY scan_ceiling_at,batch_sequence,created_at,event_id
       ) AS first_rank,
       row_number() OVER (
         PARTITION BY source_instance_id,dependency_key_hmac
         ORDER BY scan_ceiling_at DESC,batch_sequence DESC,created_at DESC,event_id DESC
       ) AS last_rank
FROM (
  SELECT source_instance_id,stream_id,event_id,dependency_key_hmac,scan_cycle_id,batch_id,
         scan_ceiling_at,scan_snapshot_id,baseline_snapshot_hash,cutover_at,
         sequence AS batch_sequence,created_at
  FROM cleanup_target
) target;

INSERT INTO cleanup_keep
SELECT source_instance_id,stream_id,event_id
FROM cleanup_ranked WHERE first_rank=1 OR last_rank=1;

INSERT INTO cleanup_purge
SELECT target.source_instance_id,target.stream_id,target.event_id,target.scan_cycle_id
FROM cleanup_target target
LEFT JOIN cleanup_keep keep
  ON keep.source_instance_id=target.source_instance_id
 AND keep.stream_id=target.stream_id AND keep.event_id=target.event_id
WHERE keep.event_id IS NULL;

DO $$
DECLARE
  target_count bigint;
  group_count bigint;
  keep_count bigint;
  purge_count bigint;
  minimum_group_count bigint;
  maximum_group_count bigint;
  cutover_anchor_count bigint;
  post_cutover_anchor_count bigint;
  baseline_time_mismatch_count bigint;
  unexpected_first_count bigint;
  latest_actual_anchor_count bigint;
  unexpected_last_count bigint;
BEGIN
  SELECT count(*) INTO target_count FROM cleanup_target;
  SELECT count(*) INTO group_count FROM (
    SELECT source_instance_id,dependency_key_hmac FROM cleanup_target
    GROUP BY source_instance_id,dependency_key_hmac
  ) grouped;
  SELECT count(*) INTO keep_count FROM cleanup_keep;
  SELECT count(*) INTO purge_count FROM cleanup_purge;
  SELECT min(group_rows),max(group_rows) INTO minimum_group_count,maximum_group_count FROM (
    SELECT count(*) AS group_rows FROM cleanup_target
    GROUP BY source_instance_id,dependency_key_hmac
  ) grouped;
  SELECT count(*) FILTER (WHERE scan_snapshot_id=baseline_snapshot_hash AND scan_ceiling_at=cutover_at),
         count(*) FILTER (WHERE scan_snapshot_id IS DISTINCT FROM baseline_snapshot_hash AND scan_ceiling_at>cutover_at),
         count(*) FILTER (WHERE scan_snapshot_id=baseline_snapshot_hash AND scan_ceiling_at<>cutover_at),
         count(*) FILTER (WHERE NOT (
           scan_snapshot_id=baseline_snapshot_hash AND scan_ceiling_at=cutover_at
           OR scan_snapshot_id IS DISTINCT FROM baseline_snapshot_hash AND scan_ceiling_at>cutover_at
         ))
    INTO cutover_anchor_count,post_cutover_anchor_count,
         baseline_time_mismatch_count,unexpected_first_count
  FROM cleanup_ranked WHERE first_rank=1;
  SELECT count(*) FILTER (
           WHERE scan_snapshot_id IS DISTINCT FROM baseline_snapshot_hash
             AND scan_ceiling_at>cutover_at),
         count(*) FILTER (
           WHERE NOT (scan_snapshot_id IS DISTINCT FROM baseline_snapshot_hash
                      AND scan_ceiling_at>cutover_at))
    INTO latest_actual_anchor_count,unexpected_last_count
  FROM cleanup_ranked WHERE last_rank=1;
  IF target_count<>1879297 OR group_count<>2747 OR keep_count<>5494
     OR purge_count<>1873803 OR minimum_group_count<>79 OR maximum_group_count<>771
     OR cutover_anchor_count<>2738 OR post_cutover_anchor_count<>9
     OR baseline_time_mismatch_count<>0 OR unexpected_first_count<>0
     OR latest_actual_anchor_count<>2747 OR unexpected_last_count<>0 THEN
    RAISE EXCEPTION 'balance history cleanup tuple mismatch T=% G=% K=% D=% min=% max=% cutover=% post_cutover=% baseline_time_mismatch=% unexpected_first=% latest_actual=% unexpected_last=%',
      target_count,group_count,keep_count,purge_count,minimum_group_count,maximum_group_count,
      cutover_anchor_count,post_cutover_anchor_count,baseline_time_mismatch_count,unexpected_first_count,
      latest_actual_anchor_count,unexpected_last_count;
  END IF;
END;
$$;

INSERT INTO cleanup_guard_before VALUES
  ('source_ingest_state',(SELECT count(*) FROM public.source_ingest_state)),
  ('source_ingest_batches',(SELECT count(*) FROM public.source_ingest_batches)),
  ('source_economic_scan_cycles',(SELECT count(*) FROM public.source_economic_scan_cycles)),
  ('source_economic_stream_watermarks',(SELECT count(*) FROM public.source_economic_stream_watermarks)),
  ('source_cutover_manifests',(SELECT count(*) FROM public.source_cutover_manifests)),
  ('source_events',(SELECT count(*) FROM public.source_events)),
  ('external_accounts',(SELECT count(*) FROM public.external_accounts)),
  ('external_account_binding_proofs',(SELECT count(*) FROM public.external_account_binding_proofs)),
  ('funding_lots',(SELECT count(*) FROM public.funding_lots)),
  ('source_usage_events',(SELECT count(*) FROM public.source_usage_events)),
  ('source_credit_events',(SELECT count(*) FROM public.source_credit_events)),
  ('balance_reconciliation_checkpoints',(SELECT count(*) FROM public.balance_reconciliation_checkpoints)),
  ('balance_checkpoint_evaluations',(SELECT count(*) FROM public.balance_checkpoint_evaluations)),
  ('balance_carry_forward_proofs',(SELECT count(*) FROM public.balance_carry_forward_proofs)),
  ('balance_carry_forward_evaluations',(SELECT count(*) FROM public.balance_carry_forward_evaluations)),
  ('source_account_eligibility_state',(SELECT count(*) FROM public.source_account_eligibility_state)),
  ('eligibility_projection_jobs',(SELECT count(*) FROM public.eligibility_projection_jobs)),
  ('invoice_requests',(SELECT count(*) FROM public.invoice_requests)),
  ('invoice_allocations',(SELECT count(*) FROM public.invoice_allocations)),
  ('invoice_documents',(SELECT count(*) FROM public.invoice_documents)),
  ('audit_events',(SELECT count(*) FROM public.audit_events)),
  ('schema_migrations',(SELECT count(*) FROM public.schema_migrations));

INSERT INTO cleanup_status_before
SELECT processing_status,count(*) AS row_count
FROM public.source_ingest_events event
WHERE NOT EXISTS (
  SELECT 1 FROM cleanup_target target
  WHERE target.source_instance_id=event.source_instance_id
    AND target.stream_id=event.stream_id AND target.event_id=event.event_id
)
GROUP BY processing_status;

INSERT INTO cleanup_baseline_metrics
SELECT
  (SELECT count(*) FROM public.source_ingest_events)-1879297::bigint AS non_target_events,
  (SELECT count(*) FROM public.source_economic_scan_cycle_events)-1879297::bigint AS non_target_mappings;

\pset format unaligned
\pset tuples_only on
\o :keep_rows_path
COPY (
  SELECT source_instance_id::text||'|'||event_id::text
  FROM cleanup_keep ORDER BY source_instance_id,event_id
) TO STDOUT;
\o
\set actual_keep_sha256 `sha256sum :'keep_rows_path' | cut -d' ' -f1`
\o :purge_rows_path
COPY (
  SELECT source_instance_id::text||'|'||event_id::text
  FROM cleanup_purge ORDER BY source_instance_id,event_id
) TO STDOUT;
\o
\set actual_purge_sha256 `sha256sum :'purge_rows_path' | cut -d' ' -f1`
\if :apply_cleanup
SELECT set_config('solov.expected_purge_sha256', :'expected_purge_sha256', true);
SELECT set_config('solov.expected_keep_sha256', :'expected_keep_sha256', true);
SELECT set_config('solov.actual_purge_sha256', :'actual_purge_sha256', true);
SELECT set_config('solov.actual_keep_sha256', :'actual_keep_sha256', true);
DO $$
DECLARE actual_purge text; actual_keep text;
BEGIN
  actual_purge:=current_setting('solov.actual_purge_sha256');
  actual_keep:=current_setting('solov.actual_keep_sha256');
  IF actual_purge!~'^[0-9a-f]{64}$'
     OR actual_purge<>current_setting('solov.expected_purge_sha256') THEN
    RAISE EXCEPTION 'balance history cleanup purge digest mismatch';
  END IF;
  IF actual_keep!~'^[0-9a-f]{64}$'
     OR actual_keep<>current_setting('solov.expected_keep_sha256') THEN
    RAISE EXCEPTION 'balance history cleanup keep digest mismatch';
  END IF;
END;
$$;
\else
SELECT 'T='||count(*) FROM cleanup_target;
SELECT 'G='||count(*) FROM (
  SELECT source_instance_id,dependency_key_hmac FROM cleanup_target
  GROUP BY source_instance_id,dependency_key_hmac
) grouped;
SELECT 'K='||count(*) FROM cleanup_keep;
SELECT 'D='||count(*) FROM cleanup_purge;
SELECT 'min_group='||min(group_rows)||'|max_group='||max(group_rows) FROM (
  SELECT count(*) AS group_rows FROM cleanup_target
  GROUP BY source_instance_id,dependency_key_hmac
) grouped;
SELECT 'cutover_anchors='||count(*) FILTER (WHERE scan_snapshot_id=baseline_snapshot_hash AND scan_ceiling_at=cutover_at)
       ||'|post_cutover_anchors='||count(*) FILTER (WHERE scan_snapshot_id IS DISTINCT FROM baseline_snapshot_hash AND scan_ceiling_at>cutover_at)
       ||'|baseline_time_mismatch='||count(*) FILTER (WHERE scan_snapshot_id=baseline_snapshot_hash AND scan_ceiling_at<>cutover_at)
FROM cleanup_ranked WHERE first_rank=1;
SELECT 'latest_actual_anchors='||count(*) FILTER (
         WHERE scan_snapshot_id IS DISTINCT FROM baseline_snapshot_hash AND scan_ceiling_at>cutover_at)
       ||'|unexpected_last='||count(*) FILTER (
         WHERE NOT (scan_snapshot_id IS DISTINCT FROM baseline_snapshot_hash AND scan_ceiling_at>cutover_at))
FROM cleanup_ranked WHERE last_rank=1;
SELECT 'keep_sha256='||:'actual_keep_sha256';
SELECT 'purge_sha256='||:'actual_purge_sha256';
ROLLBACK;
\quit
\endif

WITH deleted AS (
  DELETE FROM public.source_economic_scan_cycle_events mapped
  USING cleanup_purge purge
  WHERE mapped.source_instance_id=purge.source_instance_id
    AND mapped.stream_id=purge.stream_id
    AND mapped.scan_cycle_id=purge.scan_cycle_id
    AND mapped.event_id=purge.event_id
  RETURNING 1
)
INSERT INTO cleanup_delete_counts SELECT 'mapping',count(*) FROM deleted;

WITH deleted AS (
  DELETE FROM public.source_ingest_events event
  USING cleanup_purge purge
  WHERE event.source_instance_id=purge.source_instance_id
    AND event.stream_id=purge.stream_id AND event.event_id=purge.event_id
  RETURNING 1
)
INSERT INTO cleanup_delete_counts SELECT 'event',count(*) FROM deleted;

DO $$
DECLARE mapping_deleted bigint; event_deleted bigint;
BEGIN
  SELECT row_count INTO STRICT mapping_deleted FROM cleanup_delete_counts WHERE kind='mapping';
  SELECT row_count INTO STRICT event_deleted FROM cleanup_delete_counts WHERE kind='event';
  IF mapping_deleted<>1873803 OR event_deleted<>1873803 THEN
    RAISE EXCEPTION 'balance history cleanup delete count mismatch mapping=% event=%',
      mapping_deleted,event_deleted;
  END IF;
END;
$$;

INSERT INTO cleanup_guard_after VALUES
  ('source_ingest_state',(SELECT count(*) FROM public.source_ingest_state)),
  ('source_ingest_batches',(SELECT count(*) FROM public.source_ingest_batches)),
  ('source_economic_scan_cycles',(SELECT count(*) FROM public.source_economic_scan_cycles)),
  ('source_economic_stream_watermarks',(SELECT count(*) FROM public.source_economic_stream_watermarks)),
  ('source_cutover_manifests',(SELECT count(*) FROM public.source_cutover_manifests)),
  ('source_events',(SELECT count(*) FROM public.source_events)),
  ('external_accounts',(SELECT count(*) FROM public.external_accounts)),
  ('external_account_binding_proofs',(SELECT count(*) FROM public.external_account_binding_proofs)),
  ('funding_lots',(SELECT count(*) FROM public.funding_lots)),
  ('source_usage_events',(SELECT count(*) FROM public.source_usage_events)),
  ('source_credit_events',(SELECT count(*) FROM public.source_credit_events)),
  ('balance_reconciliation_checkpoints',(SELECT count(*) FROM public.balance_reconciliation_checkpoints)),
  ('balance_checkpoint_evaluations',(SELECT count(*) FROM public.balance_checkpoint_evaluations)),
  ('balance_carry_forward_proofs',(SELECT count(*) FROM public.balance_carry_forward_proofs)),
  ('balance_carry_forward_evaluations',(SELECT count(*) FROM public.balance_carry_forward_evaluations)),
  ('source_account_eligibility_state',(SELECT count(*) FROM public.source_account_eligibility_state)),
  ('eligibility_projection_jobs',(SELECT count(*) FROM public.eligibility_projection_jobs)),
  ('invoice_requests',(SELECT count(*) FROM public.invoice_requests)),
  ('invoice_allocations',(SELECT count(*) FROM public.invoice_allocations)),
  ('invoice_documents',(SELECT count(*) FROM public.invoice_documents)),
  ('audit_events',(SELECT count(*) FROM public.audit_events)),
  ('schema_migrations',(SELECT count(*) FROM public.schema_migrations));

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM cleanup_guard_before guard_before
    FULL JOIN cleanup_guard_after guard_after USING(object_name)
    WHERE guard_before.row_count IS DISTINCT FROM guard_after.row_count
  ) THEN
    RAISE EXCEPTION 'balance history cleanup changed a guarded non-target table';
  END IF;
  IF (SELECT count(*) FROM public.source_ingest_events)<>(
       SELECT non_target_events+5494 FROM cleanup_baseline_metrics) THEN
    RAISE EXCEPTION 'balance history cleanup event total mismatch';
  END IF;
  IF (SELECT count(*) FROM public.source_economic_scan_cycle_events)<>(
       SELECT non_target_mappings+5494 FROM cleanup_baseline_metrics) THEN
    RAISE EXCEPTION 'balance history cleanup mapping total mismatch';
  END IF;
  IF EXISTS (
    (SELECT processing_status,row_count FROM cleanup_status_before
     EXCEPT
     SELECT event.processing_status,count(*)
     FROM public.source_ingest_events event
     WHERE NOT EXISTS (
       SELECT 1 FROM cleanup_keep keep
       WHERE keep.source_instance_id=event.source_instance_id
         AND keep.stream_id=event.stream_id AND keep.event_id=event.event_id
     ) GROUP BY event.processing_status)
    UNION ALL
    (SELECT event.processing_status,count(*)
     FROM public.source_ingest_events event
     WHERE NOT EXISTS (
       SELECT 1 FROM cleanup_keep keep
       WHERE keep.source_instance_id=event.source_instance_id
         AND keep.stream_id=event.stream_id AND keep.event_id=event.event_id
     ) GROUP BY event.processing_status
     EXCEPT SELECT processing_status,row_count FROM cleanup_status_before)
  ) THEN
    RAISE EXCEPTION 'balance history cleanup changed non-target event status counts';
  END IF;
  IF EXISTS (
    SELECT 1 FROM cleanup_keep keep
    WHERE NOT EXISTS (
      SELECT 1 FROM public.source_ingest_events event
      WHERE event.source_instance_id=keep.source_instance_id
        AND event.stream_id=keep.stream_id AND event.event_id=keep.event_id
    ) OR NOT EXISTS (
      SELECT 1 FROM public.source_economic_scan_cycle_events mapped
      WHERE mapped.source_instance_id=keep.source_instance_id
        AND mapped.stream_id=keep.stream_id AND mapped.event_id=keep.event_id
    )
  ) THEN
    RAISE EXCEPTION 'balance history cleanup removed a keep row';
  END IF;
  IF EXISTS (
    SELECT 1 FROM cleanup_purge purge
    WHERE EXISTS (
      SELECT 1 FROM public.source_ingest_events event
      WHERE event.source_instance_id=purge.source_instance_id
        AND event.stream_id=purge.stream_id AND event.event_id=purge.event_id
    ) OR EXISTS (
      SELECT 1 FROM public.source_economic_scan_cycle_events mapped
      WHERE mapped.source_instance_id=purge.source_instance_id
        AND mapped.stream_id=purge.stream_id AND mapped.event_id=purge.event_id
    )
  ) THEN
    RAISE EXCEPTION 'balance history cleanup retained a purge row';
  END IF;
  IF EXISTS (
    SELECT target.source_instance_id,target.dependency_key_hmac
    FROM cleanup_target target
    JOIN public.source_ingest_events event
      ON event.source_instance_id=target.source_instance_id
     AND event.stream_id=target.stream_id AND event.event_id=target.event_id
    GROUP BY target.source_instance_id,target.dependency_key_hmac
    HAVING count(*)<>2
  ) THEN
    RAISE EXCEPTION 'balance history cleanup did not retain exactly two rows per group';
  END IF;
END;
$$;

SELECT 'T='||count(*) FROM cleanup_target;
SELECT 'G='||count(*) FROM (
  SELECT source_instance_id,dependency_key_hmac FROM cleanup_target
  GROUP BY source_instance_id,dependency_key_hmac
) grouped;
SELECT 'K='||count(*) FROM cleanup_keep;
SELECT 'D='||count(*) FROM cleanup_purge;
SELECT kind||'_deleted='||row_count FROM cleanup_delete_counts ORDER BY kind;
COMMIT;
