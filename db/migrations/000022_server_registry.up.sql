-- XM-SERVER0：服务器登记簿（用户拍板：「服务器只做记录」——不装 Agent、
-- 不做任何 SSH/docker 探测、不接实时监控）。forward-only（规格 §5.7）。
--
-- 四张纯登记表，字段清单取自现有占位页 ServerDetailPage.tsx 的蓝图态与
-- ADMIN-IA §2.1 服务器 7 页签：
--   core.server_supplier      供应商与购买账号（联系方式，不存密码）
--   core.server_asset         服务器资产（规格、状态、月度成本、到期日）
--   core.server_domain        域名与证书生命周期
--   core.server_service_note  服务器上手工登记的服务/容器（不扫描 Docker）
--
-- 与 finance.upstream_account 一样，写路径全部经 server.* Action（宪法 2 条）；
-- 本迁移只建表与库层不变量，不写任何数据。
CREATE TABLE core.server_supplier (
    id           uuid PRIMARY KEY,
    name         text NOT NULL,
    website      text,
    console_url  text,
    contact_name text,
    contact_info text,
    notes        text,

    -- 环境显式外键（宪法 15 条）：不允许供应商登记落在不存在的环境上，
    -- 更不允许 staging 的供应商混进生产的采购台账。
    environment  text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,

    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT server_supplier_name_not_blank CHECK (btrim(name) <> ''),
    -- 官网/控制台地址只接受 https 且不含用户信息段——与 finance 的 base_url
    -- 同一条纪律（ADR-018 闸 1）：只读通道不接受明文凭证藏在 URL 里。
    CONSTRAINT server_supplier_website_https
        CHECK (website IS NULL OR website ~ '^https://[^@\s]+$'),
    CONSTRAINT server_supplier_console_url_https
        CHECK (console_url IS NULL OR console_url ~ '^https://[^@\s]+$')
);

-- 同一环境下同一个供应商名只登记一次；重复登记会让「关联服务器」台数被拆到
-- 两行上，看起来两个都没几台，其实是同一个供应商被算漏了一半。
CREATE UNIQUE INDEX server_supplier_env_name_key
    ON core.server_supplier (environment, name);

CREATE TABLE core.server_asset (
    id                       uuid PRIMARY KEY,
    hostname                 text NOT NULL,

    -- ip_addresses 允许一台服务器登记多个地址（公网/内网/多网卡），
    -- 用 jsonb 数组而不是逗号拼接字符串——后者没有形态约束，
    -- 一次手滑的分号会让某个地址悄悄消失且不报错。
    ip_addresses             jsonb NOT NULL DEFAULT '[]'::jsonb,

    datacenter               text,
    supplier_id              uuid REFERENCES core.server_supplier (id) ON DELETE RESTRICT,

    vcpu                     integer,
    memory_gb                integer,
    disk_gb                  integer,
    purpose                  text,

    -- 状态是登记簿口径，不是心跳判定——**只做记录**：没有 Agent 上报，
    -- 「在线/离线」这类实时态本迁移不建模。
    status                   text NOT NULL DEFAULT 'active',

    -- 月付金额按整数最小单位（宪法 13 条），标度取决于 currency
    -- （internal/platform/money.CurrencyScale：USD/CNY 等 2 位，JPY/KRW 0 位）。
    -- 两者要么都空要么都填，见下面的 CHECK——一个有金额没币种的行没法展示。
    monthly_cost_minor_units bigint,
    currency                 text,
    billing_cycle            text,

    -- 到期日是**续费/合同到期**，自然日（宪法 14 条：时间库内 UTC，
    -- 但到期日是业务日期不是时间点，用 date 而不是 timestamptz）。
    expires_at               date,

    notes                    text,

    environment              text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,

    created_at               timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT server_asset_hostname_not_blank CHECK (btrim(hostname) <> ''),
    CONSTRAINT server_asset_status_allowed
        CHECK (status IN ('active', 'retired', 'planned')),
    CONSTRAINT server_asset_ip_addresses_is_array
        CHECK (jsonb_typeof(ip_addresses) = 'array'),
    CONSTRAINT server_asset_vcpu_positive CHECK (vcpu IS NULL OR vcpu > 0),
    CONSTRAINT server_asset_memory_gb_positive CHECK (memory_gb IS NULL OR memory_gb > 0),
    CONSTRAINT server_asset_disk_gb_positive CHECK (disk_gb IS NULL OR disk_gb > 0),
    CONSTRAINT server_asset_currency_format
        CHECK (currency IS NULL OR currency ~ '^[A-Z]{3}$'),
    CONSTRAINT server_asset_billing_cycle_allowed
        CHECK (billing_cycle IS NULL OR billing_cycle IN ('monthly', 'quarterly', 'yearly')),
    -- 金额与币种成对出现：不允许「有金额没币种」（没法展示）或
    -- 「有币种没金额」（币种代表着什么无法解释）。
    CONSTRAINT server_asset_cost_currency_paired
        CHECK ((monthly_cost_minor_units IS NULL) = (currency IS NULL)),
    CONSTRAINT server_asset_monthly_cost_non_negative
        CHECK (monthly_cost_minor_units IS NULL OR monthly_cost_minor_units >= 0)
);

