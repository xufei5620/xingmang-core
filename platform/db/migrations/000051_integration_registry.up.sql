-- XM-EXT-INTEGRATION「接口与自动化」的两张登记表（ADMIN-IA §5.4.1，2026-09-08 裁定）。
--
-- 两张表都是**纯登记簿**：它们记录「我们打算让谁来调、打算在什么时候做什么」，
-- 本身不发凭据、不授权、不限流、不执行。这不是一句免责声明，而是这两张表
-- 存在的前提——把它们做成会生效的东西，就绕过了授权与审批那两道闸。

-- core.api_client：谁在调**我们的** API 的登记簿。
--
-- 为什么需要它：平台今天没有服务身份的任何登记面。身份来源只有两处——
-- 员工账号（internal/platform/localauth，全是 HUMAN）与 Keycloak
-- （internal/platform/oidcauth，Realm 角色翻译成 scope）。于是「有哪些机器
-- 身份该来调我们」这个问题，今天只能靠翻 action.action_run 里**已经来过**的
-- principal_id 反推——那只回答得了「谁来过」，回答不了「谁本该来」和
-- 「谁不该再来」。本表补的正是后两个。
--
-- **本表不是授权面**：登记一行不会发放任何凭据，也不会让任何请求被放行；
-- 停用一行不会挡住任何请求。授权仍然完整地由 Keycloak Realm 角色
-- （oidcauth.DefaultRoleScopeMap）与员工账号角色决定。做成授权面需要在请求
-- 路径上加一次查表，那是一个独立的、需要另行裁定的改动——在那之前，这张表
-- 与真实授权之间的差异必须靠**对账**发现（列表端点把登记簿与 action_run 里
-- 观测到的调用方两侧都给出来），而不是靠假装它们一致。
CREATE TABLE core.api_client (
    id             uuid PRIMARY KEY,
    -- principal_id 是 internal/platform/principal.Principal.ID 的字面值，
    -- 也就是 action_run.principal_id 里会出现的那个串。两边同源才对得上账；
    -- 起一个"客户端编号"再另外维护映射，等于给对账多加一层会漂的中间层。
    principal_id   text NOT NULL CHECK (btrim(principal_id) <> ''),
    -- 词表与 internal/platform/principal 的 Type 常量逐字一致
    -- （同 core.approval_request.requester_type 的做法）。
    principal_type text NOT NULL
                   CHECK (principal_type IN ('HUMAN', 'SERVICE', 'AI', 'SERVER_AGENT')),
    display_name   text NOT NULL CHECK (btrim(display_name) <> ''),
    purpose        text NOT NULL DEFAULT '',
    -- owner 是"出了事找谁"，自由文本（人名/岗位/群名都可以）。
    owner          text NOT NULL DEFAULT '',
    -- expected_scopes 是**期望**的权限范围，不是生效的权限范围。
    -- 列名带 expected_ 前缀是刻意的：叫 scopes 会让人以为改这一列就能改授权。
    expected_scopes text[] NOT NULL DEFAULT '{}',
    -- 凭据只存引用（宪法 7 条）。形如 secret://<scope>/<name>，两段的字符集
    -- 与 internal/platform/secrets.ParseCredentialRef 的 ^[a-z0-9][a-z0-9-]{0,63}$
    -- 逐字一致——库层与解析器不一致的话，一条能存进来的引用会在解析时才炸。
    -- 空串 = 未登记凭据引用（例如员工账号这类不用 API Key 的调用方）。
    credential_ref text NOT NULL DEFAULT ''
                   CHECK (credential_ref = ''
                          OR credential_ref ~ '^secret://[a-z0-9][a-z0-9-]{0,63}/[a-z0-9][a-z0-9-]{0,63}$'),
    status         text NOT NULL DEFAULT 'active'
                   CHECK (status IN ('active', 'disabled')),
    notes          text NOT NULL DEFAULT '',
    environment    text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    created_at     timestamptz NOT NULL DEFAULT now(),
    created_by     text NOT NULL CHECK (btrim(created_by) <> ''),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by     text NOT NULL CHECK (btrim(updated_by) <> ''),

    -- 一个环境里一个 principal_id 只能登记一次。没有这条唯一键，同一个身份
    -- 会被登记成两行不同用途，对账时"这个调用方是谁"就有两个答案。
    CONSTRAINT api_client_unique_principal UNIQUE (environment, principal_id)
);

