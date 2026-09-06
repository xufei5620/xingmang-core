-- XM-INV-NOTICE-WEBHOOK-SETTING：企业微信通知地址改为在管理端配置。
--
-- 产品负责人 2026-09-06：「这个地址我希望的是在前端可以设置配置。如果写入
-- 服务器中，那不是想更换很麻烦？」——原方案（XM-INV-SUBMIT-NOTICE）把地址
-- 放在宿主机一个 0600 文件里，换群或改错要 SSH 上去改文件再重启 api。
-- 运营会调的东西不该长在服务器上。
--
-- 手法照抄本仓库既有的 SMTP 口令：密文进库、页面上填、**永不回读明文**、
-- 投递时现取（于是轮换之后下一条通知就用新地址，不必重启）。
--
-- **为什么是独立的表，不加一列到 admin_setting_secrets**：那张表的
-- smtp_secret_ciphertext 是 NOT NULL，而 ClearSMTPSecret 走的是
-- `DELETE FROM admin_setting_secrets`——把 Webhook 挂在那张表上，清一次
-- SMTP 口令就会把通知地址一起抹掉。两个凭据的生命周期本来就无关。
--
-- **不存地址的任何明文片段**，连 key 的前几位都不存：整个 URL 就是凭据
-- （企微把鉴权 key 放在查询参数里）。页面要回答的"配的是哪一个"用指纹回答
-- （sha256 前缀），与星芒平台凭据页显示指纹前缀而非值前缀是同一条纪律。
-- 真要确认有没有配对，用"发送测试消息"——消息到没到那个群，比看一段前缀
-- 可靠得多。

CREATE TABLE notice_webhook_setting (
    singleton_id        SMALLINT PRIMARY KEY DEFAULT 1 CHECK (singleton_id = 1),
    webhook_ciphertext  BYTEA NOT NULL CHECK (octet_length(webhook_ciphertext) > 0),
    key_version         TEXT NOT NULL CHECK (btrim(key_version) <> ''),
    -- sha256(地址) 的十六进制前 16 位，带 "sha256:" 前缀。可核对、不可反推。
    fingerprint         TEXT NOT NULL CHECK (fingerprint ~ '^sha256:[0-9a-f]{16}$'),
    updated_by          TEXT NOT NULL CHECK (btrim(updated_by) <> ''),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE notice_webhook_setting IS
    '企业微信群机器人 Webhook 地址（单例）。整个 URL 是凭据，只存密文与指纹。';
