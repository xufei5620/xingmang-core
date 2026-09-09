-- XM-EXT-PUBLISHING：内容发布（`/ext/publishing`）第一层的落库面。
--
-- 裁定见 docs/architecture/ADMIN-IA.md §5.4.1（2026-09-08 产品负责人推翻了
-- 「扩展能力四页只读蓝图、不得提前建后端」在这一页上的适用）。
--
-- 本迁移建的是「内容的生命周期在平台内是真的」这一层：草稿与版本、素材引用、
-- 排期、渠道与账号登记、发布记录。**平台今天没有任何出站投递器**——这张
-- publish_record 记的是「谁在什么时候请求把哪一版发到哪个渠道、审批走到哪一步」，
-- 不是「已经发出去了」。这一点在 result 的取值上就写死了，见该列的注释。
--
-- 站内公告刻意**不在这里**：Sub2API/NewAPI 的公告接口都要求平台向它们发写请求，
-- 被 ADR-018 四道只读闸与 ADR-021「适用范围仅限平台作为客户购买服务的供应商」
-- 排除；不建一个走不通的渠道类型，免得有人配了以为能发。

CREATE SCHEMA IF NOT EXISTS publishing;

COMMENT ON SCHEMA publishing IS
    '内容发布（XM-EXT-PUBLISHING）。第一层：草稿/版本/素材/排期/渠道登记/发布记录。出站投递器尚未存在。';

-- ---------------------------------------------------------------------------
-- 渠道与账号
-- ---------------------------------------------------------------------------

CREATE TABLE publishing.channel (
    id              uuid PRIMARY KEY,
    environment     text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    -- 闭集。**没有 announcement / sub2api / newapi**：站内公告这条路今天走不通
    -- （见文件头），列一个发不出去的取值等于在页面上摆一个假入口。
    -- 'other' 是给「X 以外的其它社交平台」的逃生口，它同样没有投递器。
    platform        text NOT NULL CHECK (platform IN ('x', 'telegram', 'other')),
    -- 账号标识（如 @xingmang）。与 platform 一起构成一个渠道的身份。
    handle          text NOT NULL CHECK (btrim(handle) <> ''),
    display_name    text NOT NULL DEFAULT '',
    purpose         text NOT NULL DEFAULT '',
    -- **凭据只存引用**（宪法 7 条 / ADR-014）。形如 secret://<scope>/<name>，
    -- 正则与 internal/platform/secrets.ParseCredentialRef 的两段规则逐字对齐：
    -- 库层多这一道是因为这张表是**运营自己填**的，一个手滑粘进来的 Bearer
    -- token 会当场变成一条永久的明文泄漏。空串表示还没登记引用。
    credential_ref  text NOT NULL DEFAULT ''
                    CHECK (credential_ref = ''
                           OR credential_ref ~ '^secret://[a-z0-9][a-z0-9-]{0,63}/[a-z0-9][a-z0-9-]{0,63}$'),
    status          text NOT NULL DEFAULT 'ACTIVE'
                    CHECK (status IN ('ACTIVE', 'PAUSED', 'RETIRED')),
    note            text NOT NULL DEFAULT '',
    created_by      text NOT NULL CHECK (btrim(created_by) <> ''),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    -- 同一环境下一个平台的一个账号只登记一次；改用 upsert 而不是建第二条。
    CONSTRAINT channel_identity_unique UNIQUE (environment, platform, handle)
);

COMMENT ON TABLE publishing.channel IS
    '对外发布渠道与账号登记。凭据只存 CredentialRef，明文永不入库（宪法 7 条）。';
COMMENT ON COLUMN publishing.channel.credential_ref IS
    'secret://<scope>/<name>；库层正则挡住直接粘进来的明文 token。';

CREATE INDEX channel_environment_status_idx
    ON publishing.channel (environment, status, platform, handle);

-- ---------------------------------------------------------------------------
-- 草稿与版本
-- ---------------------------------------------------------------------------

