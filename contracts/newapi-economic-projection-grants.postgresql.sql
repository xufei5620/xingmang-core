-- REVIEW-ONLY NEW API ECONOMIC BRIDGE V4 FOR rc.25.
-- Requires newapi-source-projection-grants.postgresql.sql. No upstream view or
-- static relation dependency is created.
-- Execute only through the reviewed maintenance wrapper, which supplies psql
-- -X and ON_ERROR_STOP=1. Never pipe this SQL to bare psql.

BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='15s';
SET LOCAL idle_in_transaction_session_timeout='15s';

DO $executor$
DECLARE executor_oid oid; database_owner oid; executor_superuser boolean;
BEGIN
  SELECT oid,rolsuper INTO executor_oid,executor_superuser FROM pg_roles WHERE rolname=current_user;
  SELECT datdba INTO database_owner FROM pg_database WHERE datname=current_database();
  IF executor_oid IS NULL OR NOT executor_superuser OR executor_oid<>database_owner THEN
    RAISE EXCEPTION 'New API economic bridge install requires the cluster superuser that owns the current database';
  END IF;
END $executor$;

DO $contract$
DECLARE owner_oid oid; bridge_namespace oid;
BEGIN
  SELECT oid INTO owner_oid FROM pg_roles
    WHERE rolname='invoice_newapi_bridge_owner' AND NOT rolcanlogin AND NOT rolsuper
      AND NOT rolcreatedb AND NOT rolcreaterole AND NOT rolinherit
      AND NOT rolreplication AND NOT rolbypassrls AND rolconnlimit=0;
  SELECT oid INTO bridge_namespace FROM pg_namespace WHERE nspname='invoice_bridge';
  IF owner_oid IS NULL OR bridge_namespace IS NULL
     OR to_regprocedure('invoice_bridge.newapi_payments_v4(text,jsonb)') IS NULL
     OR to_regprocedure('invoice_bridge.newapi_identities_v4(text,jsonb)') IS NULL THEN
    RAISE EXCEPTION 'verified New API source bridge is required';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_auth_members WHERE member=owner_oid OR roleid=owner_oid)
     OR EXISTS (SELECT 1 FROM pg_class WHERE relnamespace=bridge_namespace)
     OR EXISTS (
       SELECT 1 FROM pg_proc p JOIN pg_language l ON l.oid=p.prolang
       WHERE p.pronamespace=bridge_namespace AND (
         p.proname NOT IN ('newapi_payments_v4','newapi_usage_v4','newapi_credits_v4','newapi_balances_v4','newapi_identities_v4')
         OR oidvectortypes(p.proargtypes)<>'text, jsonb' OR p.prorettype<>'jsonb'::regtype
         OR NOT p.proretset OR NOT p.prosecdef OR l.lanname<>'plpgsql' OR p.proowner<>owner_oid
         OR NOT COALESCE('search_path=pg_catalog'=ANY(p.proconfig),false)
         OR NOT COALESCE('row_security=on'=ANY(p.proconfig),false)
       )
     ) THEN RAISE EXCEPTION 'New API source bridge preflight failed'; END IF;
END
$contract$;

