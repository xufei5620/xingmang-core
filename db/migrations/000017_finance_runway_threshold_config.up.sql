-- XM-C-RUNWAY0：按环境保存可用天数阈值与不可变历史。
--
-- 环境变量只用于一次性 bootstrap；运行时 Query/worker 从这两张表读取同一份
-- revision 快照。history 由权限与触发器双重保护，只允许追加。
CREATE TABLE finance.runway_threshold_config (
    environment   text PRIMARY KEY REFERENCES core.environment (id) ON DELETE RESTRICT,
    critical_days integer NOT NULL CHECK (critical_days > 0),
    warning_days  integer NOT NULL CHECK (warning_days > 0),
    serious_days  integer NOT NULL CHECK (serious_days > 0),
    revision      bigint NOT NULL CHECK (revision > 0),
    updated_at    timestamptz NOT NULL,
    updated_by    text NOT NULL CHECK (btrim(updated_by) <> ''),
    reason        text NOT NULL CHECK (btrim(reason) <> ''),
    request_id    text NOT NULL CHECK (btrim(request_id) <> ''),
    CONSTRAINT runway_threshold_ordered
        CHECK (critical_days < warning_days AND warning_days < serious_days)
);

CREATE TABLE finance.runway_threshold_history (
    environment   text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    revision      bigint NOT NULL CHECK (revision > 0),
    critical_days integer NOT NULL CHECK (critical_days > 0),
    warning_days  integer NOT NULL CHECK (warning_days > 0),
    serious_days  integer NOT NULL CHECK (serious_days > 0),
    changed_at    timestamptz NOT NULL,
    changed_by    text NOT NULL CHECK (btrim(changed_by) <> ''),
    reason        text NOT NULL CHECK (btrim(reason) <> ''),
    request_id    text NOT NULL CHECK (btrim(request_id) <> ''),
    change_source text NOT NULL CHECK (change_source IN ('bootstrap', 'action')),
    PRIMARY KEY (environment, revision),
    CONSTRAINT runway_threshold_history_ordered
        CHECK (critical_days < warning_days AND warning_days < serious_days)
);

CREATE INDEX runway_threshold_history_revision_idx
    ON finance.runway_threshold_history (environment, revision DESC);

-- Worker 只应拥有一个「当前快照」读取权限，但仍必须拒绝孤立或被手工
-- 篡改的 current 行。这个只读 view 在数据库侧完成 current/history 的
-- revision 与三档值匹配；不匹配时返回零行，应用 provider 因而 fail closed，
-- 而 worker 不需要直接 SELECT history 表。DB 角色拆分片负责向 worker
-- 运行身份授予该 view 的 SELECT 权限。
CREATE VIEW finance.runway_threshold_current_verified AS
SELECT
    c.environment,
    c.critical_days,
    c.warning_days,
    c.serious_days,
    c.revision,
    c.updated_at,
    c.updated_by,
    c.reason,
    c.request_id
FROM finance.runway_threshold_config AS c
JOIN finance.runway_threshold_history AS h
  ON h.environment = c.environment
 AND h.revision = c.revision
 AND h.critical_days = c.critical_days
 AND h.warning_days = c.warning_days
 AND h.serious_days = c.serious_days;

CREATE OR REPLACE FUNCTION finance.reject_runway_threshold_history_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'runway threshold history is append-only'
        USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER runway_threshold_history_no_row_mutation
    BEFORE UPDATE OR DELETE ON finance.runway_threshold_history
    FOR EACH ROW EXECUTE FUNCTION finance.reject_runway_threshold_history_mutation();

CREATE TRIGGER runway_threshold_history_no_truncate
    BEFORE TRUNCATE ON finance.runway_threshold_history
    FOR EACH STATEMENT EXECUTE FUNCTION finance.reject_runway_threshold_history_mutation();
