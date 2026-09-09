-- XM-EXT-APP：前端应用登记簿 + 发布记录簿（ADMIN-IA §5.4 的 2026-09-08 裁定
-- 变更：`应用与配置` 从只读蓝图改为真建）。forward-only（规格 §5.7）。
--
-- 「应用」在这里的定义是**平台自己纳管的前端站点**（ADMIN-IA §5.4.1）：
-- 我们自己部署的那些前端（admin-web、console 这类）。
--   * **不是**被管平台（Sub2API / NewAPI）的前端——那些是被管系统，仍然只在
--     `core.service` 里；
--   * **不是**通用低代码页面搭建器的「应用」——平台没有页面搭建器。
--
-- 为什么另起一张表而不是扩 `core.service`（登记簿唯一入口本该尽量少）：
--   1. `core.service` 的行会**直接出现在管理端侧栏的「平台」段**——
--      `web/apps/admin-web/src/lib/platforms.ts` 的 `groupPlatforms()` 有一条
--      「登记即出现」规则：目录里没有、注册表里有的 `service_type` 会被自动
--      补成一个平台条目。把 admin-web 登记成 Service，侧栏就会多出一个叫
--      `admin-web` 的「平台」，除非再往 `NON_PLATFORM_SERVICE_TYPES` 排除名单
--      里加一条——那份名单存在的理由恰恰是「我们认识、并且已经确定它不是平台」，
--      每登记一个前端就要改一次前端代码，等于登记簿不能自助登记；
--   2. `core.service` 是 Connector / Connection 的挂载点：一个 Service 意味着
--      「平台会带着凭据去连它」（ADR-004 / ADR-014）。我们自己部署的前端站点
--      没有 Connector，也没有凭据要挂——让它成为 Service 会让「有 Service 就
--      该有 Connection」这条判断在整个注册表上不再成立；
--   3. 字段对不上：Service 的动态事实是采集水位（`source_watermark` /
--      `observed_at`，由 `registry.service.observe` 回写），前端站点的动态事实
--      是「现在线上跑的是哪个版本」，两者不是同一类东西。
--
-- 与 `internal/platform/server`（服务器登记簿）同一套纪律：
--   * 写路径全部经 `extapp.*` Action（宪法 2 条 / ADR-003），本迁移只建表与
--     库层不变量，不写任何数据；
--   * environment 显式（宪法 15 条），外键指向 `core.environment`；
--   * 纯登记：不探测域名、不探测证书、不请求站点、不触发任何发布。

