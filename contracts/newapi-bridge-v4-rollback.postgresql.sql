-- REVIEW-ONLY COMPLETE ROLLBACK FOR THE NEW API BRIDGE V4.
-- Stop all New API source agents first. This transaction removes only the
-- exact five bridge functions, integration schema and seven roles. It contains
-- no CASCADE and never drops or mutates an upstream application relation.
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
    RAISE EXCEPTION 'New API bridge rollback requires the cluster superuser that owns the current database';
  END IF;
END $executor$;

DO $rollback$
DECLARE bridge_namespace oid; bridge_owner oid; expected_gucs text[] := ARRAY[
  'default_transaction_read_only=on','statement_timeout=15s','lock_timeout=5s',
  'idle_in_transaction_session_timeout=15s'];
BEGIN
  SELECT oid INTO bridge_namespace FROM pg_namespace WHERE nspname='invoice_bridge';
  SELECT oid INTO bridge_owner FROM pg_roles WHERE rolname='invoice_newapi_bridge_owner';
  IF bridge_namespace IS NULL OR bridge_owner IS NULL THEN RAISE EXCEPTION 'New API bridge V4 is not installed'; END IF;
  IF (SELECT nspowner FROM pg_namespace WHERE oid=bridge_namespace)<>(SELECT datdba FROM pg_database WHERE datname=current_database())
     OR EXISTS (SELECT 1 FROM pg_class WHERE relnamespace=bridge_namespace) THEN
    RAISE EXCEPTION 'invoice_bridge schema inventory is unsafe';
  END IF;
  IF (SELECT count(*) FROM pg_proc WHERE pronamespace=bridge_namespace)<>5
     OR EXISTS (
       SELECT 1 FROM pg_proc p JOIN pg_language l ON l.oid=p.prolang
       WHERE p.pronamespace=bridge_namespace AND (
         p.proname NOT IN ('newapi_payments_v4','newapi_usage_v4','newapi_credits_v4','newapi_balances_v4','newapi_identities_v4')
         OR oidvectortypes(p.proargtypes)<>'text, jsonb' OR p.prorettype<>'jsonb'::regtype OR NOT p.proretset
         OR NOT p.prosecdef OR p.proowner<>bridge_owner OR l.lanname<>'plpgsql'
         OR NOT COALESCE('search_path=pg_catalog'=ANY(p.proconfig),false)
         OR NOT COALESCE('row_security=on'=ANY(p.proconfig),false)
       )
     ) THEN RAISE EXCEPTION 'New API bridge function inventory is unsafe'; END IF;
  IF (SELECT count(*) FROM pg_roles WHERE rolname LIKE 'invoice_newapi_%')<>7
     OR EXISTS (
       SELECT 1 FROM pg_roles WHERE rolname LIKE 'invoice_newapi_%'
         AND rolname NOT IN ('invoice_newapi_bridge_owner','invoice_newapi_payments_reader',
           'invoice_newapi_identities_reader','invoice_newapi_payments_v3_reader',
           'invoice_newapi_usage_reader','invoice_newapi_credits_reader','invoice_newapi_balances_reader')
     ) THEN RAISE EXCEPTION 'New API bridge role inventory is unsafe'; END IF;
  IF EXISTS (
    SELECT 1 FROM pg_roles WHERE rolname LIKE 'invoice_newapi_%' AND
      (rolsuper OR rolcreatedb OR rolcreaterole OR rolinherit OR rolreplication OR rolbypassrls)
  ) OR EXISTS (
    SELECT 1 FROM pg_roles WHERE rolname='invoice_newapi_bridge_owner'
      AND (rolcanlogin OR rolconnlimit<>0 OR rolconfig IS NOT NULL)
  ) OR EXISTS (
    SELECT 1 FROM pg_roles WHERE rolname='invoice_newapi_payments_reader'
      AND (rolcanlogin OR rolconnlimit<>0 OR NOT (rolconfig @> expected_gucs AND array_length(rolconfig,1)=4))
  ) OR EXISTS (
    SELECT 1 FROM pg_roles WHERE rolname IN ('invoice_newapi_identities_reader','invoice_newapi_payments_v3_reader',
      'invoice_newapi_usage_reader','invoice_newapi_credits_reader','invoice_newapi_balances_reader')
      AND (NOT rolcanlogin OR rolconnlimit<>2 OR NOT (rolconfig @> expected_gucs AND array_length(rolconfig,1)=4))
  ) THEN RAISE EXCEPTION 'New API bridge role attributes are unsafe'; END IF;
  IF EXISTS (
    SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.member OR r.oid=m.roleid
    WHERE r.rolname LIKE 'invoice_newapi_%'
  ) THEN RAISE EXCEPTION 'New API bridge role membership exists'; END IF;
