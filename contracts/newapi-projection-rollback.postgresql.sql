-- REVIEW-ONLY ROLLBACK FOR THE AUXILIARY NEW API PROJECTION BOUNDARY.
-- Stop all six reader roles before running this as the source database owner.
-- This script never changes a New API application table and never uses CASCADE.

BEGIN;
SET LOCAL lock_timeout = '5s';

DO $rollback$
DECLARE
  role_names text[] := ARRAY[
    'invoice_newapi_payments_reader',
    'invoice_newapi_identities_reader',
    'invoice_newapi_payments_v3_reader',
    'invoice_newapi_usage_reader',
    'invoice_newapi_credits_reader',
    'invoice_newapi_balances_reader'
  ];
  role_name text;
BEGIN
  FOREACH role_name IN ARRAY role_names LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=role_name) THEN
      RAISE EXCEPTION 'required rollback role % is missing',role_name;
    END IF;
    EXECUTE format('ALTER ROLE %I NOLOGIN CONNECTION LIMIT 0',role_name);
    EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM %I',current_database(),role_name);
  END LOOP;
  IF EXISTS (
    SELECT 1 FROM pg_stat_activity
    WHERE datname=current_database() AND usename=ANY(role_names)
      AND pid<>pg_backend_pid()
  ) THEN
    RAISE EXCEPTION 'New API projection readers still have active sessions';
  END IF;
END $rollback$;

DROP VIEW public.invoice_newapi_cutover_contract_v3 RESTRICT;
DROP VIEW public.invoice_newapi_payments_projection_health_v3 RESTRICT;
DROP VIEW public.invoice_newapi_payments_projection_v3 RESTRICT;
DROP VIEW public.invoice_newapi_usage_projection_health_v3 RESTRICT;
DROP VIEW public.invoice_newapi_usage_projection_v3 RESTRICT;
DROP VIEW public.invoice_newapi_credits_projection_health_v3 RESTRICT;
DROP VIEW public.invoice_newapi_credits_projection_v3 RESTRICT;
DROP VIEW public.invoice_newapi_balance_projection_v3 RESTRICT;
DROP VIEW public.invoice_newapi_wallet_config_contract_v3 RESTRICT;
DROP VIEW public.invoice_newapi_oidc_binding_projection_v1 RESTRICT;
DROP VIEW public.invoice_newapi_oidc_provider_contract_v1 RESTRICT;

DO $rollback$
DECLARE
  role_names text[] := ARRAY[
    'invoice_newapi_payments_reader',
    'invoice_newapi_identities_reader',
    'invoice_newapi_payments_v3_reader',
    'invoice_newapi_usage_reader',
    'invoice_newapi_credits_reader',
    'invoice_newapi_balances_reader'
  ];
  role_name text;
BEGIN
  FOREACH role_name IN ARRAY role_names LOOP
    EXECUTE format('DROP OWNED BY %I RESTRICT',role_name);
    EXECUTE format('DROP ROLE %I',role_name);
  END LOOP;
  EXECUTE format('GRANT TEMPORARY ON DATABASE %I TO PUBLIC',current_database());
END $rollback$;

COMMIT;