CREATE TABLE publishing.draft (
    id              uuid PRIMARY KEY,
    environment     text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    title           text NOT NULL CHECK (btrim(title) <> ''),
    body            text NOT NULL DEFAULT '',
    -- 排期时间。NULL = 还没排期（状态必然是 DRAFT，见下面的 CHECK）。
    scheduled_at    timestamptz NULL,
    -- 三态而已：**没有 PUBLISHED**。草稿不会因为提交了发布就变成「已发布」——
    -- 今天没有任何东西发得出去，多一个终态就是多一处可以骗人的地方。
    -- 「这一版发到哪儿了、走到哪一步」全部在 publish_record 里回答。
    status          text NOT NULL DEFAULT 'DRAFT'
                    CHECK (status IN ('DRAFT', 'SCHEDULED', 'ARCHIVED')),
    -- 当前版本号，与 draft_revision 里最大的 version 一致。冗余一列是为了让
    -- 「发布记录钉的是哪一版」能在不 join 的情况下读出来。
    current_version integer NOT NULL DEFAULT 1 CHECK (current_version >= 1),
    created_by      text NOT NULL CHECK (btrim(created_by) <> ''),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    -- 排期状态必须真的有排期时间。一条 SCHEDULED 却没有时间的草稿会让内容
    -- 日历显示一个落在「未知」格里的条目，运营看不出它到底排没排。
    CONSTRAINT draft_scheduled_needs_time CHECK (
        (status = 'SCHEDULED') = (scheduled_at IS NOT NULL)
    )
);

COMMENT ON TABLE publishing.draft IS
    '内容草稿。状态只有 DRAFT/SCHEDULED/ARCHIVED——没有 PUBLISHED，因为平台今天发不出去。';

CREATE INDEX draft_environment_schedule_idx
    ON publishing.draft (environment, scheduled_at)
    WHERE scheduled_at IS NOT NULL;
CREATE INDEX draft_environment_status_idx
    ON publishing.draft (environment, status, updated_at DESC);

-- 每次改动落一条不可变修订。**不是软删的历史表，是审计事实**：发布记录钉住
-- 的是某一个 (draft_id, version)，改了草稿不该让已经提交审批的那一版变样。
CREATE TABLE publishing.draft_revision (
    draft_id     uuid NOT NULL REFERENCES publishing.draft (id) ON DELETE CASCADE,
    version      integer NOT NULL CHECK (version >= 1),
    title        text NOT NULL,
    body         text NOT NULL,
    scheduled_at timestamptz NULL,
    -- 这一版改了什么，由改的人自己写。空串允许（第一版通常没什么可说的）。
    note         text NOT NULL DEFAULT '',
    created_by   text NOT NULL CHECK (btrim(created_by) <> ''),
    created_at   timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (draft_id, version)
);

COMMENT ON TABLE publishing.draft_revision IS
    '草稿的不可变修订。发布记录钉 (draft_id, version)，事后改草稿不改写已提交的那一版。';

-- ---------------------------------------------------------------------------
-- 素材
-- ---------------------------------------------------------------------------

-- 素材是**引用**，不是上传。平台没有对象存储，也不该为了这一页顺手建一个：
-- 存一条外部地址 + 一句说明，比建一套没人验证过保留期与配额的文件面诚实。
CREATE TABLE publishing.asset (
    id          uuid PRIMARY KEY,
    environment text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    name        text NOT NULL CHECK (btrim(name) <> ''),
    kind        text NOT NULL CHECK (kind IN ('image', 'video', 'link')),
    -- 外部地址。要求 https：素材地址会被贴进对外内容里，允许 http 等于允许
    -- 把一条明文链接发到公网（与 alerts.WebhookNotifier 同一条理由）。
    uri         text NOT NULL CHECK (uri ~ '^https://[^[:space:]]+$'),
    note        text NOT NULL DEFAULT '',
    created_by  text NOT NULL CHECK (btrim(created_by) <> ''),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT asset_name_unique UNIQUE (environment, name)
);

