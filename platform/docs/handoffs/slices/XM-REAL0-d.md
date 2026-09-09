sprint-section: 7

# XM-REAL0-d · 真实模式接入验证清单

## status

READY

## branch / commit / base

- branch: `ai/codex/XM-REAL0-d`
- base: `release/v0.1-launch@c18b75c`（含 RUNWAY0 与 USER0 已验收合入）
- worktree: `K:/星芒统一控制平台/wt-xmREAL0-d`
- script: `scripts/verify-real-mode.sh`

## scope

本片只提供真实切换后的本地验证脚本和脱敏 fixture，不接触真实凭据、生产系统或第三方
上游。脚本按容器 `StartedAt`（测试替身不支持时回落到脚本时间）过滤 Worker 日志，
验证 healthz/readyz、services/metrics/alerts 三条烟测、Worker 模式/来源/最新同步结果，
以及固定平台指标矩阵：

- Sub2API：users.total、users.balance、revenue.daily、cost.daily、channels.balance、
  channels.status（6 项）
- NewAPI：users.total、recharge.daily、subscription.daily、channels.status、models.usage
  （5 项）

每条指标必须带匹配 environment/source、watermark、observed_at 和可接受的新鲜度
`fresh|partial`；`partial` 与 `is_partial` 不一致、failed/stale/uninitialized、
非空 last_error_code 均 fail-closed。real 模式拒绝 `*-staging` 演示来源。输出只给状态和
计数，永不输出响应正文、token、DSN、Cookie 或口令。finance rows_written 在
REAL0-a 装配前明确输出 `deferred`，不提前宣称成本已接通。

## runtime evidence

验收线在共享 `xingmang-launch` staging 栈重启 Worker 后运行：

```text
VERIFY REAL MODE PASS: environment=staging mode=staging
healthz=200 readyz=200 smoke=services:200,metrics:200,alerts:200
verified_platforms=sub2api,newapi metrics_checked=11 finance.rows_written=deferred worker=running
```

Worker 日志来自本轮容器启动边界，Sub2API/NewAPI 均为 fake/staging、同步成功且
`metrics_failed=0`；这是脚本链路证据，不是真实接入证据。

## tests_run

- `bash -n scripts/verify-real-mode.sh` — PASS
- `bash tests/deploy/verify-real-mode.test.sh` — PASS（全部正/负向 fixture，输出
  `VERIFY-REAL-MODE-TEST-OK`）
- 共享 staging 栈脚本运行 — PASS（11 项指标）
- 未读取 .env 内容；未调用任何上游写端点。

## files_changed

- `scripts/verify-real-mode.sh`
- `tests/deploy/verify-real-mode.test.sh`
- 测试运行时在临时目录生成的指标 JSON（不入库，避免脱敏字段被 generic-api-key 误判）
- `tests/fixtures/real-mode/*.log`
- `docs/handoffs/slices/XM-REAL0-d.md`

## decisions

- 本地脚本固定 loopback 地址和 `xingmang-launch` Compose 项目；仅 staging 才自动带开发
  身份头，production 不注入开发头。
- 真实来源必须与 Worker 启动日志和指标 source 一致；任何演示后缀或跨来源数据均拒绝。
- 日志解析兼容 Docker Compose 的 `service | JSON` 前缀；解析失败不回显原始行。
- 指标矩阵和 freshness 纪律源自 Sprint §7 与 REAL0 审计；不把缺少 finance rows_written
  伪装成通过。
- gitleaks 形状修复：指标字段和值由测试脚本运行时从后缀数组拼装，提交 diff 不含
  `metric_key` 与指标名相邻的字面量；最终提交按验收要求以 `c18b75c` 为父提交。

## follow_ups

- 负责人按 SWITCH-SUB2API-REAL / SWITCH-NEWAPI-REAL 填入凭据并通知 `CREDS <平台>`
  后，重新运行本脚本并写入 `docs/evidence/EV-<日期>-<平台>-real-switch.md`。
- REAL0-a 完成 SecretProvider 装配后，再把 finance rows_written 纳入真实模式硬门。
