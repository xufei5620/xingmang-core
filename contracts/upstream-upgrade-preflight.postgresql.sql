-- READ-ONLY UPGRADE GATE FOR AN UPSTREAM NEW API OR SUB2API DATABASE.
--
-- Required psql variables:
--   invoice_source = newapi | sub2api
--   boundary_state = detached | bridge-v4
--
-- The gate proves that no legacy invoice view remains, no source-agent session
-- is connected, and the invoice boundary has zero persistent pg_depend edges
-- to upstream application relations.  It never writes the database.

\set ON_ERROR_STOP on

\if :{?invoice_source}
\else
  \echo 'invoice_source is required (newapi or sub2api)'
  \quit 2
\endif

\if :{?boundary_state}
\else
  \echo 'boundary_state is required (detached or bridge-v4)'
  \quit 2
\endif

BEGIN READ ONLY;
SET LOCAL statement_timeout = '15s';
SET LOCAL idle_in_transaction_session_timeout = '15s';

SELECT set_config('invoice.upgrade.source', :'invoice_source', true);
SELECT set_config('invoice.upgrade.boundary_state', :'boundary_state', true);

DO $preflight$
DECLARE
  source_name text := current_setting('invoice.upgrade.source');
  boundary_mode text := current_setting('invoice.upgrade.boundary_state');
  object_prefix text;
  expected_functions text[];
  expected_function_hashes text[];
  expected_owner_columns text[];
  expected_grant_functions text[];
  expected_grant_roles text[];
  expected_reader_roles text[];
  expected_bridge_roles text[];
  fingerprint_tables text[];
  bridge_owner text;
  unexpected text;
  check_count bigint;
  dependency_count bigint;
  public_temp boolean;
  database_owner name;
  executor_is_superuser boolean;
  role_name text;
  expected_login boolean;
  expected_connection_limit integer;
  actual_role_config text[];
  expected_role_config text[] := ARRAY[
    'default_transaction_read_only=on',
    'idle_in_transaction_session_timeout=15s',
    'lock_timeout=5s',
    'statement_timeout=15s'
  ];
