-- 仅供本地开发重置使用。规格 §5.7：生产回滚不依赖 down migration，
-- 而是走备份恢复 + 前进式修复迁移（Platform Lifecycle Operation）。
DROP TABLE IF EXISTS core.connection;
DROP TABLE IF EXISTS core.connector;
DROP TABLE IF EXISTS core.service;
DROP TABLE IF EXISTS core.environment;
DROP SCHEMA IF EXISTS core;
