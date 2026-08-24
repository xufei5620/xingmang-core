-- REVIEW-ONLY NEW API BRIDGE V4. Never execute from a source agent.
-- Fixed dynamic SQL and builtin return types keep pg_depend to upstream
-- relations at zero while preserving a row-filtered least-privilege boundary.
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
    RAISE EXCEPTION 'New API bridge install requires the cluster superuser that owns the current database';
  END IF;
END $executor$;

DO $contract$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='invoice_newapi_bridge_owner') THEN
    CREATE ROLE invoice_newapi_bridge_owner NOLOGIN;
  END IF;
END
$contract$;

ALTER ROLE invoice_newapi_bridge_owner
  NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 0;
ALTER ROLE invoice_newapi_payments_reader
  NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 0;
ALTER ROLE invoice_newapi_identities_reader
  LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 2;

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
    RAISE EXCEPTION 'New API bridge source relations are missing, role-owned or protected by unreviewed RLS';
  END IF;
END $relations$;

CREATE SCHEMA IF NOT EXISTS invoice_bridge;
REVOKE ALL ON SCHEMA invoice_bridge FROM PUBLIC;
REVOKE CREATE ON SCHEMA invoice_bridge FROM invoice_newapi_bridge_owner;
GRANT USAGE ON SCHEMA invoice_bridge TO invoice_newapi_bridge_owner,
  invoice_newapi_payments_reader,invoice_newapi_identities_reader;

DO $contract$
DECLARE owner_oid oid; bridge_namespace oid; database_owner oid;
BEGIN
  SELECT oid INTO owner_oid FROM pg_roles WHERE rolname='invoice_newapi_bridge_owner';
  SELECT oid,nspowner INTO bridge_namespace,database_owner FROM pg_namespace WHERE nspname='invoice_bridge';
  IF database_owner<>(SELECT datdba FROM pg_database WHERE datname=current_database()) THEN
    RAISE EXCEPTION 'invoice_bridge schema is not owned by the database owner';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_class WHERE relnamespace=bridge_namespace) THEN
    RAISE EXCEPTION 'invoice_bridge contains a non-function object';
  END IF;
  IF EXISTS (
    SELECT 1 FROM pg_proc p JOIN pg_language l ON l.oid=p.prolang
    WHERE p.pronamespace=bridge_namespace AND (
      p.proname NOT IN ('newapi_payments_v4','newapi_usage_v4','newapi_credits_v4','newapi_balances_v4','newapi_identities_v4')
      OR oidvectortypes(p.proargtypes)<>'text, jsonb' OR p.prorettype<>'jsonb'::regtype
      OR NOT p.proretset OR NOT p.prosecdef OR l.lanname<>'plpgsql' OR p.proowner<>owner_oid
      OR NOT COALESCE('search_path=pg_catalog'=ANY(p.proconfig),false)
      OR NOT COALESCE('row_security=on'=ANY(p.proconfig),false)
    )
  ) THEN RAISE EXCEPTION 'invoice_bridge contains an unexpected or unsafe function'; END IF;
END
$contract$;

DO $contract$
DECLARE role_name text;
BEGIN
  EXECUTE format('REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC',current_database());
  FOREACH role_name IN ARRAY ARRAY[
    'invoice_newapi_bridge_owner','invoice_newapi_payments_reader','invoice_newapi_identities_reader'
  ] LOOP
    EXECUTE format('REVOKE CREATE ON SCHEMA public FROM %I',role_name);
    EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA public FROM %I',role_name);
    EXECUTE format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM %I',role_name);
    EXECUTE format('REVOKE TEMPORARY ON DATABASE %I FROM %I',current_database(),role_name);
    IF has_database_privilege(role_name,current_database(),'TEMP') THEN RAISE EXCEPTION '% retains TEMP',role_name; END IF;
    IF EXISTS (
      SELECT 1 FROM pg_auth_members m
      WHERE m.member=(SELECT oid FROM pg_roles WHERE rolname=role_name)
         OR m.roleid=(SELECT oid FROM pg_roles WHERE rolname=role_name)
    ) THEN RAISE EXCEPTION '% has a role membership in either direction',role_name; END IF;
  END LOOP;
  EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM invoice_newapi_bridge_owner',current_database());
  EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM invoice_newapi_payments_reader',current_database());
  EXECUTE format('GRANT CONNECT ON DATABASE %I TO invoice_newapi_identities_reader',current_database());
END
$contract$;