DO $contract$
DECLARE role_name text;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='invoice_newapi_bridge_owner') THEN
    RAISE EXCEPTION 'apply New API source bridge before economic bridge';
  END IF;
  FOREACH role_name IN ARRAY ARRAY[
    'invoice_newapi_payments_v3_reader','invoice_newapi_usage_reader',
    'invoice_newapi_credits_reader','invoice_newapi_balances_reader'
  ] LOOP
    EXECUTE format('ALTER ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 2',role_name);
    EXECUTE format('ALTER ROLE %I SET default_transaction_read_only=on',role_name);
    EXECUTE format('ALTER ROLE %I SET statement_timeout=''15s''',role_name);
    EXECUTE format('ALTER ROLE %I SET lock_timeout=''5s''',role_name);
    EXECUTE format('ALTER ROLE %I SET idle_in_transaction_session_timeout=''15s''',role_name);
    EXECUTE format('GRANT CONNECT ON DATABASE %I TO %I',current_database(),role_name);
    EXECUTE format('REVOKE CREATE ON SCHEMA public FROM %I',role_name);
    EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA public FROM %I',role_name);
    EXECUTE format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM %I',role_name);
    EXECUTE format('REVOKE TEMPORARY ON DATABASE %I FROM %I',current_database(),role_name);
    EXECUTE format('GRANT USAGE ON SCHEMA invoice_bridge TO %I',role_name);
    IF has_database_privilege(role_name,current_database(),'TEMP') THEN RAISE EXCEPTION '% retains TEMP',role_name; END IF;
    IF EXISTS (
      SELECT 1 FROM pg_auth_members m
      WHERE m.member=(SELECT oid FROM pg_roles WHERE rolname=role_name)
         OR m.roleid=(SELECT oid FROM pg_roles WHERE rolname=role_name)
    ) THEN RAISE EXCEPTION '% has a role membership in either direction',role_name; END IF;
  END LOOP;
END
$contract$;

DO $relations$
DECLARE owner_oid oid; expected text[] := ARRAY[
  'top_ups','subscription_orders','options','custom_oauth_providers',
  'user_oauth_bindings','logs','checkins','redemptions','users'];
relation_count integer; unsafe_count integer;
BEGIN
  SELECT oid INTO owner_oid FROM pg_roles WHERE rolname='invoice_newapi_bridge_owner';
  SELECT count(*),count(*) FILTER (
    WHERE c.relrowsecurity OR c.relforcerowsecurity OR c.relowner=owner_oid
  ) INTO relation_count,unsafe_count
  FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname='public' AND c.relkind IN ('r','p') AND c.relname=ANY(expected);
  IF relation_count<>array_length(expected,1) OR unsafe_count<>0 THEN
    RAISE EXCEPTION 'New API economic bridge relations are missing, role-owned or protected by unreviewed RLS';
  END IF;
END $relations$;

GRANT SELECT(id,user_id,created_at,type,quota) ON public.logs TO invoice_newapi_bridge_owner;
GRANT SELECT(id,user_id,quota_awarded,created_at) ON public.checkins TO invoice_newapi_bridge_owner;
GRANT SELECT(id,status,quota,redeemed_time,used_user_id) ON public.redemptions TO invoice_newapi_bridge_owner;
GRANT SELECT(id,quota,deleted_at) ON public.users TO invoice_newapi_bridge_owner;

