-- XM-SMS2（ADR-022 决策 2）：供应商清单搬进代码里的注册表，数据库不再按名字
-- 做 CHECK。
--
-- 此前六张表各有一条 CHECK (provider IN ('sms62','hero_sms'))，外加 sms_resource
-- 上一条按名字写的 token 形状约束。它们把「接第三家供应商」变成一次迁移——而
-- 一条迁移的代价不是写 SQL，是生产上必须停机跑一遍。写路径全部经 Action →
-- Service → 注册表，拼错的 provider 名在到达表之前就被拒；token 形状由
-- PgStore.UpsertResource 按注册表的 token 能力校验。
ALTER TABLE sms.provider_status DROP CONSTRAINT IF EXISTS provider_status_provider_known;
ALTER TABLE sms.sms_order       DROP CONSTRAINT IF EXISTS sms_order_provider_known;
ALTER TABLE sms.sms_resource    DROP CONSTRAINT IF EXISTS sms_resource_provider_known;
ALTER TABLE sms.sms_resource    DROP CONSTRAINT IF EXISTS sms_resource_token_shape;
ALTER TABLE sms.sms_operation   DROP CONSTRAINT IF EXISTS sms_operation_provider_known;
ALTER TABLE sms.sms_code        DROP CONSTRAINT IF EXISTS sms_code_provider_known;
ALTER TABLE sms.sms_email       DROP CONSTRAINT IF EXISTS sms_email_provider_known;
