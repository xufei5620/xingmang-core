-- REVIEW-ONLY SUB2API BRIDGE V4. Never execute from a source agent.
-- No view or static SQL dependency on an upstream relation is created. The
-- NOLOGIN owner receives exact columns; LOGIN callers receive EXECUTE only.
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
    RAISE EXCEPTION 'Sub2API bridge install requires the cluster superuser that owns the current database';
  END IF;
END $executor$;

DO $contract$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='invoice_sub2api_bridge_owner') THEN
    CREATE ROLE invoice_sub2api_bridge_owner NOLOGIN;
  END IF;
END
$contract$;

ALTER ROLE invoice_sub2api_bridge_owner
  NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 0;
ALTER ROLE invoice_sub2api_payments_reader
  NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 0;
ALTER ROLE invoice_sub2api_identities_reader
  LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 2;

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
    RAISE EXCEPTION 'Sub2API bridge source relations are missing, role-owned or protected by unreviewed RLS';
  END IF;
END $relations$;

CREATE SCHEMA IF NOT EXISTS invoice_bridge;
REVOKE ALL ON SCHEMA invoice_bridge FROM PUBLIC;
REVOKE CREATE ON SCHEMA invoice_bridge FROM invoice_sub2api_bridge_owner;
GRANT USAGE ON SCHEMA invoice_bridge TO invoice_sub2api_bridge_owner,
  invoice_sub2api_payments_reader,invoice_sub2api_identities_reader;

DO $contract$
DECLARE owner_oid oid; bridge_namespace oid; database_owner oid;
BEGIN
  SELECT oid INTO owner_oid FROM pg_roles WHERE rolname='invoice_sub2api_bridge_owner';
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
      p.proname NOT IN ('sub2api_payments_v4','sub2api_usage_v4','sub2api_credits_v4','sub2api_balances_v4','sub2api_identities_v4')
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
    'invoice_sub2api_bridge_owner','invoice_sub2api_payments_reader','invoice_sub2api_identities_reader'
  ] LOOP
    EXECUTE format('REVOKE CREATE ON SCHEMA public FROM %I',role_name);
    EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA public FROM %I',role_name);
    EXECUTE format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM %I',role_name);
    EXECUTE format('REVOKE TEMPORARY ON DATABASE %I FROM %I',current_database(),role_name);
    IF has_database_privilege(role_name,current_database(),'TEMP') THEN
      RAISE EXCEPTION '% retains TEMP privilege',role_name;
    END IF;
    IF EXISTS (
      SELECT 1 FROM pg_auth_members m
      WHERE m.member=(SELECT oid FROM pg_roles WHERE rolname=role_name)
         OR m.roleid=(SELECT oid FROM pg_roles WHERE rolname=role_name)
    ) THEN
      RAISE EXCEPTION '% has a role membership in either direction',role_name;
    END IF;
  END LOOP;
  EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM invoice_sub2api_bridge_owner',current_database());
  EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM invoice_sub2api_payments_reader',current_database());
  EXECUTE format('GRANT CONNECT ON DATABASE %I TO invoice_sub2api_identities_reader',current_database());
END
$contract$;

ALTER ROLE invoice_sub2api_identities_reader SET default_transaction_read_only=on;
ALTER ROLE invoice_sub2api_identities_reader SET statement_timeout='15s';
ALTER ROLE invoice_sub2api_identities_reader SET lock_timeout='5s';
ALTER ROLE invoice_sub2api_identities_reader SET idle_in_transaction_session_timeout='15s';
ALTER ROLE invoice_sub2api_payments_reader SET default_transaction_read_only=on;
ALTER ROLE invoice_sub2api_payments_reader SET statement_timeout='15s';
ALTER ROLE invoice_sub2api_payments_reader SET lock_timeout='5s';
ALTER ROLE invoice_sub2api_payments_reader SET idle_in_transaction_session_timeout='15s';

GRANT USAGE ON SCHEMA public TO invoice_sub2api_bridge_owner;
GRANT SELECT(id,user_id,status,order_type,amount,pay_amount,refund_amount,
  completed_at,refund_at,created_at,updated_at,payment_type,provider_key,
  provider_snapshot)
  ON public.payment_orders TO invoice_sub2api_bridge_owner;
GRANT SELECT(key,value,updated_at)
  ON public.settings TO invoice_sub2api_bridge_owner;
GRANT SELECT(id,user_id,provider_type,provider_key,provider_subject,verified_at,
  issuer,created_at,updated_at)
  ON public.auth_identities TO invoice_sub2api_bridge_owner;
