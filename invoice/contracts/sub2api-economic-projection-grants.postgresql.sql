-- REVIEW-ONLY SUB2API ECONOMIC BRIDGE V4.
-- Requires sub2api-source-projection-grants.postgresql.sql and the four named
-- LOGIN reader roles. It creates functions only, never source-dependent views.
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
    RAISE EXCEPTION 'Sub2API economic bridge install requires the cluster superuser that owns the current database';
  END IF;
END $executor$;

DO $contract$
DECLARE owner_oid oid; bridge_namespace oid;
BEGIN
  SELECT oid INTO owner_oid FROM pg_roles
    WHERE rolname='invoice_sub2api_bridge_owner' AND NOT rolcanlogin AND NOT rolsuper
      AND NOT rolcreatedb AND NOT rolcreaterole AND NOT rolinherit
      AND NOT rolreplication AND NOT rolbypassrls AND rolconnlimit=0;
  SELECT oid INTO bridge_namespace FROM pg_namespace WHERE nspname='invoice_bridge';
  IF owner_oid IS NULL OR bridge_namespace IS NULL
     OR to_regprocedure('invoice_bridge.sub2api_payments_v4(text,jsonb)') IS NULL
     OR to_regprocedure('invoice_bridge.sub2api_identities_v4(text,jsonb)') IS NULL THEN
    RAISE EXCEPTION 'verified Sub2API source bridge is required';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_auth_members WHERE member=owner_oid OR roleid=owner_oid)
     OR EXISTS (SELECT 1 FROM pg_class WHERE relnamespace=bridge_namespace)
     OR EXISTS (
       SELECT 1 FROM pg_proc p JOIN pg_language l ON l.oid=p.prolang
       WHERE p.pronamespace=bridge_namespace AND (
         p.proname NOT IN ('sub2api_payments_v4','sub2api_usage_v4','sub2api_credits_v4','sub2api_balances_v4','sub2api_identities_v4')
         OR oidvectortypes(p.proargtypes)<>'text, jsonb' OR p.prorettype<>'jsonb'::regtype
         OR NOT p.proretset OR NOT p.prosecdef OR l.lanname<>'plpgsql' OR p.proowner<>owner_oid
         OR NOT COALESCE('search_path=pg_catalog'=ANY(p.proconfig),false)
         OR NOT COALESCE('row_security=on'=ANY(p.proconfig),false)
       )
     ) THEN RAISE EXCEPTION 'Sub2API source bridge preflight failed'; END IF;
END
$contract$;

DO $contract$
DECLARE role_name text;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='invoice_sub2api_bridge_owner') THEN
    RAISE EXCEPTION 'apply Sub2API source bridge before economic bridge';
  END IF;
  FOREACH role_name IN ARRAY ARRAY[
    'invoice_sub2api_payments_v3_reader','invoice_sub2api_usage_reader',
    'invoice_sub2api_credits_reader','invoice_sub2api_balances_reader'
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
  'payment_orders','settings','auth_identities','usage_logs',
  'promo_code_usages','user_affiliate_ledger','redeem_codes','users'];
relation_count integer; unsafe_count integer;
BEGIN
  SELECT oid INTO owner_oid FROM pg_roles WHERE rolname='invoice_sub2api_bridge_owner';
  SELECT count(*),count(*) FILTER (
    WHERE c.relrowsecurity OR c.relforcerowsecurity OR c.relowner=owner_oid
  ) INTO relation_count,unsafe_count
  FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname='public' AND c.relkind IN ('r','p') AND c.relname=ANY(expected);
  IF relation_count<>array_length(expected,1) OR unsafe_count<>0 THEN
    RAISE EXCEPTION 'Sub2API economic bridge relations are missing, role-owned or protected by unreviewed RLS';
  END IF;
END $relations$;

GRANT SELECT(id,user_id,billing_type,actual_cost,created_at)
  ON public.usage_logs TO invoice_sub2api_bridge_owner;
GRANT SELECT(id,user_id,bonus_amount,used_at)
  ON public.promo_code_usages TO invoice_sub2api_bridge_owner;
GRANT SELECT(id,user_id,action,amount,created_at)
  ON public.user_affiliate_ledger TO invoice_sub2api_bridge_owner;
