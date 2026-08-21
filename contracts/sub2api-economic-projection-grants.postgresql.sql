-- REVIEW-ONLY V3 ECONOMIC PROJECTION. This changes no Sub2API source code and
-- grants no write path. Run only after creating the three named LOGIN roles.
-- The cutover-init command reads these views inside one REPEATABLE READ READ
-- ONLY transaction; normal agents never receive raw-table privileges.

BEGIN;

CREATE OR REPLACE VIEW public.invoice_sub2api_wallet_config_contract_v3
WITH (security_barrier = true) AS
SELECT
  max(value) FILTER (WHERE key='BALANCE_RECHARGE_MULTIPLIER')::text AS multiplier_value,
  max(updated_at) FILTER (WHERE key='BALANCE_RECHARGE_MULTIPLIER') AS multiplier_updated_at,
  max(value) FILTER (WHERE key='RECHARGE_FEE_RATE')::text AS fee_rate_value,
  max(updated_at) FILTER (WHERE key='RECHARGE_FEE_RATE') AS fee_rate_updated_at,
  (count(*) FILTER (WHERE key='BALANCE_RECHARGE_MULTIPLIER')=1
   AND max(value) FILTER (WHERE key='BALANCE_RECHARGE_MULTIPLIER') IN ('1','1.0','1.00','1.00000000')
   AND count(*) FILTER (WHERE key='RECHARGE_FEE_RATE')=1) AS contract_ok
FROM public.settings
WHERE key IN ('BALANCE_RECHARGE_MULTIPLIER','RECHARGE_FEE_RATE');

CREATE OR REPLACE VIEW public.invoice_sub2api_usage_projection_v3
WITH (security_barrier = true) AS
SELECT ul.id AS source_id,
       ul.user_id,
       ul.created_at AS event_time,
       ((round(ul.actual_cost,8) * 100000000)::numeric(78,0))::text AS service_units,
       'usage_logs'::text AS causal_domain,
       ('usage_logs:' || ul.id::text)::text AS source_cursor
FROM public.usage_logs ul
WHERE ul.billing_type=0
  AND round(ul.actual_cost,8)>0;

CREATE OR REPLACE VIEW public.invoice_sub2api_usage_projection_health_v3
WITH (security_barrier = true) AS
WITH stats AS (
  SELECT count(*)::bigint AS total_rows,
         COALESCE(max(id)-min(id)+1-count(*),0)::bigint AS gap_count,
         count(*) FILTER (WHERE billing_type NOT IN (0,1) OR actual_cost<0)::bigint AS invalid_rows
  FROM public.usage_logs
)
SELECT (stats.invalid_rows=0 AND w.contract_ok) AS contract_ok,
       CASE WHEN NOT w.contract_ok THEN 'wallet_configuration_invalid'
            WHEN invalid_rows>0 THEN 'usage_contract_invalid'
            ELSE '' END::text AS blocked_reason,
       total_rows,gap_count,
       encode(sha256(convert_to(COALESCE(multiplier_value,'<missing>') || '|' || COALESCE((extract(epoch from multiplier_updated_at)*1000000)::numeric(30,0)::text,'<missing>') || '|' ||
       COALESCE(fee_rate_value,'<missing>') || '|' || COALESCE((extract(epoch from fee_rate_updated_at)*1000000)::numeric(30,0)::text,'<missing>') ||
       '|SUB2_BALANCE_1E8|v3','UTF8')),'hex') AS configuration_hash
FROM stats,public.invoice_sub2api_wallet_config_contract_v3 w;

CREATE OR REPLACE VIEW public.invoice_sub2api_credits_projection_v3
WITH (security_barrier = true) AS
SELECT pcu.id AS source_id,pcu.user_id,pcu.used_at AS event_time,
       ((pcu.bonus_amount * 100000000)::numeric(78,0))::text AS service_units,
       'bonus'::text AS credit_kind,'promo_code_usages'::text AS causal_domain,
       ('promo_code_usages:' || pcu.id::text)::text AS source_cursor
