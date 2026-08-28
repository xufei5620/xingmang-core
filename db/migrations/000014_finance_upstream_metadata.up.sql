-- XM-C003 上游管理当前元数据（UI 原型 `V["s2/suppliers"]`）。
-- forward-only（规格 §5.7）。
--
-- 这三列是**一个 upstream_account 当前登记的展示元数据**，不是供应商实体，
-- 也不是历史快照：它们不改变 supplier_key、唯一索引、采集、成本、收入、
-- recharge_ratio 或 group_rate 的任何口径。将来真正按供应商归并与展示未接入
-- 分组，需要独立 supplier/group 实体，不拿名称字符串冒充归并键。
ALTER TABLE finance.upstream_account
    ADD COLUMN upstream_name text,
    ADD COLUMN upstream_contact text,
    ADD COLUMN upstream_group text;

