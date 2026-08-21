-- REVIEW-ONLY V3 ECONOMIC PROJECTION FOR NEW API rc.25.
-- No New API source code is changed and no mutation privilege is granted.

BEGIN;

CREATE OR REPLACE VIEW public.invoice_newapi_wallet_config_contract_v3
WITH (security_barrier = true) AS
SELECT COALESCE((SELECT value FROM public.options WHERE key='QuotaPerUnit'),'500000')::text AS quota_per_unit,
       COALESCE((SELECT value FROM public.options WHERE key='Price'),'1')::text AS price,
       COALESCE((SELECT value FROM public.options WHERE key='TopupGroupRatio'),'{}')::text AS topup_group_ratio,
       ((SELECT count(*) FROM public.options WHERE key='QuotaPerUnit')<=1
        AND COALESCE((SELECT value FROM public.options WHERE key='QuotaPerUnit'),'500000')='500000'
        AND (SELECT count(*) FROM public.options WHERE key='Price')<=1
        AND COALESCE((SELECT value FROM public.options WHERE key='Price'),'1')='1'
        AND NOT EXISTS (
          SELECT 1 FROM jsonb_each_text(COALESCE((SELECT value FROM public.options WHERE key='TopupGroupRatio'),'{}')::jsonb)
          WHERE value::numeric<>1
        )) AS contract_ok;

CREATE OR REPLACE VIEW public.invoice_newapi_usage_projection_v3
WITH (security_barrier = true) AS
SELECT l.id::bigint AS source_id,l.user_id::bigint AS user_id,to_timestamp(l.created_at) AS event_time,
       l.quota::numeric(78,0)::text AS service_units,
       'logs'::text AS causal_domain,('logs:' || l.id::text)::text AS source_cursor
FROM public.logs l
WHERE l.type=2 AND l.quota>0;

CREATE OR REPLACE VIEW public.invoice_newapi_usage_projection_health_v3
WITH (security_barrier = true) AS
WITH stats AS (
  SELECT count(*)::bigint AS total_rows,
         COALESCE(max(id)-min(id)+1-count(*),0)::bigint AS gap_count,
         count(*) FILTER (WHERE type=2 AND quota<0)::bigint AS invalid_rows
  FROM public.logs
), option_contract AS (
  SELECT COALESCE((SELECT value FROM public.options WHERE key='LogConsumeEnabled'),'true')='true' AS enabled,
         (SELECT count(*) FROM public.options WHERE key='LogConsumeEnabled')<=1 AS singleton
)
SELECT (enabled AND singleton AND invalid_rows=0 AND w.contract_ok) AS contract_ok,
       CASE WHEN NOT singleton THEN 'log_consume_option_duplicate'
            WHEN NOT enabled THEN 'consume_logging_disabled'
            WHEN NOT w.contract_ok THEN 'wallet_configuration_invalid'
            WHEN invalid_rows>0 THEN 'consume_log_contract_invalid'
            ELSE '' END::text AS blocked_reason,total_rows,gap_count
       ,encode(sha256(convert_to(quota_per_unit || '|' || price || '|' || topup_group_ratio || '|NEWAPI_QUOTA|rc.25|v3','UTF8')),'hex') AS configuration_hash
FROM stats,option_contract,public.invoice_newapi_wallet_config_contract_v3 w;

CREATE OR REPLACE VIEW public.invoice_newapi_credits_projection_v3
WITH (security_barrier = true) AS
SELECT c.id::bigint AS source_id,c.user_id::bigint AS user_id,to_timestamp(c.created_at) AS event_time,
       c.quota_awarded::numeric(78,0)::text AS service_units,'bonus'::text AS credit_kind,
       'checkins'::text AS causal_domain,('checkins:' || c.id::text)::text AS source_cursor
FROM public.checkins c WHERE c.quota_awarded>0
UNION ALL
SELECT r.id::bigint,r.used_user_id::bigint,to_timestamp(r.redeemed_time),
       r.quota::numeric(78,0)::text,'bonus'::text,'redemptions'::text,
       ('redemptions:' || r.id::text)::text
FROM public.redemptions r
WHERE r.status=3 AND r.used_user_id>0 AND r.redeemed_time>0 AND r.quota>0;

CREATE OR REPLACE VIEW public.invoice_newapi_credits_projection_health_v3
WITH (security_barrier = true) AS
WITH c AS (
  SELECT count(*)::bigint n,COALESCE(max(id)-min(id)+1-count(*),0)::bigint gaps,
         count(*) FILTER (WHERE quota_awarded<=0)::bigint invalid FROM public.checkins
), r AS (
  SELECT count(*)::bigint n,COALESCE(max(id)-min(id)+1-count(*),0)::bigint gaps,
         count(*) FILTER (WHERE quota<0)::bigint invalid FROM public.redemptions
), combined AS (
  SELECT c.n+r.n AS total_rows,c.gaps+r.gaps AS gap_count,c.invalid+r.invalid AS invalid_rows FROM c,r
)
SELECT (invalid_rows=0 AND w.contract_ok) AS contract_ok,
       CASE WHEN NOT w.contract_ok THEN 'wallet_configuration_invalid'
            WHEN invalid_rows>0 THEN 'credit_contract_invalid'
            ELSE '' END::text AS blocked_reason,total_rows,gap_count
       ,encode(sha256(convert_to(quota_per_unit || '|' || price || '|' || topup_group_ratio || '|NEWAPI_QUOTA|rc.25|v3','UTF8')),'hex') AS configuration_hash
