-- XM-READONLY-QUERIES：让后台读得到「这套部署的库跑到哪一版」。
-- forward-only：本文件一旦发布不得修改（规格 §5.7）。
--
-- ── 为什么是一个 core 里的视图，而不是直接给 API 授 public 的权限 ──────────
-- `public` schema 被契约化为 **River 专用**
-- （contracts/database/role-policy.v1.json 的 `public_schema_contract:
-- "river-only"`，由 dbroles/policy.go 硬校验），而 `public.schema_migrations`
-- 在同一份策略里标着 `no-runtime-access`——任何 runtime 角色都不给。
-- 设计稿 2026-08-28-database-role-separation-design.md §7.6 逐条写死了这一点。
--
-- 直接给 xm_api_runtime 授 public 的 USAGE + 那张表的 SELECT，等于推翻 §7.6。
-- 而 §7.6 自己留了出口：「ops 若后续需要任务诊断，**另批只读视图**」。
-- 这就是那个视图。它把跨 schema 的读取收敛成一个 core 对象：
--
--   * 读者只需要 `core` 的 USAGE（各 runtime 角色本来就有）+ 本视图的 SELECT；
--   * `public` 的权限一格都不用动，River 专用这条隔离原样保留；
--   * 视图 owner 是 xm_migrator，而 PostgreSQL 的视图默认
--     `security_invoker = false`，所以对基表的权限检查用的是 **owner** 的身份
--     ——xm_migrator 本来就是 public.schema_migrations 的 owner。
--
-- 先例：finance.runway_threshold_current_verified（迁移 000017）同样是一个
-- 只读视图，并且作为 `view` 类型对象登记在角色策略里。
--
-- ── 为什么这条 DDL 包在 EXECUTE 里 ─────────────────────────────────────────
-- `public.schema_migrations` **不是任何迁移脚本建的**：golang-migrate 自己在
-- 应用第一条迁移之前创建它（它要用这张表记版本号）。所以本迁移执行时它一定
-- 已经存在，**但它不在 sqlc 的 schema 视野里**——sqlc 把 db/migrations 当作
-- 全部 schema，于是直接写 `FROM public.schema_migrations` 会让
-- `go tool sqlc generate` 报 `relation "schema_migrations" does not exist`
-- 并退出 1，把生成一致性门禁整条打断（实测过）。
--
-- 三条路里选了这一条：
--   1. 往 db/migrations 里加一条 `CREATE TABLE IF NOT EXISTS
--      public.schema_migrations …` 让 sqlc 看见——**否决**：那是在
--      forward-only 的不可变迁移流里写一条永远不生效的 DDL，还等于宣称我们
--      拥有另一个工具的台账表；
--   2. 给 sqlc.yaml 的八份配置各加一个「外部对象声明」schema 目录——**否决**：
--      八处配置改动 + 八个 models.go 会各多出一个没人用的结构体，
--      为一个视图付的代价太大；
--   3. 用 EXECUTE 把这条 DDL 挡在 sqlc 的解析之外——**选它**。代价是读者看到
--      的是一个字符串而不是裸 DDL，所以理由写在这里。
--
-- 这样做**不损失任何东西**：core.schema_migration_state 不进 db/queries
--（sqlc 不需要认识它），它由 internal/platform/lifecycle 手写查询读取。
--
-- 本迁移**不含 GRANT**：本仓库的库权限全部由角色策略（DBR2）施加，
-- 迁移脚本只负责建对象（同 000017 的做法与注释）。

DO $$
BEGIN
    EXECUTE 'CREATE VIEW core.schema_migration_state AS '
         || 'SELECT m.version, m.dirty FROM public.schema_migrations AS m';
    EXECUTE 'COMMENT ON VIEW core.schema_migration_state IS '
         || '''只读投影：golang-migrate 记的已应用版本号与 dirty 标记。'
         || '存在的理由是让 xm_api_runtime 读得到它而不必碰 river-only 的 public schema。''';
END $$;
