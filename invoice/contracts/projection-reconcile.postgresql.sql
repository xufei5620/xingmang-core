-- REVIEWED RECONCILIATION FOR LEGACY INVOICE PROJECTION OBJECTS.
--
-- Required psql variables:
--   invoice_source  = newapi | sub2api
--   reconcile_apply = 0 (audit/dry-run) | 1 (apply)
--
-- This contract is deliberately limited to the exact legacy public views and
-- six reader roles listed below.  It never changes an upstream application
-- table or row, never terminates a session, and never requests dependent-object
-- removal. Any unknown invoice-prefixed object, role membership, elevated role,
-- owned object, active reader session, or unexpected role grant makes the
-- transaction fail closed.

\set ON_ERROR_STOP on

\if :{?invoice_source}
\else
  \echo 'invoice_source is required (newapi or sub2api)'
  \quit 2
\endif

\if :{?reconcile_apply}
\else
  \echo 'reconcile_apply is required (0 for audit, 1 for apply)'
  \quit 2
\endif

\if :reconcile_apply
BEGIN;
\else
BEGIN READ ONLY;
\endif

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '15s';
SET LOCAL idle_in_transaction_session_timeout = '15s';

SELECT set_config('invoice.reconcile.source', :'invoice_source', true);
SELECT set_config('invoice.reconcile.apply', :'reconcile_apply', true);

DO $audit$
DECLARE
  source_name text := current_setting('invoice.reconcile.source');
  apply_mode text := current_setting('invoice.reconcile.apply');
  object_prefix text;
  expected_views text[];
  expected_roles text[];
  fingerprint_tables text[];
  unexpected text;
  unsafe_count bigint;
  database_owner name;
  executor_is_superuser boolean;
