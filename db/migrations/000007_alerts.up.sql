-- XM-0033 告警中心（规格 §9.3 告警生命周期、§9.4 告警渠道）。forward-only。
--
-- 本迁移建两张表：alerts.alert（告警实例）与 alerts.alert_silence（静默窗口）。
-- 规则本身**不入库**：Foundation-A 的第一批规则写死在
-- internal/platform/alerts/rules.go 里。理由是规则要跑代码（读 ops 观测、
-- 解 value_json、算持续时间），把它做成数据就得同时发明一门表达式语言和它的
-- 沙箱——那是 Foundation-B 的事。规则进库之前，改规则是一次带 PR 与测试的
-- 代码变更，这在 Foundation-A 反而是更强的保障。
CREATE SCHEMA IF NOT EXISTS alerts;

CREATE TABLE alerts.alert (
    id                uuid PRIMARY KEY,
    -- rule_key 指回 rules.go 里那条规则。它是静默窗口的匹配键，也是运维
    -- 「这类告警又来了」的分组依据，因此必须稳定：改名等于让历史告警与
    -- 已存在的静默窗口失配。
    rule_key          text NOT NULL,
    -- dedup_key 是规格 §9.3 要求的「去重键」。同一个真实问题在多轮评估里
    -- 必须收敛成**一条**告警 + 递增的 fire_count，而不是每分钟新开一条。
    dedup_key         text NOT NULL,
    severity          text NOT NULL,
    status            text NOT NULL,
    title             text NOT NULL,
    detail            text NOT NULL DEFAULT '',
    -- 环境是显式外键（宪法 15 条）：不允许告警落在一个不存在的环境上，
    -- 更不允许 staging 的告警混进生产列表。
    environment       text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    -- opened_at 是「首次发现」，last_seen_at 是「最近一次仍然满足条件」。
    -- 两个都留是有意的：只留其一就无法回答「这个问题持续了多久」——
    -- 而那正是判断要不要升级处理的第一个依据。
    opened_at         timestamptz NOT NULL,
    acknowledged_at   timestamptz,
    resolved_at       timestamptz,
    last_seen_at      timestamptz NOT NULL,
    -- fire_count 是这条告警被重复命中的次数（去重合并掉的那些）。
    -- 它是「抖动一次」与「持续两小时」的区分依据，不能省。
    fire_count        integer NOT NULL DEFAULT 1,
    -- source_metric_key 让告警能一路指回 ops.metric_observation 里那条指标。
    -- 渠道类规则（余额、token）也填它所依据的聚合指标键，空串表示与指标无关。
    source_metric_key text NOT NULL DEFAULT '',
    -- 投递状态是规格 §9.3 明文要求的「通知投递状态」。它与 status 正交：
    -- 一条 OPEN 的告警完全可能还没投递出去（渠道没配、上游 429），
    -- 而那恰恰是最危险的组合——有人以为「告警会通知我」。
    notify_status     text NOT NULL DEFAULT 'pending',
    notify_error      text NOT NULL DEFAULT '',
    notified_at       timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT alert_severity_allowed
        CHECK (severity IN ('info', 'warning', 'critical')),
    CONSTRAINT alert_status_allowed
        CHECK (status IN ('OPEN', 'ACKNOWLEDGED', 'SILENCED', 'RESOLVED', 'REOPENED')),
    CONSTRAINT alert_notify_status_allowed
        CHECK (notify_status IN ('pending', 'delivered', 'failed')),
    CONSTRAINT alert_rule_key_format
        CHECK (rule_key ~ '^[a-z0-9][a-z0-9_.-]{0,127}$'),
    CONSTRAINT alert_dedup_key_not_blank
        CHECK (btrim(dedup_key) <> ''),
    CONSTRAINT alert_title_not_blank
        CHECK (btrim(title) <> ''),
    CONSTRAINT alert_fire_count_positive
        CHECK (fire_count >= 1),
    -- 投递失败必须有原因，成功必须没有——与 ops.metric_observation 同一条
    -- 规则：让「静默失败」在库层不可表示。一条 failed 而没有 notify_error
    -- 的告警会让运维无从判断是重试就能好还是配置错了。
    CONSTRAINT alert_notify_error_consistency
        CHECK ((notify_status = 'failed') = (notify_error <> '')),
    -- 已投递必须有投递时刻。「delivered 但不知道什么时候」在事后复盘
    -- （告警延迟了多久才到人手上）时等于没投递记录。
    CONSTRAINT alert_notified_at_consistency
        CHECK ((notify_status = 'delivered') = (notified_at IS NOT NULL)),
    -- 终态必须有终态时刻，反之亦然。
    CONSTRAINT alert_resolved_at_consistency
        CHECK ((status = 'RESOLVED') = (resolved_at IS NOT NULL))
);

