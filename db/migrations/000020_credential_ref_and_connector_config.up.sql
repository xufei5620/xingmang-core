-- XM-CRED0：凭据引用登记与连接器运行配置。
--
-- 两条纪律（宪法 7 条 / ADR-014）：
--   1. core.credential_ref **只存元数据**——引用、指纹、版本、吊销时间。
--      明文以文件形式落在 XM_SECRET_ROOT（<root>/<scope>/<name>），由
--      SecretProvider 读取；任何一列都不允许承载值本身。
--   2. core.connector_config 是 worker 每轮读取的按平台配置（fake/real、
--      端点、主机白名单、凭据引用）。它记的是「用哪个引用」，不是凭据。
--
-- fingerprint 是 sha256(value) 的前 16 位十六进制：足够让运营核对
-- 「粘贴的是不是同一把 token」，又短到不可能反推出值。
CREATE TABLE core.credential_ref (
    ref          text PRIMARY KEY,
    scope        text NOT NULL,
    name         text NOT NULL,
    environment  text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    fingerprint  text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{16}$'),
    version      integer NOT NULL DEFAULT 1 CHECK (version > 0),
    revoked_at   timestamptz NULL,
    updated_at   timestamptz NOT NULL,
    updated_by   text NOT NULL CHECK (btrim(updated_by) <> ''),

    -- ref 必须与 scope/name 一致，且两段都符合 secrets.ParseCredentialRef 的形态
    CONSTRAINT credential_ref_parts_match CHECK (ref = 'secret://' || scope || '/' || name),
    CONSTRAINT credential_ref_scope_format CHECK (scope ~ '^[a-z0-9][a-z0-9-]{0,63}$'),
    CONSTRAINT credential_ref_name_format CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,63}$')
);

CREATE INDEX credential_ref_environment_idx
    ON core.credential_ref (environment, ref);

CREATE TABLE core.connector_config (
    platform         text NOT NULL CHECK (platform IN ('sub2api', 'newapi')),
    environment      text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    mode             text NOT NULL CHECK (mode IN ('fake', 'real')),
    endpoint         text NOT NULL DEFAULT '',
    target_allowlist text[] NOT NULL DEFAULT '{}',
    credential_ref   text NOT NULL DEFAULT '',
    version          integer NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at       timestamptz NOT NULL,
    updated_by       text NOT NULL CHECK (btrim(updated_by) <> ''),
    PRIMARY KEY (platform, environment)
);
