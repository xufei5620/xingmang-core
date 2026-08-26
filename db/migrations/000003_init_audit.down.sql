-- 仅供本地开发重置（规格 §5.7：生产回滚不依赖 down migration）。
DROP RULE IF EXISTS audit_event_no_update ON audit.audit_event;
DROP RULE IF EXISTS audit_event_no_delete ON audit.audit_event;
DROP TABLE IF EXISTS audit.chain_root;
DROP TABLE IF EXISTS audit.audit_event;
DROP SCHEMA IF EXISTS audit;