FROM public.promo_code_usages pcu
WHERE pcu.bonus_amount>0
UNION ALL
SELECT ual.id,ual.user_id,ual.created_at,
       ((ual.amount * 100000000)::numeric(78,0))::text,
       'rebate'::text,'user_affiliate_ledger'::text,
       ('user_affiliate_ledger:' || ual.id::text)::text
FROM public.user_affiliate_ledger ual
WHERE ual.action='transfer' AND ual.amount>0
UNION ALL
SELECT rc.id,rc.used_by,rc.used_at,
       ((rc.value * 100000000)::numeric(78,0))::text,
       'bonus'::text,'redeem_codes'::text,
       ('redeem_codes:' || rc.id::text)::text
FROM public.redeem_codes rc
WHERE rc.type='balance' AND rc.used_by IS NOT NULL AND rc.used_at IS NOT NULL
  AND rc.status='used' AND rc.value>0
  AND NOT EXISTS (SELECT 1 FROM public.payment_orders po WHERE po.recharge_code=rc.code);

CREATE OR REPLACE VIEW public.invoice_sub2api_credits_projection_health_v3
WITH (security_barrier = true) AS
WITH p AS (
  SELECT count(*)::bigint n,COALESCE(max(id)-min(id)+1-count(*),0)::bigint gaps,
         count(*) FILTER (WHERE bonus_amount<=0)::bigint invalid FROM public.promo_code_usages
), a AS (
  SELECT count(*)::bigint n,COALESCE(max(id)-min(id)+1-count(*),0)::bigint gaps,
         count(*) FILTER (WHERE action NOT IN ('accrue','transfer') OR amount<=0)::bigint invalid FROM public.user_affiliate_ledger
), r AS (
  SELECT count(*)::bigint n,COALESCE(max(id)-min(id)+1-count(*),0)::bigint gaps,
         count(*) FILTER (
           WHERE type='balance' AND status='used' AND used_by IS NOT NULL
             AND used_at IS NOT NULL AND value<=0
         )::bigint invalid
  FROM public.redeem_codes
), combined AS (
  SELECT p.n+a.n+r.n AS total_rows,p.gaps+a.gaps+r.gaps AS gap_count,p.invalid+a.invalid+r.invalid AS invalid_rows FROM p,a,r
)
SELECT (combined.invalid_rows=0 AND w.contract_ok) AS contract_ok,
       CASE WHEN NOT w.contract_ok THEN 'wallet_configuration_invalid'
            WHEN invalid_rows>0 THEN 'credit_contract_invalid'
            ELSE '' END::text AS blocked_reason,total_rows,gap_count
       ,encode(sha256(convert_to(COALESCE(multiplier_value,'<missing>') || '|' || COALESCE((extract(epoch from multiplier_updated_at)*1000000)::numeric(30,0)::text,'<missing>') || '|' ||
       COALESCE(fee_rate_value,'<missing>') || '|' || COALESCE((extract(epoch from fee_rate_updated_at)*1000000)::numeric(30,0)::text,'<missing>') ||
       '|SUB2_BALANCE_1E8|v3','UTF8')),'hex') AS configuration_hash
FROM combined,public.invoice_sub2api_wallet_config_contract_v3 w;

CREATE OR REPLACE VIEW public.invoice_sub2api_balance_projection_v3
WITH (security_barrier = true) AS
SELECT u.id AS user_id,
       ((GREATEST(u.balance,0) * 100000000)::numeric(78,0))::text AS balance_service_units,
       (u.balance<0) AS balance_negative
FROM public.users u
WHERE u.deleted_at IS NULL;

