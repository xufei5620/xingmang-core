-- 仅供本地开发重置（规格 §5.7）。生产回滚走备份恢复与前进式修复迁移。
DROP INDEX IF EXISTS action.action_run_environment_started_idx;