BEGIN
  IF source_name = 'newapi' THEN
    object_prefix := 'invoice!_newapi!_%';
    expected_views := ARRAY[
      'invoice_newapi_cutover_contract_v3',
      'invoice_newapi_payments_projection_health_v3',
      'invoice_newapi_payments_projection_v3',
      'invoice_newapi_usage_projection_health_v3',
      'invoice_newapi_usage_projection_v3',
      'invoice_newapi_credits_projection_health_v3',
      'invoice_newapi_credits_projection_v3',
      'invoice_newapi_balance_projection_v3',
      'invoice_newapi_wallet_config_contract_v3',
      'invoice_newapi_oidc_binding_projection_v1',
      'invoice_newapi_oidc_provider_contract_v1'
    ];
    expected_roles := ARRAY[
      'invoice_newapi_payments_reader',
      'invoice_newapi_identities_reader',
      'invoice_newapi_payments_v3_reader',
      'invoice_newapi_usage_reader',
      'invoice_newapi_credits_reader',
      'invoice_newapi_balances_reader'
    ];
    fingerprint_tables := ARRAY[
      'public.top_ups',
      'public.subscription_orders',
      'public.logs',
      'public.options',
      'public.users',
      'public.checkins',
      'public.redemptions',
      'public.custom_oauth_providers',
      'public.user_oauth_bindings'
    ];
  ELSIF source_name = 'sub2api' THEN
    object_prefix := 'invoice!_sub2api!_%';
    expected_views := ARRAY[
      'invoice_sub2api_cutover_contract_v3',
      'invoice_sub2api_payments_projection_health_v3',
      'invoice_sub2api_payments_projection_v3',
      'invoice_sub2api_usage_projection_health_v3',
      'invoice_sub2api_usage_projection_v3',
      'invoice_sub2api_credits_projection_health_v3',
      'invoice_sub2api_credits_projection_v3',
      'invoice_sub2api_balance_projection_v3',
      'invoice_sub2api_wallet_config_contract_v3',
      'invoice_sub2api_oidc_binding_projection_v1',
      'invoice_sub2api_payment_projection_health_v1',
      'invoice_sub2api_payment_projection_v1'
    ];
    expected_roles := ARRAY[
      'invoice_sub2api_payments_reader',
      'invoice_sub2api_identities_reader',
      'invoice_sub2api_payments_v3_reader',
      'invoice_sub2api_usage_reader',
      'invoice_sub2api_credits_reader',
      'invoice_sub2api_balances_reader'
    ];
    fingerprint_tables := ARRAY[
      'public.payment_orders',
      'public.auth_identities',
      'public.usage_logs',
      'public.settings',
      'public.users',
      'public.promo_code_usages',
      'public.redeem_codes',
      'public.user_affiliate_ledger'
    ];
  ELSE
    RAISE EXCEPTION 'unsupported invoice_source: %', source_name;
  END IF;

  IF apply_mode NOT IN ('0', '1') THEN
    RAISE EXCEPTION 'reconcile_apply must be exactly 0 or 1';
  END IF;

  IF current_database() IN ('postgres', 'template0', 'template1') THEN
    RAISE EXCEPTION 'refusing reconciliation in maintenance database %', current_database();
  END IF;

  SELECT owner_role.rolname, executor_role.rolsuper
    INTO database_owner, executor_is_superuser
  FROM pg_database database_row
  JOIN pg_roles owner_role ON owner_role.oid = database_row.datdba
  JOIN pg_roles executor_role ON executor_role.rolname = current_user
  WHERE database_row.datname = current_database();

  IF database_owner IS NULL OR NOT (
    executor_is_superuser
    OR current_user = database_owner
    OR pg_has_role(current_user, database_owner, 'MEMBER')
  ) THEN
    RAISE EXCEPTION 'run as the current source database owner or a superuser';
  END IF;

  SELECT string_agg(table_name, ', ' ORDER BY table_name)
    INTO unexpected
  FROM unnest(fingerprint_tables) AS required(table_name)
  WHERE NOT EXISTS (
    SELECT 1
    FROM pg_class relation
    JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
    WHERE format('%I.%I', namespace.nspname, relation.relname) = required.table_name
      AND relation.relkind IN ('r', 'p')
  );
  IF unexpected IS NOT NULL THEN
    RAISE EXCEPTION '% source fingerprint is incomplete; missing application tables: %',
      source_name, unexpected;
  END IF;

  IF EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'invoice_bridge') THEN
    RAISE EXCEPTION 'invoice_bridge is already installed; use the bridge-v4 rollback contract instead';
  END IF;

  SELECT string_agg(
      format('%I.%I[%s]', namespace.nspname, relation.relname, relation.relkind),
      ', ' ORDER BY namespace.nspname, relation.relname
    )
    INTO unexpected
  FROM pg_class relation
  JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
  WHERE relation.relname LIKE object_prefix ESCAPE '!'
    AND NOT (
      namespace.nspname = 'public'
      AND relation.relkind = 'v'
      AND relation.relname = ANY(expected_views)
    );
  IF unexpected IS NOT NULL THEN
    RAISE EXCEPTION 'unknown or wrong-kind legacy % projection objects: %',
      source_name, unexpected;
  END IF;

  SELECT string_agg(role_row.rolname, ', ' ORDER BY role_row.rolname)
    INTO unexpected
  FROM pg_roles role_row
  WHERE role_row.rolname LIKE object_prefix ESCAPE '!'
    AND role_row.rolname <> ALL(expected_roles);
  IF unexpected IS NOT NULL THEN
    RAISE EXCEPTION 'unknown % invoice roles require manual review: %',
      source_name, unexpected;
  END IF;

  SELECT count(*) INTO unsafe_count
  FROM pg_roles role_row
  WHERE role_row.rolname = ANY(expected_roles)
    AND (
      role_row.rolsuper
      OR role_row.rolcreatedb
      OR role_row.rolcreaterole
      OR role_row.rolreplication
      OR role_row.rolbypassrls
    );
  IF unsafe_count <> 0 THEN
    RAISE EXCEPTION '% legacy invoice role(s) have elevated attributes', unsafe_count;
  END IF;

  SELECT count(*) INTO unsafe_count
  FROM pg_auth_members membership
  JOIN pg_roles member_role ON member_role.oid = membership.member
  JOIN pg_roles granted_role ON granted_role.oid = membership.roleid
  WHERE member_role.rolname = ANY(expected_roles)
     OR granted_role.rolname = ANY(expected_roles);
  IF unsafe_count <> 0 THEN
    RAISE EXCEPTION '% legacy invoice role membership(s) require manual review', unsafe_count;
  END IF;

  SELECT count(*) INTO unsafe_count
  FROM pg_shdepend dependency
  JOIN pg_roles role_row
    ON dependency.refclassid = 'pg_authid'::regclass
   AND dependency.refobjid = role_row.oid
  WHERE role_row.rolname = ANY(expected_roles)
    AND dependency.deptype = 'o';
  IF unsafe_count <> 0 THEN
    RAISE EXCEPTION '% object ownership dependency/dependencies block automatic role cleanup',
      unsafe_count;
  END IF;

  -- ACL dependencies are allowed only on the current database, public schema,
  -- the exact legacy views that this transaction drops, and one observed New
  -- API partial-state compatibility grant. Any ACL on another raw relation,
  -- routine, default ACL or database remains an unknown dependency and fails
  -- before apply changes role attributes.
  SELECT count(*) INTO unsafe_count
  FROM pg_shdepend dependency
  JOIN pg_roles role_row
    ON dependency.refclassid = 'pg_authid'::regclass
   AND dependency.refobjid = role_row.oid
  WHERE role_row.rolname = ANY(expected_roles)
    AND dependency.deptype = 'a'
    AND NOT (
      (
        dependency.classid = 'pg_database'::regclass
        AND dependency.objid = (SELECT oid FROM pg_database WHERE datname = current_database())
        AND dependency.objsubid = 0
      ) OR (
        dependency.dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
        AND dependency.classid = 'pg_namespace'::regclass
        AND dependency.objid = (SELECT oid FROM pg_namespace WHERE nspname = 'public')
        AND dependency.objsubid = 0
      ) OR (
        dependency.dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
        AND dependency.classid = 'pg_class'::regclass
        AND EXISTS (
          SELECT 1
          FROM pg_class relation
          JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
          WHERE relation.oid = dependency.objid
            AND namespace.nspname = 'public'
            AND relation.relkind = 'v'
            AND relation.relname = ANY(expected_views)
        )
      ) OR (
        source_name = 'newapi'
        AND role_row.rolname = 'invoice_newapi_payments_reader'
        AND dependency.dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
        AND dependency.classid = 'pg_class'::regclass
        AND dependency.objid = to_regclass('public.top_ups')
        AND dependency.objsubid IN (
          SELECT attribute.attnum
          FROM pg_attribute attribute
          WHERE attribute.attrelid = to_regclass('public.top_ups')
            AND attribute.attname = ANY(ARRAY[
              'id','user_id','amount','money','payment_method','payment_provider',
              'create_time','complete_time','status'
            ])
            AND NOT attribute.attisdropped
        )
      )
    );
  IF unsafe_count <> 0 THEN
    RAISE EXCEPTION '% unexpected legacy invoice ACL dependency/dependencies require manual review',
      unsafe_count;
  END IF;

  SELECT count(*) INTO unsafe_count
  FROM pg_database database_row
  CROSS JOIN LATERAL aclexplode(
    COALESCE(database_row.datacl, acldefault('d', database_row.datdba))
  ) privilege
  JOIN pg_roles grantee ON grantee.oid = privilege.grantee
  WHERE database_row.datname = current_database()
    AND grantee.rolname = ANY(expected_roles)
    AND NOT (
      privilege.privilege_type = 'CONNECT'
      AND NOT privilege.is_grantable
    );
  IF unsafe_count <> 0 THEN
    RAISE EXCEPTION '% unsafe direct database privilege(s) require manual review', unsafe_count;
  END IF;

  SELECT count(*) INTO unsafe_count
  FROM pg_namespace namespace
  CROSS JOIN LATERAL aclexplode(
    COALESCE(namespace.nspacl, acldefault('n', namespace.nspowner))
  ) privilege
  JOIN pg_roles grantee ON grantee.oid = privilege.grantee
  WHERE namespace.nspname = 'public'
    AND grantee.rolname = ANY(expected_roles)
    AND NOT (
      privilege.privilege_type = 'USAGE'
      AND NOT privilege.is_grantable
    );
  IF unsafe_count <> 0 THEN
    RAISE EXCEPTION '% unsafe direct public-schema privilege(s) require manual review', unsafe_count;
  END IF;

  SELECT count(*) INTO unsafe_count
  FROM pg_class relation
  JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
  CROSS JOIN LATERAL aclexplode(relation.relacl) privilege
  JOIN pg_roles grantee ON grantee.oid = privilege.grantee
  WHERE namespace.nspname = 'public'
    AND relation.relkind = 'v'
    AND relation.relname = ANY(expected_views)
    AND grantee.rolname = ANY(expected_roles)
    AND NOT (
      privilege.privilege_type = 'SELECT'
      AND NOT privilege.is_grantable
    );
  IF unsafe_count <> 0 THEN
    RAISE EXCEPTION '% unsafe direct legacy-view privilege(s) require manual review', unsafe_count;
  END IF;

  SELECT count(*) INTO unsafe_count
  FROM pg_attribute attribute
  JOIN pg_class relation ON relation.oid = attribute.attrelid
  JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
  CROSS JOIN LATERAL aclexplode(attribute.attacl) privilege
  JOIN pg_roles grantee ON grantee.oid = privilege.grantee
  WHERE namespace.nspname = 'public'
    AND relation.relkind = 'v'
    AND relation.relname = ANY(expected_views)
    AND grantee.rolname = ANY(expected_roles)
    AND NOT (
      privilege.privilege_type = 'SELECT'
      AND NOT privilege.is_grantable
    );
  IF unsafe_count <> 0 THEN
    RAISE EXCEPTION '% unsafe direct legacy-view column privilege(s) require manual review',
      unsafe_count;
  END IF;

  IF source_name = 'newapi' THEN
    SELECT count(*) INTO unsafe_count
    FROM pg_attribute attribute
    JOIN pg_class relation ON relation.oid = attribute.attrelid
    JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
    CROSS JOIN LATERAL aclexplode(attribute.attacl) privilege
    JOIN pg_roles grantee ON grantee.oid = privilege.grantee
    WHERE namespace.nspname = 'public'
      AND relation.relname = 'top_ups'
      AND grantee.rolname = 'invoice_newapi_payments_reader'
      AND NOT (
        attribute.attname = ANY(ARRAY[
          'id','user_id','amount','money','payment_method','payment_provider',
          'create_time','complete_time','status'
        ])
        AND privilege.privilege_type = 'SELECT'
        AND NOT privilege.is_grantable
      );
    IF unsafe_count <> 0 THEN
      RAISE EXCEPTION '% unsafe New API compatibility column privilege(s) require manual review',
        unsafe_count;
    END IF;
  END IF;

  SELECT count(*) INTO unsafe_count
  FROM pg_stat_activity activity
  WHERE activity.datname = current_database()
    AND activity.usename = ANY(expected_roles)
    AND activity.pid <> pg_backend_pid();
  IF unsafe_count <> 0 THEN
    RAISE EXCEPTION '% active legacy invoice reader session(s); stop agents and retry',
      unsafe_count;
  END IF;
