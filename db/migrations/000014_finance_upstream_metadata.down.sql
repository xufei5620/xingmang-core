-- 仅供本地开发重置（规格 §5.7）。生产回滚走备份恢复与前进式修复迁移。
ALTER TABLE finance.upstream_account
    DROP COLUMN IF EXISTS upstream_group,
    DROP COLUMN IF EXISTS upstream_contact,
    DROP COLUMN IF EXISTS upstream_name;
