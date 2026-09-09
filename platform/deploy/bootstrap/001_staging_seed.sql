-- ============================================================================
-- XM-0023  staging 引导数据（Bootstrap）
-- ============================================================================
--
-- 这是什么：一个**幂等**的引导脚本，把 staging 环境跑起来所需的最小登记数据
-- 写进 core.*。由 deploy/compose/launch.yaml 的一次性 `bootstrap` 服务执行。
--
-- 为什么不走 Action（宪法 2 条：所有平台配置写操作走 Action）
-- ----------------------------------------------------------------------------
-- 宪法 3 条给了 Platform Lifecycle Operation 一条独立的、版本化治理的通道，
-- 迁移与 Bootstrap 走这条通道是合法的。这里必须走它，不是图省事：
--
--   1. 先有蛋问题。Action 的执行路径要求一个已解析的 Principal，而
--      Foundation-A 的 Principal 来自 X-Dev-* 头（CR-0001 之后来自 Keycloak）。
--      要用 Action 建第一条服务登记，就得先有一个能通过鉴权的调用方、
--      一个已经在跑的 platform-api、以及一套已经建好的库表——而这些正是
--      Bootstrap 要准备的东西。
--   2. Bootstrap 写的是「环境自身的事实」（这个环境存在、它里面有哪些被管
--      系统），不是业务决定。业务决定（建 Connection、发凭据、改配额）
--      仍然只能走 Action，本脚本刻意不碰那些表。
--   3. 它受同一套治理约束：进仓库、走 PR、可审阅、可重复执行、可回滚
--      （删掉这两行即可，没有副作用）。
--
-- 红线：任何模块、任何 Handler、任何人都不得把这个通道当作绕过 Action 的
-- 写入口。这个文件只能增长「让环境能启动」的行，不能增长业务数据。
--
-- 幂等性：全部 ON CONFLICT DO NOTHING。重复执行不报错、不改已有行——
-- 「重跑一次」必须永远是安全操作，否则没人敢在事故中重跑。
-- ============================================================================

BEGIN;

-- ---------------------------------------------------------------------------
-- 1) 环境登记
-- ---------------------------------------------------------------------------
-- db/migrations/000001_init_core_registry.up.sql 已经种下了三个环境行。
-- 这里再写一次不是冗余：Bootstrap 必须能在「只跑过迁移」和「库被人手工
-- 动过」两种状态下都得到同一个结果，所以它自己声明依赖的前置事实，
-- 而不是假设某个迁移的种子数据还在。
INSERT INTO core.environment (id, description) VALUES
    ('staging', '验收环境 admin-staging.solov.cc')
ON CONFLICT (id) DO NOTHING;

-- ---------------------------------------------------------------------------
-- 2) Sub2API 服务实例登记
-- ---------------------------------------------------------------------------
-- 登记的是「这个环境里有这么一个被管系统」，不含任何凭据。
--
-- endpoint 是占位值：真实的 staging 端点等 XM-0017 拿到 Sub2API 只读账号后
-- 才能确定。表约束要求 https://（service_endpoint_https），所以不能留空。
-- status='active' 表示「登记有效」，不表示「已经连上了」——是否连得上由
-- observed_at / source_watermark 反映，它们此刻为空，前端据此显示
-- 「未初始化」而不是显示一个裸数字（宪法 12 条）。
--
-- 刻意**不建** core.connection：
--   - 真实凭据还没到（XM-0017）；
--   - 当前栈跑 XM_SUB2API_MODE=fake，Fake 连接器不需要 Connection；
--   - connection.credential_ref 有 secret:// 格式约束，为了「先建起来」
--     去编一个假的 ref，等于在库里种一条将来会被当成真的假事实。
INSERT INTO core.service (
    id, service_type, instance_id, environment, endpoint,
    owner, health_check_path, runbook_path, status
) VALUES (
    -- 固定 UUID：让 Bootstrap 在任何机器上得到同一份数据，
    -- 便于 Runbook 里直接引用、也便于跨环境对账
    '0195f3a2-7c41-7c00-9a3b-5f0e2d6b1a01',
    'sub2api',
    'sub2api-staging',
    'staging',
    'https://api.solov.cc',
    'ops',
    '/health',
    'docs/runbooks/LAUNCH.md',
    'active'
)
ON CONFLICT (service_type, instance_id) DO NOTHING;

COMMIT;

-- 自检：把结果打出来，让一次性容器的日志就是执行证据
\echo '--- bootstrap result: core.environment ---'
SELECT id, description FROM core.environment ORDER BY id;
\echo '--- bootstrap result: core.service (staging) ---'
SELECT service_type, instance_id, environment, endpoint, owner, status, observed_at
FROM core.service
WHERE environment = 'staging'
ORDER BY service_type, instance_id;
