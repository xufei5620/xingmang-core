sprint-section: 7

# XM-R210-impl · JobManifestV1 与 EffectiveJobManifest

## status

READY

R210-1 仅冻结当前六个 River 周期任务的静态契约与非秘密 effective 读模型；没有迁移、
真实队列切换、外部服务调用或生产凭据访问。

## branch / commit / base

- branch: `ai/codex/XM-R210-impl`
- base: `release/v0.1-launch@c657a02`（rebase 后复读 ACCEPTANCE-LOG；最新信号为
  `MERGED bca4036 XM-REAL0-d`，以及 CR-0003 BLOCKED 注记）
- worktree: `K:/星芒统一控制平台/wt-xmR210-impl`
- commit: see READY line / branch HEAD

## authorization boundary

- 依据 R2-10 设计与实施计划 R210-1（Sprint §7.2 已批准 R210 路线）；本片不实现 R210-2
  fleet/replica identity、R210-3 probe/UI 或 R210-4 staging 双副本验收。
- River 仍是唯一 scheduler；本片没有新增 ticker、lease 表、队列或数据库对象。

## summary

- 新增 `contracts/jobs/cluster-jobs.v1.json`，冻结 `platform_heartbeat`、`sub2api_sync`、
  `newapi_sync`、`finance_cost_sync`、`retention_prune`、`alert_evaluate` 六个 ID/kind、
  queue、配置来源、catch-up、enqueue fences、River v0.45 unique state 集合、
  at-least-once 与副作用幂等证据。
- 新增 `internal/platform/jobs/job_manifest.go`：严格 unknown-field/trailing-data 解码、
  重复/缺失/未注册 ID 与 kind、scheduler/ownership/fence/source/side-effect 校验，并返回
  canonical JSON 的小写 SHA-256；`RegisteredPeriodicJobSpecs` 返回防御性副本。
- 新增 `internal/platform/jobs/effective_manifest.go`：按非秘密 `Config` 解析 enabled、
  RunOnStart、整秒 interval，校验 environment/worker cluster/River schema 与 production
  RunID 约束，按 ID 字节序排序并生成确定性 effective hash。
- 六个 Args 显式设置 `rivertype.UniqueOptsByStateDefault()`；`NewClient` 统一经 manifest
  registry helper 构造 PeriodicJob，配置周期只覆盖 effective `ByPeriod`。
- `Config` 增加非秘密 `WorkerClusterID`、`RiverSchema` 字段供后续 R210-2 使用，不改变现有
  默认配置或运行时队列。

## files_changed

- `contracts/jobs/cluster-jobs.v1.json`
- `internal/platform/jobs/job_manifest.go`
- `internal/platform/jobs/job_manifest_test.go`
- `internal/platform/jobs/effective_manifest.go`
- `internal/platform/jobs/client.go`
- `internal/platform/jobs/{heartbeat,sub2api_sync,newapi_sync,cost_sync,retention,alert_evaluate}.go`

## decisions

- **queue 以当前代码为准**：当前六个 Args 的 `InsertOpts.Queue` 均为 `maintenance`，因此契约
  也冻结为 `maintenance`。设计稿 §3.1 表格把前三项写成 `default` 与实现不一致；本片不改
  运行时队列（实施计划明确要求保持现有 queues），并以 Args/`NewClient` 的可执行事实作为
  source of truth。
- contract 的 `unique_states` 使用设计稿列出的可读顺序；Args 使用 River v0.45 函数的
  类型顺序（集合相同），避免依赖库默认漂移。
- environment、worker cluster ID、River schema 必须显式、ASCII、首尾字母/数字且不含路径
  或控制字符；不从 hostname、DSN、凭据或环境变量隐式推断。
- static loader 的第二返回值与 effective builder 的第二返回值均为 canonical JSON 的
  lowercase SHA-256；输入格式化/字段顺序不进入摘要，语义字段进入摘要。

## grid → source mapping

| 字段/行为 | 权威源 | 状态 |
| --- | --- | --- |
| 六个周期 ID/kind/queue/fence | `contracts/jobs/cluster-jobs.v1.json` + `RegisteredPeriodicJobSpecs` | 静态 v1；未知/重复/缺失 fail closed |
| unique state 集合 | River v0.45 `rivertype.UniqueOptsByStateDefault()` | 六个 Args 显式声明；manifest 冻结同一集合 |
| effective enabled/RunOnStart/interval | `jobs.Config`（非秘密字段） | 整秒；禁用任务仍保留在读模型并标 `enabled=false` |
| effective identity/hash | `Config.Environment/WorkerClusterID/RiverSchema` + canonical JSON | 后续 fleet slice 比对；本片不连库 |
| 副作用与幂等证据 | 每条 JobSpec 的 `side_effect_class`/`idempotency_evidence` | at-least-once；不宣称 exactly-once |

## tests_run

- TDD RED：实现前执行目标测试，因 `LoadJobManifest`、`RegisteredPeriodicJobSpecs`、
  `JobSpec`、`BuildEffectiveManifest` 未定义而失败；随后以同一测试集验证 GREEN。
- `go test ./internal/platform/jobs` — PASS。
- `go test -p 1 ./...` — PASS（全 Go 包）。
- `go vet ./...` — PASS。
- changed Go files `gofmt -l` — PASS；仓库基线仍有既有未格式化的
  `internal/platform/httpapi/finance_test.go`、`internal/platform/httpapi/router.go`，本片未改动。
- `bash scripts/check-governance.sh` — PASS（本地 WSL worktree 元数据路径导致迁移基线提示，
  脚本以非强制本地模式继续；本片没有迁移文件）。
- `gitleaks protect --staged --redact --verbose` — PASS（0 commits scanned，38.53 KB，no leaks）。
- `gitleaks git --redact --log-opts='c657a02..HEAD'` — PASS（1 commit，no leaks）。
- `git diff --check` — PASS。

## tests_not_run / runtime evidence

- 不运行 Docker、River、PostgreSQL 或真实上游：R210-1 明确是纯模型/契约片，无运行时切换，
  因而没有可声称的 staging/production evidence。
- R210-2 的多副本/leader failover harness、R210-3 probe/UI、R210-4 human staging acceptance
  留给后续独立切片。

## risks

- 若未来任一周期 ID、queue、schedule source、catch-up、fence 或副作用口径变化，必须发布
  新 manifest version；不能原地修改 v1。
- 设计稿队列表格与当前 Args queue 存在历史漂移；验收时应先审该 decisions，再决定是否另
  立批准变更，避免通过 manifest 偷换运行时队列。

## follow_ups

1. 验收线审读本分支并按本地流程合入 release；合入后再由部署线执行 deploy-local.sh。
2. 下一片 R210-2 负责 fleet/replica identity 与 River client ID，不应把本片 hash 当作多副本
   所有权证明。
