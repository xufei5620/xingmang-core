-- 仅供本地开发重置（规格 §5.7）。生产回滚走备份恢复与前进式修复迁移。
--
-- 不 DROP SCHEMA finance：登记簿（000008）、利润台账（000009）、
-- 订阅付款（000010）都还在里面。
DROP TABLE IF EXISTS finance.balance_history;