CREATE OR REPLACE VIEW public.invoice_sub2api_cutover_contract_v3
WITH (security_barrier = true) AS
WITH payment_high AS (
  SELECT COALESCE(p.updated_at,transaction_timestamp()) AS at,COALESCE(p.id,0)::bigint AS id
  FROM (SELECT 1) seed LEFT JOIN LATERAL (
    SELECT updated_at,id FROM public.payment_orders ORDER BY updated_at DESC,id DESC LIMIT 1
  ) p ON true
), usage_high AS (
  SELECT COALESCE(u.created_at,transaction_timestamp()) AS at,COALESCE(u.id,0)::bigint AS id
  FROM (SELECT 1) seed LEFT JOIN LATERAL (
    SELECT created_at,id FROM public.usage_logs ORDER BY created_at DESC,id DESC LIMIT 1
  ) u ON true
), credit_high AS (
  SELECT COALESCE(max(event_time),transaction_timestamp()) AS at,
         concat('promo_code_usages:',COALESCE((SELECT max(id) FROM public.promo_code_usages),0),
                ';user_affiliate_ledger:',COALESCE((SELECT max(id) FROM public.user_affiliate_ledger),0),
                ';redeem_codes:',COALESCE((SELECT max(id) FROM public.redeem_codes),0)) AS cursor
  FROM public.invoice_sub2api_credits_projection_v3
), cfg AS (
  SELECT contract_ok,
         encode(sha256(convert_to(COALESCE(multiplier_value,'<missing>') || '|' || COALESCE((extract(epoch from multiplier_updated_at)*1000000)::numeric(30,0)::text,'<missing>') || '|' ||
         COALESCE(fee_rate_value,'<missing>') || '|' || COALESCE((extract(epoch from fee_rate_updated_at)*1000000)::numeric(30,0)::text,'<missing>') ||
         '|SUB2_BALANCE_1E8|v3','UTF8')),'hex') AS configuration_hash
  FROM public.invoice_sub2api_wallet_config_contract_v3
)
SELECT 'sub2api-economic-v3'::text AS projection_contract,
       contract_ok,configuration_hash,
       payment_high.at AS payments_event_at,('payment_orders:' || payment_high.id)::text AS payments_cursor,
       usage_high.at AS usage_event_at,('usage_logs:' || usage_high.id)::text AS usage_cursor,
       credit_high.at AS credits_event_at,credit_high.cursor::text AS credits_cursor
FROM payment_high,usage_high,credit_high,cfg;

CREATE OR REPLACE VIEW public.invoice_sub2api_payments_projection_v3
WITH (security_barrier = true) AS
SELECT p.id AS source_id,p.user_id,p.updated_at AS event_time,
       'payment_orders'::text AS causal_domain,('payment_orders:'||p.id::text)::text AS source_cursor,
       CASE WHEN p.order_type='subscription' THEN 'subscription_purchase' ELSE 'payment_order' END::text AS entity_kind,
       p.status,p.order_type,p.amount::text,p.pay_amount::text,p.currency,
       p.refund_amount::text,
       round(p.pay_amount*p.refund_amount/NULLIF(p.amount,0),2)::text AS gateway_refund_amount,
       p.completed_at,p.refund_at,p.created_at,p.updated_at,p.payment_type,p.provider_key,
       CASE WHEN p.order_type='balance' AND cfg.contract_ok AND cfg.multiplier_updated_at<=p.completed_at
            THEN ((p.amount*100000000)::numeric(78,0))::text END AS wallet_cash_service_units,
       CASE WHEN p.order_type='subscription' AND p.pay_amount>0 AND p.pay_amount*100=trunc(p.pay_amount*100)
            THEN (p.pay_amount*100)::numeric(78,0)::text END AS paid_minor,
       CASE WHEN p.order_type='subscription' AND p.status='COMPLETED' AND p.refund_amount=0 THEN 'verified'
            WHEN p.order_type='subscription' THEN 'frozen' END::text AS verification_state,
       (p.refund_amount>0) AS refunded
FROM public.invoice_sub2api_payment_projection_v1 p
CROSS JOIN public.invoice_sub2api_wallet_config_contract_v3 cfg;