ALTER ROLE invoice_newapi_identities_reader SET default_transaction_read_only=on;
ALTER ROLE invoice_newapi_identities_reader SET statement_timeout='15s';
ALTER ROLE invoice_newapi_identities_reader SET lock_timeout='5s';
ALTER ROLE invoice_newapi_identities_reader SET idle_in_transaction_session_timeout='15s';
ALTER ROLE invoice_newapi_payments_reader SET default_transaction_read_only=on;
ALTER ROLE invoice_newapi_payments_reader SET statement_timeout='15s';
ALTER ROLE invoice_newapi_payments_reader SET lock_timeout='5s';
ALTER ROLE invoice_newapi_payments_reader SET idle_in_transaction_session_timeout='15s';

GRANT USAGE ON SCHEMA public TO invoice_newapi_bridge_owner;
GRANT SELECT(id,user_id,amount,money,payment_method,payment_provider,create_time,complete_time,status)
  ON public.top_ups TO invoice_newapi_bridge_owner;
GRANT SELECT(id,user_id,money,payment_method,payment_provider,status,create_time,complete_time)
  ON public.subscription_orders TO invoice_newapi_bridge_owner;
GRANT SELECT(key,value) ON public.options TO invoice_newapi_bridge_owner;
GRANT SELECT(id,slug,enabled,well_known,authorization_endpoint,token_endpoint,user_info_endpoint)
  ON public.custom_oauth_providers TO invoice_newapi_bridge_owner;
GRANT SELECT(id,user_id,provider_id,provider_user_id,created_at)
  ON public.user_oauth_bindings TO invoice_newapi_bridge_owner;
-- Full five-stream ACL is repeated here so rerunning this source contract
-- after the economic contract cannot silently revoke economic bridge access.
GRANT SELECT(id,user_id,created_at,type,quota) ON public.logs TO invoice_newapi_bridge_owner;
GRANT SELECT(id,user_id,quota_awarded,created_at) ON public.checkins TO invoice_newapi_bridge_owner;
GRANT SELECT(id,status,quota,redeemed_time,used_user_id) ON public.redemptions TO invoice_newapi_bridge_owner;
GRANT SELECT(id,quota,deleted_at) ON public.users TO invoice_newapi_bridge_owner;

