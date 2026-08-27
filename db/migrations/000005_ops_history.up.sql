-- XM-0024 指标历史样本（规格 §9.1 的时序补充）。forward-only。
--
-- 为什么单独一张表，而不是往 ops.metric_observation 里塞一个数组列：
-- 那张表回答的是「现在是什么」，每个 (metric_key, environment) 只有一行、
-- 被整行覆盖。历史序列是**追加**语义，两者放一起会让每次同步都要读出、
-- 反序列化、追加、再整行写回一个越来越大的 jsonb——写放大随保留期线性增长，
-- 而且并发写会互相覆盖。追加型独立表让每次写只是一条 INSERT。
CREATE TABLE ops.metric_observation_sample (
    -- 自增主键：样本没有跨表引用，也不需要客户端预先生成 ID；
    -- 单调递增的整数还顺带给了「插入顺序」这条与时钟无关的兜底次序。
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    metric_key      text NOT NULL,
    source          text NOT NULL,
    -- 与 metric_observation 一致的外键：环境是显式的，不允许写进一个
    -- 不存在的环境（宪法 15 条）
    environment     text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    -- observed_at 可空，语义与 metric_observation 完全一致：上游没给就是空，
    -- 表示这一刻没有成功采集到数据。不要用零值时间冒充。
    observed_at     timestamptz,
    -- synced_at 是采样时刻（同步**尝试**的时刻，无论成败），也是趋势图的横轴。
    -- 用它而不是 observed_at 当横轴：失败样本没有新的 observed_at，
    -- 按 observed_at 排会让「那段红」全部堆在最后一次成功的位置上。
    synced_at       timestamptz NOT NULL,
    status          text NOT NULL,
    is_partial      boolean NOT NULL DEFAULT false,
    watermark       text NOT NULL DEFAULT '',
    last_error_code text NOT NULL DEFAULT '',
    value_json      jsonb NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT metric_observation_sample_key_format
        CHECK (metric_key ~ '^[a-z0-9][a-z0-9_.-]{0,127}$'),
    CONSTRAINT metric_observation_sample_status_allowed
        CHECK (status IN ('ok', 'failed')),
    -- 与 metric_observation 同一条规则：让「静默失败」在库层不可表示。
    -- 趋势图上那段红完全靠 status/last_error_code 渲染，这里松一寸，
    -- 图上就会出现一段没有原因的红。
    CONSTRAINT metric_observation_sample_error_consistency
        CHECK ((status = 'failed') = (last_error_code <> ''))
);

-- 唯一的查询形态就是「某环境某指标最近 N 小时」：三列复合索引正好覆盖
-- 等值 + 范围 + 排序。synced_at DESC 是为了取「最近 N 条」时能顺序扫，
-- 窗口内样本数超过 limit 时被丢掉的必须是**最旧**的那些（见 ops.sql）。
CREATE INDEX metric_observation_sample_series_idx
    ON ops.metric_observation_sample (environment, metric_key, synced_at DESC);

-- 关于「要不要像 audit.audit_event 那样加 DO INSTEAD NOTHING 规则防改」：
-- **不加**，理由是两张表的义务不同。
--
-- audit 表是合规证据，宪法 11 条要求 append-only 并在库外锚定签名摘要，
-- 「永远不删」本身就是需求，把 DELETE 变成静默空操作是对的。
-- 本表是运营遥测，它的路线图里**明确包含**保留期清理（见
-- docs/modules/ops/README.md：5 分钟粒度约 288 条/日/指标）。给它加规则会让
-- 将来那个清理任务的 DELETE 变成静默空操作——不报错、不删数据、表照样涨，
-- 而且要再写一条只为 DROP RULE 的前进式迁移才能解开。用一个静默失败的机制
-- 去防一个本来就没有代码路径的风险，代价大于收益。
--
-- 防改的实际保障是别处：代码里只有 INSERT 与 SELECT 两条路径（见
-- db/queries/ops.sql 与 internal/platform/ops/store.go，没有 UPDATE/DELETE
-- 语句可用），部署时对应用账号 REVOKE UPDATE/DELETE 才是真正的库层闸门。
