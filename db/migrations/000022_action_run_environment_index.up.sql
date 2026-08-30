-- XM-ACTIONS0：操作与审批页「执行记录」子页签的复合索引。
--
-- 跨 Action 的执行记录查询（action.ListActionRuns）按 environment 过滤 +
-- (started_at DESC, id DESC) 做 keyset 分页。没有这条索引时，稀疏环境的一页
-- 要沿 started_at 的全局索引（或全表）倒扫直到凑够 limit 行——与迁移 000006
-- 给 audit.audit_event 打的补丁是同一类问题、同一个解法：environment 放前、
-- started_at DESC 放后，规划器可以直接跳到该环境的最新一行往回走 limit 行。
-- id 一并入索引以匹配 keyset 游标的完整排序键，避免同一 started_at 上出现
-- 需要额外排序的尾部。
--
-- 不用 CONCURRENTLY：迁移在事务里跑（cmd/migrate），CONCURRENTLY 不能在事务
-- 内执行；当前 action_run 量级（Foundation-A 阶段，L2 以上尚被内核拒绝执行）
-- 建索引是毫秒级，同 000006 的说明。
CREATE INDEX action_run_environment_started_idx
    ON action.action_run (environment, started_at DESC, id DESC);