BEGIN
  IF source_name = 'newapi' THEN
    object_prefix := 'invoice!_newapi!_%';
    bridge_owner := 'invoice_newapi_bridge_owner';
    expected_functions := ARRAY[
      'newapi_payments_v4',
      'newapi_usage_v4',
      'newapi_credits_v4',
      'newapi_balances_v4',
      'newapi_identities_v4'
    ];
    expected_function_hashes := ARRAY[
      'd908e1ef57383ad10ccf0c4d2e266577e51a5e37a1684866ad5dd4f347c10dcf',
      'ca68cbf1a9ce5eaacde3c52b2778f535bf3510b24c095150ed2bf7bd7fd8843a',
      '307183eda0f2e6900ea9c7dd49194e499e9abfbbb07d308b1558769e257bd8ff',
      '63ab9a5c45cb06267c59a6b8e105149679249fce94b7f73abefd85e6f856d484',
      'dd92d2fe4b37a8b22509d19507ebae1143dde9952a185a0a180336cc3ef4a7a1'
    ];
    expected_owner_columns := ARRAY[
      'top_ups.id','top_ups.user_id','top_ups.amount','top_ups.money',
      'top_ups.payment_method','top_ups.payment_provider','top_ups.create_time','top_ups.complete_time','top_ups.status',
      'subscription_orders.id','subscription_orders.user_id','subscription_orders.money',
      'subscription_orders.payment_method','subscription_orders.payment_provider','subscription_orders.status',
      'subscription_orders.create_time','subscription_orders.complete_time',
      'options.key','options.value',
      'custom_oauth_providers.id','custom_oauth_providers.slug','custom_oauth_providers.enabled',
      'custom_oauth_providers.well_known','custom_oauth_providers.authorization_endpoint',
      'custom_oauth_providers.token_endpoint','custom_oauth_providers.user_info_endpoint',
      'user_oauth_bindings.id','user_oauth_bindings.user_id','user_oauth_bindings.provider_id',
      'user_oauth_bindings.provider_user_id','user_oauth_bindings.created_at',
      'logs.id','logs.user_id','logs.created_at','logs.type','logs.quota',
      'checkins.id','checkins.user_id','checkins.quota_awarded','checkins.created_at',
      'redemptions.id','redemptions.status','redemptions.quota','redemptions.redeemed_time','redemptions.used_user_id',
      'users.id','users.quota','users.deleted_at'
    ];
    expected_reader_roles := ARRAY[
      'invoice_newapi_payments_reader',
      'invoice_newapi_identities_reader',
      'invoice_newapi_payments_v3_reader',
      'invoice_newapi_usage_reader',
      'invoice_newapi_credits_reader',
      'invoice_newapi_balances_reader'
    ];
    expected_grant_functions := ARRAY[
      'newapi_payments_v4',
      'newapi_payments_v4',
      'newapi_identities_v4',
      'newapi_usage_v4',
      'newapi_credits_v4',
      'newapi_balances_v4'
    ];
    expected_grant_roles := ARRAY[
      'invoice_newapi_payments_reader',
      'invoice_newapi_payments_v3_reader',
      'invoice_newapi_identities_reader',
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
    bridge_owner := 'invoice_sub2api_bridge_owner';
    expected_functions := ARRAY[
      'sub2api_payments_v4',
      'sub2api_usage_v4',
      'sub2api_credits_v4',
      'sub2api_balances_v4',
      'sub2api_identities_v4'
    ];
    expected_function_hashes := ARRAY[
      '3ce533217c9535cec7ca711ccb2c011540f4976c414d4543bf3bb13f991c3df1',
      '9291f757e1e0c5b0daa0c6858020e6f61f1cfd63f66cfbe4677b7c6dff3b1530',
      '5683a8b5eec1b50f33740b63d6a361c003686fa6d90eafc92306882a337ab087',
      '00697ce59c06a5a5715dfd4ac58df15e83f76418d8cc4f20ec904cf59d0b36d5',
      '443fc8aa1ed8232c742bfe2964a2869e279886694dddeda0a9d532ecad8f1f90'
    ];
    expected_owner_columns := ARRAY[
      'payment_orders.id','payment_orders.user_id','payment_orders.status','payment_orders.order_type',
      'payment_orders.amount','payment_orders.pay_amount','payment_orders.fee_rate','payment_orders.refund_amount',
      'payment_orders.completed_at','payment_orders.refund_at','payment_orders.created_at','payment_orders.updated_at',
      'payment_orders.payment_type','payment_orders.provider_key','payment_orders.provider_snapshot','payment_orders.recharge_code',
      'settings.key','settings.value',
      'auth_identities.id','auth_identities.user_id','auth_identities.provider_type','auth_identities.provider_key',
      'auth_identities.provider_subject','auth_identities.verified_at','auth_identities.issuer','auth_identities.metadata',
      'auth_identities.created_at','auth_identities.updated_at',
      'usage_logs.id','usage_logs.user_id','usage_logs.billing_type','usage_logs.actual_cost','usage_logs.created_at',
      'promo_code_usages.id','promo_code_usages.user_id','promo_code_usages.bonus_amount','promo_code_usages.used_at',
      'user_affiliate_ledger.id','user_affiliate_ledger.user_id','user_affiliate_ledger.action',
      'user_affiliate_ledger.amount','user_affiliate_ledger.created_at',
      'redeem_codes.id','redeem_codes.code','redeem_codes.type','redeem_codes.value',
      'redeem_codes.status','redeem_codes.used_by','redeem_codes.used_at',
      'users.id','users.balance','users.deleted_at'
    ];
    expected_reader_roles := ARRAY[
      'invoice_sub2api_payments_reader',
      'invoice_sub2api_identities_reader',
      'invoice_sub2api_payments_v3_reader',
      'invoice_sub2api_usage_reader',
      'invoice_sub2api_credits_reader',
      'invoice_sub2api_balances_reader'
    ];
    expected_grant_functions := ARRAY[
      'sub2api_payments_v4',
      'sub2api_payments_v4',
      'sub2api_identities_v4',
      'sub2api_usage_v4',
      'sub2api_credits_v4',
      'sub2api_balances_v4'
    ];
    expected_grant_roles := ARRAY[
      'invoice_sub2api_payments_reader',
      'invoice_sub2api_payments_v3_reader',
      'invoice_sub2api_identities_reader',
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
  expected_bridge_roles := array_append(expected_reader_roles, bridge_owner);

  IF boundary_mode NOT IN ('detached', 'bridge-v4') THEN
    RAISE EXCEPTION 'boundary_state must be exactly detached or bridge-v4';
  END IF;

  IF current_database() IN ('postgres', 'template0', 'template1') THEN
    RAISE EXCEPTION 'refusing upgrade preflight in maintenance database %', current_database();
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
    RAISE EXCEPTION 'run the upgrade gate as the current source database owner or a superuser';
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

  SELECT count(*) INTO check_count
  FROM pg_class relation
  JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
  WHERE namespace.nspname = 'public'
    AND relation.relname LIKE object_prefix ESCAPE '!';
  IF check_count <> 0 THEN
    RAISE EXCEPTION '% legacy public invoice object(s) remain; detach them before upgrade',
      check_count;
  END IF;

  SELECT count(*) INTO check_count
  FROM pg_stat_activity activity
  WHERE activity.datname = current_database()
    AND activity.usename = ANY(expected_bridge_roles)
    AND activity.pid <> pg_backend_pid();
  IF check_count <> 0 THEN
    RAISE EXCEPTION '% active invoice source session(s); stop agents before upgrade', check_count;
  END IF;

  SELECT EXISTS (
    SELECT 1
    FROM pg_database database_row
    CROSS JOIN LATERAL aclexplode(
      COALESCE(database_row.datacl, acldefault('d', database_row.datdba))
    ) privilege
    WHERE database_row.datname = current_database()
      AND privilege.grantee = 0
      AND privilege.privilege_type IN ('TEMP', 'TEMPORARY')
  ) INTO public_temp;

  IF boundary_mode = 'detached' THEN
    IF EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'invoice_bridge') THEN
      RAISE EXCEPTION 'detached gate found invoice_bridge schema';
    END IF;

    SELECT count(*) INTO check_count
    FROM pg_roles role_row
    WHERE role_row.rolname LIKE object_prefix ESCAPE '!';
    IF check_count <> 0 THEN
      RAISE EXCEPTION 'detached gate found % invoice role(s)', check_count;
    END IF;

    IF NOT public_temp THEN
      RAISE EXCEPTION 'detached gate requires the original PUBLIC TEMPORARY default';
    END IF;
  ELSE
    IF NOT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'invoice_bridge') THEN
      RAISE EXCEPTION 'bridge-v4 gate requires invoice_bridge schema';
    END IF;

    SELECT string_agg(role_row.rolname, ', ' ORDER BY role_row.rolname)
      INTO unexpected
    FROM pg_roles role_row
    WHERE role_row.rolname LIKE object_prefix ESCAPE '!'
      AND role_row.rolname <> ALL(expected_bridge_roles);
    IF unexpected IS NOT NULL THEN
      RAISE EXCEPTION 'bridge-v4 gate found unknown source role(s): %', unexpected;
    END IF;

    SELECT string_agg(expected_role, ', ' ORDER BY expected_role)
      INTO unexpected
    FROM unnest(expected_bridge_roles) expected(expected_role)
    WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = expected.expected_role);
    IF unexpected IS NOT NULL THEN
      RAISE EXCEPTION 'bridge-v4 gate is missing source role(s): %', unexpected;
    END IF;

    FOREACH role_name IN ARRAY expected_bridge_roles LOOP
      expected_login := role_name NOT IN (
        bridge_owner,
        format('invoice_%s_payments_reader', source_name)
      );
      expected_connection_limit := CASE WHEN expected_login THEN 2 ELSE 0 END;

      IF EXISTS (
        SELECT 1
        FROM pg_roles role_row
        WHERE role_row.rolname = role_name
          AND (
            role_row.rolsuper
            OR role_row.rolcreatedb
            OR role_row.rolcreaterole
            OR role_row.rolreplication
            OR role_row.rolbypassrls
            OR role_row.rolinherit
            OR role_row.rolcanlogin <> expected_login
            OR role_row.rolconnlimit <> expected_connection_limit
          )
      ) THEN
        RAISE EXCEPTION 'bridge role % has wrong attributes/login/connection limit', role_name;
      END IF;

      SELECT COALESCE(array_agg(setting ORDER BY setting), ARRAY[]::text[])
        INTO actual_role_config
      FROM unnest(COALESCE(
        (SELECT rolconfig FROM pg_roles WHERE rolname = role_name),
        ARRAY[]::text[]
      )) configured(setting);

      IF role_name = bridge_owner THEN
        IF actual_role_config <> ARRAY[]::text[] THEN
          RAISE EXCEPTION 'bridge owner % has unexpected role GUC(s)', role_name;
        END IF;
      ELSIF actual_role_config <> expected_role_config THEN
        RAISE EXCEPTION 'bridge caller % role GUC allowlist mismatch', role_name;
      END IF;
    END LOOP;

    SELECT count(*) INTO check_count
    FROM pg_auth_members membership
    JOIN pg_roles member_role ON member_role.oid = membership.member
    JOIN pg_roles granted_role ON granted_role.oid = membership.roleid
    WHERE member_role.rolname = ANY(expected_bridge_roles)
       OR granted_role.rolname = ANY(expected_bridge_roles);
    IF check_count <> 0 THEN
      RAISE EXCEPTION '% bridge role membership(s) require manual review', check_count;
    END IF;

    IF (SELECT nspowner FROM pg_namespace WHERE nspname = 'invoice_bridge') <>
       (SELECT oid FROM pg_roles WHERE rolname = database_owner) THEN
      RAISE EXCEPTION 'invoice_bridge schema owner is not the source database owner %', database_owner;
    END IF;

    SELECT count(*) INTO check_count
    FROM unnest(expected_bridge_roles) expected(role_name)
    CROSS JOIN pg_namespace namespace
    WHERE namespace.nspname <> 'information_schema'
      AND namespace.nspname NOT LIKE 'pg!_%' ESCAPE '!'
      AND has_schema_privilege(expected.role_name, namespace.oid, 'CREATE');
    IF check_count <> 0 THEN
      RAISE EXCEPTION '% effective bridge-role schema CREATE privilege(s) remain', check_count;
    END IF;

    SELECT count(*) INTO check_count
    FROM unnest(expected_bridge_roles) expected(role_name)
    WHERE NOT has_schema_privilege(expected.role_name,'invoice_bridge','USAGE');
    IF check_count <> 0 THEN
      RAISE EXCEPTION '% bridge role(s) are missing invoice_bridge USAGE',check_count;
    END IF;

    SELECT count(*) INTO check_count
    FROM unnest(expected_bridge_roles) expected(role_name)
    CROSS JOIN pg_namespace namespace
    WHERE namespace.nspname NOT IN ('public','invoice_bridge','information_schema')
      AND namespace.nspname NOT LIKE 'pg!_%' ESCAPE '!'
      AND has_schema_privilege(expected.role_name,namespace.oid,'USAGE');
    IF check_count <> 0 THEN
      RAISE EXCEPTION '% unexpected bridge-role schema USAGE privilege(s) remain',check_count;
    END IF;

    IF EXISTS (
      SELECT 1
      FROM pg_namespace namespace
      CROSS JOIN LATERAL aclexplode(
        COALESCE(namespace.nspacl, acldefault('n', namespace.nspowner))
      ) privilege
      WHERE namespace.nspname = 'invoice_bridge'
        AND privilege.grantee = 0
        AND privilege.privilege_type = 'USAGE'
    ) THEN
      RAISE EXCEPTION 'PUBLIC retains USAGE on invoice_bridge';
    END IF;

    SELECT count(*) INTO check_count
    FROM pg_namespace namespace
    CROSS JOIN LATERAL aclexplode(
      COALESCE(namespace.nspacl,acldefault('n',namespace.nspowner))
    ) privilege
    LEFT JOIN pg_roles grantee_role ON grantee_role.oid=privilege.grantee
    WHERE namespace.nspname='invoice_bridge'
      AND NOT (
        (privilege.grantee=namespace.nspowner AND privilege.privilege_type IN ('CREATE','USAGE'))
        OR (grantee_role.rolname=ANY(expected_bridge_roles) AND privilege.privilege_type='USAGE')
      );
    IF check_count<>0 THEN
      RAISE EXCEPTION '% unexpected invoice_bridge schema ACL entry/entries',check_count;
    END IF;

    SELECT count(*) INTO check_count
    FROM unnest(fingerprint_tables) expected(table_name)
    JOIN pg_class relation ON relation.oid=to_regclass(expected.table_name)
    WHERE relation.relrowsecurity OR relation.relforcerowsecurity;
    IF check_count<>0 THEN
      RAISE EXCEPTION '% reviewed upstream relation(s) have RLS enabled; bridge visibility is not provable',check_count;
    END IF;

    WITH expected(column_name) AS (
      SELECT unnest(expected_owner_columns)
    ), actual(column_name) AS (
      SELECT columns.table_name||'.'||columns.column_name
      FROM information_schema.columns columns
      WHERE columns.table_schema='public'
        AND has_column_privilege(
          bridge_owner,
          format('%I.%I',columns.table_schema,columns.table_name),
          columns.column_name,
          'SELECT'
        )
    ), mismatch AS (
      (SELECT * FROM expected EXCEPT SELECT * FROM actual)
      UNION ALL
      (SELECT * FROM actual EXCEPT SELECT * FROM expected)
    )
    SELECT count(*) INTO check_count FROM mismatch;
    IF check_count<>0 THEN
      RAISE EXCEPTION '% bridge-owner effective SELECT column mismatch(es)',check_count;
    END IF;

    SELECT count(*) INTO check_count
    FROM pg_class relation
    JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
    WHERE namespace.nspname='public'
      AND relation.relkind IN ('r','p','v','m','f')
      AND (
        has_table_privilege(bridge_owner,relation.oid,'INSERT')
        OR has_table_privilege(bridge_owner,relation.oid,'UPDATE')
        OR has_table_privilege(bridge_owner,relation.oid,'DELETE')
        OR has_table_privilege(bridge_owner,relation.oid,'TRUNCATE')
        OR has_table_privilege(bridge_owner,relation.oid,'REFERENCES')
        OR has_table_privilege(bridge_owner,relation.oid,'TRIGGER')
      );
    IF check_count<>0 THEN
      RAISE EXCEPTION '% bridge-owner effective mutation privilege(s) remain',check_count;
    END IF;

    SELECT count(*) INTO check_count
    FROM unnest(expected_reader_roles) expected(role_name)
    CROSS JOIN information_schema.columns columns
    WHERE columns.table_schema NOT IN ('information_schema','invoice_bridge')
      AND columns.table_schema NOT LIKE 'pg!_%' ESCAPE '!'
      AND has_column_privilege(
        expected.role_name,
        format('%I.%I',columns.table_schema,columns.table_name),
        columns.column_name,
        'SELECT'
      );
    IF check_count<>0 THEN
      RAISE EXCEPTION '% effective raw caller SELECT column privilege(s) remain',check_count;
    END IF;

    SELECT count(*) INTO check_count
    FROM unnest(expected_reader_roles) expected(role_name)
    CROSS JOIN pg_class relation
    JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
    WHERE namespace.nspname<>'information_schema'
      AND namespace.nspname NOT LIKE 'pg!_%' ESCAPE '!'
      AND relation.relkind IN ('r','p','v','m','f')
      AND (
        has_table_privilege(expected.role_name,relation.oid,'INSERT')
        OR has_table_privilege(expected.role_name,relation.oid,'UPDATE')
        OR has_table_privilege(expected.role_name,relation.oid,'DELETE')
        OR has_table_privilege(expected.role_name,relation.oid,'TRUNCATE')
        OR has_table_privilege(expected.role_name,relation.oid,'REFERENCES')
        OR has_table_privilege(expected.role_name,relation.oid,'TRIGGER')
      );
    IF check_count<>0 THEN
      RAISE EXCEPTION '% effective raw caller mutation privilege(s) remain',check_count;
    END IF;

    SELECT count(*) INTO check_count
    FROM unnest(expected_bridge_roles) expected(role_name)
    CROSS JOIN pg_class relation
    WHERE relation.relkind='S'
      AND (
        has_sequence_privilege(expected.role_name,relation.oid,'USAGE')
        OR has_sequence_privilege(expected.role_name,relation.oid,'SELECT')
        OR has_sequence_privilege(expected.role_name,relation.oid,'UPDATE')
      );
    IF check_count<>0 THEN
      RAISE EXCEPTION '% bridge-role sequence privilege(s) remain',check_count;
    END IF;

    SELECT count(*) INTO check_count
    FROM pg_default_acl default_acl
    JOIN pg_roles role_row ON role_row.oid=default_acl.defaclrole
    WHERE role_row.rolname=ANY(expected_bridge_roles);
    IF check_count<>0 THEN
      RAISE EXCEPTION '% bridge-role default ACL object(s) remain',check_count;
    END IF;

    SELECT count(*) INTO check_count
    FROM pg_shdepend dependency
    JOIN pg_roles role_row
      ON dependency.refclassid='pg_authid'::regclass
     AND dependency.refobjid=role_row.oid
    WHERE role_row.rolname=ANY(expected_bridge_roles)
      AND dependency.deptype='o'
      AND NOT (
        role_row.rolname=bridge_owner
        AND dependency.dbid=(SELECT oid FROM pg_database WHERE datname=current_database())
        AND dependency.classid='pg_proc'::regclass
        AND dependency.objid IN (
          SELECT function_row.oid FROM pg_proc function_row
          JOIN pg_namespace namespace ON namespace.oid=function_row.pronamespace
          WHERE namespace.nspname='invoice_bridge'
            AND function_row.proname=ANY(expected_functions)
        )
      );
    IF check_count<>0 THEN
      RAISE EXCEPTION '% unexpected bridge-role ownership dependency/dependencies remain',check_count;
    END IF;

    SELECT count(*) INTO check_count
    FROM pg_class relation
    JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
    WHERE namespace.nspname = 'invoice_bridge';
    IF check_count <> 0 THEN
      RAISE EXCEPTION 'invoice_bridge contains % relation-like object(s); only dynamic functions are allowed',
        check_count;
    END IF;

    SELECT string_agg(
        format('%I(%s)', function_row.proname, pg_get_function_identity_arguments(function_row.oid)),
        ', ' ORDER BY function_row.proname, function_row.oid
      )
      INTO unexpected
    FROM pg_proc function_row
    JOIN pg_namespace namespace ON namespace.oid = function_row.pronamespace
    JOIN pg_language language_row ON language_row.oid = function_row.prolang
    WHERE namespace.nspname = 'invoice_bridge'
      AND NOT (
        function_row.proname = ANY(expected_functions)
        AND function_row.prokind = 'f'
        AND function_row.pronargs = 2
        AND oidvectortypes(function_row.proargtypes) = 'text, jsonb'
        AND function_row.proretset
        AND function_row.prorettype = 'jsonb'::regtype
        AND language_row.lanname = 'plpgsql'
        AND function_row.provolatile = 'v'
        AND function_row.prosecdef
        AND function_row.proowner = (SELECT oid FROM pg_roles WHERE rolname = bridge_owner)
        AND encode(sha256(convert_to(function_row.prosrc,'UTF8')),'hex') =
          expected_function_hashes[array_position(expected_functions,function_row.proname)]
        AND (
          SELECT COALESCE(array_agg(setting ORDER BY setting), ARRAY[]::text[])
          FROM unnest(COALESCE(function_row.proconfig, ARRAY[]::text[])) configured(setting)
        ) = ARRAY['row_security=on', 'search_path=pg_catalog']::text[]
      );
    IF unexpected IS NOT NULL THEN
      RAISE EXCEPTION 'invoice_bridge contains unapproved/wrong-owner function(s): %', unexpected;
    END IF;

    SELECT count(*) INTO check_count
    FROM pg_proc function_row
    JOIN pg_namespace namespace ON namespace.oid = function_row.pronamespace
    WHERE namespace.nspname = 'invoice_bridge'
      AND function_row.proname = ANY(expected_functions);
    IF check_count <> 5 THEN
      RAISE EXCEPTION 'bridge-v4 function set must be exactly 5; found %', check_count;
    END IF;

    SELECT string_agg(function_row.proname, ', ' ORDER BY function_row.proname)
      INTO unexpected
    FROM pg_proc function_row
    JOIN pg_namespace namespace ON namespace.oid = function_row.pronamespace
    CROSS JOIN LATERAL aclexplode(
      COALESCE(function_row.proacl, acldefault('f', function_row.proowner))
    ) privilege
    WHERE namespace.nspname = 'invoice_bridge'
      AND privilege.grantee = 0
      AND privilege.privilege_type = 'EXECUTE';
    IF unexpected IS NOT NULL THEN
      RAISE EXCEPTION 'PUBLIC retains EXECUTE on bridge function(s): %', unexpected;
    END IF;

    SELECT count(*) INTO check_count
    FROM pg_proc function_row
    JOIN pg_namespace namespace ON namespace.oid = function_row.pronamespace
    CROSS JOIN LATERAL aclexplode(
      COALESCE(function_row.proacl, acldefault('f', function_row.proowner))
    ) privilege
    JOIN pg_roles grantee_role ON grantee_role.oid = privilege.grantee
    WHERE namespace.nspname = 'invoice_bridge'
      AND privilege.privilege_type = 'EXECUTE'
      AND privilege.grantee <> function_row.proowner
      AND NOT EXISTS (
        SELECT 1
        FROM unnest(expected_grant_functions, expected_grant_roles)
          expected(function_name, role_name)
        WHERE expected.function_name = function_row.proname
          AND expected.role_name = grantee_role.rolname
      );
    IF check_count <> 0 THEN
      RAISE EXCEPTION '% unexpected bridge function EXECUTE grant(s)', check_count;
    END IF;

    SELECT count(*) INTO check_count
    FROM unnest(expected_grant_functions, expected_grant_roles)
      expected(function_name, role_name)
    WHERE NOT EXISTS (
      SELECT 1
      FROM pg_proc function_row
      JOIN pg_namespace namespace ON namespace.oid = function_row.pronamespace
      CROSS JOIN LATERAL aclexplode(
        COALESCE(function_row.proacl, acldefault('f', function_row.proowner))
      ) privilege
      JOIN pg_roles grantee_role ON grantee_role.oid = privilege.grantee
      WHERE namespace.nspname = 'invoice_bridge'
        AND function_row.proname = expected.function_name
        AND grantee_role.rolname = expected.role_name
        AND privilege.privilege_type = 'EXECUTE'
    );
    IF check_count <> 0 THEN
      RAISE EXCEPTION '% required bridge function EXECUTE grant(s) are missing', check_count;
    END IF;

    SELECT count(*) INTO check_count
    FROM pg_proc function_row
    JOIN pg_namespace namespace ON namespace.oid = function_row.pronamespace
    CROSS JOIN LATERAL aclexplode(
      COALESCE(function_row.proacl, acldefault('f', function_row.proowner))
    ) privilege
    JOIN pg_roles grantee_role ON grantee_role.oid = privilege.grantee
    WHERE grantee_role.rolname = ANY(expected_reader_roles)
      AND privilege.privilege_type = 'EXECUTE'
      AND NOT (
        namespace.nspname = 'invoice_bridge'
        AND EXISTS (
          SELECT 1
          FROM unnest(expected_grant_functions, expected_grant_roles)
            expected(function_name, role_name)
          WHERE expected.function_name = function_row.proname
            AND expected.role_name = grantee_role.rolname
        )
      );
    IF check_count <> 0 THEN
      RAISE EXCEPTION '% reader EXECUTE grant(s) fall outside the exact bridge allowlist', check_count;
    END IF;

    IF public_temp THEN
      RAISE EXCEPTION 'bridge-v4 gate requires PUBLIC TEMPORARY to remain revoked';
    END IF;
  END IF;

  WITH legacy_views AS (
    SELECT relation.oid
    FROM pg_class relation
    JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
    WHERE namespace.nspname = 'public'
      AND relation.relname LIKE object_prefix ESCAPE '!'
      AND relation.relkind IN ('v', 'm')
  ), legacy_edges AS (
    SELECT dependency.objid, dependency.refobjid, dependency.refobjsubid
    FROM legacy_views invoice_view
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
  ), bridge_edges AS (
    SELECT dependency.objid, dependency.refobjid, dependency.refobjsubid
    FROM pg_proc function_row
    JOIN pg_namespace function_namespace ON function_namespace.oid = function_row.pronamespace
    JOIN pg_depend dependency
      ON dependency.classid = 'pg_proc'::regclass
     AND dependency.objid = function_row.oid
    JOIN pg_class referenced_relation
      ON dependency.refclassid = 'pg_class'::regclass
     AND dependency.refobjid = referenced_relation.oid
    JOIN pg_namespace referenced_namespace
      ON referenced_namespace.oid = referenced_relation.relnamespace
    WHERE function_namespace.nspname = 'invoice_bridge'
      AND referenced_namespace.nspname = 'public'
  )
  SELECT count(*) INTO dependency_count
  FROM (
    SELECT * FROM legacy_edges
    UNION ALL
    SELECT * FROM bridge_edges
  ) all_edges;

  IF dependency_count <> 0 THEN
    RAISE EXCEPTION 'upgrade blocked: invoice boundary has % persistent pg_depend edge(s) to upstream relations',
      dependency_count;
  END IF;
