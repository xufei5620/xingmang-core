sprint-section: 7

# XM-REAL0-a · FinanceCollectSecrets 动态 SecretProvider

## status

READY

## branch / base

- branch: `ai/codex/XM-REAL0-a`
- base: `release/v0.1-launch@c18b75c`
- worktree: `K:/星芒统一控制平台/wt-xmREAL0-a`
- migration: none

## scope

为成本采集登记簿接入动态凭据解析，但不读取真实凭据、不请求上游。Worker 入口新增
FinanceCollectSecrets 装配：

- `env`：CredentialRef 即时映射为
  `XM_FINANCE_SECRET_<SCOPE>__<NAME>`，scope/name 受 CredentialRef 规则约束，
  scope 必须在 `XM_FINANCE_COLLECT_SECRET_SCOPES` 白名单。
- `file`：嵌套布局 `<root>/<scope>/<name>`，root 为固定非根绝对路径，scope 同样
  受白名单限制；可用只读 override 挂载宿主 `var/secrets/<env>`。
- 两种来源显式互斥，不静默回退；每次 Resolve 现场读取，支持登记簿新增/轮换而无需
  重启。SecretProvider 外包 Audited，SecretValue 的日志/JSON 仍自动脱敏。
- 未配置 provider 时保持 nil，real 成本采集按既有 not_supported 观测降级，不影响心跳；
  provider 配置错误在 Worker 启动阶段 fail-closed。

## files_changed

- `internal/platform/secrets/convention.go` 与测试：动态 env convention、scope-filtered
  file provider、metadata/空值/未登记测试。
- `cmd/platform-worker/finance_secrets.go`、`config.go`、`main.go` 及测试：配置
  解析、os.LookupEnv、入口装配和启动可见字段。
- `internal/platform/jobs/client.go`：provider 配置字段与 scope 校验。
- `deploy/compose/launch.yaml`、`.env.example`：
  成本采集参数和 provider 选项。
- `deploy/compose/finance-secrets.override.example.yaml`：
  只读文件挂载示例。
- `docs/runbooks/secrets.md`：运维切换与安全边界说明。

## decisions

- 采用显式 `env|file` 单一来源与精确 scope allowlist；不实现 universal 任意 scope
  读取，避免 ADR-014 的 Router 边界被动态登记簿绕过。
- file root 默认容器路径 `/run/xm/finance-secrets`，目录不存在只导致对应 ref
  解析失败；不在启动时预枚举 DB refs。
- env provider 使用 `os.LookupEnv` 区分未设置与显式空值（分别 ErrNotFound /
  ErrEmptySecret）；不把变量值写入错误或日志。
- 不新增迁移、Action 或外部写请求；真实 provider/上游联调属于后续 CREDS 操作卡。

## tests_run

- `go test ./internal/platform/secrets ./cmd/platform-worker ./internal/platform/jobs` — PASS
- `go test -p 1 ./...` — PASS
- `go vet ./...` — PASS
- `bash scripts/check-governance.sh` — PASS
- `docker compose ... config --quiet` — PASS
- `git diff --check` — PASS

## runtime evidence

共享 `xingmang-launch` staging 栈已以本片提交 `02f6f91693fbf676b161c5f48cf4596d744c2277`
串行重建 Worker（未填充真实凭据、未访问上游）：

```text
DEPLOY LOCAL PASS: sha=02f6f91693fbf676b161c5f48cf4596d744c2277 project=xingmang-launch
healthz=200 readyz=200 smoke=services:200,metrics:200,alerts:200 worker-log=available
```

Worker 启动日志（脱敏摘要）确认本片配置链已加载且默认 staging 保持未装配：

```text
worker_started environment=staging finance_collect_mode=fake
finance secrets configured: false; provider: none; allowlisted scopes: 0
```

动态 env/file 解析、空值与 scope fail-closed 已由单元测试覆盖；未配置 provider 时
成本链路继续 `not_supported`，因此不伪造 `rows_written`。真实 provider 证据须等
负责人填入凭据并发出 `CREDS <平台>` 后按 REAL0-d 操作卡补写。

## follow_ups

1. 负责人填入真实 file/env provider 与 scopes 后，先按 `scripts/verify-real-mode.sh`
   运行 staging/real 验证，再通知 `CREDS <平台>`。
2. provider 接通后把 REAL0-d 的 `finance.rows_written=deferred` 升级为硬门，并记录
   `rows_written>0` 的真实证据。
