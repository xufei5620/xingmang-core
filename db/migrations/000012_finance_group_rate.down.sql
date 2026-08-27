-- 仅供本地开发重置（规格 §5.7）。生产回滚走备份恢复与前进式修复迁移。
--
-- 先删约束再删列：列删掉之后约束会跟着走，但显式写出来让 down 与 up 逐条对称
-- ——对称的 down 才看得出「up 到底做了几件事」。
ALTER TABLE finance.upstream_account
    DROP CONSTRAINT IF EXISTS upstream_account_group_rate_positive;
ALTER TABLE finance.upstream_account DROP COLUMN IF EXISTS group_rate;