FROM combined,public.invoice_newapi_wallet_config_contract_v3 w;

CREATE OR REPLACE VIEW public.invoice_newapi_balance_projection_v3
WITH (security_barrier = true) AS
SELECT u.id::bigint AS user_id,GREATEST(u.quota,0)::numeric(78,0)::text AS balance_service_units,
       (u.quota<0) AS balance_negative
FROM public.users u WHERE u.deleted_at IS NULL;

CREATE OR REPLACE VIEW public.invoice_newapi_cutover_contract_v3
WITH (security_barrier = true) AS
WITH payment_high AS (
  SELECT concat('subscription_orders:',COALESCE((SELECT max(id) FROM public.subscription_orders),0),
                ';top_ups:',COALESCE((SELECT max(id) FROM public.top_ups),0)) AS cursor,
         to_timestamp(GREATEST(COALESCE((SELECT max(GREATEST(create_time,complete_time)) FROM public.top_ups),0),
                               COALESCE((SELECT max(GREATEST(create_time,complete_time)) FROM public.subscription_orders),0))) AS at
), usage_high AS (
  SELECT COALESCE(to_timestamp(max(created_at)),transaction_timestamp()) AS at,
         ('logs:' || COALESCE(max(id),0)::text)::text AS cursor FROM public.logs
), credit_high AS (
  SELECT concat('checkins:',COALESCE((SELECT max(id) FROM public.checkins),0),
                ';redemptions:',COALESCE((SELECT max(id) FROM public.redemptions),0)) AS cursor,
         to_timestamp(GREATEST(COALESCE((SELECT max(created_at) FROM public.checkins),0),
                               COALESCE((SELECT max(redeemed_time) FROM public.redemptions),0))) AS at
), cfg AS (
  SELECT (contract_ok AND (SELECT count(*) FROM public.options WHERE key='LogConsumeEnabled')<=1
          AND COALESCE((SELECT value FROM public.options WHERE key='LogConsumeEnabled'),'true')='true') AS contract_ok,
         encode(sha256(convert_to(quota_per_unit || '|' || price || '|' || topup_group_ratio || '|NEWAPI_QUOTA|rc.25|v3','UTF8')),'hex') AS configuration_hash
  FROM public.invoice_newapi_wallet_config_contract_v3
)
SELECT 'newapi-economic-rc25-v3'::text AS projection_contract,contract_ok,configuration_hash,
       COALESCE(payment_high.at,transaction_timestamp()) AS payments_event_at,payment_high.cursor::text AS payments_cursor,
       usage_high.at AS usage_event_at,usage_high.cursor::text AS usage_cursor,
       COALESCE(credit_high.at,transaction_timestamp()) AS credits_event_at,credit_high.cursor::text AS credits_cursor
FROM payment_high,usage_high,credit_high,cfg;

CREATE OR REPLACE VIEW public.invoice_newapi_payments_projection_v3
WITH (security_barrier = true) AS
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
         WHEN 'creem' THEN t.amount::numeric(78,0)::text
       END AS wallet_cash_service_units,
       NULL::text AS paid_minor,'pending'::text AS verification_state,false AS refunded
FROM public.top_ups t
WHERE t.amount>0 AND t.complete_time>0 AND t.status='success'
UNION ALL
SELECT s.id::bigint,s.user_id::bigint,to_timestamp(GREATEST(s.create_time,s.complete_time)),
       'subscription_orders'::text,('subscription_orders:'||s.id::text)::text,
       'subscription_purchase'::text,s.status,'subscription'::text,
       '0'::text,s.money::text,'CNY'::text,'0'::text,'0'::text,
       CASE WHEN s.complete_time>0 THEN to_timestamp(s.complete_time) END,NULL::timestamptz,
       to_timestamp(s.create_time),to_timestamp(GREATEST(s.create_time,s.complete_time)),
       s.payment_method::text,s.payment_provider::text,NULL::text,
       CASE WHEN s.money>0 AND s.money*100=trunc(s.money*100) THEN (s.money*100)::numeric(78,0)::text END,
       'pending'::text,false
FROM public.subscription_orders s
WHERE s.complete_time>0 AND s.status='success';

