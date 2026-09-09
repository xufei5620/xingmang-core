-- 仅供本地开发重置（规格 §5.7）。生产回滚走备份恢复与前进式修复迁移。
--
-- 顺序是外键的反向：损失引用批次与代理，批次引用代理。
-- 不 DROP SCHEMA finance：登记簿（000008）与利润台账（000009）还在里面。
DROP TABLE IF EXISTS finance.amortization_loss;
DROP TABLE IF EXISTS finance.subscription_cost_batch;
DROP TABLE IF EXISTS finance.proxy_asset;

-- 两处对既有表的增补也要撤干净，否则 down 之后再 up 会撞上「约束已存在」。
ALTER TABLE finance.token_map
    DROP CONSTRAINT IF EXISTS token_map_upstream_token_id_not_account_grain;
ALTER TABLE finance.profit_daily
    DROP CONSTRAINT IF EXISTS profit_daily_account_grain_token_id;

DROP INDEX IF EXISTS finance.upstream_account_platform_idx;
ALTER TABLE finance.upstream_account
    DROP CONSTRAINT IF EXISTS upstream_account_platform_id_format;
-- 这一列上可能已经有归属数据；本迁移的 down 只服务于本地重置，
-- 生产不会走到这里（同上）。
ALTER TABLE finance.upstream_account DROP COLUMN IF EXISTS platform_id;
