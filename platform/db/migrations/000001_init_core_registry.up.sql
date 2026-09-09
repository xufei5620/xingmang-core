-- XM-0009 core registry：Environment / Service / Connector / Connection。
-- 规格 §2.2、§5.6、§18.7；ADR-004、ADR-014。
-- forward-only：本文件一旦发布不得修改（规格 §5.7）。

CREATE SCHEMA IF NOT EXISTS core;

-- 环境表：显式绑定，生产权限不从测试继承（规格 §20.5）。
CREATE TABLE core.environment (
    id          text PRIMARY KEY,
    description text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT environment_id_allowed
        CHECK (id IN ('development', 'staging', 'production'))
);

INSERT INTO core.environment (id, description) VALUES
    ('development', '本地与开发环境'),
    ('staging',     '验收环境 admin-staging.solov.cc'),
    ('production',  '生产环境 admin.solov.cc');

-- 被管理系统实例（规格 §2.2 Service Registry）。
CREATE TABLE core.service (
    id                 uuid PRIMARY KEY,
    service_type       text NOT NULL,
    instance_id        text NOT NULL,
    environment        text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    endpoint           text NOT NULL,
    internal_endpoint  text NOT NULL DEFAULT '',
    owner              text NOT NULL,
    health_check_path  text NOT NULL DEFAULT '',
    native_console_url text NOT NULL DEFAULT '',
    runbook_path       text NOT NULL DEFAULT '',
    status             text NOT NULL,
    source_watermark   text NOT NULL DEFAULT '',
    observed_at        timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT service_type_format
        CHECK (service_type ~ '^[a-z0-9][a-z0-9-]{0,63}$'),
    CONSTRAINT service_instance_id_format
        CHECK (instance_id ~ '^[a-z0-9][a-z0-9-]{0,63}$'),
    CONSTRAINT service_endpoint_https
        CHECK (endpoint LIKE 'https://%'),
    CONSTRAINT service_status_allowed
        CHECK (status IN ('active', 'degraded', 'retired'))
);

-- 业务唯一键与主键分开（规格 §18.7）。
CREATE UNIQUE INDEX service_type_instance_key
    ON core.service (service_type, instance_id);
CREATE INDEX service_environment_idx ON core.service (environment);

-- Connector 类型登记（规格 §2.2 Connector Registry；ADR-004）。
CREATE TABLE core.connector (
    id                          uuid PRIMARY KEY,
    key                         text NOT NULL,
    version                     text NOT NULL,
    contract_version            text NOT NULL,
    connection_schema_path      text NOT NULL,
    target_allowlist            text[] NOT NULL,
    read_capabilities           text[] NOT NULL DEFAULT '{}',
    write_capabilities          text[] NOT NULL DEFAULT '{}',
    supported_upstream_versions text[] NOT NULL DEFAULT '{}',
    compatibility_test_path     text NOT NULL DEFAULT '',
    created_at                  timestamptz NOT NULL DEFAULT now(),
    updated_at                  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT connector_key_format
        CHECK (key ~ '^[a-z0-9][a-z0-9-]{0,63}$'),
    CONSTRAINT connector_version_semver
        CHECK (version ~ '^[0-9]+\.[0-9]+\.[0-9]+$'),
    -- ADR-004：必须声明目标地址 allowlist
    CONSTRAINT connector_allowlist_required
        CHECK (cardinality(target_allowlist) >= 1)
);

CREATE UNIQUE INDEX connector_key_version_key
    ON core.connector (key, version);

-- 实际连接（规格 §2.3 Connection）。只存 CredentialRef，绝不存明文（宪法 7 条）。
CREATE TABLE core.connection (
    id                        uuid PRIMARY KEY,
    connector_id              uuid NOT NULL REFERENCES core.connector (id) ON DELETE RESTRICT,
    service_id                uuid NOT NULL REFERENCES core.service (id) ON DELETE RESTRICT,
    environment               text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    credential_ref            text NOT NULL,
    target_allowlist          text[] NOT NULL,
    granted_capabilities      text[] NOT NULL DEFAULT '{}',
    kill_switch               text NOT NULL DEFAULT '',
    status                    text NOT NULL,
    detected_upstream_version text NOT NULL DEFAULT '',
    version_fingerprint       text NOT NULL DEFAULT '',
    last_verified_at          timestamptz,
    created_at                timestamptz NOT NULL DEFAULT now(),
    updated_at                timestamptz NOT NULL DEFAULT now(),
    -- ADR-014：只接受 secret://<scope>/<name>，从数据库层再挡一道明文
    CONSTRAINT connection_credential_ref_format
        CHECK (credential_ref ~ '^secret://[a-z0-9][a-z0-9-]{0,63}/[a-z0-9][a-z0-9-]{0,63}$'),
    CONSTRAINT connection_allowlist_required
        CHECK (cardinality(target_allowlist) >= 1),
    CONSTRAINT connection_status_allowed
        CHECK (status IN ('enabled', 'disabled', 'killed'))
);

CREATE UNIQUE INDEX connection_connector_service_env_key
    ON core.connection (connector_id, service_id, environment);
CREATE INDEX connection_service_idx ON core.connection (service_id);
CREATE INDEX connection_environment_idx ON core.connection (environment);