COMMENT ON TABLE publishing.asset IS
    '素材引用（不是上传）。uri 强制 https：素材地址会随对外内容一起公开。';

CREATE TABLE publishing.draft_asset (
    draft_id uuid NOT NULL REFERENCES publishing.draft (id) ON DELETE CASCADE,
    asset_id uuid NOT NULL REFERENCES publishing.asset (id) ON DELETE RESTRICT,
    position integer NOT NULL CHECK (position >= 0),

    PRIMARY KEY (draft_id, asset_id)
);

COMMENT ON TABLE publishing.draft_asset IS
    '草稿引用的素材。asset 侧是 RESTRICT：还被草稿引着的素材不能删。';

CREATE INDEX draft_asset_asset_idx ON publishing.draft_asset (asset_id);

-- ---------------------------------------------------------------------------
-- 发布记录
-- ---------------------------------------------------------------------------

CREATE TABLE publishing.publish_record (
    id            uuid PRIMARY KEY,
    environment   text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    draft_id      uuid NOT NULL REFERENCES publishing.draft (id) ON DELETE RESTRICT,
    -- 钉住版本：审批人批的是这一版的正文，事后改草稿不改写这条记录。
    draft_version integer NOT NULL CHECK (draft_version >= 1),
    channel_id    uuid NOT NULL REFERENCES publishing.channel (id) ON DELETE RESTRICT,
    scheduled_at  timestamptz NULL,
    requested_by  text NOT NULL CHECK (btrim(requested_by) <> ''),
    -- **刻意没有 approval_id 列。**
    --
    -- 这条记录必然是一张 L3 审批单批准后执行出来的，但 Handler 拿不到单号：
    -- `action.Kernel.ExecuteApproved` 不把 approvalID 放进 Handler 的 context
    -- （它只进审计事件的 resource_id）。为此改内核是一次共享热点文件的改动，
    -- 不值当。
    --
    -- 链路仍然是完整的，只是绕一跳：
    --   core.approval_request.execution_run_id → action.action_run.id
    --   → audit.audit_event.action_run_id（resource_type='approval_request'）
    -- 加一列永远为空的 approval_id 只会让人以为这里有数据可读。
    -- **闭集只有一个取值**，这是刻意的。
    --
    -- 平台今天没有任何出站投递器：没有 X 的 Connector、没有 OAuth、没有速率
    -- 限制、没有失败重试（第二层，见 docs/handoffs/slices/XM-EXT-PUBLISHING.md
    -- 的 follow_ups）。让库层只认 'NOT_DELIVERED'，是为了让「接上投递器」
    -- 这件事**必须**经过一次显式迁移与一次显式评审，而不是某天有人往枚举里
    -- 补两个词就悄悄成立了。
    result        text NOT NULL DEFAULT 'NOT_DELIVERED'
                  CHECK (result IN ('NOT_DELIVERED')),
    -- 平台返回编号 / 投递时刻：都留列，因为它们是第二层的落点；今天必须为空，
    -- 由下面的 CHECK 保证——一条带着编号的「未投递」记录会让人以为发出去了。
    external_ref  text NOT NULL DEFAULT '',
    delivered_at  timestamptz NULL,
    detail        text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT publish_record_not_delivered_shape CHECK (
        result <> 'NOT_DELIVERED'
        OR (external_ref = '' AND delivered_at IS NULL)
    )
);

COMMENT ON TABLE publishing.publish_record IS
    '发布记录。result 今天只有 NOT_DELIVERED——平台没有出站投递器，这张表记的是请求与审批，不是「已发出」。';
COMMENT ON COLUMN publishing.publish_record.result IS
    '闭集且只有一个取值；接上出站投递器需要一次显式迁移与评审，不能靠悄悄补枚举。';

CREATE INDEX publish_record_environment_created_idx
    ON publishing.publish_record (environment, created_at DESC);
CREATE INDEX publish_record_draft_idx
    ON publishing.publish_record (draft_id, created_at DESC);
CREATE INDEX publish_record_channel_idx
    ON publishing.publish_record (channel_id, created_at DESC);
