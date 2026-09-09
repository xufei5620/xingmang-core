-- XM-LOGIN：星芒自带员工登录（不依赖 Keycloak 的第三种身份模式）。
--
-- 两条纪律对齐既有的凭据/审计模式（宪法 7 条同一精神，只是标的物从"第三方
-- 凭据"换成"本地口令"）：
--   1. core.staff_account 只存 argon2id 的 PHC 串，不存明文；
--   2. core.staff_session 只存原始会话 token 的 sha256 摘要（不是 token 本身）
--      ——即便这张表被拖走，攻击者也拿不到一个能直接冒充的 Cookie 值。
CREATE TABLE core.staff_account (
    id                   uuid PRIMARY KEY,
    username             text NOT NULL UNIQUE CHECK (username ~ '^[a-z0-9][a-z0-9._-]{1,63}$'),
    display_name         text NOT NULL DEFAULT '',
    password_hash        text NOT NULL CHECK (btrim(password_hash) <> ''),
    -- 平台角色名子集（staff / admin / credential-admin / ...），与
    -- oidcauth.DefaultRoleScopeMap 及 XM_OIDC_ROLE_SCOPES 解析出的表共用同一
    -- 份"角色 -> scope"定义，见 internal/platform/localauth/resolver.go。
    roles                text[] NOT NULL DEFAULT '{}',
    disabled             boolean NOT NULL DEFAULT false,
    must_change_password boolean NOT NULL DEFAULT true,
    failed_attempts      integer NOT NULL DEFAULT 0 CHECK (failed_attempts >= 0),
    locked_until         timestamptz NULL,
    last_login_at        timestamptz NULL,
    created_at           timestamptz NOT NULL,
    updated_at           timestamptz NOT NULL,
    updated_by           text NOT NULL CHECK (btrim(updated_by) <> '')
);

-- 会话表：id 是原始 token 的 sha256 摘要（固定 64 位十六进制），原始 token
-- 只在登录成功那一次的 Set-Cookie 响应里出现，从不落库。environment 与其它
-- 按环境隔离的表同一纪律——引用 core.environment，跨环境的会话在数据层就
-- 不可能被建出来。
CREATE TABLE core.staff_session (
    id           text PRIMARY KEY CHECK (id ~ '^[0-9a-f]{64}$'),
    account_id   uuid NOT NULL REFERENCES core.staff_account (id) ON DELETE CASCADE,
    environment  text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    created_at   timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    revoked_at   timestamptz NULL,
    user_agent   text NOT NULL DEFAULT '',
    ip           text NOT NULL DEFAULT ''
);

CREATE INDEX staff_session_account_idx ON core.staff_session (account_id, expires_at);