END $audit$;

WITH configuration AS (
  SELECT current_setting('invoice.reconcile.source') AS source_name
), expected_views AS (
  SELECT unnest(CASE source_name
    WHEN 'newapi' THEN ARRAY[
      'invoice_newapi_cutover_contract_v3',
      'invoice_newapi_payments_projection_health_v3',
      'invoice_newapi_payments_projection_v3',
      'invoice_newapi_usage_projection_health_v3',
      'invoice_newapi_usage_projection_v3',
      'invoice_newapi_credits_projection_health_v3',
      'invoice_newapi_credits_projection_v3',
      'invoice_newapi_balance_projection_v3',
      'invoice_newapi_wallet_config_contract_v3',
      'invoice_newapi_oidc_binding_projection_v1',
      'invoice_newapi_oidc_provider_contract_v1'
    ]
    ELSE ARRAY[
      'invoice_sub2api_cutover_contract_v3',
      'invoice_sub2api_payments_projection_health_v3',
      'invoice_sub2api_payments_projection_v3',
      'invoice_sub2api_usage_projection_health_v3',
      'invoice_sub2api_usage_projection_v3',
      'invoice_sub2api_credits_projection_health_v3',
      'invoice_sub2api_credits_projection_v3',
      'invoice_sub2api_balance_projection_v3',
      'invoice_sub2api_wallet_config_contract_v3',
      'invoice_sub2api_oidc_binding_projection_v1',
      'invoice_sub2api_payment_projection_health_v1',
      'invoice_sub2api_payment_projection_v1'
    ] END) AS view_name
  FROM configuration
), expected_roles AS (
  SELECT unnest(CASE source_name
    WHEN 'newapi' THEN ARRAY[
      'invoice_newapi_payments_reader',
      'invoice_newapi_identities_reader',
      'invoice_newapi_payments_v3_reader',
      'invoice_newapi_usage_reader',
      'invoice_newapi_credits_reader',
      'invoice_newapi_balances_reader'
    ]
    ELSE ARRAY[
      'invoice_sub2api_payments_reader',
      'invoice_sub2api_identities_reader',
      'invoice_sub2api_payments_v3_reader',
      'invoice_sub2api_usage_reader',
      'invoice_sub2api_credits_reader',
      'invoice_sub2api_balances_reader'
    ] END) AS role_name
  FROM configuration
), present_views AS (
  SELECT relation.oid
  FROM pg_class relation
  JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
  JOIN expected_views expected ON expected.view_name = relation.relname
  WHERE namespace.nspname = 'public' AND relation.relkind = 'v'
), upstream_dependencies AS (
  SELECT dependency.objid, dependency.refobjid, dependency.refobjsubid
  FROM present_views invoice_view
  JOIN pg_rewrite rewrite_rule ON rewrite_rule.ev_class = invoice_view.oid
  JOIN pg_depend dependency
    ON dependency.classid = 'pg_rewrite'::regclass
   AND dependency.objid = rewrite_rule.oid
  JOIN pg_class referenced_relation
    ON dependency.refclassid = 'pg_class'::regclass
   AND dependency.refobjid = referenced_relation.oid
  JOIN pg_namespace referenced_namespace
    ON referenced_namespace.oid = referenced_relation.relnamespace
  WHERE referenced_namespace.nspname = 'public'
    AND referenced_relation.oid <> invoice_view.oid
)
SELECT
  configuration.source_name,
  current_database() AS database_name,
  current_user AS executor,
  (SELECT count(*) FROM present_views) AS legacy_views_present,
  (SELECT count(*) FROM pg_roles role_row JOIN expected_roles expected ON expected.role_name = role_row.rolname)
    AS legacy_roles_present,
  (SELECT count(*) FROM pg_stat_activity activity JOIN expected_roles expected ON expected.role_name = activity.usename
    WHERE activity.datname = current_database() AND activity.pid <> pg_backend_pid()) AS active_reader_sessions,
  (SELECT count(*) FROM upstream_dependencies) AS upstream_pg_depend_edges,
  EXISTS (
    SELECT 1
    FROM pg_database database_row
    CROSS JOIN LATERAL aclexplode(
      COALESCE(database_row.datacl, acldefault('d', database_row.datdba))
    ) privilege
    WHERE database_row.datname = current_database()
      AND privilege.grantee = 0
      AND privilege.privilege_type IN ('TEMP', 'TEMPORARY')
  ) AS public_temporary,
  CASE current_setting('invoice.reconcile.apply') WHEN '1' THEN 'apply' ELSE 'audit-only' END AS mode