GRANT SELECT(id,code,type,value,status,used_by,used_at)
  ON public.redeem_codes TO invoice_sub2api_bridge_owner;
GRANT SELECT(recharge_code)
  ON public.payment_orders TO invoice_sub2api_bridge_owner;
GRANT SELECT(id,balance,deleted_at)
  ON public.users TO invoice_sub2api_bridge_owner;

CREATE OR REPLACE FUNCTION invoice_bridge.sub2api_usage_v4(operation text,request jsonb)
RETURNS SETOF jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=on
AS $bridge$
DECLARE requested_limit integer;
BEGIN
  IF request IS NULL OR jsonb_typeof(request)<>'object' THEN RAISE EXCEPTION 'invalid bridge request'; END IF;
  IF operation='health' THEN
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        WITH raw_cfg AS (
          SELECT max(value) FILTER (WHERE key='BALANCE_RECHARGE_MULTIPLIER')::text AS multiplier_raw,
            max(value) FILTER (WHERE key='RECHARGE_FEE_RATE')::text AS fee_rate_raw,
            count(*) FILTER (WHERE key='BALANCE_RECHARGE_MULTIPLIER') AS multiplier_count,
            count(*) FILTER (WHERE key='RECHARGE_FEE_RATE') AS fee_rate_count
          FROM public.settings WHERE key IN ('BALANCE_RECHARGE_MULTIPLIER','RECHARGE_FEE_RATE')
        ), cfg AS (
          SELECT CASE WHEN COALESCE(btrim(multiplier_raw)~'^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)$',FALSE)
                 THEN trim_scale(btrim(multiplier_raw)::numeric)::text END AS multiplier_value,
            CASE WHEN COALESCE(btrim(fee_rate_raw)~'^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)$',FALSE)
                 THEN trim_scale(btrim(fee_rate_raw)::numeric)::text END AS fee_rate_value,
            (multiplier_count=1 AND fee_rate_count=1
             AND COALESCE(btrim(multiplier_raw)~'^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)$',FALSE)
             AND COALESCE(btrim(fee_rate_raw)~'^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)$',FALSE)
             AND CASE WHEN COALESCE(btrim(fee_rate_raw)~'^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)$',FALSE)
                  THEN btrim(fee_rate_raw)::numeric BETWEEN 0 AND 100
                    AND scale(trim_scale(btrim(fee_rate_raw)::numeric))<=2 ELSE FALSE END
             AND CASE WHEN COALESCE(btrim(multiplier_raw)~'^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)$',FALSE)
                  THEN btrim(multiplier_raw)::numeric=1 ELSE FALSE END) AS contract_ok
          FROM raw_cfg
        ), stats AS (
          SELECT count(*)::bigint AS total_rows,COALESCE(max(id)-min(id)+1-count(*),0)::bigint AS gap_count,
            count(*) FILTER (WHERE billing_type NOT IN (0,1) OR actual_cost<0)::bigint AS invalid_rows
          FROM public.usage_logs
        )
        SELECT (stats.invalid_rows=0 AND cfg.contract_ok) AS contract_ok,
          CASE WHEN NOT cfg.contract_ok THEN 'wallet_configuration_invalid'
               WHEN invalid_rows>0 THEN 'usage_contract_invalid' ELSE '' END::text AS blocked_reason,
          total_rows,gap_count,
          encode(sha256(convert_to(COALESCE(multiplier_value,'<invalid>')||'|'||
            COALESCE(fee_rate_value,'<invalid>')||'|SUB2_BALANCE_1E8|v4','UTF8')),'hex') AS configuration_hash
        FROM stats,cfg
      ) result
    $query$;
    RETURN;
  ELSIF operation='ceiling' THEN
    IF request->>'domain'<>'usage_logs' THEN RAISE EXCEPTION 'invalid usage domain'; END IF;
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        SELECT COALESCE(CASE WHEN min(id) FILTER (WHERE created_at>$1::timestamptz) IS NOT NULL
          THEN min(id) FILTER (WHERE created_at>$1::timestamptz)-1 ELSE max(id) END,$2::bigint)::bigint AS source_id
        FROM public.usage_logs
        WHERE billing_type=0 AND round(actual_cost,8)>0 AND id>$2::bigint
      ) result
    $query$ USING request->>'horizon',request->>'position';
    RETURN;
  ELSIF operation='page' THEN
    requested_limit := (request->>'limit')::integer;
    IF requested_limit<1 OR requested_limit>500 THEN RAISE EXCEPTION 'invalid bridge limit'; END IF;
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        SELECT id AS source_id,user_id,created_at AS event_time,
          ((round(actual_cost,8)*100000000)::numeric(78,0))::text AS service_units,
          NULL::text AS credit_kind,'usage_logs'::text AS causal_domain,
          ('usage_logs:'||id::text)::text AS source_cursor
        FROM public.usage_logs
        WHERE billing_type=0 AND round(actual_cost,8)>0
          AND id>$1::bigint AND id<=$2::bigint
          AND created_at>=$3::timestamptz AND created_at<=$4::timestamptz
        ORDER BY id LIMIT $5
      ) result
    $query$ USING request->>'usage_logs_position',request->>'usage_logs_ceiling',
      request->>'cutover',request->>'horizon',requested_limit;
    RETURN;
  END IF;
  RAISE EXCEPTION 'unsupported Sub2API usage bridge operation';