-- 同一环境下同一个主机名只登记一次，避免同一台服务器被登记成两行，
-- 月度成本合计因此被算重复。
CREATE UNIQUE INDEX server_asset_env_hostname_key
    ON core.server_asset (environment, hostname);

CREATE INDEX server_asset_env_status_idx
    ON core.server_asset (environment, status);

CREATE INDEX server_asset_supplier_idx
    ON core.server_asset (supplier_id);

-- 概览页「30 天内到期」统计与列表页的到期筛选都按 (environment, expires_at)
-- 扫描；未登记到期日的资产不参与到期筛选，索引按惯例排除 NULL。
CREATE INDEX server_asset_env_expires_idx
    ON core.server_asset (environment, expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE core.server_domain (
    id                 uuid PRIMARY KEY,
    domain_name        text NOT NULL,
    registrar          text,
    dns_provider       text,
    expires_at         date,

    -- 证书来源三选一：acme 自动续期、managed 由供应商/CDN 托管、manual 手工上传。
    -- 只做记录：证书健康度不探测，来源与到期日全部手工登记。
    cert_source        text,
    cert_expires_at    date,

    -- 绑定服务只是一句备注（哪个服务在用这个域名），不是外键——服务与容器
    -- 目前也是手工登记表，两边都没有稳定到能互相引用的主键（服务名可能
    -- 跨服务器重复），勉强建外键换不来真实的引用完整性，只会锁死措辞。
    bound_service_note text,

    environment        text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,

    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT server_domain_name_not_blank CHECK (btrim(domain_name) <> ''),
    CONSTRAINT server_domain_cert_source_allowed
        CHECK (cert_source IS NULL OR cert_source IN ('acme', 'managed', 'manual'))
);

CREATE UNIQUE INDEX server_domain_env_name_key
    ON core.server_domain (environment, domain_name);

CREATE INDEX server_domain_env_expires_idx
    ON core.server_domain (environment, expires_at) WHERE expires_at IS NOT NULL;

CREATE INDEX server_domain_env_cert_expires_idx
    ON core.server_domain (environment, cert_expires_at) WHERE cert_expires_at IS NOT NULL;

-- 服务与容器的手工登记行。**不扫描 Docker、不探测端口**——这张表只是运营
-- 手动写下「这台服务器上有什么」，与 finance.token_map 同一个形状：
-- 挂在父行（server_asset）下，自己不重复存 environment 列
-- （见 internal/platform/finance/actions.go 的 resolveOwningAccount 注释：
-- 环境判定必须先把父行读出来，而不是让子表自称一个环境）。
CREATE TABLE core.server_service_note (
    id           uuid PRIMARY KEY,
    server_id    uuid NOT NULL REFERENCES core.server_asset (id) ON DELETE RESTRICT,
    service_name text NOT NULL,

    -- 类型三选一：容器 / systemd 单元 / 裸进程。纯分类标注，不驱动任何探测。
    service_kind text NOT NULL,
    port         integer,
    notes        text,

    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT server_service_note_name_not_blank CHECK (btrim(service_name) <> ''),
    CONSTRAINT server_service_note_kind_allowed
        CHECK (service_kind IN ('container', 'systemd', 'process')),
    CONSTRAINT server_service_note_port_range
        CHECK (port IS NULL OR (port > 0 AND port <= 65535))
);

-- 同一台服务器下同一个服务名只登记一次。
CREATE UNIQUE INDEX server_service_note_server_name_key
    ON core.server_service_note (server_id, service_name);

CREATE INDEX server_service_note_server_idx
    ON core.server_service_note (server_id);
