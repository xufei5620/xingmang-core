-- XM-AUTH-TOTP0：控制台员工登录（XM-LOGIN）加一层 TOTP 二因素。
--
-- 四条纪律对齐既有 XM-LOGIN 表（000021_staff_accounts.up.sql）：
--   1. core.staff_account 只存 TOTP 密钥的**间接引用**（secret://staff-totp/<id>），
--      不存密钥本身——密钥实体经既有 credential.secret.upsert（internal/platform/
--      credentials）写进 SecretProvider 目录，与其它凭据同一条纪律（宪法 7 条）；
--   2. must_enroll_totp 与既有 must_change_password 语义完全对称：账号被授予
--      需要 TOTP 的角色时置真，账号自己完成一次 confirm_totp 后置假；
--   3. core.staff_totp_recovery_code 只存恢复码的 sha256 摘要，不存明文
--      （同 core.staff_session.id 只存原始会话 token 摘要的纪律）；
--   4. core.staff_login_challenge 是"密码已验证、TOTP 待验证"的中间态凭据，
--      形态与 core.staff_session 同构（id = 原始 token 的 sha256 摘要），
--      短 TTL、一次性消费（consumed_at）。

ALTER TABLE core.staff_account
    ADD COLUMN totp_secret_ref  text NULL
        CHECK (totp_secret_ref IS NULL OR totp_secret_ref ~ '^secret://[a-z0-9-]+/[a-z0-9-]+$'),
    ADD COLUMN totp_enrolled_at timestamptz NULL,
    ADD COLUMN must_enroll_totp boolean NOT NULL DEFAULT false;

-- core.staff_session 新增列：mfa_at 是"最近一次 TOTP/恢复码校验通过"的时刻
-- （与开票侧 auth_sessions.mfa_at 同名同义，见 CR-0006 技术规格 §6.1）；
-- amr 是该会话建立时实际经过的验证因子集合（{"pwd"} 或 {"pwd","otp"}），
-- 供 RequireFreshOTP 一类的步进校验读取，不需要每次重新推导。
ALTER TABLE core.staff_session
    ADD COLUMN mfa_at timestamptz NULL,
    ADD COLUMN amr    text[] NOT NULL DEFAULT '{}';

-- 恢复码：确认启用时一次性生成 10 个，仅哈希落库，一次性展示给操作员，
-- 单次可用（used_at 非空＝已消费）。PRIMARY KEY 用 (account_id, code_hash)
-- 而不是自增 id——判重与消费都按这个复合键做，不需要额外唯一索引。
CREATE TABLE core.staff_totp_recovery_code (
    account_id uuid NOT NULL REFERENCES core.staff_account (id) ON DELETE CASCADE,
    code_hash  char(64) NOT NULL CHECK (code_hash ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    used_at    timestamptz NULL,
    PRIMARY KEY (account_id, code_hash)
);

-- 只索引尚未使用的恢复码：登录时"这个账号还有几张没用的恢复码"只关心这一部分，
-- 已用完的历史行留作审计（谁、什么时候用掉了哪一张），不参与该查询。
CREATE INDEX staff_totp_recovery_code_unused_idx
    ON core.staff_totp_recovery_code (account_id)
    WHERE used_at IS NULL;

-- 登录第二步的中间态："密码已验证，等待 TOTP/恢复码"。id 是原始 temp token
-- 的 sha256 摘要（与 core.staff_session.id 同一形态），原始值只在密码校验
-- 通过那一次的响应里出现一次，从不落库。短 TTL（5 分钟，与断言契约的
-- 有效期上限同一量级）+ consumed_at 单次消费，防止同一个 temp token 被
-- 反复用来试探 TOTP 码。
CREATE TABLE core.staff_login_challenge (
    id          text PRIMARY KEY CHECK (id ~ '^[0-9a-f]{64}$'),
    account_id  uuid NOT NULL REFERENCES core.staff_account (id) ON DELETE CASCADE,
    environment text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    created_at  timestamptz NOT NULL,
    expires_at  timestamptz NOT NULL,
    consumed_at timestamptz NULL,
    user_agent  text NOT NULL DEFAULT '',
    ip          text NOT NULL DEFAULT ''
);

CREATE INDEX staff_login_challenge_account_idx ON core.staff_login_challenge (account_id, expires_at);