FROM configuration;

\if :reconcile_apply

DO $cleanup$
DECLARE
  source_name text := current_setting('invoice.reconcile.source');
  expected_views text[];
  expected_roles text[];
  view_name text;
  role_name text;
  compatibility_columns text;
  unsafe_count bigint;
BEGIN
  IF NOT pg_try_advisory_xact_lock(
    hashtextextended(current_database() || ':invoice:' || source_name || ':legacy-reconcile', 0)
  ) THEN
    RAISE EXCEPTION 'another % projection reconciliation is running', source_name;
  END IF;

  IF source_name = 'newapi' THEN
    expected_views := ARRAY[
      'invoice_newapi_cutover_contract_v3',
      'invoice_newapi_payments_projection_health_v3',
      'invoice_newapi_payments_projection_v3',
      'invoice_newapi_usage_projection_health_v3',
      'invoice_newapi_usage_projection_v3',
      'invoice_newapi_credits_projection_health_v3',
      'invoice_newapi_credits_projection_v3',
      'invoice_newapi_balance_projection_v3',
      'invoice_newapi_wallet_config_contract_v3',
      'invoice_newapi_oidc_binding_projection_v1',
      'invoice_newapi_oidc_provider_contract_v1'
    ];
    expected_roles := ARRAY[
      'invoice_newapi_payments_reader',
      'invoice_newapi_identities_reader',
      'invoice_newapi_payments_v3_reader',
      'invoice_newapi_usage_reader',
      'invoice_newapi_credits_reader',
      'invoice_newapi_balances_reader'
    ];
  ELSE
    expected_views := ARRAY[
      'invoice_sub2api_cutover_contract_v3',
      'invoice_sub2api_payments_projection_health_v3',
      'invoice_sub2api_payments_projection_v3',
      'invoice_sub2api_usage_projection_health_v3',
      'invoice_sub2api_usage_projection_v3',
      'invoice_sub2api_credits_projection_health_v3',
      'invoice_sub2api_credits_projection_v3',
      'invoice_sub2api_balance_projection_v3',
      'invoice_sub2api_wallet_config_contract_v3',
      'invoice_sub2api_oidc_binding_projection_v1',
      'invoice_sub2api_payment_projection_health_v1',
      'invoice_sub2api_payment_projection_v1'
    ];
    expected_roles := ARRAY[
      'invoice_sub2api_payments_reader',
      'invoice_sub2api_identities_reader',
      'invoice_sub2api_payments_v3_reader',
      'invoice_sub2api_usage_reader',
      'invoice_sub2api_credits_reader',
      'invoice_sub2api_balances_reader'
    ];
  END IF;

  FOREACH role_name IN ARRAY expected_roles LOOP
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
      EXECUTE format('ALTER ROLE %I NOLOGIN CONNECTION LIMIT 0', role_name);
      EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM %I', current_database(), role_name);
    END IF;
  END LOOP;

  SELECT count(*) INTO unsafe_count
  FROM pg_stat_activity activity
  WHERE activity.datname = current_database()
    AND activity.usename = ANY(expected_roles)
    AND activity.pid <> pg_backend_pid();
  IF unsafe_count <> 0 THEN
    RAISE EXCEPTION '% reader session(s) appeared during reconciliation; transaction rolled back',
      unsafe_count;
  END IF;

  FOREACH view_name IN ARRAY expected_views LOOP
    IF EXISTS (
      SELECT 1
      FROM pg_class relation
      JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
      WHERE namespace.nspname = 'public'
        AND relation.relname = view_name
        AND relation.relkind = 'v'
    ) THEN
      EXECUTE format('DROP VIEW %I.%I RESTRICT', 'public', view_name);
    END IF;
  END LOOP;

  IF source_name = 'newapi'
     AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'invoice_newapi_payments_reader') THEN
    SELECT string_agg(format('%I', allowed.attname), ',' ORDER BY allowed.attnum)
      INTO compatibility_columns
    FROM (
      SELECT DISTINCT attribute.attname, attribute.attnum
      FROM pg_attribute attribute
      CROSS JOIN LATERAL aclexplode(attribute.attacl) privilege
      JOIN pg_roles grantee ON grantee.oid = privilege.grantee
      WHERE attribute.attrelid = to_regclass('public.top_ups')
        AND attribute.attname = ANY(ARRAY[
          'id','user_id','amount','money','payment_method','payment_provider',
          'create_time','complete_time','status'
        ])
        AND NOT attribute.attisdropped
        AND grantee.rolname = 'invoice_newapi_payments_reader'
        AND privilege.privilege_type = 'SELECT'
        AND NOT privilege.is_grantable
    ) allowed;
    IF compatibility_columns IS NOT NULL THEN
      EXECUTE format(
        'REVOKE SELECT (%s) ON TABLE public.top_ups FROM %I RESTRICT',
        compatibility_columns,
        'invoice_newapi_payments_reader'
      );
    END IF;
  END IF;

  FOREACH role_name IN ARRAY expected_roles LOOP
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
      EXECUTE format('REVOKE ALL PRIVILEGES ON DATABASE %I FROM %I', current_database(), role_name);
      EXECUTE format('REVOKE ALL PRIVILEGES ON SCHEMA %I FROM %I', 'public', role_name);
      -- DROP ROLE itself is the final guard: any unexpected ACL/default ACL or
      -- dependency that was not one of the named views aborts the transaction.
      EXECUTE format('DROP ROLE %I', role_name);
    END IF;
  END LOOP;

  EXECUTE format('GRANT TEMPORARY ON DATABASE %I TO PUBLIC', current_database());