CREATE OR REPLACE FUNCTION invoice_bridge.newapi_usage_v4(operation text,request jsonb)
RETURNS SETOF jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=on
AS $bridge$
DECLARE requested_limit integer;
BEGIN
  IF request IS NULL OR jsonb_typeof(request)<>'object' THEN RAISE EXCEPTION 'invalid bridge request'; END IF;
  IF operation='health' THEN
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        WITH cfg AS (
          SELECT trim_scale(btrim(COALESCE((SELECT value FROM public.options WHERE key='QuotaPerUnit'),'500000'))::numeric)::text AS quota_per_unit,
            trim_scale(btrim(COALESCE((SELECT value FROM public.options WHERE key='Price'),'1'))::numeric)::text AS price,
            'ALL_GROUP_RATIOS_ONE'::text AS topup_group_ratio_semantics,
            ((SELECT count(*) FROM public.options WHERE key='QuotaPerUnit')<=1
             AND btrim(COALESCE((SELECT value FROM public.options WHERE key='QuotaPerUnit'),'500000'))::numeric=500000
             AND (SELECT count(*) FROM public.options WHERE key='Price')<=1
             AND btrim(COALESCE((SELECT value FROM public.options WHERE key='Price'),'1'))::numeric=1
             AND (SELECT count(*) FROM public.options WHERE key='TopupGroupRatio')<=1
             AND jsonb_typeof(COALESCE((SELECT value FROM public.options WHERE key='TopupGroupRatio'),'{}')::jsonb)='object'
             AND NOT EXISTS (SELECT 1 FROM jsonb_each_text(COALESCE((SELECT value FROM public.options WHERE key='TopupGroupRatio'),'{}')::jsonb)
               WHERE value IS NULL OR btrim(value)::numeric<>1)) AS contract_ok
        ), stats AS (
          SELECT count(*)::bigint AS total_rows,COALESCE(max(id)-min(id)+1-count(*),0)::bigint AS gap_count,
            count(*) FILTER (WHERE type=2 AND quota<0)::bigint AS invalid_rows FROM public.logs
        ), logging AS (
          SELECT COALESCE((SELECT value FROM public.options WHERE key='LogConsumeEnabled'),'true')='true' AS enabled,
            (SELECT count(*) FROM public.options WHERE key='LogConsumeEnabled')<=1 AS singleton
        )
        SELECT (logging.enabled AND logging.singleton AND stats.invalid_rows=0 AND cfg.contract_ok) AS contract_ok,
          CASE WHEN NOT logging.singleton THEN 'log_consume_option_duplicate'
               WHEN NOT logging.enabled THEN 'consume_logging_disabled'
               WHEN NOT cfg.contract_ok THEN 'wallet_configuration_invalid'
               WHEN stats.invalid_rows>0 THEN 'consume_log_contract_invalid' ELSE '' END::text AS blocked_reason,
          stats.total_rows,stats.gap_count,
          encode(sha256(convert_to(quota_per_unit||'|'||price||'|'||topup_group_ratio_semantics||'|NEWAPI_QUOTA|rc.25|v4','UTF8')),'hex') AS configuration_hash
        FROM cfg,stats,logging
      ) result
    $query$;
    RETURN;
  ELSIF operation='ceiling' THEN
    IF request->>'domain'<>'logs' THEN RAISE EXCEPTION 'invalid usage domain'; END IF;
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        SELECT COALESCE(CASE WHEN min(id) FILTER (WHERE to_timestamp(created_at)>$1::timestamptz) IS NOT NULL
          THEN min(id) FILTER (WHERE to_timestamp(created_at)>$1::timestamptz)-1 ELSE max(id) END,$2::bigint)::bigint AS source_id
        FROM public.logs WHERE type=2 AND quota>0 AND id>$2::bigint
      ) result
    $query$ USING request->>'horizon',request->>'position';
    RETURN;
  ELSIF operation='page' THEN
    requested_limit := (request->>'limit')::integer;
    IF requested_limit<1 OR requested_limit>500 THEN RAISE EXCEPTION 'invalid bridge limit'; END IF;
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        SELECT id AS source_id,user_id,to_timestamp(created_at) AS event_time,
          quota::numeric(78,0)::text AS service_units,NULL::text AS credit_kind,
          'logs'::text AS causal_domain,('logs:'||id::text)::text AS source_cursor
        FROM public.logs WHERE type=2 AND quota>0
          AND id>$1::bigint AND id<=$2::bigint
          AND to_timestamp(created_at)>=$3::timestamptz AND to_timestamp(created_at)<=$4::timestamptz
        ORDER BY id LIMIT $5
      ) result
    $query$ USING request->>'logs_position',request->>'logs_ceiling',request->>'cutover',request->>'horizon',requested_limit;
    RETURN;
  END IF;
  RAISE EXCEPTION 'unsupported New API usage bridge operation';
END
$bridge$;

