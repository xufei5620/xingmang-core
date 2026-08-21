-- REVIEW-ONLY TEMPLATE. Do not run automatically or from a source agent.
-- Create two independent LOGIN roles first. Replace the role names below only
-- after an operator has reviewed the resulting SQL. Neither role may be a
-- member of an upstream application/owner role. Passwords belong only in the
-- matching source-agent secret files.

BEGIN;

ALTER ROLE invoice_sub2api_payments_reader
  NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS;
ALTER ROLE invoice_sub2api_payments_reader CONNECTION LIMIT 2;
ALTER ROLE invoice_sub2api_payments_reader SET default_transaction_read_only = on;
ALTER ROLE invoice_sub2api_payments_reader SET statement_timeout = '15s';
ALTER ROLE invoice_sub2api_payments_reader SET lock_timeout = '5s';
ALTER ROLE invoice_sub2api_payments_reader SET idle_in_transaction_session_timeout = '15s';
DO $contract$ BEGIN
  EXECUTE format('GRANT CONNECT ON DATABASE %I TO invoice_sub2api_payments_reader', current_database());
END $contract$;

REVOKE CREATE ON SCHEMA public FROM invoice_sub2api_payments_reader;
GRANT USAGE ON SCHEMA public TO invoice_sub2api_payments_reader;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM invoice_sub2api_payments_reader;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM invoice_sub2api_payments_reader;

-- payment_orders has no currency column in Sub2API v0.1.179. Stripe/Airwallex
-- are configurable; their v2 snapshot currency is mandatory. The v0.1.179
-- paymentProviderConfigCurrency function fixes EasyPay/Alipay/WeChat Pay to
-- default CNY, so only those exact provider keys may safely derive CNY when an
-- older v2 snapshot omitted currency. Unknown/configurable providers with a
-- missing/invalid currency are excluded and counted by the aggregate health
-- view. Never widen the allowlist without a new source-contract review.
CREATE OR REPLACE VIEW public.invoice_sub2api_payment_projection_v1
WITH (security_barrier = true) AS
SELECT id,user_id,status,order_type,amount,pay_amount,refund_amount,currency,
       completed_at,refund_at,created_at,updated_at,payment_type,provider_key
FROM (
  SELECT
    po.id,po.user_id,po.status,po.order_type,po.amount,po.pay_amount,po.refund_amount,
    CASE
      WHEN jsonb_typeof(po.provider_snapshot)='object'
       AND po.provider_snapshot ->> 'schema_version'='2'
       AND lower(btrim(po.provider_snapshot ->> 'provider_key'))=lower(btrim(po.provider_key))
       AND upper(btrim(po.provider_snapshot ->> 'currency')) ~ '^[A-Z]{3}$'
      THEN upper(btrim(po.provider_snapshot ->> 'currency'))
      WHEN jsonb_typeof(po.provider_snapshot)='object'
       AND po.provider_snapshot ->> 'schema_version'='2'
       AND lower(btrim(po.provider_snapshot ->> 'provider_key'))=lower(btrim(po.provider_key))
       AND lower(btrim(po.provider_key)) IN ('easypay','alipay','wxpay')
      THEN 'CNY'
      ELSE NULL
    END::text AS currency,
    po.completed_at,po.refund_at,po.created_at,po.updated_at,
    po.payment_type,po.provider_key
  FROM public.payment_orders po
) classified
WHERE currency='CNY';

CREATE OR REPLACE VIEW public.invoice_sub2api_payment_projection_health_v1
WITH (security_barrier = true) AS
SELECT count(*)::bigint AS total_rows,
       count(*) FILTER (WHERE classification='exposed_cny')::bigint AS exposed_cny_rows,
       count(*) FILTER (WHERE classification='unsupported_known_non_cny')::bigint AS unsupported_known_non_cny_rows,
       count(*) FILTER (WHERE classification='blocked_unknown')::bigint AS blocked_unknown_currency_rows
FROM (
  SELECT CASE
    WHEN jsonb_typeof(po.provider_snapshot)='object'
     AND po.provider_snapshot ->> 'schema_version'='2'
     AND lower(btrim(po.provider_snapshot ->> 'provider_key'))=lower(btrim(po.provider_key))
     AND upper(btrim(po.provider_snapshot ->> 'currency')) ~ '^[A-Z]{3}$'
	 AND upper(btrim(po.provider_snapshot ->> 'currency'))='CNY'
	THEN 'exposed_cny'
    WHEN jsonb_typeof(po.provider_snapshot)='object'
     AND po.provider_snapshot ->> 'schema_version'='2'
     AND lower(btrim(po.provider_snapshot ->> 'provider_key'))=lower(btrim(po.provider_key))
     AND upper(btrim(po.provider_snapshot ->> 'currency')) ~ '^[A-Z]{3}$'
	THEN 'unsupported_known_non_cny'
    WHEN jsonb_typeof(po.provider_snapshot)='object'
     AND po.provider_snapshot ->> 'schema_version'='2'
     AND lower(btrim(po.provider_snapshot ->> 'provider_key'))=lower(btrim(po.provider_key))
     AND lower(btrim(po.provider_key)) IN ('easypay','alipay','wxpay')
	THEN 'exposed_cny'
	ELSE 'blocked_unknown'
	END::text AS classification
  FROM public.payment_orders po
) classified;