END
$bridge$;

CREATE OR REPLACE FUNCTION invoice_bridge.sub2api_credits_v4(operation text,request jsonb)
RETURNS SETOF jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=on
AS $bridge$
DECLARE requested_limit integer; requested_domain text;
BEGIN
  IF request IS NULL OR jsonb_typeof(request)<>'object' THEN RAISE EXCEPTION 'invalid bridge request'; END IF;
  IF operation='health' THEN
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        WITH raw_cfg AS (
          SELECT max(value) FILTER (WHERE key='BALANCE_RECHARGE_MULTIPLIER')::text AS multiplier_raw,
            max(value) FILTER (WHERE key='RECHARGE_FEE_RATE')::text AS fee_rate_raw,
            count(*) FILTER (WHERE key='BALANCE_RECHARGE_MULTIPLIER') AS multiplier_count,
            count(*) FILTER (WHERE key='RECHARGE_FEE_RATE') AS fee_rate_count
          FROM public.settings WHERE key IN ('BALANCE_RECHARGE_MULTIPLIER','RECHARGE_FEE_RATE')
        ), cfg AS (
          SELECT CASE WHEN COALESCE(btrim(multiplier_raw)~'^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)$',FALSE)
                 THEN trim_scale(btrim(multiplier_raw)::numeric)::text END AS multiplier_value,
            CASE WHEN COALESCE(btrim(fee_rate_raw)~'^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)$',FALSE)
                 THEN trim_scale(btrim(fee_rate_raw)::numeric)::text END AS fee_rate_value,
            (multiplier_count=1 AND fee_rate_count=1
             AND COALESCE(btrim(multiplier_raw)~'^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)$',FALSE)
             AND COALESCE(btrim(fee_rate_raw)~'^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)$',FALSE)
             AND CASE WHEN COALESCE(btrim(fee_rate_raw)~'^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)$',FALSE)
                  THEN btrim(fee_rate_raw)::numeric BETWEEN 0 AND 100
                    AND scale(trim_scale(btrim(fee_rate_raw)::numeric))<=2 ELSE FALSE END
             AND CASE WHEN COALESCE(btrim(multiplier_raw)~'^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)$',FALSE)
                  THEN btrim(multiplier_raw)::numeric=1 ELSE FALSE END) AS contract_ok
          FROM raw_cfg
        ), p AS (
          SELECT count(*)::bigint n,COALESCE(max(id)-min(id)+1-count(*),0)::bigint gaps,
            count(*) FILTER (WHERE bonus_amount<=0)::bigint invalid FROM public.promo_code_usages
        ), a AS (
          SELECT count(*)::bigint n,COALESCE(max(id)-min(id)+1-count(*),0)::bigint gaps,
            count(*) FILTER (WHERE action NOT IN ('accrue','transfer') OR amount<=0)::bigint invalid FROM public.user_affiliate_ledger
        ), r AS (
          SELECT count(*)::bigint n,COALESCE(max(id)-min(id)+1-count(*),0)::bigint gaps,
            count(*) FILTER (WHERE type='balance' AND status='used' AND used_by IS NOT NULL AND used_at IS NOT NULL AND value<=0)::bigint invalid
          FROM public.redeem_codes
        ), stats AS (
          SELECT p.n+a.n+r.n AS total_rows,p.gaps+a.gaps+r.gaps AS gap_count,p.invalid+a.invalid+r.invalid AS invalid_rows FROM p,a,r
        )
        SELECT (stats.invalid_rows=0 AND cfg.contract_ok) AS contract_ok,
          CASE WHEN NOT cfg.contract_ok THEN 'wallet_configuration_invalid'
               WHEN invalid_rows>0 THEN 'credit_contract_invalid' ELSE '' END::text AS blocked_reason,
          total_rows,gap_count,
          encode(sha256(convert_to(COALESCE(multiplier_value,'<invalid>')||'|'||
            COALESCE(fee_rate_value,'<invalid>')||'|SUB2_BALANCE_1E8|v4','UTF8')),'hex') AS configuration_hash
        FROM stats,cfg
      ) result
    $query$;
    RETURN;
  ELSIF operation='ceiling' THEN
    requested_domain := request->>'domain';
    IF requested_domain NOT IN ('promo_code_usages','user_affiliate_ledger','redeem_codes') THEN RAISE EXCEPTION 'invalid credit domain'; END IF;
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        WITH projected AS (
          SELECT id AS source_id,used_at AS event_time,'promo_code_usages'::text AS causal_domain
          FROM public.promo_code_usages WHERE bonus_amount>0
          UNION ALL
          SELECT id,created_at,'user_affiliate_ledger'::text FROM public.user_affiliate_ledger WHERE action='transfer' AND amount>0
          UNION ALL
          SELECT rc.id,rc.used_at,'redeem_codes'::text FROM public.redeem_codes rc
          WHERE rc.type='balance' AND rc.used_by IS NOT NULL AND rc.used_at IS NOT NULL AND rc.status='used' AND rc.value>0
            AND NOT EXISTS (SELECT 1 FROM public.payment_orders po WHERE po.recharge_code=rc.code)
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
          SELECT id AS source_id,user_id,used_at AS event_time,
            ((bonus_amount*100000000)::numeric(78,0))::text AS service_units,
            'bonus'::text AS credit_kind,'promo_code_usages'::text AS causal_domain,
            ('promo_code_usages:'||id::text)::text AS source_cursor
          FROM public.promo_code_usages WHERE bonus_amount>0
          UNION ALL
          SELECT id,user_id,created_at,((amount*100000000)::numeric(78,0))::text,
            'rebate'::text,'user_affiliate_ledger'::text,('user_affiliate_ledger:'||id::text)::text
          FROM public.user_affiliate_ledger WHERE action='transfer' AND amount>0
          UNION ALL
          SELECT rc.id,rc.used_by,rc.used_at,((rc.value*100000000)::numeric(78,0))::text,
            'bonus'::text,'redeem_codes'::text,('redeem_codes:'||rc.id::text)::text
          FROM public.redeem_codes rc
          WHERE rc.type='balance' AND rc.used_by IS NOT NULL AND rc.used_at IS NOT NULL AND rc.status='used' AND rc.value>0
            AND NOT EXISTS (SELECT 1 FROM public.payment_orders po WHERE po.recharge_code=rc.code)
        )
        SELECT * FROM projected
        WHERE event_time>=$1::timestamptz AND event_time<=$2::timestamptz AND (
          (causal_domain='promo_code_usages' AND source_id>$3::bigint AND source_id<=$4::bigint) OR
          (causal_domain='user_affiliate_ledger' AND source_id>$5::bigint AND source_id<=$6::bigint) OR
          (causal_domain='redeem_codes' AND source_id>$7::bigint AND source_id<=$8::bigint))
        ORDER BY causal_domain,source_id LIMIT $9
      ) result
    $query$ USING request->>'cutover',request->>'horizon',
      request->>'promo_code_usages_position',request->>'promo_code_usages_ceiling',
      request->>'user_affiliate_ledger_position',request->>'user_affiliate_ledger_ceiling',
      request->>'redeem_codes_position',request->>'redeem_codes_ceiling',requested_limit;
    RETURN;
  END IF;
  RAISE EXCEPTION 'unsupported Sub2API credits bridge operation';