COMMENT ON TABLE core.api_client IS
    'API 调用方登记簿（XM-EXT-INTEGRATION）。**不是授权面**：登记不发凭据、不授权、不限流，停用也不挡请求；授权仍在 Keycloak 与员工账号角色。';
COMMENT ON COLUMN core.api_client.expected_scopes IS
    '期望的权限范围，不是生效的权限范围——改这一列不会改变任何请求的鉴权结果。';
COMMENT ON COLUMN core.api_client.credential_ref IS
    '凭据引用（secret://<scope>/<name>），不是凭据值；明文永不入库（宪法 7 条）。';
COMMENT ON COLUMN core.api_client.status IS
    'active/disabled 只描述登记状态。disabled **不会**让该身份的请求被拒绝。';

-- 列表页按环境取全量、按登记时间排；调用方登记簿是几十行量级的表，
-- 不需要分页游标（同 core.server_asset 的判断）。
CREATE INDEX api_client_environment_created_idx
    ON core.api_client (environment, created_at DESC);

-- core.automation_rule：「当 X 发生时执行 Y」的规则登记簿。
--
-- **本表没有执行器**（ADMIN-IA §5.4.1，2026-09-08 产品负责人裁定：
-- 「自动化流程先做只读」）。仓库里没有任何代码会读这张表去触发 Action——
-- internal/platform/integration 只做增删改查，不 import 内核的执行路径。
--
-- 为什么不顺手做执行：自动触发 Action 会绕开人工审批那道闸。L2 及以上要人批
-- （宪法 9 条），而机器凑不出审批人；L1 虽然直接执行，但"哪些 L1 允许被规则
-- 自动触发"本身就是一个需要单独裁定的授权决定。要不要让规则引擎真执行、
-- 能执行到哪个风险等级，是另一件事，不在本表的范围内。
--
-- 所以 status 的三个取值**都不会让任何 Action 跑起来**：它们描述的是这条
-- 登记处在编写、已定稿、还是已作废，不是运行状态。
CREATE TABLE core.automation_rule (
    id             uuid PRIMARY KEY,
    name           text NOT NULL CHECK (btrim(name) <> ''),
    description    text NOT NULL DEFAULT '',
    -- 触发条件只**记录**，不订阅、不轮询、不监听。
    trigger_kind   text NOT NULL
                   CHECK (trigger_kind IN ('manual', 'schedule', 'event', 'webhook')),
    trigger_detail text NOT NULL DEFAULT '',
    -- 目标 Action 的 ID 与版本按 action.Definition 的字面值存。
    -- **刻意不加外键、也不校验它是否已注册**：注册表是进程内的运行期对象，
    -- 不在数据库里；在库层假装能校验只会在 Action 改版本那天变成一条挡住
    -- 登记的假约束。是否指向一个真实存在的 Action，由列表端点在读的时候
    -- 与注册表对照后如实标出。
    target_action_id      text NOT NULL CHECK (btrim(target_action_id) <> ''),
    target_action_version text NOT NULL CHECK (btrim(target_action_version) <> ''),
    status         text NOT NULL DEFAULT 'draft'
                   CHECK (status IN ('draft', 'registered', 'disabled')),
    notes          text NOT NULL DEFAULT '',
    environment    text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    created_at     timestamptz NOT NULL DEFAULT now(),
    created_by     text NOT NULL CHECK (btrim(created_by) <> ''),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by     text NOT NULL CHECK (btrim(updated_by) <> ''),

    CONSTRAINT automation_rule_unique_name UNIQUE (environment, name)
);

COMMENT ON TABLE core.automation_rule IS
    '自动化规则登记簿（XM-EXT-INTEGRATION）。**没有执行器**：本表任何状态都不会导致 Action 被自动执行（ADMIN-IA §5.4.1）。';
COMMENT ON COLUMN core.automation_rule.status IS
    'draft/registered/disabled 描述这条登记的编写进度，不是运行状态——三个取值都不会让任何 Action 跑起来。';
COMMENT ON COLUMN core.automation_rule.target_action_id IS
    '规则**打算**调用的 Action；没有任何代码读它去执行。是否指向已注册的 Action 由读取端点如实标出。';

CREATE INDEX automation_rule_environment_created_idx
    ON core.automation_rule (environment, created_at DESC);
