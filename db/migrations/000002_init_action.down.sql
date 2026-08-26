-- 仅供本地开发重置使用（规格 §5.7：生产回滚不依赖 down migration）。
DROP RULE IF EXISTS action_run_no_update ON action.action_run;
DROP RULE IF EXISTS action_run_no_delete ON action.action_run;
DROP TABLE IF EXISTS action.action_run;
DROP SCHEMA IF EXISTS action;
