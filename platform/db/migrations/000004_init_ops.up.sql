-- XM-0014 数据新鲜度（规格 §9.1、附录 K）。forward-only。
CREATE SCHEMA IF NOT EXISTS ops;

CREATE TABLE ops.metric_observation (
    id                          uuid PRIMARY KEY,
    metric_key                  text NOT NULL,
    source                      text NOT NULL,
    environment                 text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    -- observed_at 可空：表示「从未成功采集过」，这正是「未初始化」状态的来源
    observed_at                 timestamptz,
    synced_at                   timestamptz NOT NULL,
    watermark                   text NOT NULL DEFAULT '',
    status                      text NOT NULL,
    is_partial                  boolean NOT NULL DEFAULT false,
    last_success                timestamptz,
    last_error_code             text NOT NULL DEFAULT '',
    staleness_threshold_seconds integer NOT NULL,
    value_json                  jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at                  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT metric_observation_key_format
        CHECK (metric_key ~ '^[a-z0-9][a-z0-9_.-]{0,127}$'),
    CONSTRAINT metric_observation_status_allowed
        CHECK (status IN ('ok', 'failed')),
    CONSTRAINT metric_observation_threshold_positive
        CHECK (staleness_threshold_seconds > 0),
    -- 失败必须有错误码：让「静默失败」在库层不可表示
    CONSTRAINT metric_observation_error_consistency
        CHECK ((status = 'failed') = (last_error_code <> ''))
);

-- 每个 (指标, 环境) 只保留最新一条观测；历史归档另立任务
CREATE UNIQUE INDEX metric_observation_key_env ON ops.metric_observation (metric_key, environment);
CREATE INDEX metric_observation_env_idx ON ops.metric_observation (environment);
