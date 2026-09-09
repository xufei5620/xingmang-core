-- XM-0010 Action Core Lite：ActionRun 执行记录。
-- 规格 §4.4（审计模型）、§18.5（Action 规范）、ADR-003。
-- forward-only：本文件一旦发布不得修改（规格 §5.7）。

CREATE SCHEMA IF NOT EXISTS action;

-- ActionRun：一次 Action 执行的记录。append-only（规格 §4.4）。
CREATE TABLE action.action_run (
    id             uuid PRIMARY KEY,
    action_id      text NOT NULL,
    action_version text NOT NULL,
    principal_id   text NOT NULL,
    principal_type text NOT NULL,
    environment    text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    request_id     text NOT NULL,
    risk_level     text NOT NULL,
    status         text NOT NULL,
    error_code     text NOT NULL DEFAULT '',
    duration_ms    bigint NOT NULL,
    started_at     timestamptz NOT NULL,
    finished_at    timestamptz NOT NULL,
    CONSTRAINT action_run_id_format
        CHECK (action_id ~ '^[a-z0-9]+(\.[a-z0-9_]+){2,3}$'),
    CONSTRAINT action_run_principal_type_allowed
        CHECK (principal_type IN ('HUMAN', 'SERVICE', 'AI', 'SERVER_AGENT')),
    CONSTRAINT action_run_risk_level_allowed
        CHECK (risk_level IN ('L0', 'L1', 'L2', 'L3', 'L4')),
    CONSTRAINT action_run_status_allowed
        CHECK (status IN ('succeeded', 'failed')),
    -- 失败必须有错误码，成功必须没有：让「静默失败」在库层就不可表示
    CONSTRAINT action_run_error_code_consistency
        CHECK ((status = 'failed') = (error_code <> '')),
    CONSTRAINT action_run_duration_non_negative
        CHECK (duration_ms >= 0)
);

CREATE INDEX action_run_action_idx ON action.action_run (action_id, started_at DESC);
CREATE INDEX action_run_principal_idx ON action.action_run (principal_id, started_at DESC);
CREATE INDEX action_run_request_idx ON action.action_run (request_id);

-- append-only：撤销 DELETE 与 UPDATE 权限的意图用规则表达（规格 §4.4）。
-- 平台应用账号在部署时另行 REVOKE；此处的规则是第二道防线，
-- 让误写的 DELETE/UPDATE 成为空操作而不是静默生效。
CREATE RULE action_run_no_delete AS ON DELETE TO action.action_run DO INSTEAD NOTHING;
CREATE RULE action_run_no_update AS ON UPDATE TO action.action_run DO INSTEAD NOTHING;