CREATE OR REPLACE VIEW public.invoice_sub2api_payments_projection_health_v3
WITH (security_barrier = true) AS
SELECT (cfg.contract_ok AND h.blocked_unknown_currency_rows=0) AS contract_ok,
       CASE WHEN NOT cfg.contract_ok THEN 'wallet_configuration_invalid'
            WHEN h.blocked_unknown_currency_rows>0 THEN 'payment_currency_evidence_missing'
            ELSE '' END::text AS blocked_reason,
       encode(sha256(convert_to(COALESCE(cfg.multiplier_value,'<missing>') || '|' || COALESCE((extract(epoch from cfg.multiplier_updated_at)*1000000)::numeric(30,0)::text,'<missing>') || '|' ||
       COALESCE(cfg.fee_rate_value,'<missing>') || '|' || COALESCE((extract(epoch from cfg.fee_rate_updated_at)*1000000)::numeric(30,0)::text,'<missing>') ||
       '|SUB2_BALANCE_1E8|v3','UTF8')),'hex') AS configuration_hash
FROM public.invoice_sub2api_wallet_config_contract_v3 cfg
CROSS JOIN public.invoice_sub2api_payment_projection_health_v1 h;

DO $contract$
DECLARE role_name text;
BEGIN
  FOREACH role_name IN ARRAY ARRAY['invoice_sub2api_payments_v3_reader','invoice_sub2api_usage_reader','invoice_sub2api_credits_reader','invoice_sub2api_balances_reader'] LOOP
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
  END LOOP;
END $contract$;

REVOKE ALL ON public.invoice_sub2api_usage_projection_v3,public.invoice_sub2api_usage_projection_health_v3,
  public.invoice_sub2api_credits_projection_v3,public.invoice_sub2api_credits_projection_health_v3,
  public.invoice_sub2api_balance_projection_v3,public.invoice_sub2api_wallet_config_contract_v3,
  public.invoice_sub2api_cutover_contract_v3,public.invoice_sub2api_payments_projection_v3,
  public.invoice_sub2api_payments_projection_health_v3 FROM PUBLIC;

GRANT SELECT(source_id,user_id,event_time,causal_domain,source_cursor,entity_kind,status,order_type,
  amount,pay_amount,currency,refund_amount,gateway_refund_amount,completed_at,refund_at,created_at,
  updated_at,payment_type,provider_key,wallet_cash_service_units,paid_minor,verification_state,refunded)
  ON public.invoice_sub2api_payments_projection_v3 TO invoice_sub2api_payments_v3_reader;
GRANT SELECT(contract_ok,blocked_reason,configuration_hash)
  ON public.invoice_sub2api_payments_projection_health_v3 TO invoice_sub2api_payments_v3_reader;

GRANT SELECT(source_id,user_id,event_time,service_units,causal_domain,source_cursor)
  ON public.invoice_sub2api_usage_projection_v3 TO invoice_sub2api_usage_reader;
GRANT SELECT(contract_ok,blocked_reason,total_rows,gap_count,configuration_hash)
  ON public.invoice_sub2api_usage_projection_health_v3 TO invoice_sub2api_usage_reader;
GRANT SELECT(source_id,user_id,event_time,service_units,credit_kind,causal_domain,source_cursor)
  ON public.invoice_sub2api_credits_projection_v3 TO invoice_sub2api_credits_reader;
GRANT SELECT(contract_ok,blocked_reason,total_rows,gap_count,configuration_hash)
  ON public.invoice_sub2api_credits_projection_health_v3 TO invoice_sub2api_credits_reader;
GRANT SELECT(user_id,balance_service_units,balance_negative)
  ON public.invoice_sub2api_balance_projection_v3 TO invoice_sub2api_balances_reader;
GRANT SELECT(projection_contract,contract_ok,configuration_hash,payments_event_at,payments_cursor,usage_event_at,usage_cursor,credits_event_at,credits_cursor)
  ON public.invoice_sub2api_cutover_contract_v3 TO invoice_sub2api_balances_reader;

COMMIT;

-- Deliberately absent: users.email/password/notes, usage request/model/token/IP
-- content, redeem codes, affiliate source-user/order details, settings secrets,
-- raw provider snapshots/trade references, and every mutation/sequence grant.