CREATE OR REPLACE FUNCTION invoice_bridge.newapi_payments_v4(operation text,request jsonb)
RETURNS SETOF jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=on
AS $bridge$
DECLARE requested_limit integer; requested_domain text;
BEGIN
  IF request IS NULL OR jsonb_typeof(request)<>'object' THEN RAISE EXCEPTION 'invalid bridge request'; END IF;
  IF operation IN ('legacy_page','page') THEN
    requested_limit := (request->>'limit')::integer;
    IF requested_limit<1 OR requested_limit>500 THEN RAISE EXCEPTION 'invalid bridge limit'; END IF;
  END IF;
  IF operation='legacy_page' THEN
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        SELECT id,user_id,amount,money::text,payment_method,payment_provider,create_time,complete_time,status
        FROM public.top_ups WHERE id>$1::bigint ORDER BY id LIMIT $2
      ) result
    $query$ USING request->>'id',requested_limit;
    RETURN;
  ELSIF operation='health' THEN
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
             AND NOT EXISTS (
               SELECT 1 FROM jsonb_each_text(COALESCE((SELECT value FROM public.options WHERE key='TopupGroupRatio'),'{}')::jsonb)
               WHERE value IS NULL OR btrim(value)::numeric<>1)) AS contract_ok
        )
        SELECT contract_ok,CASE WHEN NOT contract_ok THEN 'wallet_configuration_invalid' ELSE '' END::text AS blocked_reason,
          encode(sha256(convert_to(quota_per_unit||'|'||price||'|'||topup_group_ratio_semantics||'|NEWAPI_QUOTA|rc.25|v4','UTF8')),'hex') AS configuration_hash
        FROM cfg
      ) result
    $query$;
    RETURN;
  ELSIF operation='ceiling' THEN
    requested_domain := request->>'domain';
    IF requested_domain='top_ups' THEN
      RETURN QUERY EXECUTE $query$
        SELECT to_jsonb(result) FROM (
          SELECT COALESCE(CASE WHEN min(id) FILTER (WHERE to_timestamp(GREATEST(create_time,complete_time))>$1::timestamptz) IS NOT NULL
            THEN min(id) FILTER (WHERE to_timestamp(GREATEST(create_time,complete_time))>$1::timestamptz)-1 ELSE max(id) END,$2::bigint)::bigint AS source_id
          FROM public.top_ups WHERE amount>0 AND complete_time>0 AND status='success' AND id>$2::bigint
        ) result
      $query$ USING request->>'horizon',request->>'position';
      RETURN;
    ELSIF requested_domain='subscription_orders' THEN
      RETURN QUERY EXECUTE $query$
        SELECT to_jsonb(result) FROM (
          SELECT COALESCE(CASE WHEN min(id) FILTER (WHERE to_timestamp(GREATEST(create_time,complete_time))>$1::timestamptz) IS NOT NULL
            THEN min(id) FILTER (WHERE to_timestamp(GREATEST(create_time,complete_time))>$1::timestamptz)-1 ELSE max(id) END,$2::bigint)::bigint AS source_id
          FROM public.subscription_orders WHERE complete_time>0 AND status='success' AND id>$2::bigint
        ) result
      $query$ USING request->>'horizon',request->>'position';
      RETURN;
    END IF;
    RAISE EXCEPTION 'invalid New API payment domain';
  ELSIF operation='page' THEN
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        WITH projected AS (
          SELECT t.id::bigint AS source_id,t.user_id::bigint AS user_id,
            to_timestamp(GREATEST(t.create_time,t.complete_time)) AS event_time,
            'top_ups'::text AS causal_domain,('top_ups:'||t.id::text)::text AS source_cursor,
            'payment_candidate'::text AS entity_kind,t.status,'topup'::text AS order_type,
            t.amount::numeric(78,0)::text AS amount,t.money::text AS pay_amount,'CNY'::text AS currency,
            '0'::text AS refund_amount,'0'::text AS gateway_refund_amount,
            CASE WHEN t.complete_time>0 THEN to_timestamp(t.complete_time) END AS completed_at,
            NULL::timestamptz AS refund_at,to_timestamp(t.create_time) AS created_at,
            to_timestamp(GREATEST(t.create_time,t.complete_time)) AS updated_at,
            t.payment_method::text AS payment_type,t.payment_provider::text AS provider_key,
            CASE lower(COALESCE(NULLIF(t.payment_provider,''),t.payment_method))
              WHEN 'epay' THEN (t.amount*500000)::numeric(78,0)::text
              WHEN 'waffo' THEN (t.amount*500000)::numeric(78,0)::text
              WHEN 'waffo_pancake' THEN (t.amount*500000)::numeric(78,0)::text
              WHEN 'stripe' THEN CASE WHEN t.money*500000=trunc(t.money*500000) THEN (t.money*500000)::numeric(78,0)::text END
              WHEN 'creem' THEN t.amount::numeric(78,0)::text END AS wallet_cash_service_units,
            NULL::text AS paid_minor,'pending'::text AS verification_state,false AS refunded
          FROM public.top_ups t WHERE t.amount>0 AND t.complete_time>0 AND t.status='success'
          UNION ALL
          SELECT s.id::bigint,s.user_id::bigint,to_timestamp(GREATEST(s.create_time,s.complete_time)),
            'subscription_orders'::text,('subscription_orders:'||s.id::text)::text,
            'subscription_purchase'::text,s.status,'subscription'::text,'0'::text,s.money::text,'CNY'::text,
            '0'::text,'0'::text,CASE WHEN s.complete_time>0 THEN to_timestamp(s.complete_time) END,
            NULL::timestamptz,to_timestamp(s.create_time),to_timestamp(GREATEST(s.create_time,s.complete_time)),
            s.payment_method::text,s.payment_provider::text,NULL::text,
            CASE WHEN s.money>0 AND s.money*100=trunc(s.money*100) THEN (s.money*100)::numeric(78,0)::text END,
            'pending'::text,false
          FROM public.subscription_orders s WHERE s.complete_time>0 AND s.status='success'
        )
        SELECT * FROM projected
        WHERE completed_at>=$1::timestamptz AND event_time<=$2::timestamptz AND (
          (causal_domain='subscription_orders' AND source_id>$3::bigint AND source_id<=$4::bigint) OR
          (causal_domain='top_ups' AND source_id>$5::bigint AND source_id<=$6::bigint))
        ORDER BY causal_domain,source_id LIMIT $7
      ) result
    $query$ USING request->>'cutover',request->>'horizon',request->>'subscription_position',
      request->>'subscription_ceiling',request->>'topup_position',request->>'topup_ceiling',requested_limit;
    RETURN;
  END IF;
  RAISE EXCEPTION 'unsupported New API payment bridge operation';
END
$bridge$;

