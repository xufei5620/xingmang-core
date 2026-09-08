-- 回退 000050：只删视图。基表 public.schema_migrations 是 golang-migrate 自己的
-- 台账，一个字都不能碰——删了它，迁移工具就不知道库跑到哪一版了。
DROP VIEW IF EXISTS core.schema_migration_state;
