-- XM-0011 审计模型（规格 §4.4）：完整字段 + 防篡改哈希链 + 签名根。
-- forward-only：本文件一旦发布不得修改（规格 §5.7）。

CREATE SCHEMA IF NOT EXISTS audit;

-- 审计事件。append-only，且通过 prev_hash/event_hash 构成防篡改链。
CREATE TABLE audit.audit_event (
    id             uuid PRIMARY KEY,
    sequence       bigint NOT NULL,
    occurred_at    timestamptz NOT NULL,
    recorded_at    timestamptz NOT NULL,
    principal_id   text NOT NULL,
    principal_type text NOT NULL,
    action_id      text NOT NULL,
    action_version text NOT NULL,
    action_run_id  uuid NOT NULL,
    resource_type  text NOT NULL,
    resource_id    text NOT NULL DEFAULT '',
    environment    text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    reason         text NOT NULL DEFAULT '',
    approval_id    text NOT NULL DEFAULT '',
    request_id     text NOT NULL,
    trace_id       text NOT NULL DEFAULT '',
    source_ip      text NOT NULL DEFAULT '',
    before_summary             jsonb NOT NULL DEFAULT '{}'::jsonb,
    after_summary              jsonb NOT NULL DEFAULT '{}'::jsonb,
    connector_request_summary  jsonb NOT NULL DEFAULT '{}'::jsonb,
    connector_response_summary jsonb NOT NULL DEFAULT '{}'::jsonb,
    result              text NOT NULL,
    compensation_result text NOT NULL DEFAULT '',
    prev_hash  text NOT NULL,
    event_hash text NOT NULL,
    CONSTRAINT audit_event_sequence_positive CHECK (sequence >= 1),
    CONSTRAINT audit_event_principal_type_allowed
        CHECK (principal_type IN ('HUMAN', 'SERVICE', 'AI', 'SERVER_AGENT')),
    CONSTRAINT audit_event_result_allowed
        CHECK (result IN ('succeeded', 'failed')),
    CONSTRAINT audit_event_hash_format
        CHECK (event_hash ~ '^[0-9a-f]{64}$' AND prev_hash ~ '^[0-9a-f]{64}$')
);

-- sequence 保证链无分叉；event_hash 唯一让重放插入失败
CREATE UNIQUE INDEX audit_event_sequence_key ON audit.audit_event (sequence);
CREATE UNIQUE INDEX audit_event_hash_key ON audit.audit_event (event_hash);
CREATE INDEX audit_event_action_run_idx ON audit.audit_event (action_run_id);
CREATE INDEX audit_event_principal_idx ON audit.audit_event (principal_id, occurred_at DESC);
CREATE INDEX audit_event_occurred_idx ON audit.audit_event (occurred_at DESC);

-- Chain Root：链尖哈希的签名快照，导出后存放于平台数据库之外（规格 §4.4）。
CREATE TABLE audit.chain_root (
    id            uuid PRIMARY KEY,
    computed_at   timestamptz NOT NULL,
    from_sequence bigint NOT NULL,
    to_sequence   bigint NOT NULL,
    root_hash     text NOT NULL,
    signature     text NOT NULL,
    key_id        text NOT NULL,
    exported_at   timestamptz,
    export_target text NOT NULL DEFAULT '',
    CONSTRAINT chain_root_range CHECK (to_sequence >= from_sequence),
    CONSTRAINT chain_root_hash_format CHECK (root_hash ~ '^[0-9a-f]{64}$')
);

CREATE UNIQUE INDEX chain_root_to_sequence_key ON audit.chain_root (to_sequence);
CREATE INDEX chain_root_computed_idx ON audit.chain_root (computed_at DESC);

-- append-only（规格 §4.4）。规则让 DELETE/UPDATE 成为静默空操作；
-- 部署时另需对应用账号 REVOKE DELETE/UPDATE。
-- chain_root 不加规则：需要回写 exported_at。
CREATE RULE audit_event_no_delete AS ON DELETE TO audit.audit_event DO INSTEAD NOTHING;
CREATE RULE audit_event_no_update AS ON UPDATE TO audit.audit_event DO INSTEAD NOTHING;
