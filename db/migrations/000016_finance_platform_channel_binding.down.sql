-- 仅供本地开发重置。生产回退走前进式修复或备份恢复。
DROP TABLE IF EXISTS finance.platform_channel_binding;
DROP INDEX IF EXISTS finance.upstream_account_id_environment_key;
DROP INDEX IF EXISTS core.service_id_environment_key;