END
$rollback$;

DO $rollback$
DECLARE owner_name text := 'invoice_newapi_bridge_owner';
BEGIN
  IF EXISTS (
    WITH declared(table_name,column_name) AS (VALUES
      ('top_ups','id'),('top_ups','user_id'),('top_ups','amount'),('top_ups','money'),
      ('top_ups','payment_method'),('top_ups','payment_provider'),('top_ups','create_time'),('top_ups','complete_time'),('top_ups','status'),
      ('subscription_orders','id'),('subscription_orders','user_id'),('subscription_orders','money'),
      ('subscription_orders','payment_method'),('subscription_orders','payment_provider'),('subscription_orders','status'),
      ('subscription_orders','create_time'),('subscription_orders','complete_time'),
      ('options','key'),('options','value'),
      ('custom_oauth_providers','id'),('custom_oauth_providers','slug'),('custom_oauth_providers','enabled'),
      ('custom_oauth_providers','well_known'),('custom_oauth_providers','authorization_endpoint'),
      ('custom_oauth_providers','token_endpoint'),('custom_oauth_providers','user_info_endpoint'),
      ('user_oauth_bindings','id'),('user_oauth_bindings','user_id'),('user_oauth_bindings','provider_id'),
      ('user_oauth_bindings','provider_user_id'),('user_oauth_bindings','created_at'),
      ('logs','id'),('logs','user_id'),('logs','created_at'),('logs','type'),('logs','quota'),
      ('checkins','id'),('checkins','user_id'),('checkins','quota_awarded'),('checkins','created_at'),
      ('redemptions','id'),('redemptions','status'),('redemptions','quota'),('redemptions','redeemed_time'),('redemptions','used_user_id'),
      ('users','id'),('users','quota'),('users','deleted_at')
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
  ) THEN RAISE EXCEPTION 'New API bridge owner column ACL differs from the exact contract'; END IF;
  IF EXISTS (
    SELECT 1 FROM information_schema.column_privileges
    WHERE grantee LIKE 'invoice_newapi_%' AND grantee<>owner_name
  ) OR EXISTS (
    SELECT 1 FROM information_schema.column_privileges
    WHERE grantee=owner_name AND privilege_type<>'SELECT'
  ) OR EXISTS (
    SELECT 1 FROM information_schema.role_table_grants WHERE grantee LIKE 'invoice_newapi_%'
  ) OR EXISTS (
    SELECT 1 FROM information_schema.role_usage_grants WHERE grantee LIKE 'invoice_newapi_%'
  ) THEN RAISE EXCEPTION 'New API bridge has an unexpected raw privilege'; END IF;
  IF EXISTS (
    WITH expected(role_name,function_name) AS (VALUES
      ('invoice_newapi_payments_reader','newapi_payments_v4'),
      ('invoice_newapi_payments_v3_reader','newapi_payments_v4'),
      ('invoice_newapi_identities_reader','newapi_identities_v4'),
      ('invoice_newapi_usage_reader','newapi_usage_v4'),
      ('invoice_newapi_credits_reader','newapi_credits_v4'),
      ('invoice_newapi_balances_reader','newapi_balances_v4')
    ), actual AS (
      SELECT r.rolname,p.proname
      FROM pg_roles r CROSS JOIN pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
      WHERE r.rolname LIKE 'invoice_newapi_%' AND r.rolname<>owner_name
        AND n.nspname='invoice_bridge' AND has_function_privilege(r.rolname,p.oid,'EXECUTE')
    ), mismatch AS (
      (SELECT * FROM expected EXCEPT SELECT * FROM actual)
      UNION ALL
      (SELECT * FROM actual EXCEPT SELECT * FROM expected)
    ) SELECT 1 FROM mismatch
  ) THEN RAISE EXCEPTION 'New API bridge caller EXECUTE ACL differs from the exact contract'; END IF;
  IF EXISTS (
    SELECT 1 FROM pg_namespace n,pg_roles r
    WHERE r.rolname LIKE 'invoice_newapi_%'
      AND n.nspname NOT IN ('public','invoice_bridge','information_schema')
      AND n.nspname NOT LIKE 'pg\_%' ESCAPE '\'
      AND (has_schema_privilege(r.rolname,n.oid,'USAGE') OR has_schema_privilege(r.rolname,n.oid,'CREATE'))
  ) OR EXISTS (
    SELECT 1 FROM pg_default_acl d JOIN pg_roles r ON r.oid=d.defaclrole WHERE r.rolname LIKE 'invoice_newapi_%'
  ) OR EXISTS (
    SELECT 1 FROM pg_class c JOIN pg_roles r ON r.oid=c.relowner WHERE r.rolname LIKE 'invoice_newapi_%'
  ) OR EXISTS (
    SELECT 1 FROM pg_namespace n JOIN pg_roles r ON r.oid=n.nspowner WHERE r.rolname LIKE 'invoice_newapi_%'
  ) OR EXISTS (
    SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace JOIN pg_roles r ON r.oid=p.proowner
    WHERE r.rolname LIKE 'invoice_newapi_%' AND (r.rolname<>owner_name OR n.nspname<>'invoice_bridge')
  ) THEN RAISE EXCEPTION 'New API bridge role has unexpected ACL, default ACL or ownership'; END IF;
END
$rollback$;

DO $rollback$
DECLARE role_name text;
BEGIN
  FOREACH role_name IN ARRAY ARRAY[
    'invoice_newapi_payments_reader','invoice_newapi_identities_reader','invoice_newapi_payments_v3_reader',
    'invoice_newapi_usage_reader','invoice_newapi_credits_reader','invoice_newapi_balances_reader'
  ] LOOP
    EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM %I',current_database(),role_name);
  END LOOP;
  IF EXISTS (
    SELECT 1 FROM pg_stat_activity WHERE datname=current_database()
      AND usename IN ('invoice_newapi_payments_reader','invoice_newapi_identities_reader','invoice_newapi_payments_v3_reader',
        'invoice_newapi_usage_reader','invoice_newapi_credits_reader','invoice_newapi_balances_reader')
  ) THEN RAISE EXCEPTION 'New API bridge readers still have active sessions'; END IF;
END
$rollback$;

DROP FUNCTION invoice_bridge.newapi_payments_v4(text,jsonb) RESTRICT;
DROP FUNCTION invoice_bridge.newapi_usage_v4(text,jsonb) RESTRICT;
DROP FUNCTION invoice_bridge.newapi_credits_v4(text,jsonb) RESTRICT;
DROP FUNCTION invoice_bridge.newapi_balances_v4(text,jsonb) RESTRICT;
DROP FUNCTION invoice_bridge.newapi_identities_v4(text,jsonb) RESTRICT;
DROP SCHEMA invoice_bridge RESTRICT;

-- The ACL inventory above proved the exact owner column grants. Revoke only
-- those known grants. Never use DROP OWNED here: it can delete an unrelated
-- enum, FDW object or other catalog object accidentally owned by this role.
DO $rollback$
DECLARE grant_row record;
BEGIN
  FOR grant_row IN
    SELECT table_schema,table_name,
      string_agg(format('%I',column_name),',' ORDER BY column_name) AS columns
    FROM information_schema.column_privileges
    WHERE grantee='invoice_newapi_bridge_owner' AND privilege_type='SELECT'
    GROUP BY table_schema,table_name
  LOOP
    EXECUTE format('REVOKE SELECT (%s) ON TABLE %I.%I FROM %I',
      grant_row.columns,grant_row.table_schema,grant_row.table_name,
      'invoice_newapi_bridge_owner');
  END LOOP;
  REVOKE USAGE ON SCHEMA public FROM invoice_newapi_bridge_owner;
END
$rollback$;

-- DROP ROLE is the final unknown-dependency guard. Any unreviewed ACL,
-- ownership, publication, type, FDW or other shared dependency aborts and
-- rolls back the complete transaction instead of deleting that object.
DROP ROLE invoice_newapi_payments_reader;
DROP ROLE invoice_newapi_identities_reader;
DROP ROLE invoice_newapi_payments_v3_reader;
DROP ROLE invoice_newapi_usage_reader;
DROP ROLE invoice_newapi_credits_reader;
DROP ROLE invoice_newapi_balances_reader;
DROP ROLE invoice_newapi_bridge_owner;

DO $rollback$ BEGIN
  EXECUTE format('GRANT TEMPORARY ON DATABASE %I TO PUBLIC',current_database());
END $rollback$;

COMMIT;