-- Repeat the complete five-stream owner ACL in this source contract. This is
-- required for idempotency because the REVOKE ALL above also removes grants
-- installed by the economic contract.
GRANT SELECT(id,user_id,billing_type,actual_cost,created_at)
  ON public.usage_logs TO invoice_sub2api_bridge_owner;
GRANT SELECT(id,user_id,bonus_amount,used_at)
  ON public.promo_code_usages TO invoice_sub2api_bridge_owner;
GRANT SELECT(id,user_id,action,amount,created_at)
  ON public.user_affiliate_ledger TO invoice_sub2api_bridge_owner;
GRANT SELECT(id,code,type,value,status,used_by,used_at)
  ON public.redeem_codes TO invoice_sub2api_bridge_owner;
GRANT SELECT(recharge_code) ON public.payment_orders TO invoice_sub2api_bridge_owner;
GRANT SELECT(id,balance,deleted_at) ON public.users TO invoice_sub2api_bridge_owner;

CREATE OR REPLACE FUNCTION invoice_bridge.sub2api_payments_v4(operation text,request jsonb)
RETURNS SETOF jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=on
AS $bridge$
DECLARE requested_limit integer;
BEGIN
  IF request IS NULL OR jsonb_typeof(request)<>'object' THEN RAISE EXCEPTION 'invalid bridge request'; END IF;
  IF operation IN ('legacy_page','page') THEN
    requested_limit := (request->>'limit')::integer;
    IF requested_limit<1 OR requested_limit>500 THEN RAISE EXCEPTION 'invalid bridge limit'; END IF;
  END IF;

  IF operation='legacy_health' THEN
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        SELECT count(*)::bigint AS total_rows,
          count(*) FILTER (WHERE classification='exposed_cny')::bigint AS exposed_cny_rows,
          count(*) FILTER (WHERE classification='unsupported_known_non_cny')::bigint AS unsupported_known_non_cny_rows,
          count(*) FILTER (WHERE classification='blocked_unknown')::bigint AS blocked_unknown_currency_rows
        FROM (
          SELECT CASE
            WHEN jsonb_typeof(po.provider_snapshot)='object'
             AND po.provider_snapshot->>'schema_version'='2'
             AND lower(btrim(po.provider_snapshot->>'provider_key'))=lower(btrim(po.provider_key))
             AND upper(btrim(po.provider_snapshot->>'currency'))~'^[A-Z]{3}$'
             AND upper(btrim(po.provider_snapshot->>'currency'))='CNY' THEN 'exposed_cny'
            WHEN jsonb_typeof(po.provider_snapshot)='object'
             AND po.provider_snapshot->>'schema_version'='2'
             AND lower(btrim(po.provider_snapshot->>'provider_key'))=lower(btrim(po.provider_key))
             AND upper(btrim(po.provider_snapshot->>'currency'))~'^[A-Z]{3}$' THEN 'unsupported_known_non_cny'
            WHEN jsonb_typeof(po.provider_snapshot)='object'
             AND po.provider_snapshot->>'schema_version'='2'
             AND lower(btrim(po.provider_snapshot->>'provider_key'))=lower(btrim(po.provider_key))
             AND lower(btrim(po.provider_key)) IN ('easypay','alipay','wxpay') THEN 'exposed_cny'
            ELSE 'blocked_unknown' END::text AS classification
          FROM public.payment_orders po
        ) classified
      ) result
    $query$;
    RETURN;
  ELSIF operation='legacy_page' THEN
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        SELECT id,user_id,status,order_type,amount::text,pay_amount::text,
          refund_amount::text,currency,completed_at,refund_at,created_at,updated_at,
          payment_type,provider_key
        FROM (
          SELECT po.id,po.user_id,po.status,po.order_type,po.amount,po.pay_amount,
            po.refund_amount,
            CASE
              WHEN jsonb_typeof(po.provider_snapshot)='object'
               AND po.provider_snapshot->>'schema_version'='2'
               AND lower(btrim(po.provider_snapshot->>'provider_key'))=lower(btrim(po.provider_key))
               AND upper(btrim(po.provider_snapshot->>'currency'))~'^[A-Z]{3}$'
                THEN upper(btrim(po.provider_snapshot->>'currency'))
              WHEN jsonb_typeof(po.provider_snapshot)='object'
               AND po.provider_snapshot->>'schema_version'='2'
               AND lower(btrim(po.provider_snapshot->>'provider_key'))=lower(btrim(po.provider_key))
               AND lower(btrim(po.provider_key)) IN ('easypay','alipay','wxpay') THEN 'CNY'
            END::text AS currency,
            po.completed_at,po.refund_at,po.created_at,po.updated_at,
            po.payment_type,po.provider_key
          FROM public.payment_orders po
        ) classified
        WHERE currency='CNY' AND (updated_at,id)>($1::timestamptz,$2::bigint)
        ORDER BY updated_at,id LIMIT $3
      ) result
    $query$ USING request->>'updated_at',request->>'id',requested_limit;
    RETURN;
  ELSIF operation='health' THEN
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        WITH cfg AS (
          SELECT max(value) FILTER (WHERE key='BALANCE_RECHARGE_MULTIPLIER')::text AS multiplier_value,
            max(updated_at) FILTER (WHERE key='BALANCE_RECHARGE_MULTIPLIER') AS multiplier_updated_at,
            max(value) FILTER (WHERE key='RECHARGE_FEE_RATE')::text AS fee_rate_value,
            max(updated_at) FILTER (WHERE key='RECHARGE_FEE_RATE') AS fee_rate_updated_at,
            (count(*) FILTER (WHERE key='BALANCE_RECHARGE_MULTIPLIER')=1
             AND max(value) FILTER (WHERE key='BALANCE_RECHARGE_MULTIPLIER') IN ('1','1.0','1.00','1.00000000')
             AND count(*) FILTER (WHERE key='RECHARGE_FEE_RATE')=1) AS contract_ok
          FROM public.settings WHERE key IN ('BALANCE_RECHARGE_MULTIPLIER','RECHARGE_FEE_RATE')
        ), health AS (
          SELECT count(*) FILTER (WHERE classification='blocked_unknown')::bigint AS blocked
          FROM (
            SELECT CASE
              WHEN jsonb_typeof(po.provider_snapshot)='object'
               AND po.provider_snapshot->>'schema_version'='2'
               AND lower(btrim(po.provider_snapshot->>'provider_key'))=lower(btrim(po.provider_key))
               AND upper(btrim(po.provider_snapshot->>'currency'))~'^[A-Z]{3}$' THEN 'known'
              WHEN jsonb_typeof(po.provider_snapshot)='object'
               AND po.provider_snapshot->>'schema_version'='2'
               AND lower(btrim(po.provider_snapshot->>'provider_key'))=lower(btrim(po.provider_key))
               AND lower(btrim(po.provider_key)) IN ('easypay','alipay','wxpay') THEN 'known'
              ELSE 'blocked_unknown' END::text AS classification
            FROM public.payment_orders po
          ) classified
        )
        SELECT (cfg.contract_ok AND health.blocked=0) AS contract_ok,
          CASE WHEN NOT cfg.contract_ok THEN 'wallet_configuration_invalid'
               WHEN health.blocked>0 THEN 'payment_currency_evidence_missing' ELSE '' END::text AS blocked_reason,
          encode(sha256(convert_to(COALESCE(multiplier_value,'<missing>')||'|'||
            COALESCE((extract(epoch FROM multiplier_updated_at)*1000000)::numeric(30,0)::text,'<missing>')||'|'||
            COALESCE(fee_rate_value,'<missing>')||'|'||
            COALESCE((extract(epoch FROM fee_rate_updated_at)*1000000)::numeric(30,0)::text,'<missing>')||
            '|SUB2_BALANCE_1E8|v3','UTF8')),'hex') AS configuration_hash
        FROM cfg,health
      ) result
    $query$;
    RETURN;
  ELSIF operation='ceiling' THEN
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        SELECT classified.updated_at AS event_time,classified.id AS source_id
        FROM (
          SELECT po.id,po.updated_at,
            CASE
              WHEN jsonb_typeof(po.provider_snapshot)='object'
               AND po.provider_snapshot->>'schema_version'='2'
               AND lower(btrim(po.provider_snapshot->>'provider_key'))=lower(btrim(po.provider_key))
               AND upper(btrim(po.provider_snapshot->>'currency'))~'^[A-Z]{3}$'
                THEN upper(btrim(po.provider_snapshot->>'currency'))
              WHEN jsonb_typeof(po.provider_snapshot)='object'
               AND po.provider_snapshot->>'schema_version'='2'
               AND lower(btrim(po.provider_snapshot->>'provider_key'))=lower(btrim(po.provider_key))
               AND lower(btrim(po.provider_key)) IN ('easypay','alipay','wxpay') THEN 'CNY'
            END::text AS currency
          FROM public.payment_orders po
        ) classified
        WHERE classified.updated_at<=$1::timestamptz AND classified.currency='CNY'
        ORDER BY classified.updated_at DESC,classified.id DESC LIMIT 1
      ) result
    $query$ USING request->>'horizon';
    RETURN;
  ELSIF operation='page' THEN
    RETURN QUERY EXECUTE $query$
      SELECT to_jsonb(result) FROM (
        WITH cfg AS (
          SELECT max(value) FILTER (WHERE key='BALANCE_RECHARGE_MULTIPLIER')::text AS multiplier_value,
            max(updated_at) FILTER (WHERE key='BALANCE_RECHARGE_MULTIPLIER') AS multiplier_updated_at,
            (count(*) FILTER (WHERE key='BALANCE_RECHARGE_MULTIPLIER')=1
             AND max(value) FILTER (WHERE key='BALANCE_RECHARGE_MULTIPLIER') IN ('1','1.0','1.00','1.00000000')
             AND count(*) FILTER (WHERE key='RECHARGE_FEE_RATE')=1) AS contract_ok
          FROM public.settings WHERE key IN ('BALANCE_RECHARGE_MULTIPLIER','RECHARGE_FEE_RATE')
        ), classified AS (
          SELECT po.id,po.user_id,po.status,po.order_type,po.amount,po.pay_amount,
            po.refund_amount,po.completed_at,po.refund_at,po.created_at,po.updated_at,
            po.payment_type,po.provider_key,
            CASE
              WHEN jsonb_typeof(po.provider_snapshot)='object'
               AND po.provider_snapshot->>'schema_version'='2'
               AND lower(btrim(po.provider_snapshot->>'provider_key'))=lower(btrim(po.provider_key))
               AND upper(btrim(po.provider_snapshot->>'currency'))~'^[A-Z]{3}$'
                THEN upper(btrim(po.provider_snapshot->>'currency'))
              WHEN jsonb_typeof(po.provider_snapshot)='object'
               AND po.provider_snapshot->>'schema_version'='2'
               AND lower(btrim(po.provider_snapshot->>'provider_key'))=lower(btrim(po.provider_key))
               AND lower(btrim(po.provider_key)) IN ('easypay','alipay','wxpay') THEN 'CNY'
            END::text AS currency
          FROM public.payment_orders po
        )
        SELECT p.id AS source_id,p.user_id,p.updated_at AS event_time,
          'payment_orders'::text AS causal_domain,('payment_orders:'||p.id::text)::text AS source_cursor,
          CASE WHEN p.order_type='subscription' THEN 'subscription_purchase' ELSE 'payment_order' END::text AS entity_kind,
          p.status,p.order_type,p.amount::text,p.pay_amount::text,p.currency,
          p.refund_amount::text,round(p.pay_amount*p.refund_amount/NULLIF(p.amount,0),2)::text AS gateway_refund_amount,
          p.completed_at,p.refund_at,p.created_at,p.updated_at,p.payment_type,p.provider_key,
          CASE WHEN p.order_type='balance' AND cfg.contract_ok AND cfg.multiplier_updated_at<=p.completed_at
            THEN ((p.amount*100000000)::numeric(78,0))::text END AS wallet_cash_service_units,
          CASE WHEN p.order_type='subscription' AND p.pay_amount>0 AND p.pay_amount*100=trunc(p.pay_amount*100)
            THEN (p.pay_amount*100)::numeric(78,0)::text END AS paid_minor,
          CASE WHEN p.order_type='subscription' AND p.status='COMPLETED' AND p.refund_amount=0 THEN 'verified'
            WHEN p.order_type='subscription' THEN 'frozen' END::text AS verification_state,
          (p.refund_amount>0) AS refunded
        FROM classified p CROSS JOIN cfg
        WHERE p.currency='CNY' AND p.completed_at>=$1::timestamptz
          AND (p.updated_at,p.id)>($2::timestamptz,$3::bigint)
          AND (p.updated_at,p.id)<=($4::timestamptz,$5::bigint)
        ORDER BY p.updated_at,p.id LIMIT $6
      ) result
    $query$ USING request->>'cutover',request->>'position_at',request->>'position_id',
      request->>'ceiling_at',request->>'ceiling_id',requested_limit;
    RETURN;
  END IF;
  RAISE EXCEPTION 'unsupported Sub2API payment bridge operation';
