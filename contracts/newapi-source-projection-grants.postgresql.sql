-- REVIEW-ONLY TEMPLATE. Do not run automatically or from a source agent.
-- Create the identity role as LOGIN and the legacy V2 payment role as
-- NOLOGIN. The V3 payment agent uses invoice_newapi_payments_v3_reader;
-- there is deliberately no credential for this compatibility holder. Replace
-- role names only after review. Neither role may belong to an upstream role.

BEGIN;

ALTER ROLE invoice_newapi_payments_reader
  NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS;
ALTER ROLE invoice_newapi_payments_reader CONNECTION LIMIT 0;
ALTER ROLE invoice_newapi_payments_reader SET default_transaction_read_only = on;
ALTER ROLE invoice_newapi_payments_reader SET statement_timeout = '15s';
ALTER ROLE invoice_newapi_payments_reader SET lock_timeout = '5s';
ALTER ROLE invoice_newapi_payments_reader SET idle_in_transaction_session_timeout = '15s';
DO $contract$ BEGIN
  EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM invoice_newapi_payments_reader', current_database());
END $contract$;

REVOKE CREATE ON SCHEMA public FROM invoice_newapi_payments_reader;
GRANT USAGE ON SCHEMA public TO invoice_newapi_payments_reader;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM invoice_newapi_payments_reader;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM invoice_newapi_payments_reader;
GRANT SELECT (
  id, user_id, amount, money, payment_method, payment_provider,
  create_time, complete_time, status
) ON TABLE public.top_ups TO invoice_newapi_payments_reader;

-- top_ups.id becomes the non-secret source_order_id display reference in the
-- invoice system. The provider trade_no column is intentionally not granted.

ALTER ROLE invoice_newapi_identities_reader
  NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS;
ALTER ROLE invoice_newapi_identities_reader CONNECTION LIMIT 2;
ALTER ROLE invoice_newapi_identities_reader SET default_transaction_read_only = on;
ALTER ROLE invoice_newapi_identities_reader SET statement_timeout = '15s';
ALTER ROLE invoice_newapi_identities_reader SET lock_timeout = '5s';
ALTER ROLE invoice_newapi_identities_reader SET idle_in_transaction_session_timeout = '15s';
DO $contract$ BEGIN
  EXECUTE format('GRANT CONNECT ON DATABASE %I TO invoice_newapi_identities_reader', current_database());
END $contract$;

REVOKE CREATE ON SCHEMA public FROM invoice_newapi_identities_reader;
GRANT USAGE ON SCHEMA public TO invoice_newapi_identities_reader;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM invoice_newapi_identities_reader;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM invoice_newapi_identities_reader;

-- Pin the only trusted New API OAuth slug in a security-barrier view. Change
-- this literal only as an independently reviewed deployment-contract update.
-- Endpoints are non-secret; client_id/client_secret/scopes/mappings/policy never
-- cross the view boundary. Go performs the final exact issuer URL validation.
CREATE OR REPLACE VIEW public.invoice_newapi_oidc_provider_contract_v1
WITH (security_barrier = true) AS
SELECT id,slug,enabled,well_known,authorization_endpoint,token_endpoint,
       user_info_endpoint,
       (
		 count(*) OVER ()=1
		 AND enabled
         AND btrim(well_known) ~ '^https://[^[:space:]]+$'
         AND btrim(authorization_endpoint) ~ '^https://[^[:space:]]+$'
         AND btrim(token_endpoint) ~ '^https://[^[:space:]]+$'
		 AND btrim(user_info_endpoint) ~ '^https://[^[:space:]]+$'
       ) AS contract_ok
FROM public.custom_oauth_providers
WHERE slug='solov-sso';

CREATE OR REPLACE VIEW public.invoice_newapi_oidc_binding_projection_v1
WITH (security_barrier = true) AS
SELECT b.id,b.user_id,b.provider_id,b.provider_user_id,b.created_at,
       p.slug AS provider_slug
FROM public.user_oauth_bindings b
JOIN public.invoice_newapi_oidc_provider_contract_v1 p ON p.id=b.provider_id
WHERE p.contract_ok;

REVOKE ALL ON TABLE public.invoice_newapi_oidc_provider_contract_v1 FROM PUBLIC;
REVOKE ALL ON TABLE public.invoice_newapi_oidc_binding_projection_v1 FROM PUBLIC;
REVOKE ALL ON TABLE public.invoice_newapi_oidc_provider_contract_v1 FROM invoice_newapi_identities_reader;
REVOKE ALL ON TABLE public.invoice_newapi_oidc_binding_projection_v1 FROM invoice_newapi_identities_reader;
GRANT SELECT (
  id,slug,enabled,well_known,authorization_endpoint,token_endpoint,
  user_info_endpoint,contract_ok
) ON TABLE public.invoice_newapi_oidc_provider_contract_v1 TO invoice_newapi_identities_reader;
GRANT SELECT (
  id,user_id,provider_id,provider_user_id,created_at,provider_slug
) ON TABLE public.invoice_newapi_oidc_binding_projection_v1 TO invoice_newapi_identities_reader;

DO $contract$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM pg_auth_members m
    WHERE m.member IN (
      SELECT oid FROM pg_roles WHERE rolname IN (
        'invoice_newapi_payments_reader','invoice_newapi_identities_reader'
      )
    ) OR m.roleid IN (
      SELECT oid FROM pg_roles WHERE rolname IN (
        'invoice_newapi_payments_reader','invoice_newapi_identities_reader'
      )
    )
  ) THEN
    RAISE EXCEPTION 'invoice New API projection roles must not inherit or assume another role';
  END IF;
END
$contract$;

-- PostgreSQL grants TEMPORARY to PUBLIC by default. An explicit role REVOKE is
-- not a deny rule, so the database owner must first revoke it from PUBLIC.
DO $contract$
DECLARE role_name text;
BEGIN
  FOREACH role_name IN ARRAY ARRAY[
    'invoice_newapi_payments_reader',
    'invoice_newapi_identities_reader'
  ] LOOP
    EXECUTE format('REVOKE TEMPORARY ON DATABASE %I FROM %I', current_database(), role_name);
    IF has_database_privilege(role_name, current_database(), 'TEMP') THEN
      RAISE EXCEPTION '% retains TEMP through PUBLIC or another role; revoke TEMPORARY ON DATABASE % FROM PUBLIC before applying this contract', role_name, current_database();
    END IF;
  END LOOP;
END
$contract$;

COMMIT;

-- Deliberately absent: users/email, trade_no, OAuth client_id/client_secret,
-- scopes, field mappings, access_policy,
-- password/TOTP/session/token tables, and every INSERT/UPDATE/DELETE/sequence
-- privilege. The production launcher inventories effective grants and refuses
-- any column beyond its one stream.