CREATE OR REPLACE FUNCTION invoice_bridge.newapi_credits_v4(operation text,request jsonb)
RETURNS SETOF jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=on
AS $bridge$
DECLARE requested_limit integer; requested_domain text;
BEGIN
  IF request IS NULL OR jsonb_typeof(request)<>'object' THEN RAISE EXCEPTION 'invalid bridge request'; END IF;
  IF operation='health' THEN
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        WITH cfg AS (
          SELECT trim_scale(btrim(COALESCE((SELECT value FROM public.options WHERE key='QuotaPerUnit'),'500000'))::numeric)::text AS quota_per_unit,
            trim_scale(btrim(COALESCE((SELECT value FROM public.options WHERE key='Price'),'1'))::numeric)::text AS price,
            'ALL_GROUP_RATIOS_ONE'::text AS topup_group_ratio_semantics,
            ((SELECT count(*) FROM public.options WHERE key='QuotaPerUnit')<=1
             AND btrim(COALESCE((SELECT value FROM public.options WHERE key='QuotaPerUnit'),'500000'))::numeric=500000
             AND (SELECT count(*) FROM public.options WHERE key='Price')<=1
             AND btrim(COALESCE((SELECT value FROM public.options WHERE key='Price'),'1'))::numeric=1
             AND (SELECT count(*) FROM public.options WHERE key='TopupGroupRatio')<=1
             AND jsonb_typeof(COALESCE((SELECT value FROM public.options WHERE key='TopupGroupRatio'),'{}')::jsonb)='object'
             AND NOT EXISTS (SELECT 1 FROM jsonb_each_text(COALESCE((SELECT value FROM public.options WHERE key='TopupGroupRatio'),'{}')::jsonb)
               WHERE value IS NULL OR btrim(value)::numeric<>1)) AS contract_ok
        ), c AS (
          SELECT count(*)::bigint n,COALESCE(max(id)-min(id)+1-count(*),0)::bigint gaps,
            count(*) FILTER (WHERE quota_awarded<=0)::bigint invalid FROM public.checkins
        ), r AS (
          SELECT count(*)::bigint n,COALESCE(max(id)-min(id)+1-count(*),0)::bigint gaps,
            count(*) FILTER (WHERE quota<0)::bigint invalid FROM public.redemptions
        ), stats AS (
          SELECT c.n+r.n AS total_rows,c.gaps+r.gaps AS gap_count,c.invalid+r.invalid AS invalid_rows FROM c,r
        )
        SELECT (stats.invalid_rows=0 AND cfg.contract_ok) AS contract_ok,
          CASE WHEN NOT cfg.contract_ok THEN 'wallet_configuration_invalid'
               WHEN stats.invalid_rows>0 THEN 'credit_contract_invalid' ELSE '' END::text AS blocked_reason,
          stats.total_rows,stats.gap_count,
          encode(sha256(convert_to(quota_per_unit||'|'||price||'|'||topup_group_ratio_semantics||'|NEWAPI_QUOTA|rc.25|v4','UTF8')),'hex') AS configuration_hash
        FROM cfg,stats
      ) result
    $query$;
    RETURN;
  ELSIF operation='ceiling' THEN
    requested_domain := request->>'domain';
    IF requested_domain NOT IN ('checkins','redemptions') THEN RAISE EXCEPTION 'invalid credit domain'; END IF;
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        WITH projected AS (
          SELECT id AS source_id,to_timestamp(created_at) AS event_time,'checkins'::text AS causal_domain
            FROM public.checkins WHERE quota_awarded>0
          UNION ALL
          SELECT id,to_timestamp(redeemed_time),'redemptions'::text FROM public.redemptions
            WHERE status=3 AND used_user_id>0 AND redeemed_time>0 AND quota>0
        )
        SELECT COALESCE(max(source_id) FILTER (WHERE event_time>=$1::timestamptz AND event_time<=$2::timestamptz),$3::bigint)::bigint AS source_id
        FROM projected WHERE causal_domain=$4 AND source_id>$3::bigint
      ) result
    $query$ USING request->>'cutover',request->>'horizon',request->>'position',requested_domain;
    RETURN;
  ELSIF operation='page' THEN
    requested_limit := (request->>'limit')::integer;
    IF requested_limit<1 OR requested_limit>500 THEN RAISE EXCEPTION 'invalid bridge limit'; END IF;
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        WITH projected AS (
          SELECT id AS source_id,user_id,to_timestamp(created_at) AS event_time,
            quota_awarded::numeric(78,0)::text AS service_units,'bonus'::text AS credit_kind,
            'checkins'::text AS causal_domain,('checkins:'||id::text)::text AS source_cursor
          FROM public.checkins WHERE quota_awarded>0
          UNION ALL
          SELECT id,used_user_id,to_timestamp(redeemed_time),quota::numeric(78,0)::text,'bonus'::text,
            'redemptions'::text,('redemptions:'||id::text)::text
          FROM public.redemptions WHERE status=3 AND used_user_id>0 AND redeemed_time>0 AND quota>0
        )
        SELECT * FROM projected
        WHERE event_time>=$1::timestamptz AND event_time<=$2::timestamptz AND (
          (causal_domain='checkins' AND source_id>$3::bigint AND source_id<=$4::bigint) OR
          (causal_domain='redemptions' AND source_id>$5::bigint AND source_id<=$6::bigint))
        ORDER BY causal_domain,source_id LIMIT $7
      ) result
    $query$ USING request->>'cutover',request->>'horizon',request->>'checkins_position',request->>'checkins_ceiling',
      request->>'redemptions_position',request->>'redemptions_ceiling',requested_limit;
    RETURN;
  END IF;
  RAISE EXCEPTION 'unsupported New API credits bridge operation';
