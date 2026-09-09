CREATE TABLE admin_settings (
    singleton_id            SMALLINT PRIMARY KEY DEFAULT 1 CHECK (singleton_id = 1),
    issuer_name             TEXT NOT NULL CHECK (btrim(issuer_name) <> '' AND char_length(issuer_name) <= 200),
    service_item            TEXT NOT NULL DEFAULT '技术服务' CHECK (service_item = '技术服务'),
    minimum_request_minor   BIGINT NOT NULL DEFAULT 20000 CHECK (minimum_request_minor >= 20000),
    smtp_host               TEXT NOT NULL CHECK (smtp_host IN ('smtp.qq.com', 'smtp.exmail.qq.com', 'smtp.gmail.com')),
    smtp_port               INTEGER NOT NULL CHECK (smtp_port = 587),
    smtp_from               TEXT NOT NULL CHECK (btrim(smtp_from) <> ''),
    smtp_from_name          TEXT NOT NULL CHECK (btrim(smtp_from_name) <> '' AND char_length(smtp_from_name) <= 128),
    smtp_starttls           BOOLEAN NOT NULL DEFAULT TRUE CHECK (smtp_starttls),
    admin_cidrs             CIDR[] NOT NULL CHECK (cardinality(admin_cidrs) > 0),
    revision                BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_by              TEXT NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE admin_setting_secrets (
    setting_id              SMALLINT PRIMARY KEY REFERENCES admin_settings(singleton_id) ON DELETE CASCADE,
    smtp_secret_ciphertext  BYTEA NOT NULL CHECK (octet_length(smtp_secret_ciphertext) > 0),
    key_version             TEXT NOT NULL CHECK (btrim(key_version) <> ''),
    updated_by              TEXT NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);
