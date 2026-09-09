-- 002_grants_evidence.sql — 追加型表的库层权限**证据查询**（XM-R012）
--
-- 用法（只读，任何时候都能跑）：
--   docker compose -f deploy/compose/launch.yaml exec -T postgres \
--     psql -U xingmang -d xingmang -f - < deploy/bootstrap/002_grants_evidence.sql
--
-- ── 为什么需要这个文件 ────────────────────────────────────────────────────
-- Codex 冷审第 8 条指出：`REVOKE UPDATE/DELETE` 一直**只在文档里承诺**
-- （db/migrations/000003、000005 的注释，docs/modules/{audit,ops}/*.md），
-- 从来没有任何东西验证它在真实部署里成立。一句没人查的承诺与没有承诺等价，
-- 但读文档的人会以为闸门已经在了——那比没有更危险。
--
-- 这个脚本**不修改任何权限**，它只把现状读出来。理由：真正的修复是把
-- 「跑迁移的 owner」与「跑应用的受限角色」拆成两个角色，那是一次部署拓扑变更
-- （迁移、备份、bootstrap 都要跟着改），不该由一个 bootstrap 脚本顺手做掉。
--
-- ── 今天的结论（先说，免得看输出的人以为自己配错了）────────────────────
-- 当前 compose 部署里应用账号 `xingmang` 同时是**超级用户与表 owner**。
-- 超级用户绕过一切权限检查，所以：
--
--   * `REVOKE` 对它是**空操作**——执行会成功，权限不变；
--   * `information_schema.role_table_grants` 也不会显示它的隐式权限。
--
-- 也就是说，闸门今天**不存在**。这个脚本的价值就在于让这句话有输出可依，
-- 而不是停留在某个人的记忆里。

\echo '=== 1. 应用账号是不是超级用户 / 表 owner（决定 REVOKE 有没有意义）==='

SELECT
    r.rolname                          AS role,
    r.rolsuper                         AS is_superuser,
    r.rolbypassrls                     AS bypasses_rls,
    CASE
        WHEN r.rolsuper THEN
            '闸门未生效：超级用户绕过一切权限检查，REVOKE 是空操作'
        ELSE
            '非超级用户：下面的 grants 才有意义'
    END                                AS verdict
FROM pg_roles r
WHERE r.rolname = current_user;

\echo ''
\echo '=== 2. 三张追加型表的 owner（owner 的隐式权限同样 REVOKE 不掉）==='

SELECT
    schemaname || '.' || tablename     AS relation,
    tableowner                         AS owner,
    CASE
        WHEN tableowner = current_user THEN '闸门未生效：应用账号就是 owner'
        ELSE 'owner 与应用账号已分离'
    END                                AS verdict
FROM pg_tables
WHERE (schemaname, tablename) IN (
    ('audit',  'audit_event'),
    ('action', 'action_run'),
    ('ops',    'metric_observation_sample')
)
ORDER BY 1;

\echo ''
\echo '=== 3. 实际授出的 UPDATE / DELETE 权限 ==='
\echo '目标态（应用角色与 owner 分离之后）：'
\echo '  audit.audit_event               无 UPDATE、无 DELETE（宪法 11 条，append-only）'
\echo '  action.action_run               无 UPDATE、无 DELETE'
\echo '  ops.metric_observation_sample   无 UPDATE，但 **需要 DELETE**'
\echo ''
\echo '⚠️ 最后一条与旧文档不同，是 XM-R012 有意改的：保留期清理任务要按时间'
\echo '   窗口批量删过期样本（internal/platform/jobs/retention.go）。连 DELETE'
\echo '   一起 REVOKE 会让清理每轮报权限错误，表继续涨。'
\echo '   UPDATE 则永远不该有——改一条已记录的样本等于篡改历史。'
\echo ''

SELECT
    grantee,
    table_schema || '.' || table_name  AS relation,
    privilege_type
FROM information_schema.role_table_grants
WHERE (table_schema, table_name) IN (
        ('audit',  'audit_event'),
        ('action', 'action_run'),
        ('ops',    'metric_observation_sample')
      )
  AND privilege_type IN ('UPDATE', 'DELETE')
ORDER BY relation, grantee, privilege_type;

\echo ''
\echo '=== 4. 规则型第二道防线（这一层与角色无关，超级用户也拦得住）==='
\echo 'audit.audit_event 上的 DO INSTEAD NOTHING 规则是真正在生效的那道闸。'
\echo 'ops.metric_observation_sample 上**故意没有**规则——理由见迁移 000005 注释：'
\echo '它的路线图里包含保留期清理，加规则会让那个 DELETE 变成静默空操作。'
\echo ''

SELECT
    schemaname || '.' || tablename     AS relation,
    rulename,
    definition
FROM pg_rules
WHERE schemaname IN ('audit', 'action', 'ops')
ORDER BY 1, 2;
