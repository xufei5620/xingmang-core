-- READ-ONLY SOURCE CUTOVER QUIESCENCE GATE.
-- Required psql variable: invoice_source = newapi | sub2api.
--
-- This proves only database-visible quiescence. The operator must stop the
-- matching upstream application container and keep it stopped from before this
-- gate until cutover manifest/baseline verification has completed.

\set ON_ERROR_STOP on

\if :{?invoice_source}
\else
  \echo 'invoice_source is required (newapi or sub2api)'
  \quit 2
\endif

BEGIN READ ONLY;
SET LOCAL statement_timeout = '15s';
SET LOCAL idle_in_transaction_session_timeout = '15s';
SELECT set_config('invoice.cutover.source', :'invoice_source', true);

DO $quiescence$
DECLARE
  source_name text := current_setting('invoice.cutover.source');
  fingerprint_tables text[];
  missing text;
  executor_oid oid;
  executor_superuser boolean;
  database_owner oid;
  other_clients bigint;
  prepared_transactions bigint;
BEGIN
  IF source_name = 'newapi' THEN
    fingerprint_tables := ARRAY[
      'public.top_ups','public.subscription_orders','public.logs','public.options',
      'public.users','public.checkins','public.redemptions',
      'public.custom_oauth_providers','public.user_oauth_bindings'
    ];
  ELSIF source_name = 'sub2api' THEN
    fingerprint_tables := ARRAY[
      'public.payment_orders','public.auth_identities','public.usage_logs',
      'public.settings','public.users','public.promo_code_usages',
      'public.redeem_codes','public.user_affiliate_ledger'
    ];
  ELSE
    RAISE EXCEPTION 'unsupported invoice_source: %', source_name;
  END IF;

  SELECT oid,rolsuper INTO executor_oid,executor_superuser
  FROM pg_roles WHERE rolname=current_user;
  SELECT datdba INTO database_owner FROM pg_database WHERE datname=current_database();
  IF executor_oid IS NULL OR NOT executor_superuser OR executor_oid<>database_owner THEN
    RAISE EXCEPTION 'cutover quiescence gate requires the cluster superuser that owns the current database';
  END IF;

  SELECT string_agg(required.table_name,', ' ORDER BY required.table_name)
  INTO missing
  FROM unnest(fingerprint_tables) required(table_name)
  WHERE NOT EXISTS (
    SELECT 1 FROM pg_class relation
    JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
    WHERE format('%I.%I',namespace.nspname,relation.relname)=required.table_name
      AND relation.relkind IN ('r','p')
  );
  IF missing IS NOT NULL THEN
    RAISE EXCEPTION '% source fingerprint is incomplete; missing application tables: %',source_name,missing;
  END IF;

  SELECT count(*) INTO other_clients
  FROM pg_stat_activity activity
  WHERE activity.datname=current_database()
    AND activity.backend_type='client backend'
    AND activity.pid<>pg_backend_pid();
  IF other_clients<>0 THEN
    RAISE EXCEPTION 'cutover blocked: % other client backend(s) remain connected',other_clients;
  END IF;

  SELECT count(*) INTO prepared_transactions
  FROM pg_prepared_xacts prepared
  WHERE prepared.database=current_database();
  IF prepared_transactions<>0 THEN
    RAISE EXCEPTION 'cutover blocked: % prepared transaction(s) can still commit',prepared_transactions;
  END IF;
END $quiescence$;

SELECT current_setting('invoice.cutover.source') AS source_name,
  current_database() AS database_name,
  0::bigint AS other_client_backends,
  0::bigint AS prepared_transactions,
  'quiescence-preflight-passed'::text AS status;

COMMIT;
