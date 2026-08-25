-- HTTP readiness must remain independent of the potentially very large
-- waiting_dependency/parked_identity backlog. This covering partial index is
-- intentionally limited to work that can affect runtime readiness.
--
-- Production must prebuild this exact index with the separately controlled
-- CONCURRENTLY operator. Even CREATE INDEX IF NOT EXISTS requests a table lock
-- before it notices an existing name, so it is not safe in this migration.
-- Plain CREATE INDEX below is reachable only for a logically and physically
-- empty new installation; every populated installation fails closed.
DO $$
DECLARE
    exact_index BOOLEAN;
BEGIN
    SELECT EXISTS (
        SELECT 1
        FROM pg_catalog.pg_index AS i
        JOIN pg_catalog.pg_class AS idx ON idx.oid=i.indexrelid
        JOIN pg_catalog.pg_namespace AS idx_ns ON idx_ns.oid=idx.relnamespace
        JOIN pg_catalog.pg_am AS am ON am.oid=idx.relam
        JOIN pg_catalog.pg_class AS tbl ON tbl.oid=i.indrelid
        JOIN pg_catalog.pg_namespace AS tbl_ns ON tbl_ns.oid=tbl.relnamespace
        WHERE idx.relname='source_ingest_events_readiness_active_idx'
          AND idx_ns.nspname='public'
          AND idx.relkind='i'
          AND am.amname='btree'
          AND tbl.relname='source_ingest_events'
          AND tbl_ns.nspname='public'
          AND tbl.relkind='r'
          AND i.indrelid='public.source_ingest_events'::pg_catalog.regclass
          AND i.indnatts=4
          AND i.indnkeyatts=4
          AND i.indexprs IS NULL
          AND pg_catalog.pg_get_indexdef(idx.oid,1,true)='source_instance_id'
          AND pg_catalog.pg_get_indexdef(idx.oid,2,true)='stream_id'
          AND pg_catalog.pg_get_indexdef(idx.oid,3,true)='processing_status'
          AND pg_catalog.pg_get_indexdef(idx.oid,4,true)='created_at'
          AND i.indpred IS NOT NULL
          AND pg_catalog.regexp_replace(
                pg_catalog.pg_get_expr(i.indpred,i.indrelid,false),
                '\s+','','g'
              )='(processing_status=ANY(ARRAY[''queued''::text,''failed''::text,''processing''::text,''dead''::text]))'
          AND NOT i.indisunique
          AND NOT i.indisprimary
          AND NOT i.indisexclusion
          AND i.indisvalid
          AND i.indisready
          AND i.indislive
    ) INTO exact_index;

    -- The normal production path returns after catalog reads only. In
    -- particular it never executes CREATE INDEX against the populated table.
    IF exact_index THEN
        RETURN;
    END IF;

    IF pg_catalog.to_regclass('public.source_ingest_events_readiness_active_idx') IS NOT NULL THEN
        RAISE EXCEPTION 'source readiness active index catalog contract mismatch'
            USING ERRCODE='55000';
    END IF;

    IF EXISTS (SELECT 1 FROM public.source_ingest_events LIMIT 1)
       OR pg_catalog.pg_relation_size('public.source_ingest_events'::pg_catalog.regclass)<>0 THEN
        RAISE EXCEPTION 'source readiness active index must be prebuilt externally with CREATE INDEX CONCURRENTLY before migration'
            USING ERRCODE='55000';
    END IF;

    -- Close the empty-install race before the second emptiness check and the
    -- ordinary index build. This branch is forbidden for populated systems.
    LOCK TABLE public.source_ingest_events IN SHARE MODE;
    IF EXISTS (SELECT 1 FROM public.source_ingest_events LIMIT 1)
       OR pg_catalog.pg_relation_size('public.source_ingest_events'::pg_catalog.regclass)<>0 THEN
        RAISE EXCEPTION 'source readiness active index must be prebuilt externally with CREATE INDEX CONCURRENTLY before migration'
            USING ERRCODE='55000';
    END IF;

    EXECUTE 'CREATE INDEX source_ingest_events_readiness_active_idx '
         || 'ON public.source_ingest_events(source_instance_id,stream_id,processing_status,created_at) '
         || 'WHERE processing_status IN (''queued'',''failed'',''processing'',''dead'')';

    SELECT EXISTS (
        SELECT 1
        FROM pg_catalog.pg_index AS i
        JOIN pg_catalog.pg_class AS idx ON idx.oid=i.indexrelid
        JOIN pg_catalog.pg_namespace AS idx_ns ON idx_ns.oid=idx.relnamespace
        JOIN pg_catalog.pg_am AS am ON am.oid=idx.relam
        JOIN pg_catalog.pg_class AS tbl ON tbl.oid=i.indrelid
        JOIN pg_catalog.pg_namespace AS tbl_ns ON tbl_ns.oid=tbl.relnamespace
        WHERE idx.relname='source_ingest_events_readiness_active_idx'
          AND idx_ns.nspname='public'
          AND idx.relkind='i'
          AND am.amname='btree'
          AND tbl.relname='source_ingest_events'
          AND tbl_ns.nspname='public'
          AND tbl.relkind='r'
          AND i.indrelid='public.source_ingest_events'::pg_catalog.regclass
          AND i.indnatts=4
          AND i.indnkeyatts=4
          AND i.indexprs IS NULL
          AND pg_catalog.pg_get_indexdef(idx.oid,1,true)='source_instance_id'
          AND pg_catalog.pg_get_indexdef(idx.oid,2,true)='stream_id'
          AND pg_catalog.pg_get_indexdef(idx.oid,3,true)='processing_status'
          AND pg_catalog.pg_get_indexdef(idx.oid,4,true)='created_at'
          AND i.indpred IS NOT NULL
          AND pg_catalog.regexp_replace(
                pg_catalog.pg_get_expr(i.indpred,i.indrelid,false),
                '\s+','','g'
              )='(processing_status=ANY(ARRAY[''queued''::text,''failed''::text,''processing''::text,''dead''::text]))'
          AND NOT i.indisunique
          AND NOT i.indisprimary
          AND NOT i.indisexclusion
          AND i.indisvalid
          AND i.indisready
          AND i.indislive
    ) INTO exact_index;

    IF NOT exact_index THEN
        RAISE EXCEPTION 'source readiness active index catalog contract mismatch after empty-install creation'
            USING ERRCODE='55000';
    END IF;
END
$$;