END
$bridge$;

CREATE OR REPLACE FUNCTION invoice_bridge.sub2api_balances_v4(operation text,request jsonb)
RETURNS SETOF jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=on
AS $bridge$
BEGIN
  IF request IS NULL OR jsonb_typeof(request)<>'object' THEN RAISE EXCEPTION 'invalid bridge request'; END IF;
  IF operation='rows' THEN
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        SELECT id AS user_id,((GREATEST(balance,0)*100000000)::numeric(78,0))::text AS balance_service_units,
          (balance<0) AS balance_negative,
          ((GREATEST(-balance,0)*100000000)::numeric(78,0))::text AS deficit_service_units
        FROM public.users WHERE deleted_at IS NULL ORDER BY id
      ) result
    $query$;
    RETURN;
  ELSIF operation='contract' THEN
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        WITH raw_cfg AS (
          SELECT max(value) FILTER (WHERE key='BALANCE_RECHARGE_MULTIPLIER')::text AS multiplier_raw,
            max(value) FILTER (WHERE key='RECHARGE_FEE_RATE')::text AS fee_rate_raw,
            count(*) FILTER (WHERE key='BALANCE_RECHARGE_MULTIPLIER') AS multiplier_count,
            count(*) FILTER (WHERE key='RECHARGE_FEE_RATE') AS fee_rate_count
          FROM public.settings WHERE key IN ('BALANCE_RECHARGE_MULTIPLIER','RECHARGE_FEE_RATE')
        ), cfg AS (
          SELECT CASE WHEN COALESCE(btrim(multiplier_raw)~'^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)$',FALSE)
                 THEN trim_scale(btrim(multiplier_raw)::numeric)::text END AS multiplier_value,
            CASE WHEN COALESCE(btrim(fee_rate_raw)~'^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)$',FALSE)
                 THEN trim_scale(btrim(fee_rate_raw)::numeric)::text END AS fee_rate_value,
            (multiplier_count=1 AND fee_rate_count=1
             AND COALESCE(btrim(multiplier_raw)~'^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)$',FALSE)
             AND COALESCE(btrim(fee_rate_raw)~'^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)$',FALSE)
             AND CASE WHEN COALESCE(btrim(fee_rate_raw)~'^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)$',FALSE)
                  THEN btrim(fee_rate_raw)::numeric BETWEEN 0 AND 100
                    AND scale(trim_scale(btrim(fee_rate_raw)::numeric))<=2 ELSE FALSE END
             AND CASE WHEN COALESCE(btrim(multiplier_raw)~'^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)$',FALSE)
                  THEN btrim(multiplier_raw)::numeric=1 ELSE FALSE END) AS contract_ok
          FROM raw_cfg
        ), payment_high AS (
          SELECT COALESCE(updated_at,transaction_timestamp()) AS at,COALESCE(id,0)::bigint AS id
          FROM (SELECT updated_at,id FROM public.payment_orders ORDER BY updated_at DESC,id DESC LIMIT 1) p
          RIGHT JOIN (SELECT 1) seed ON true
        ), usage_high AS (
          SELECT COALESCE(created_at,transaction_timestamp()) AS at,COALESCE(id,0)::bigint AS id
          FROM (SELECT created_at,id FROM public.usage_logs ORDER BY created_at DESC,id DESC LIMIT 1) u
          RIGHT JOIN (SELECT 1) seed ON true
        ), credits AS (
          SELECT id,used_at AS event_time,'promo_code_usages'::text AS domain FROM public.promo_code_usages WHERE bonus_amount>0
          UNION ALL SELECT id,created_at,'user_affiliate_ledger'::text FROM public.user_affiliate_ledger WHERE action='transfer' AND amount>0
          UNION ALL SELECT rc.id,rc.used_at,'redeem_codes'::text FROM public.redeem_codes rc
            WHERE rc.type='balance' AND rc.used_by IS NOT NULL AND rc.used_at IS NOT NULL AND rc.status='used' AND rc.value>0
              AND NOT EXISTS (SELECT 1 FROM public.payment_orders po WHERE po.recharge_code=rc.code)
        ), credit_high AS (
          SELECT COALESCE(max(event_time),transaction_timestamp()) AS at,
            concat('promo_code_usages:',COALESCE((SELECT max(id) FROM public.promo_code_usages),0),
              ';user_affiliate_ledger:',COALESCE((SELECT max(id) FROM public.user_affiliate_ledger),0),
              ';redeem_codes:',COALESCE((SELECT max(id) FROM public.redeem_codes),0)) AS cursor FROM credits
        )
        SELECT 'sub2api-economic-v4'::text AS projection_contract,cfg.contract_ok,
          encode(sha256(convert_to(COALESCE(multiplier_value,'<invalid>')||'|'||
            COALESCE(fee_rate_value,'<invalid>')||'|SUB2_BALANCE_1E8|v4','UTF8')),'hex') AS configuration_hash,
          payment_high.at AS payments_event_at,('payment_orders:'||payment_high.id)::text AS payments_cursor,
          usage_high.at AS usage_event_at,('usage_logs:'||usage_high.id)::text AS usage_cursor,
          credit_high.at AS credits_event_at,credit_high.cursor::text AS credits_cursor
        FROM cfg,payment_high,usage_high,credit_high
      ) result
    $query$;
    RETURN;
  END IF;
  RAISE EXCEPTION 'unsupported Sub2API balances bridge operation';
