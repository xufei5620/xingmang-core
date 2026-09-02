-- XM-ASSURE1-core：渠道主动探测（检测任务）。
--
-- 依据 docs/adr/ADR-019-渠道主动探测通道.md 与配套规格文档
-- docs/superpowers/specs/2026-09-03-xm-assure1-active-probes-design.md
-- （文末明确写着这份 JSON 结构是设计稿，字段名/结构在实现前仍可能被
-- 验收线要求调整）。本迁移相对设计稿 §1 有以下几处实现期调整，均在
-- docs/handoffs/slices/XM-ASSURE1-core.md 里逐条记录：
--   1. probe_credential_ref 定为 NOT NULL DEFAULT ''（而非可空 text），
--      与既有 core.connector_config.credential_ref 的"空串表示未登记"
--      惯例一致，不引入第二种"没有值"的表示方式；
--   2. probe_declaration/probe_run/probe_result 的 environment 一律加
--      REFERENCES core.environment(id)，与 core.connector_config /
--      core.credential_ref 的既有纪律一致（设计稿原文没写这条外键）；
--   3. created_at/updated_at 不带 DEFAULT now()，由应用层注入的时钟显式
--      写入——这是本仓库既有的可测试性约定（credentials.Store 等所有
--      写入路径都是这样，从不依赖数据库时钟），不是漏写；
--   4. probe_run 新增 client_run_key 列：设计稿 §2.4 明确要求"同一
--      declaration_id + client_run_key 已存在一条 pending/running 的
--      run 直接返回该 run"，但 §1.3 的建表语句遗漏了这一列——没有它这条
--      去重语义就无法真正实现，因此在这里补上。

ALTER TABLE core.connector_config
    ADD COLUMN probe_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN probe_credential_ref text NOT NULL DEFAULT '';

CREATE SCHEMA IF NOT EXISTS assurance;

-- 检测任务声明：从平台预置模板中选一个、声明目标渠道/模型、探测目标主机。
-- 不接受自由文本 Prompt（ADR-019 决策·五）。
CREATE TABLE assurance.probe_declaration (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    platform            text NOT NULL CHECK (platform IN ('sub2api', 'newapi')),
    environment         text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    name                text NOT NULL CHECK (btrim(name) <> ''),
    prompt_template_key text NOT NULL CHECK (prompt_template_key IN
                            ('model_fingerprint', 'benchmark_set', 'context_length', 'min_viable_request')),
    target_host         text NOT NULL CHECK (btrim(target_host) <> ''),
    -- [{"channel_id": "...", "external_channel_id": "...", "model": "..."}, ...]
    targets             jsonb NOT NULL,
    max_tokens          integer NOT NULL CHECK (max_tokens > 0 AND max_tokens <= 512),
    -- {"min_length": ..., "max_length": ..., "must_contain": [...], "timeout_ms": ...}
    expected_shape      jsonb NOT NULL,
    schedule_cron       text,
    status              text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'cancelled')),
    version             integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at          timestamptz NOT NULL,
    created_by          text NOT NULL CHECK (btrim(created_by) <> ''),
    updated_at          timestamptz NOT NULL,
    updated_by          text NOT NULL CHECK (btrim(updated_by) <> ''),
    cancelled_at        timestamptz,
    cancelled_by        text,
    cancel_reason       text
);
CREATE INDEX probe_declaration_platform_env_status_idx
    ON assurance.probe_declaration (platform, environment, status);

-- 一次探测批次（按需或——本片未接线——定时触发一次 assurance.probe.run@1）。
-- status='refused' 的行是一次完整的历史记录（宪法 12 条：拒绝执行也要可见），
-- 不是"这次调用没有发生所以不记"。
CREATE TABLE assurance.probe_run (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    declaration_id uuid NOT NULL REFERENCES assurance.probe_declaration (id),
    platform       text NOT NULL CHECK (platform IN ('sub2api', 'newapi')),
    environment    text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    trigger        text NOT NULL CHECK (trigger IN ('manual', 'scheduled')),
    requested_by   text,
    -- 调用方可传的去重键（可空）：同一 declaration_id + client_run_key 已有
    -- pending/running 的行时直接复用，见 store.go InsertRun 的注释。
    client_run_key text,
    status         text NOT NULL CHECK (status IN
                        ('pending', 'running', 'succeeded', 'failed', 'refused', 'cancelled')),
    refusal_reason text,
    action_run_id  uuid,
    started_at     timestamptz,
    finished_at    timestamptz,
    created_at     timestamptz NOT NULL
);
CREATE INDEX probe_run_platform_env_created_idx
    ON assurance.probe_run (platform, environment, created_at DESC);
CREATE INDEX probe_run_declaration_created_idx
    ON assurance.probe_run (declaration_id, created_at DESC);

-- 每个渠道/模型一条探测结果。严禁任何存储被探测模型原始返回文本的字段
-- （威胁模型 §7.1）：verdict/error_kind 必须是探测代码计算好的结构化结论。
CREATE TABLE assurance.probe_result (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id               uuid NOT NULL REFERENCES assurance.probe_run (id),
    channel_id           text NOT NULL,
    external_channel_id  text,
    model                text NOT NULL,
    status               text NOT NULL CHECK (status IN ('ok', 'degraded', 'failed', 'timeout')),
    verdict              text NOT NULL,
    latency_ms           integer,
    first_token_ms       integer,
    measured_first_token boolean NOT NULL DEFAULT false,
    tokens_used          integer,
    http_status          integer,
    error_kind           text,
    evidence_ref         text NOT NULL,
    observed_at          timestamptz NOT NULL,
    created_at           timestamptz NOT NULL
);
CREATE INDEX probe_result_channel_model_created_idx
    ON assurance.probe_result (channel_id, model, created_at DESC);
CREATE INDEX probe_result_run_idx ON assurance.probe_result (run_id);