END
$bridge$;

CREATE OR REPLACE FUNCTION invoice_bridge.sub2api_identities_v4(operation text,request jsonb)
RETURNS SETOF jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=on
AS $bridge$
DECLARE requested_limit integer;
BEGIN
  IF operation<>'page' OR request IS NULL OR jsonb_typeof(request)<>'object'
     OR request->>'provider_key'<>'https://auth.solov.cc/realms/solov'
     OR request->>'issuer'<>'https://auth.solov.cc/realms/solov' THEN
    RAISE EXCEPTION 'unsupported Sub2API identity bridge request';
  END IF;
  requested_limit := (request->>'limit')::integer;
  IF requested_limit<1 OR requested_limit>500 THEN RAISE EXCEPTION 'invalid bridge limit'; END IF;
  RETURN QUERY EXECUTE $query$
    SELECT to_jsonb(result) FROM (
      SELECT id,user_id,provider_type,provider_key,provider_subject,verified_at,
        issuer,created_at,updated_at
      FROM public.auth_identities
      WHERE provider_type='oidc'
        AND provider_key='https://auth.solov.cc/realms/solov'
        AND issuer='https://auth.solov.cc/realms/solov'
        AND verified_at IS NOT NULL
        AND (updated_at,id)>($1::timestamptz,$2::bigint)
      ORDER BY updated_at,id LIMIT $3
    ) result
  $query$ USING request->>'updated_at',request->>'id',requested_limit;
