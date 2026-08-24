-- REVIEW-ONLY COMPLETE ROLLBACK FOR THE SUB2API BRIDGE V4.
-- Stop all Sub2API source agents first. Exact allowlists and RESTRICT protect
-- every upstream table and row from this rollback.
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
    RAISE EXCEPTION 'Sub2API bridge rollback requires the cluster superuser that owns the current database';
  END IF;
END $executor$;

DO $rollback$
DECLARE bridge_namespace oid; bridge_owner oid; expected_gucs text[] := ARRAY[
  'default_transaction_read_only=on','statement_timeout=15s','lock_timeout=5s',
  'idle_in_transaction_session_timeout=15s'];
BEGIN
  SELECT oid INTO bridge_namespace FROM pg_namespace WHERE nspname='invoice_bridge';
  SELECT oid INTO bridge_owner FROM pg_roles WHERE rolname='invoice_sub2api_bridge_owner';
  IF bridge_namespace IS NULL OR bridge_owner IS NULL THEN RAISE EXCEPTION 'Sub2API bridge V4 is not installed'; END IF;
  IF (SELECT nspowner FROM pg_namespace WHERE oid=bridge_namespace)<>(SELECT datdba FROM pg_database WHERE datname=current_database())
     OR EXISTS (SELECT 1 FROM pg_class WHERE relnamespace=bridge_namespace) THEN
    RAISE EXCEPTION 'invoice_bridge schema inventory is unsafe';
  END IF;
  IF (SELECT count(*) FROM pg_proc WHERE pronamespace=bridge_namespace)<>5
     OR EXISTS (
       SELECT 1 FROM pg_proc p JOIN pg_language l ON l.oid=p.prolang
       WHERE p.pronamespace=bridge_namespace AND (
         p.proname NOT IN ('sub2api_payments_v4','sub2api_usage_v4','sub2api_credits_v4','sub2api_balances_v4','sub2api_identities_v4')
         OR oidvectortypes(p.proargtypes)<>'text, jsonb' OR p.prorettype<>'jsonb'::regtype OR NOT p.proretset
         OR NOT p.prosecdef OR p.proowner<>bridge_owner OR l.lanname<>'plpgsql'
         OR NOT COALESCE('search_path=pg_catalog'=ANY(p.proconfig),false)
         OR NOT COALESCE('row_security=on'=ANY(p.proconfig),false)
       )
     ) THEN RAISE EXCEPTION 'Sub2API bridge function inventory is unsafe'; END IF;
  IF (SELECT count(*) FROM pg_roles WHERE rolname LIKE 'invoice_sub2api_%')<>7
     OR EXISTS (
       SELECT 1 FROM pg_roles WHERE rolname LIKE 'invoice_sub2api_%'
         AND rolname NOT IN ('invoice_sub2api_bridge_owner','invoice_sub2api_payments_reader',
           'invoice_sub2api_identities_reader','invoice_sub2api_payments_v3_reader',
           'invoice_sub2api_usage_reader','invoice_sub2api_credits_reader','invoice_sub2api_balances_reader')
     ) THEN RAISE EXCEPTION 'Sub2API bridge role inventory is unsafe'; END IF;
  IF EXISTS (
    SELECT 1 FROM pg_roles WHERE rolname LIKE 'invoice_sub2api_%' AND
      (rolsuper OR rolcreatedb OR rolcreaterole OR rolinherit OR rolreplication OR rolbypassrls)
  ) OR EXISTS (
    SELECT 1 FROM pg_roles WHERE rolname='invoice_sub2api_bridge_owner'
      AND (rolcanlogin OR rolconnlimit<>0 OR rolconfig IS NOT NULL)
  ) OR EXISTS (
    SELECT 1 FROM pg_roles WHERE rolname='invoice_sub2api_payments_reader'
      AND (rolcanlogin OR rolconnlimit<>0 OR NOT (rolconfig @> expected_gucs AND array_length(rolconfig,1)=4))
  ) OR EXISTS (
    SELECT 1 FROM pg_roles WHERE rolname IN ('invoice_sub2api_identities_reader','invoice_sub2api_payments_v3_reader',
      'invoice_sub2api_usage_reader','invoice_sub2api_credits_reader','invoice_sub2api_balances_reader')
      AND (NOT rolcanlogin OR rolconnlimit<>2 OR NOT (rolconfig @> expected_gucs AND array_length(rolconfig,1)=4))
  ) THEN RAISE EXCEPTION 'Sub2API bridge role attributes are unsafe'; END IF;
  IF EXISTS (
    SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.member OR r.oid=m.roleid
    WHERE r.rolname LIKE 'invoice_sub2api_%'
  ) THEN RAISE EXCEPTION 'Sub2API bridge role membership exists'; END IF;