-- 去重的库层保障（规格 §9.3「去重」）。
--
-- 覆盖**除 RESOLVED 外的四个状态**，而不只是 OPEN/ACKNOWLEDGED/REOPENED：
-- SILENCED 是一个**活着**的状态，不是终态。静默窗口过期后那条告警要「转回
-- OPEN 并投递」（见 alerts.Store.UpsertByDedup），说明下一轮评估必须能按
-- dedup_key 找到它。如果 SILENCED 不在唯一索引里，同一个被静默的问题会
-- 每 60 秒新插一行——一天 1440 条僵尸告警，而且窗口一过会同时炸出来。
-- 这是本迁移相对任务书原始描述的一处**有意收紧**，理由记在
-- docs/modules/alerts/README.md。
--
-- RESOLVED 必须留在索引外：同一个问题反复发生、反复恢复是常态，
-- 历史上的每一次都要作为独立的一行留下来（那正是「这周炸了七次」的证据）。
--
-- 显式列举状态而不是写 status <> 'RESOLVED'：将来若新增一个状态，
-- 不写就默认**不**进唯一集合，需要有人显式决定——比默默被卷进来安全。
CREATE UNIQUE INDEX alert_active_dedup_key
    ON alerts.alert (dedup_key)
    WHERE status IN ('OPEN', 'ACKNOWLEDGED', 'SILENCED', 'REOPENED');

-- 告警页的唯一查询形态就是「某环境、某状态集合、按时间倒序」。
CREATE INDEX alert_environment_status_idx
    ON alerts.alert (environment, status);
-- 「最近的告警」列表（含已解决）按环境 + 时间倒序取前 N。
CREATE INDEX alert_environment_last_seen_idx
    ON alerts.alert (environment, last_seen_at DESC);

-- 「这个问题以前发生过并且被解决了吗」——新建告警时用它判定该开 OPEN 还是
-- REOPENED（规格 §9.3「重新打开」）。RESOLVED 的行不在上面那条唯一索引里，
-- 没有这条索引就只能全表扫；而这张表的 RESOLVED 行会随时间单调增长，
-- 正是最不该留全表扫的地方。
CREATE INDEX alert_dedup_key_resolved_idx
    ON alerts.alert (dedup_key, resolved_at DESC)
    WHERE status = 'RESOLVED';

CREATE TABLE alerts.alert_silence (
    id          uuid PRIMARY KEY,
    -- rule_key 可空（空串）= 全局静默：该环境下所有规则都不投递。
    -- 用空串而不是 NULL：匹配条件写成 (rule_key = '' OR rule_key = $1)
    -- 比 IS NULL 分支少一种三值逻辑的坑，索引也用得上。
    rule_key    text NOT NULL DEFAULT '',
    environment text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    -- reason 非空是硬约束（规格 §9.3「静默策略」）。静默是**主动让告警闭嘴**，
    -- 没有理由的静默在事后复盘时与「有人手滑」不可区分。库层挡住比代码层
    -- 挡住可靠——代码路径会长出第二条，表约束不会。
    reason      text NOT NULL,
    starts_at   timestamptz NOT NULL,
    ends_at     timestamptz NOT NULL,
    created_by  text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT alert_silence_reason_not_blank
        CHECK (btrim(reason) <> ''),
    CONSTRAINT alert_silence_created_by_not_blank
        CHECK (btrim(created_by) <> ''),
    -- 窗口必须有正长度。ends_at <= starts_at 的窗口不会静默任何东西，
    -- 但会让创建者以为已经静默了——一个静默失败的静默。
    CONSTRAINT alert_silence_window_ordered
        CHECK (ends_at > starts_at),
    CONSTRAINT alert_silence_rule_key_format
        CHECK (rule_key = '' OR rule_key ~ '^[a-z0-9][a-z0-9_.-]{0,127}$')
);

-- 每轮评估都要问「此刻这个环境有哪些生效的静默窗口」：
-- 等值过滤环境 + 按 ends_at 找还没过期的。
CREATE INDEX alert_silence_environment_ends_at_idx
    ON alerts.alert_silence (environment, ends_at DESC);
