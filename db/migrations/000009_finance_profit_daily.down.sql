-- 仅供本地开发重置（规格 §5.7）。生产回滚走备份恢复与前进式修复迁移。
--
-- 不 DROP SCHEMA finance：登记簿（000008）还在里面，把 schema 一起删掉会让
-- 「回滚一步」变成「回滚两步」。
DROP TABLE IF EXISTS finance.profit_daily;
