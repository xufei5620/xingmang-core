-- 仅供本地开发重置（规格 §5.7）。生产回滚走备份恢复与前进式修复迁移。
DROP TABLE IF EXISTS finance.token_map;
DROP TABLE IF EXISTS finance.upstream_account;
DROP SCHEMA IF EXISTS finance;