END $cleanup$;

DO $postcondition$
DECLARE
  source_name text := current_setting('invoice.reconcile.source');
  object_prefix text := CASE source_name
    WHEN 'newapi' THEN 'invoice!_newapi!_%'
    ELSE 'invoice!_sub2api!_%'
  END;
  remaining_count bigint;
BEGIN
  SELECT count(*) INTO remaining_count
  FROM pg_class relation
  JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
  WHERE namespace.nspname = 'public'
    AND relation.relname LIKE object_prefix ESCAPE '!';
  IF remaining_count <> 0 THEN
    RAISE EXCEPTION '% legacy public projection object(s) remain', remaining_count;
  END IF;

  SELECT count(*) INTO remaining_count
  FROM pg_roles role_row
  WHERE role_row.rolname LIKE object_prefix ESCAPE '!';
  IF remaining_count <> 0 THEN
    RAISE EXCEPTION '% legacy invoice role(s) remain', remaining_count;
  END IF;

  IF NOT EXISTS (
    SELECT 1
    FROM pg_database database_row
    CROSS JOIN LATERAL aclexplode(
      COALESCE(database_row.datacl, acldefault('d', database_row.datdba))
    ) privilege
    WHERE database_row.datname = current_database()
      AND privilege.grantee = 0
      AND privilege.privilege_type IN ('TEMP', 'TEMPORARY')
  ) THEN
    RAISE EXCEPTION 'PUBLIC TEMPORARY default was not restored';
  END IF;
END $postcondition$;

COMMIT;

SELECT
  :'invoice_source'::text AS source_name,
  current_database() AS database_name,
  0::bigint AS legacy_views_present,
  0::bigint AS legacy_roles_present,
  0::bigint AS active_reader_sessions,
  0::bigint AS upstream_pg_depend_edges,
  EXISTS (
    SELECT 1
    FROM pg_database database_row
    CROSS JOIN LATERAL aclexplode(
      COALESCE(database_row.datacl, acldefault('d', database_row.datdba))
    ) privilege
    WHERE database_row.datname = current_database()
      AND privilege.grantee = 0
      AND privilege.privilege_type IN ('TEMP', 'TEMPORARY')
  ) AS public_temporary,
  'reconciled'::text AS status;

\else

ROLLBACK;
\echo 'Audit only: no catalog or privilege change was committed.'

\endif
