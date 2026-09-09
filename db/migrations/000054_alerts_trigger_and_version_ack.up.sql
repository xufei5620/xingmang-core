-- XM-OPS-TRUTH 子片 B：把「评估轮数」与「触发次数」分开，并给「已核对的上游
-- 版本」一个可以落账的地方。forward-only（规格 §5.7）。
--
-- 需求来源是 docs/handoffs/PLATFORM-ALERT-STORM-2026-09-08.md 的两条：
--   §二「669 不是发生了 669 次」——那是评估轮数（每 60 秒重算一遍条件，
--      成立就加 1），界面上却写着「触发 669 次」；
--   §二「系统里没有『这个版本我看过了』这个动作」——版本告警唯一的结束方式
--      是旧探测样本被挤出回看窗口后自己消失（约 16h40m），不是因为有人核对了。

-- 一、alerts.alert 加两列。**都可空、都不回填**。
--
-- 库里已有的行不知道自己被真正触发过几次，也不知道第一次是什么时候开的。
-- 填 0 或 now() 会造出一个看起来像真答案的假答案（宪法 12 条）。
-- NULL 的意思在这两列上是同一个：「本列上线之前就存在的旧行」。
ALTER TABLE alerts.alert
    ADD COLUMN trigger_count   integer,
    ADD COLUMN first_opened_at timestamptz;

ALTER TABLE alerts.alert
    ADD CONSTRAINT alert_trigger_count_positive
        CHECK (trigger_count IS NULL OR trigger_count >= 1);

COMMENT ON COLUMN alerts.alert.trigger_count IS
    '真正「触发」的次数：新开 +1、从静默转回 OPEN 重新投递 +1；持续命中不加。与 fire_count（评估轮数）是两个量。NULL＝本列上线前的旧行。';
COMMENT ON COLUMN alerts.alert.first_opened_at IS
    '这个去重键第一次开的时刻，跨 RESOLVED→REOPENED 继承（限 24 小时复发窗口内），不因抖动归零。NULL＝本列上线前的旧行，读取侧按 opened_at 兜底。';

-- 二、alerts.upstream_version_ack：「这条上游的这个版本，我核对过了」。
--
-- 为什么是新表而不是复用 alerts.alert_silence：静默的三条性质全都反着——
-- 它是限时窗口（上限 7 天）、按 rule_key 匹配、只挡投递不挡命中。用它实现的话，
-- 核对过的版本会在窗口到期后自己回来，等于把一件已经做完的事做成一个会复发的
-- 提醒，而且既有 OPEN 告警不会被解决。
--
-- 为什么不加列到 alerts.alert：核对的对象是**上游**而不是某一条告警行。挂在行上
-- 会随 PruneResolvedAlerts（db/queries/alerts.sql 的保留期清理）被删掉——那个失效
-- 没有任何人做错任何事，也不留任何痕迹。
CREATE TABLE alerts.upstream_version_ack (
    -- 环境是显式外键（宪法 15 条），与 alerts.alert 同一条纪律。
    environment     text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    -- metric_key 指回 ops.metric_observation 里那条**探测**指标（如
    -- sub2api.connector.health）。它而不是 source 做主键的一半：source 是连接器
    -- 自报的展示字段，metric_key 才是平台自己注册过的稳定标识
    -- （ops.KnownMetricKey 认得它）。
    metric_key      text NOT NULL,
    version         text NOT NULL,
    -- source 由服务端从当轮观测里读出后写入，**不来自调用方参数**
    -- （宪法 15 条：不许调用方自称身份）。留它是为了让审计与列表能直接说
    -- 「核对的是 sub2api-prod」，不必再去连一次观测表。
    source          text NOT NULL DEFAULT '',
    acknowledged_by text NOT NULL,
    acknowledged_at timestamptz NOT NULL,
    note            text NOT NULL DEFAULT '',
    -- 主键不含 version：「已核对的版本」对一条上游只有一个**当前**值，
    -- 新的核对覆盖旧的，历史留在审计链里（每次执行都写 audit_event）。
    -- 带上 version 的话，表会随每次升级无限长，而规则侧要问的问题
    -- （「现在这个版本核对过没有」）反而要多一次排序。
    PRIMARY KEY (environment, metric_key),
    CONSTRAINT upstream_version_ack_metric_key_format
        CHECK (metric_key ~ '^[a-z0-9][a-z0-9_.-]{0,127}$'),
    CONSTRAINT upstream_version_ack_version_not_blank
        CHECK (btrim(version) <> ''),
    CONSTRAINT upstream_version_ack_by_not_blank
        CHECK (btrim(acknowledged_by) <> '')
);

COMMENT ON TABLE alerts.upstream_version_ack IS
    '已核对的上游自报版本。upstream.version.changed 规则命中时若观测版本等于这里记着的版本，则不再命中，既有 OPEN 告警在下一轮被通用恢复逻辑转 RESOLVED。';