CREATE OR REPLACE FUNCTION invoice_bridge.newapi_identities_v4(operation text,request jsonb)
RETURNS SETOF jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=on
AS $bridge$
DECLARE requested_limit integer;
BEGIN
  IF request IS NULL OR jsonb_typeof(request)<>'object' OR request->>'slug'<>'solov-sso' THEN
    RAISE EXCEPTION 'unsupported New API identity bridge request';
  END IF;
  IF operation='provider_contract' THEN
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        SELECT slug,enabled,well_known,authorization_endpoint,token_endpoint,user_info_endpoint,
          (count(*) OVER ()=1 AND enabled
           AND btrim(well_known)~'^https://[^[:space:]]+$'
           AND btrim(authorization_endpoint)~'^https://[^[:space:]]+$'
           AND btrim(token_endpoint)~'^https://[^[:space:]]+$'
           AND btrim(user_info_endpoint)~'^https://[^[:space:]]+$') AS contract_ok
        FROM public.custom_oauth_providers WHERE slug='solov-sso'
      ) result
    $query$;
    RETURN;
  ELSIF operation='page' THEN
    requested_limit := (request->>'limit')::integer;
    IF requested_limit<1 OR requested_limit>500 THEN RAISE EXCEPTION 'invalid bridge limit'; END IF;
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        WITH provider AS (
          SELECT id,slug,enabled,
            (count(*) OVER ()=1 AND enabled
             AND btrim(well_known)~'^https://[^[:space:]]+$'
             AND btrim(authorization_endpoint)~'^https://[^[:space:]]+$'
             AND btrim(token_endpoint)~'^https://[^[:space:]]+$'
             AND btrim(user_info_endpoint)~'^https://[^[:space:]]+$') AS contract_ok
          FROM public.custom_oauth_providers WHERE slug='solov-sso'
        )
        SELECT b.id,b.user_id,b.provider_id,b.provider_user_id,b.created_at,p.slug AS provider_slug
        FROM public.user_oauth_bindings b JOIN provider p ON p.id=b.provider_id
        WHERE p.contract_ok AND b.id>$1::bigint ORDER BY b.id LIMIT $2
      ) result
    $query$ USING request->>'id',requested_limit;
    RETURN;
  END IF;
  RAISE EXCEPTION 'unsupported New API identity bridge operation';
END
$bridge$;

ALTER FUNCTION invoice_bridge.newapi_payments_v4(text,jsonb) OWNER TO invoice_newapi_bridge_owner;
ALTER FUNCTION invoice_bridge.newapi_identities_v4(text,jsonb) OWNER TO invoice_newapi_bridge_owner;
DO $contract$
DECLARE role_name text; function_name text;
BEGIN
  FOREACH function_name IN ARRAY ARRAY['newapi_payments_v4','newapi_usage_v4','newapi_credits_v4','newapi_balances_v4','newapi_identities_v4'] LOOP
    IF to_regprocedure(format('invoice_bridge.%I(text,jsonb)',function_name)) IS NOT NULL THEN
      EXECUTE format('REVOKE ALL ON FUNCTION invoice_bridge.%I(text,jsonb) FROM PUBLIC',function_name);
      FOREACH role_name IN ARRAY ARRAY['invoice_newapi_payments_reader','invoice_newapi_identities_reader','invoice_newapi_payments_v3_reader','invoice_newapi_usage_reader','invoice_newapi_credits_reader','invoice_newapi_balances_reader'] LOOP
        EXECUTE format('REVOKE ALL ON FUNCTION invoice_bridge.%I(text,jsonb) FROM %I',function_name,role_name);
      END LOOP;
    END IF;
  END LOOP;
  GRANT EXECUTE ON FUNCTION invoice_bridge.newapi_payments_v4(text,jsonb) TO invoice_newapi_payments_reader,invoice_newapi_payments_v3_reader;
  GRANT EXECUTE ON FUNCTION invoice_bridge.newapi_identities_v4(text,jsonb) TO invoice_newapi_identities_reader;
  IF to_regprocedure('invoice_bridge.newapi_usage_v4(text,jsonb)') IS NOT NULL THEN GRANT EXECUTE ON FUNCTION invoice_bridge.newapi_usage_v4(text,jsonb) TO invoice_newapi_usage_reader; END IF;
  IF to_regprocedure('invoice_bridge.newapi_credits_v4(text,jsonb)') IS NOT NULL THEN GRANT EXECUTE ON FUNCTION invoice_bridge.newapi_credits_v4(text,jsonb) TO invoice_newapi_credits_reader; END IF;
  IF to_regprocedure('invoice_bridge.newapi_balances_v4(text,jsonb)') IS NOT NULL THEN GRANT EXECUTE ON FUNCTION invoice_bridge.newapi_balances_v4(text,jsonb) TO invoice_newapi_balances_reader; END IF;
END
$contract$;
REVOKE ALL ON FUNCTION invoice_bridge.newapi_payments_v4(text,jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION invoice_bridge.newapi_identities_v4(text,jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION invoice_bridge.newapi_payments_v4(text,jsonb) TO invoice_newapi_payments_reader;
GRANT EXECUTE ON FUNCTION invoice_bridge.newapi_identities_v4(text,jsonb) TO invoice_newapi_identities_reader;

COMMIT;