END $preflight$;

WITH source_configuration AS (
  SELECT
    current_setting('invoice.upgrade.source') AS source_name,
    current_setting('invoice.upgrade.boundary_state') AS boundary_state,
    CASE current_setting('invoice.upgrade.source')
      WHEN 'newapi' THEN 'invoice!_newapi!_%'
      ELSE 'invoice!_sub2api!_%'
    END AS object_prefix,
    CASE current_setting('invoice.upgrade.source')
      WHEN 'newapi' THEN ARRAY[
        'invoice_newapi_payments_reader',
        'invoice_newapi_identities_reader',
        'invoice_newapi_payments_v3_reader',
        'invoice_newapi_usage_reader',
        'invoice_newapi_credits_reader',
        'invoice_newapi_balances_reader',
        'invoice_newapi_bridge_owner'
      ]
      ELSE ARRAY[
        'invoice_sub2api_payments_reader',
        'invoice_sub2api_identities_reader',
        'invoice_sub2api_payments_v3_reader',
        'invoice_sub2api_usage_reader',
        'invoice_sub2api_credits_reader',
        'invoice_sub2api_balances_reader',
        'invoice_sub2api_bridge_owner'
      ]
    END AS source_roles
), public_temp AS (
  SELECT EXISTS (
    SELECT 1
    FROM pg_database database_row
    CROSS JOIN LATERAL aclexplode(
      COALESCE(database_row.datacl, acldefault('d', database_row.datdba))
    ) privilege
    WHERE database_row.datname = current_database()
      AND privilege.grantee = 0
      AND privilege.privilege_type IN ('TEMP', 'TEMPORARY')
  ) AS enabled
)
SELECT
  source_configuration.source_name,
  source_configuration.boundary_state,
  current_database() AS database_name,
  0::bigint AS legacy_public_objects,
  0::bigint AS active_source_sessions,
  0::bigint AS upstream_pg_depend_edges,
  public_temp.enabled AS public_temporary,
  'upgrade-preflight-passed'::text AS status
FROM source_configuration, public_temp;

COMMIT;
