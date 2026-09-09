-- XM-CARD-VISIBILITY：按账号暂停卡片同步的开关。
--
-- **为什么进表而不进环境变量**：这是运营在出事当下要改的开关——某个账号被
-- 上游拒到底、或者正在上游后台做维护，此刻要能立刻把它摘出同步循环。做成
-- 环境变量意味着改一次要重启 worker，而重启 worker 恰好是事故当中最不该做的
-- 动作。env 只留「会不会花真钱」那一类（XM_CARDS_MODE 的 off/fake/real）。
--
-- 与 000034（提现额度从 env 搬进库）同一条先例，连理由都一样：约束力从来
-- 不是来自「改不了」，而是来自**改它要留痕、而且权限与用它的权限分开**。
-- 写路径只经 cards.account_sync.pause / .resume 两个 Action（宪法条款 2 /
-- ADR-003），权限 card.manage，仅人类身份，reason 必填。
--
-- **行缺席 = 未暂停。** 不为每个账号预建行：账号清单来自运行时配置
-- （XM_CARDS_ACCOUNTS），一个账号被摘掉之后留下的那一行不该继续参与判断。
--
-- paused 仍然显式存布尔而不是靠行的存在与否表达：resume 之后要保留 reason
-- 与时间戳，供事后回答「上次为什么停过、停了多久」——直接删行的话，
-- 「这个账号昨天停过两小时」这件事就没有任何地方记得。
--
-- created_at/updated_at 不带 DEFAULT now()，由应用层注入的时钟显式写入
-- ——本仓库既有约定（见 000026 文件头第 1 条），不是漏写。时间一律
-- timestamptz 存 UTC（宪法条款 14）。
--
-- 本迁移不改 contracts/database/role-policy.v1.json：cards schema 的对象
-- 一个都不在那份策略里（该策略只覆盖 core/action/audit/ops/alerts/finance/
-- public/ui），而 dbroles 的策略是审批门控的，不该顺手扩。这是一处既有欠账，
-- 已记在切片 handoff 里，不在本片关闭。
CREATE TABLE IF NOT EXISTS cards.account_sync_pause (
    environment TEXT NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,

    -- account 与 cards.infini_card.account 同一口径（平台侧稳定标识，
    -- 如 MAIN / LINFENG）。没有外键可指：账号清单在运行时配置里，不在库里。
    account     TEXT NOT NULL,

    paused      BOOLEAN NOT NULL,

    -- reason 是运营填的自由文本，同时进审计摘要与管理端的暂停徽标。
    -- 非空由 Action 的 Schema 强制（resume 时允许为空串）。
    reason      TEXT NOT NULL DEFAULT '',

    -- paused_by 是 Principal id；与审计事件互为对照。
    -- 与 000034 的 updated_by 同一条理由：为一个徽标去翻审计太贵。
    paused_by   TEXT NOT NULL DEFAULT '',

    -- expires_at 预留给「到点自动恢复」，本片不实现，恒为 NULL。
    --
    -- 不实现的理由：自动恢复会在没人看着的时候把同步重新打向一个仍然拒绝
    -- 我们的上游，而这个切片存在的意义正是不要让失败反复且无人知晓。
    -- 但列先建好，将来真要做时不必再来一次迁移。
    -- NULL = 不自动恢复（当前唯一取值）。
    expires_at  TIMESTAMPTZ,

    paused_at   TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL,

    PRIMARY KEY (environment, account)
);

COMMENT ON TABLE cards.account_sync_pause IS
    '按账号暂停 card_sync 的开关；行缺席 = 未暂停。写路径只经 cards.account_sync.* Action。';