END
$bridge$;

CREATE OR REPLACE FUNCTION invoice_bridge.newapi_balances_v4(operation text,request jsonb)
RETURNS SETOF jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=on
AS $bridge$
BEGIN
  IF request IS NULL OR jsonb_typeof(request)<>'object' THEN RAISE EXCEPTION 'invalid bridge request'; END IF;
  IF operation='rows' THEN
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        SELECT id AS user_id,GREATEST(quota,0)::numeric(78,0)::text AS balance_service_units,
          (quota<0) AS balance_negative FROM public.users WHERE deleted_at IS NULL ORDER BY id
      ) result
    $query$;
    RETURN;
  ELSIF operation='contract' THEN
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        WITH cfg AS (
          SELECT trim_scale(btrim(COALESCE((SELECT value FROM public.options WHERE key='QuotaPerUnit'),'500000'))::numeric)::text AS quota_per_unit,
            trim_scale(btrim(COALESCE((SELECT value FROM public.options WHERE key='Price'),'1'))::numeric)::text AS price,
            'ALL_GROUP_RATIOS_ONE'::text AS topup_group_ratio_semantics,
            ((SELECT count(*) FROM public.options WHERE key='QuotaPerUnit')<=1
             AND btrim(COALESCE((SELECT value FROM public.options WHERE key='QuotaPerUnit'),'500000'))::numeric=500000
             AND (SELECT count(*) FROM public.options WHERE key='Price')<=1
             AND btrim(COALESCE((SELECT value FROM public.options WHERE key='Price'),'1'))::numeric=1
             AND (SELECT count(*) FROM public.options WHERE key='TopupGroupRatio')<=1
             AND jsonb_typeof(COALESCE((SELECT value FROM public.options WHERE key='TopupGroupRatio'),'{}')::jsonb)='object'
             AND NOT EXISTS (SELECT 1 FROM jsonb_each_text(COALESCE((SELECT value FROM public.options WHERE key='TopupGroupRatio'),'{}')::jsonb)
               WHERE value IS NULL OR btrim(value)::numeric<>1)
             AND (SELECT count(*) FROM public.options WHERE key='LogConsumeEnabled')<=1
             AND COALESCE((SELECT value FROM public.options WHERE key='LogConsumeEnabled'),'true')='true') AS contract_ok
        ), payment_high AS (
          SELECT concat('subscription_orders:',COALESCE((SELECT max(id) FROM public.subscription_orders),0),
            ';top_ups:',COALESCE((SELECT max(id) FROM public.top_ups),0)) AS cursor,
            to_timestamp(GREATEST(COALESCE((SELECT max(GREATEST(create_time,complete_time)) FROM public.top_ups),0),
              COALESCE((SELECT max(GREATEST(create_time,complete_time)) FROM public.subscription_orders),0))) AS at
        ), usage_high AS (
          SELECT COALESCE(to_timestamp(max(created_at)),transaction_timestamp()) AS at,
            ('logs:'||COALESCE(max(id),0)::text)::text AS cursor FROM public.logs
        ), credit_high AS (
          SELECT concat('checkins:',COALESCE((SELECT max(id) FROM public.checkins),0),
            ';redemptions:',COALESCE((SELECT max(id) FROM public.redemptions),0)) AS cursor,
            to_timestamp(GREATEST(COALESCE((SELECT max(created_at) FROM public.checkins),0),
              COALESCE((SELECT max(redeemed_time) FROM public.redemptions),0))) AS at
        )
        SELECT 'newapi-economic-rc25-v4'::text AS projection_contract,cfg.contract_ok,
          encode(sha256(convert_to(quota_per_unit||'|'||price||'|'||topup_group_ratio_semantics||'|NEWAPI_QUOTA|rc.25|v4','UTF8')),'hex') AS configuration_hash,
          COALESCE(payment_high.at,transaction_timestamp()) AS payments_event_at,payment_high.cursor::text AS payments_cursor,
          usage_high.at AS usage_event_at,usage_high.cursor::text AS usage_cursor,
          COALESCE(credit_high.at,transaction_timestamp()) AS credits_event_at,credit_high.cursor::text AS credits_cursor
        FROM cfg,payment_high,usage_high,credit_high
      ) result
    $query$;
    RETURN;
  END IF;
  RAISE EXCEPTION 'unsupported New API balances bridge operation';