END
$rollback$;

DO $rollback$
DECLARE owner_name text := 'invoice_sub2api_bridge_owner';
BEGIN
  IF EXISTS (
    WITH declared(table_name,column_name) AS (VALUES
      ('payment_orders','id'),('payment_orders','user_id'),('payment_orders','status'),('payment_orders','order_type'),
      ('payment_orders','amount'),('payment_orders','pay_amount'),('payment_orders','refund_amount'),
      ('payment_orders','completed_at'),('payment_orders','refund_at'),('payment_orders','created_at'),('payment_orders','updated_at'),
      ('payment_orders','payment_type'),('payment_orders','provider_key'),('payment_orders','provider_snapshot'),('payment_orders','recharge_code'),
      ('settings','key'),('settings','value'),('settings','updated_at'),
      ('auth_identities','id'),('auth_identities','user_id'),('auth_identities','provider_type'),('auth_identities','provider_key'),
      ('auth_identities','provider_subject'),('auth_identities','verified_at'),('auth_identities','issuer'),
      ('auth_identities','created_at'),('auth_identities','updated_at'),
      ('usage_logs','id'),('usage_logs','user_id'),('usage_logs','billing_type'),('usage_logs','actual_cost'),('usage_logs','created_at'),
      ('promo_code_usages','id'),('promo_code_usages','user_id'),('promo_code_usages','bonus_amount'),('promo_code_usages','used_at'),
      ('user_affiliate_ledger','id'),('user_affiliate_ledger','user_id'),('user_affiliate_ledger','action'),
      ('user_affiliate_ledger','amount'),('user_affiliate_ledger','created_at'),
      ('redeem_codes','id'),('redeem_codes','code'),('redeem_codes','type'),('redeem_codes','value'),
      ('redeem_codes','status'),('redeem_codes','used_by'),('redeem_codes','used_at'),
      ('users','id'),('users','balance'),('users','deleted_at')
    ), expected AS (
      SELECT d.* FROM declared d JOIN information_schema.columns c
        ON c.table_schema='public' AND c.table_name=d.table_name AND c.column_name=d.column_name
    ), actual AS (
      SELECT table_name,column_name FROM information_schema.column_privileges
      WHERE grantee=owner_name AND table_schema='public' AND privilege_type='SELECT'
    ), mismatch AS (
      (SELECT * FROM expected EXCEPT SELECT * FROM actual)
      UNION ALL
      (SELECT * FROM actual EXCEPT SELECT * FROM expected)
    ) SELECT 1 FROM mismatch
  ) THEN RAISE EXCEPTION 'Sub2API bridge owner column ACL differs from the exact contract'; END IF;
  IF EXISTS (
    SELECT 1 FROM information_schema.column_privileges
    WHERE grantee LIKE 'invoice_sub2api_%' AND grantee<>owner_name
  ) OR EXISTS (
    SELECT 1 FROM information_schema.column_privileges
    WHERE grantee=owner_name AND privilege_type<>'SELECT'
  ) OR EXISTS (
    SELECT 1 FROM information_schema.role_table_grants WHERE grantee LIKE 'invoice_sub2api_%'
  ) OR EXISTS (
    SELECT 1 FROM information_schema.role_usage_grants WHERE grantee LIKE 'invoice_sub2api_%'
  ) THEN RAISE EXCEPTION 'Sub2API bridge has an unexpected raw privilege'; END IF;
  IF EXISTS (
    WITH expected(role_name,function_name) AS (VALUES
      ('invoice_sub2api_payments_reader','sub2api_payments_v4'),
      ('invoice_sub2api_payments_v3_reader','sub2api_payments_v4'),
      ('invoice_sub2api_identities_reader','sub2api_identities_v4'),
      ('invoice_sub2api_usage_reader','sub2api_usage_v4'),
      ('invoice_sub2api_credits_reader','sub2api_credits_v4'),
      ('invoice_sub2api_balances_reader','sub2api_balances_v4')
    ), actual AS (
      SELECT r.rolname,p.proname
      FROM pg_roles r CROSS JOIN pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
      WHERE r.rolname LIKE 'invoice_sub2api_%' AND r.rolname<>owner_name
        AND n.nspname='invoice_bridge' AND has_function_privilege(r.rolname,p.oid,'EXECUTE')
    ), mismatch AS (
      (SELECT * FROM expected EXCEPT SELECT * FROM actual)
      UNION ALL
      (SELECT * FROM actual EXCEPT SELECT * FROM expected)
    ) SELECT 1 FROM mismatch
  ) THEN RAISE EXCEPTION 'Sub2API bridge caller EXECUTE ACL differs from the exact contract'; END IF;
  IF EXISTS (
    SELECT 1 FROM pg_namespace n,pg_roles r
    WHERE r.rolname LIKE 'invoice_sub2api_%'
      AND n.nspname NOT IN ('public','invoice_bridge','information_schema')
      AND n.nspname NOT LIKE 'pg\_%' ESCAPE '\'
      AND (has_schema_privilege(r.rolname,n.oid,'USAGE') OR has_schema_privilege(r.rolname,n.oid,'CREATE'))
  ) OR EXISTS (
    SELECT 1 FROM pg_default_acl d JOIN pg_roles r ON r.oid=d.defaclrole WHERE r.rolname LIKE 'invoice_sub2api_%'
  ) OR EXISTS (
    SELECT 1 FROM pg_class c JOIN pg_roles r ON r.oid=c.relowner WHERE r.rolname LIKE 'invoice_sub2api_%'
  ) OR EXISTS (
    SELECT 1 FROM pg_namespace n JOIN pg_roles r ON r.oid=n.nspowner WHERE r.rolname LIKE 'invoice_sub2api_%'
  ) OR EXISTS (
    SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace JOIN pg_roles r ON r.oid=p.proowner
    WHERE r.rolname LIKE 'invoice_sub2api_%' AND (r.rolname<>owner_name OR n.nspname<>'invoice_bridge')
  ) THEN RAISE EXCEPTION 'Sub2API bridge role has unexpected ACL, default ACL or ownership'; END IF;