END
$bridge$;

ALTER FUNCTION invoice_bridge.sub2api_payments_v4(text,jsonb) OWNER TO invoice_sub2api_bridge_owner;
ALTER FUNCTION invoice_bridge.sub2api_identities_v4(text,jsonb) OWNER TO invoice_sub2api_bridge_owner;
DO $contract$
DECLARE role_name text; function_name text;
BEGIN
  FOREACH function_name IN ARRAY ARRAY['sub2api_payments_v4','sub2api_usage_v4','sub2api_credits_v4','sub2api_balances_v4','sub2api_identities_v4'] LOOP
    IF to_regprocedure(format('invoice_bridge.%I(text,jsonb)',function_name)) IS NOT NULL THEN
      EXECUTE format('REVOKE ALL ON FUNCTION invoice_bridge.%I(text,jsonb) FROM PUBLIC',function_name);
      FOREACH role_name IN ARRAY ARRAY['invoice_sub2api_payments_reader','invoice_sub2api_identities_reader','invoice_sub2api_payments_v3_reader','invoice_sub2api_usage_reader','invoice_sub2api_credits_reader','invoice_sub2api_balances_reader'] LOOP
        EXECUTE format('REVOKE ALL ON FUNCTION invoice_bridge.%I(text,jsonb) FROM %I',function_name,role_name);
      END LOOP;
    END IF;
  END LOOP;
  GRANT EXECUTE ON FUNCTION invoice_bridge.sub2api_payments_v4(text,jsonb) TO invoice_sub2api_payments_reader,invoice_sub2api_payments_v3_reader;
  GRANT EXECUTE ON FUNCTION invoice_bridge.sub2api_identities_v4(text,jsonb) TO invoice_sub2api_identities_reader;
  IF to_regprocedure('invoice_bridge.sub2api_usage_v4(text,jsonb)') IS NOT NULL THEN GRANT EXECUTE ON FUNCTION invoice_bridge.sub2api_usage_v4(text,jsonb) TO invoice_sub2api_usage_reader; END IF;
  IF to_regprocedure('invoice_bridge.sub2api_credits_v4(text,jsonb)') IS NOT NULL THEN GRANT EXECUTE ON FUNCTION invoice_bridge.sub2api_credits_v4(text,jsonb) TO invoice_sub2api_credits_reader; END IF;
  IF to_regprocedure('invoice_bridge.sub2api_balances_v4(text,jsonb)') IS NOT NULL THEN GRANT EXECUTE ON FUNCTION invoice_bridge.sub2api_balances_v4(text,jsonb) TO invoice_sub2api_balances_reader; END IF;
END
$contract$;
REVOKE ALL ON FUNCTION invoice_bridge.sub2api_payments_v4(text,jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION invoice_bridge.sub2api_identities_v4(text,jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION invoice_bridge.sub2api_payments_v4(text,jsonb) TO invoice_sub2api_payments_reader;
GRANT EXECUTE ON FUNCTION invoice_bridge.sub2api_identities_v4(text,jsonb) TO invoice_sub2api_identities_reader;

COMMIT;

-- Deliberately absent from LOGIN callers: every raw-table SELECT, schema
-- CREATE, TEMP, role membership, sequence/mutation privilege, users/email,
-- provider_snapshot, unrelated identity subjects, and secrets.