-- 前端应用登记簿。一行 = 一个我们自己部署的前端站点（在一个环境里）。
CREATE TABLE core.ext_app (
    id             uuid PRIMARY KEY,

    -- app_key 是稳定的机器可读标识（admin-web、console……），同时是人在
    -- 运行手册与部署脚本里指代这个站点时用的词。用与 `core.service.instance_id`
    -- 同一条标识符规则，好让两张表里的键长得一样、能互相对照着读。
    app_key        text NOT NULL,
    -- display_name 是侧栏与表格里显示的名字（「运营后台」这类），可以是中文。
    display_name   text NOT NULL,

    -- primary_domain 是**主机名**（admin.solov.cc），不是 URL：
    -- 下面的 CHECK 因此顺带挡掉了 `https://user:pass@host/...` 这类把凭据写进
    -- 地址的形态（`@`、`:`、`/`、`?` 一个都过不了主机名正则）——同
    -- `registry.validateEndpointCarriesNoCredential` 要挡的那条泄漏链，
    -- 只是这里靠字段形态本身就关掉了。NULL = 还没有域名（规划中的站点）。
    primary_domain text,

    -- auth_mode 是**登录方式**，三个取值逐字取自 deploy/docker/web-app-config.sh
    -- 的 XM_WEB_AUTH_MODE（dev-header / oidc / local）——那是这套前端运行时
    -- 真正认得的三个值，不是这里现编的枚举。NULL = 未登记。
    auth_mode      text,

    owner          text NOT NULL,

    -- status 是**登记簿口径**，不是探活结果：本表不请求任何站点。
    -- 三档与 core.server_asset 一致（planned 规划中 / active 在线 / retired 已下线）。
    status         text NOT NULL DEFAULT 'active',

    notes          text,

    environment    text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,

    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT ext_app_key_format
        CHECK (app_key ~ '^[a-z0-9][a-z0-9-]{0,63}$'),
    CONSTRAINT ext_app_display_name_not_blank CHECK (btrim(display_name) <> ''),
    CONSTRAINT ext_app_owner_not_blank CHECK (btrim(owner) <> ''),
    CONSTRAINT ext_app_status_allowed
        CHECK (status IN ('active', 'planned', 'retired')),
    CONSTRAINT ext_app_auth_mode_allowed
        CHECK (auth_mode IS NULL OR auth_mode IN ('dev-header', 'oidc', 'local')),
    -- 主机名：小写字母数字与连字符，至少一个点，段首段尾不是连字符。
    CONSTRAINT ext_app_primary_domain_format
        CHECK (primary_domain IS NULL
               OR primary_domain ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$')
);

-- 同一环境下同一个应用键只登记一次：重复登记会让发布记录被拆到两行上，
-- 「当前版本」于是取决于你点开的是哪一行。
CREATE UNIQUE INDEX ext_app_env_key_key ON core.ext_app (environment, app_key);

-- 同一环境下一个域名只能属于一个应用。两个应用登记同一个域名，说明其中一条
-- 记错了——而记错的那条会让「这个域名归谁负责」有两个答案。
CREATE UNIQUE INDEX ext_app_env_domain_key
    ON core.ext_app (environment, primary_domain) WHERE primary_domain IS NOT NULL;

CREATE INDEX ext_app_env_status_idx ON core.ext_app (environment, status);

-- 发布记录簿。一行 = 「某个应用在某个时刻上了某个版本」这一件**已经发生**的事。
--
-- 这张表**不驱动发布**：平台没有、也不会有发布 / 回滚端点。发布与迁移是
-- Platform Lifecycle Operation（宪法 2、3 条 / ADR-003）——走版本化脚本 +
-- 人工批准，不经 Action 通道。这里唯一的写入是「把已经发生的事记下来」，
-- 那是一次平台配置写操作，因此**必须**经 Action（宪法 2 条），而它记录的
-- 那次发布本身不经 Action。两件事不要混。
--
-- 不存 environment 列：环境挂在父行（ext_app）上，同
-- `core.server_service_note` 挂在 `server_asset` 下的形状——子表自称一个环境，
-- 就等于给了它一条与父行不一致的可能。
CREATE TABLE core.ext_app_release (
    id          uuid PRIMARY KEY,
    app_id      uuid NOT NULL REFERENCES core.ext_app (id) ON DELETE RESTRICT,

    -- version 是这次发布的版本 / 构建标签，自由文本：部署脚本今天把
    -- BUILD_VERSION 写成环境名（见 deploy/scripts/deploy-local.sh），
    -- 强制语义化版本只会逼人编一个假版本号。
    version     text NOT NULL,
    -- commit_sha 可空：不是每次发布都取得到提交号。7~40 位小写十六进制，
    -- 短 SHA 与全长 SHA 都收。
    commit_sha  text,

    -- kind 区分「发布」与「回滚」。回滚同样是一次「让某个版本上线」，
    -- 所以 version 记的是**回滚到的那个版本**，不是被回滚掉的那个。
    kind        text NOT NULL DEFAULT 'deploy',

    -- released_at 是**发布真正发生的时刻**（可以是过去），不是登记时刻；
    -- 登记时刻是 created_at。两者分开，否则补记历史发布会把时间线搅乱。
    released_at timestamptz NOT NULL,
    -- released_by 是执行那次发布的人，可能不是来登记的人——登记者由审计链
    -- 记录（Action 的 actor），这一列记的是发布者本人。
    released_by text NOT NULL,

    notes       text,

    created_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT ext_app_release_version_not_blank CHECK (btrim(version) <> ''),
    CONSTRAINT ext_app_release_released_by_not_blank CHECK (btrim(released_by) <> ''),
    CONSTRAINT ext_app_release_kind_allowed CHECK (kind IN ('deploy', 'rollback')),
    CONSTRAINT ext_app_release_commit_format
        CHECK (commit_sha IS NULL OR commit_sha ~ '^[0-9a-f]{7,40}$')
);

-- 同一个应用、同一个版本、同一个时刻只记一次：重复提交同一条记录（表单双击、
-- 补记时抄了两遍）会让「当前版本」的判定多出一条并列的行。
CREATE UNIQUE INDEX ext_app_release_app_version_at_key
    ON core.ext_app_release (app_id, version, released_at);

-- 「这个应用现在跑的是哪个版本」= 按 released_at 倒序取第一条，列表页每一行
-- 都要问一次，走这个索引。
CREATE INDEX ext_app_release_app_at_idx
    ON core.ext_app_release (app_id, released_at DESC);