REVOKE ALL ON TABLE public.invoice_sub2api_payment_projection_v1 FROM PUBLIC;
REVOKE ALL ON TABLE public.invoice_sub2api_payment_projection_v1 FROM invoice_sub2api_payments_reader;
REVOKE ALL ON TABLE public.invoice_sub2api_payment_projection_health_v1 FROM PUBLIC;
REVOKE ALL ON TABLE public.invoice_sub2api_payment_projection_health_v1 FROM invoice_sub2api_payments_reader;
GRANT SELECT (
  id, user_id, status, order_type, amount, pay_amount, refund_amount,
	currency, completed_at, refund_at, created_at, updated_at, payment_type, provider_key
) ON TABLE public.invoice_sub2api_payment_projection_v1 TO invoice_sub2api_payments_reader;
GRANT SELECT (total_rows,exposed_cny_rows,unsupported_known_non_cny_rows,blocked_unknown_currency_rows)
ON TABLE public.invoice_sub2api_payment_projection_health_v1
TO invoice_sub2api_payments_reader;

ALTER ROLE invoice_sub2api_identities_reader
  NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS;
ALTER ROLE invoice_sub2api_identities_reader CONNECTION LIMIT 2;
ALTER ROLE invoice_sub2api_identities_reader SET default_transaction_read_only = on;
ALTER ROLE invoice_sub2api_identities_reader SET statement_timeout = '15s';
ALTER ROLE invoice_sub2api_identities_reader SET lock_timeout = '5s';
ALTER ROLE invoice_sub2api_identities_reader SET idle_in_transaction_session_timeout = '15s';
DO $contract$ BEGIN
  EXECUTE format('GRANT CONNECT ON DATABASE %I TO invoice_sub2api_identities_reader', current_database());
END $contract$;

REVOKE CREATE ON SCHEMA public FROM invoice_sub2api_identities_reader;
GRANT USAGE ON SCHEMA public TO invoice_sub2api_identities_reader;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM invoice_sub2api_identities_reader;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM invoice_sub2api_identities_reader;

-- provider_key is the canonical issuer for Sub2API's built-in OIDC identity.
-- Filter at the database boundary so the reader never sees email/social
-- identities or their provider_subject values. Changing the central issuer is
-- a reviewed contract migration, not an agent runtime option.
CREATE OR REPLACE VIEW public.invoice_sub2api_oidc_binding_projection_v1
WITH (security_barrier = true) AS
SELECT id,user_id,provider_type,provider_key,provider_subject,
       verified_at,issuer,created_at,updated_at
FROM public.auth_identities
WHERE provider_type='oidc'
  AND provider_key='https://auth.solov.cc/realms/solov'
  AND issuer='https://auth.solov.cc/realms/solov'
  AND verified_at IS NOT NULL;

REVOKE ALL ON TABLE public.auth_identities FROM invoice_sub2api_identities_reader;
REVOKE ALL ON TABLE public.invoice_sub2api_oidc_binding_projection_v1 FROM PUBLIC;
REVOKE ALL ON TABLE public.invoice_sub2api_oidc_binding_projection_v1 FROM invoice_sub2api_identities_reader;
GRANT SELECT (
  id, user_id, provider_type, provider_key, provider_subject,
  verified_at, issuer, created_at, updated_at
) ON TABLE public.invoice_sub2api_oidc_binding_projection_v1 TO invoice_sub2api_identities_reader;

DO $contract$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM pg_auth_members m
    JOIN pg_roles r ON r.oid=m.member
    WHERE r.rolname IN (
      'invoice_sub2api_payments_reader',
      'invoice_sub2api_identities_reader'
    )
  ) THEN
    RAISE EXCEPTION 'invoice Sub2API projection roles must not inherit or assume another role';
  END IF;
END
$contract$;

-- PostgreSQL grants TEMPORARY to PUBLIC by default. An explicit role REVOKE is
-- not a deny rule, so the database owner must first revoke it from PUBLIC.
-- Refuse to bless the reader roles while either still has effective TEMP.
DO $contract$
DECLARE role_name text;
BEGIN
  FOREACH role_name IN ARRAY ARRAY[
    'invoice_sub2api_payments_reader',
    'invoice_sub2api_identities_reader'
  ] LOOP
    EXECUTE format('REVOKE TEMPORARY ON DATABASE %I FROM %I', current_database(), role_name);
    IF has_database_privilege(role_name, current_database(), 'TEMP') THEN
      RAISE EXCEPTION '% retains TEMP through PUBLIC or another role; revoke TEMPORARY ON DATABASE % FROM PUBLIC before applying this contract', role_name, current_database();
    END IF;
  END LOOP;
END
$contract$;

COMMIT;

-- Deliberately absent: raw auth_identities access, users/email, metadata,
-- payment trade references, the provider_snapshot object itself,
-- password/TOTP/session/token tables, API/Admin keys, and every
-- INSERT/UPDATE/DELETE/sequence privilege. The production launcher inventories
-- effective grants and refuses any column beyond its one stream.