CREATE OR REPLACE VIEW public.invoice_newapi_payments_projection_health_v3
WITH (security_barrier = true) AS
SELECT cfg.contract_ok,
       CASE WHEN NOT cfg.contract_ok THEN 'wallet_configuration_invalid' ELSE '' END::text AS blocked_reason,
       encode(sha256(convert_to(cfg.quota_per_unit || '|' || cfg.price || '|' || cfg.topup_group_ratio || '|NEWAPI_QUOTA|rc.25|v3','UTF8')),'hex') AS configuration_hash
FROM public.invoice_newapi_wallet_config_contract_v3 cfg;

DO $contract$
DECLARE role_name text;
BEGIN
  FOREACH role_name IN ARRAY ARRAY['invoice_newapi_payments_v3_reader','invoice_newapi_usage_reader','invoice_newapi_credits_reader','invoice_newapi_balances_reader'] LOOP
    EXECUTE format('ALTER ROLE %I NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 2',role_name);
    EXECUTE format('ALTER ROLE %I SET default_transaction_read_only=on',role_name);
    EXECUTE format('ALTER ROLE %I SET statement_timeout=''15s''',role_name);
    EXECUTE format('ALTER ROLE %I SET lock_timeout=''5s''',role_name);
    EXECUTE format('ALTER ROLE %I SET idle_in_transaction_session_timeout=''15s''',role_name);
    EXECUTE format('GRANT CONNECT ON DATABASE %I TO %I',current_database(),role_name);
    EXECUTE format('REVOKE CREATE ON SCHEMA public FROM %I',role_name);
    EXECUTE format('GRANT USAGE ON SCHEMA public TO %I',role_name);
    EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA public FROM %I',role_name);
    EXECUTE format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM %I',role_name);
    EXECUTE format('REVOKE TEMPORARY ON DATABASE %I FROM %I',current_database(),role_name);
    IF has_database_privilege(role_name,current_database(),'TEMP') THEN
      RAISE EXCEPTION '% retains TEMP through PUBLIC; revoke TEMPORARY on this database from PUBLIC first',role_name;
    END IF;
    IF EXISTS (
      SELECT 1 FROM pg_auth_members m
      WHERE m.member=(SELECT oid FROM pg_roles WHERE rolname=role_name)
         OR m.roleid=(SELECT oid FROM pg_roles WHERE rolname=role_name)
    ) THEN
      RAISE EXCEPTION '% has a role membership in either direction',role_name;
    END IF;
  END LOOP;
END $contract$;

REVOKE ALL ON public.invoice_newapi_usage_projection_v3,public.invoice_newapi_usage_projection_health_v3,
  public.invoice_newapi_credits_projection_v3,public.invoice_newapi_credits_projection_health_v3,
  public.invoice_newapi_balance_projection_v3,public.invoice_newapi_wallet_config_contract_v3,
  public.invoice_newapi_cutover_contract_v3,public.invoice_newapi_payments_projection_v3,
  public.invoice_newapi_payments_projection_health_v3 FROM PUBLIC;

GRANT SELECT(source_id,user_id,event_time,causal_domain,source_cursor,entity_kind,status,order_type,
  amount,pay_amount,currency,refund_amount,gateway_refund_amount,completed_at,refund_at,created_at,
  updated_at,payment_type,provider_key,wallet_cash_service_units,paid_minor,verification_state,refunded)
  ON public.invoice_newapi_payments_projection_v3 TO invoice_newapi_payments_v3_reader;
GRANT SELECT(contract_ok,blocked_reason,configuration_hash)
  ON public.invoice_newapi_payments_projection_health_v3 TO invoice_newapi_payments_v3_reader;

GRANT SELECT(source_id,user_id,event_time,service_units,causal_domain,source_cursor)
  ON public.invoice_newapi_usage_projection_v3 TO invoice_newapi_usage_reader;
GRANT SELECT(contract_ok,blocked_reason,total_rows,gap_count,configuration_hash)
  ON public.invoice_newapi_usage_projection_health_v3 TO invoice_newapi_usage_reader;
GRANT SELECT(source_id,user_id,event_time,service_units,credit_kind,causal_domain,source_cursor)
  ON public.invoice_newapi_credits_projection_v3 TO invoice_newapi_credits_reader;
GRANT SELECT(contract_ok,blocked_reason,total_rows,gap_count,configuration_hash)
  ON public.invoice_newapi_credits_projection_health_v3 TO invoice_newapi_credits_reader;
GRANT SELECT(user_id,balance_service_units,balance_negative)
  ON public.invoice_newapi_balance_projection_v3 TO invoice_newapi_balances_reader;
GRANT SELECT(projection_contract,contract_ok,configuration_hash,payments_event_at,payments_cursor,usage_event_at,usage_cursor,credits_event_at,credits_cursor)
  ON public.invoice_newapi_cutover_contract_v3 TO invoice_newapi_balances_reader;

COMMIT;

-- Deliberately absent: users.email/password/access_token/bindings, logs.content,
-- username/token/model/IP/request IDs/other, redemption keys/names, payment
-- trade numbers/provider payloads, option secrets, and every mutation grant.