END
$rollback$;

DO $rollback$
DECLARE role_name text;
BEGIN
  FOREACH role_name IN ARRAY ARRAY[
    'invoice_sub2api_payments_reader','invoice_sub2api_identities_reader','invoice_sub2api_payments_v3_reader',
    'invoice_sub2api_usage_reader','invoice_sub2api_credits_reader','invoice_sub2api_balances_reader'
  ] LOOP
    EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM %I',current_database(),role_name);
  END LOOP;
  IF EXISTS (
    SELECT 1 FROM pg_stat_activity WHERE datname=current_database()
      AND usename IN ('invoice_sub2api_payments_reader','invoice_sub2api_identities_reader','invoice_sub2api_payments_v3_reader',
        'invoice_sub2api_usage_reader','invoice_sub2api_credits_reader','invoice_sub2api_balances_reader')
  ) THEN RAISE EXCEPTION 'Sub2API bridge readers still have active sessions'; END IF;
END
$rollback$;

DROP FUNCTION invoice_bridge.sub2api_payments_v4(text,jsonb) RESTRICT;
DROP FUNCTION invoice_bridge.sub2api_usage_v4(text,jsonb) RESTRICT;
DROP FUNCTION invoice_bridge.sub2api_credits_v4(text,jsonb) RESTRICT;
DROP FUNCTION invoice_bridge.sub2api_balances_v4(text,jsonb) RESTRICT;
DROP FUNCTION invoice_bridge.sub2api_identities_v4(text,jsonb) RESTRICT;
DROP SCHEMA invoice_bridge RESTRICT;

-- Revoke only the exact column ACL that was proved above. DROP OWNED is
-- intentionally forbidden because it can delete unrelated enum/FDW/catalog
-- objects accidentally owned by an integration role.
DO $rollback$
DECLARE grant_row record;
BEGIN
  FOR grant_row IN
    SELECT table_schema,table_name,
      string_agg(format('%I',column_name),',' ORDER BY column_name) AS columns
    FROM information_schema.column_privileges
    WHERE grantee='invoice_sub2api_bridge_owner' AND privilege_type='SELECT'
    GROUP BY table_schema,table_name
  LOOP
    EXECUTE format('REVOKE SELECT (%s) ON TABLE %I.%I FROM %I',
      grant_row.columns,grant_row.table_schema,grant_row.table_name,
      'invoice_sub2api_bridge_owner');
  END LOOP;
  REVOKE USAGE ON SCHEMA public FROM invoice_sub2api_bridge_owner;
END
$rollback$;

-- Direct role removal is the final fail-closed dependency check. Unknown
-- ownership or ACL state aborts the transaction and preserves every object.
DROP ROLE invoice_sub2api_payments_reader;
DROP ROLE invoice_sub2api_identities_reader;
DROP ROLE invoice_sub2api_payments_v3_reader;
DROP ROLE invoice_sub2api_usage_reader;
DROP ROLE invoice_sub2api_credits_reader;
DROP ROLE invoice_sub2api_balances_reader;
DROP ROLE invoice_sub2api_bridge_owner;

DO $rollback$ BEGIN
  EXECUTE format('GRANT TEMPORARY ON DATABASE %I TO PUBLIC',current_database());
END $rollback$;

COMMIT;