END
$bridge$;

ALTER FUNCTION invoice_bridge.sub2api_usage_v4(text,jsonb) OWNER TO invoice_sub2api_bridge_owner;
ALTER FUNCTION invoice_bridge.sub2api_credits_v4(text,jsonb) OWNER TO invoice_sub2api_bridge_owner;
ALTER FUNCTION invoice_bridge.sub2api_balances_v4(text,jsonb) OWNER TO invoice_sub2api_bridge_owner;
DO $contract$
DECLARE role_name text; function_name text;
BEGIN
  FOREACH function_name IN ARRAY ARRAY['sub2api_payments_v4','sub2api_usage_v4','sub2api_credits_v4','sub2api_balances_v4','sub2api_identities_v4'] LOOP
    EXECUTE format('REVOKE ALL ON FUNCTION invoice_bridge.%I(text,jsonb) FROM PUBLIC',function_name);
    FOREACH role_name IN ARRAY ARRAY['invoice_sub2api_payments_reader','invoice_sub2api_identities_reader','invoice_sub2api_payments_v3_reader','invoice_sub2api_usage_reader','invoice_sub2api_credits_reader','invoice_sub2api_balances_reader'] LOOP
      EXECUTE format('REVOKE ALL ON FUNCTION invoice_bridge.%I(text,jsonb) FROM %I',function_name,role_name);
    END LOOP;
  END LOOP;
  GRANT EXECUTE ON FUNCTION invoice_bridge.sub2api_payments_v4(text,jsonb) TO invoice_sub2api_payments_reader,invoice_sub2api_payments_v3_reader;
  GRANT EXECUTE ON FUNCTION invoice_bridge.sub2api_identities_v4(text,jsonb) TO invoice_sub2api_identities_reader;
  GRANT EXECUTE ON FUNCTION invoice_bridge.sub2api_usage_v4(text,jsonb) TO invoice_sub2api_usage_reader;
  GRANT EXECUTE ON FUNCTION invoice_bridge.sub2api_credits_v4(text,jsonb) TO invoice_sub2api_credits_reader;
  GRANT EXECUTE ON FUNCTION invoice_bridge.sub2api_balances_v4(text,jsonb) TO invoice_sub2api_balances_reader;
END
$contract$;
REVOKE ALL ON FUNCTION invoice_bridge.sub2api_usage_v4(text,jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION invoice_bridge.sub2api_credits_v4(text,jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION invoice_bridge.sub2api_balances_v4(text,jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION invoice_bridge.sub2api_payments_v4(text,jsonb) TO invoice_sub2api_payments_v3_reader;
GRANT EXECUTE ON FUNCTION invoice_bridge.sub2api_usage_v4(text,jsonb) TO invoice_sub2api_usage_reader;
GRANT EXECUTE ON FUNCTION invoice_bridge.sub2api_credits_v4(text,jsonb) TO invoice_sub2api_credits_reader;
GRANT EXECUTE ON FUNCTION invoice_bridge.sub2api_balances_v4(text,jsonb) TO invoice_sub2api_balances_reader;

COMMIT;