END
$bridge$;

ALTER FUNCTION invoice_bridge.newapi_usage_v4(text,jsonb) OWNER TO invoice_newapi_bridge_owner;
ALTER FUNCTION invoice_bridge.newapi_credits_v4(text,jsonb) OWNER TO invoice_newapi_bridge_owner;
ALTER FUNCTION invoice_bridge.newapi_balances_v4(text,jsonb) OWNER TO invoice_newapi_bridge_owner;
DO $contract$
DECLARE role_name text; function_name text;
BEGIN
  FOREACH function_name IN ARRAY ARRAY['newapi_payments_v4','newapi_usage_v4','newapi_credits_v4','newapi_balances_v4','newapi_identities_v4'] LOOP
    EXECUTE format('REVOKE ALL ON FUNCTION invoice_bridge.%I(text,jsonb) FROM PUBLIC',function_name);
    FOREACH role_name IN ARRAY ARRAY['invoice_newapi_payments_reader','invoice_newapi_identities_reader','invoice_newapi_payments_v3_reader','invoice_newapi_usage_reader','invoice_newapi_credits_reader','invoice_newapi_balances_reader'] LOOP
      EXECUTE format('REVOKE ALL ON FUNCTION invoice_bridge.%I(text,jsonb) FROM %I',function_name,role_name);
    END LOOP;
  END LOOP;
  GRANT EXECUTE ON FUNCTION invoice_bridge.newapi_payments_v4(text,jsonb) TO invoice_newapi_payments_reader,invoice_newapi_payments_v3_reader;
  GRANT EXECUTE ON FUNCTION invoice_bridge.newapi_identities_v4(text,jsonb) TO invoice_newapi_identities_reader;
  GRANT EXECUTE ON FUNCTION invoice_bridge.newapi_usage_v4(text,jsonb) TO invoice_newapi_usage_reader;
  GRANT EXECUTE ON FUNCTION invoice_bridge.newapi_credits_v4(text,jsonb) TO invoice_newapi_credits_reader;
  GRANT EXECUTE ON FUNCTION invoice_bridge.newapi_balances_v4(text,jsonb) TO invoice_newapi_balances_reader;
END
$contract$;
REVOKE ALL ON FUNCTION invoice_bridge.newapi_usage_v4(text,jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION invoice_bridge.newapi_credits_v4(text,jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION invoice_bridge.newapi_balances_v4(text,jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION invoice_bridge.newapi_payments_v4(text,jsonb) TO invoice_newapi_payments_v3_reader;
GRANT EXECUTE ON FUNCTION invoice_bridge.newapi_usage_v4(text,jsonb) TO invoice_newapi_usage_reader;
GRANT EXECUTE ON FUNCTION invoice_bridge.newapi_credits_v4(text,jsonb) TO invoice_newapi_credits_reader;
GRANT EXECUTE ON FUNCTION invoice_bridge.newapi_balances_v4(text,jsonb) TO invoice_newapi_balances_reader;

COMMIT;
