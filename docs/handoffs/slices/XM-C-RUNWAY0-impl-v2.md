sprint-section: 7

# XM-C-RUNWAY0-impl-v2 · Runway 阈值运行时配置、预览与规则 UI

## status

READY

代码、静态门禁和共享 `xingmang-launch` 实机验收均完成。本片不触碰真实凭据、生产系统
或外部上游。

## branch / commit / base

- branch: `ai/codex/XM-C-RUNWAY0-impl-v2`
- base: `release/v0.1-launch@72d99441041a650d41af902d009eb30044cd3939`
- worktree: `K:/星芒统一控制平台/wt-xmRUNWAY0-impl-v2`
- migration: `000017_finance_runway_threshold_config.{up,down}.sql`
- implementation commits: `70133c7`, `7877dea`, `e9cc55b`, `0272d16`, `4e4acab`

## scope / summary

本片按 Sprint §7.2 的批准完成 RUNWAY0（C3a/C3b/C3d-read-cutover）实现：

- 以 `finance.runway_threshold_config` 保存每环境 current 快照，以
  `finance.runway_threshold_history` 保存不可变 revision 历史；迁移 000017 包含严格递增
  CHECK、append-only UPDATE/DELETE/TRUNCATE 触发器、revision 索引，以及
  `runway_threshold_current_verified` 只读 JOIN view。API/Worker 均通过 view 取值，
  因而 worker 只需 view 的 SELECT 仍能在 current/history 不成对时 fail closed。
- `RunwayThresholdStore` 提供 DB-only `Current`、事务性 bootstrap 和有界历史 Query；
  current/history 不成对、缺行或数据库错误均 fail closed，不回落 5/10/20 默认值。
- 新增带 buildinfo 的 `cmd/runway-threshold-bootstrap` 生命周期命令，并把它编入 migrate
  镜像；Compose `tools` profile 提供一次性服务。`deploy-local.sh` 现在显式运行该服务，
  再运行原有演示种子，保证部署后阈值 current/history 已存在。
- API 新增当前值、历史、影响预览端点；summary 每请求只读取一次 DB snapshot，响应回显
  相同 revision/source/updated_at；错误码 `RUNWAY_CONFIG_UNAVAILABLE`→503、
  `REVISION_CONFLICT`→409 已加入统一映射。
- 统一 `RunwayThresholds.Classify` 的 `<=` 边界和错误传播，R5 预览区分打开/升级/降级/
  恢复/不一致；serious 只展示不通知；所有 `!AccessMethod.IsMetered()` 均排除出
  runway 覆盖率/影响（若遗留错误 R5 则仅保留不一致证据）；Worker 每轮使用 DB snapshot。
- Worker 告警评估完成日志带非敏感 `threshold_revision` 与 `threshold_source`，即使本轮
  没有 runway 告警也能证明消费了 DB snapshot。
- `/alerts?sub=rules` 增加无 Drawer 的内联规则、当前三档、历史和影响预览 UI；Foundation-B
  未合入时只显示固定门禁说明，不创建 Action、scope 或写请求。

## files_changed (grouped)

- schema/query/generated: `db/migrations/000017_*`（含
  `runway_threshold_current_verified` view）、`db/queries/finance.sql`,
  `internal/platform/finance/gen/*` 及相关生成模型。
- runtime: `internal/platform/finance/runway_config.go`, `runway.go`, `summary.go`,
  `internal/platform/alerts/runway_preview.go`, `rules.go`、HTTP handlers/router、
  `cmd/platform-api`, `cmd/platform-worker`, `cmd/runway-threshold-bootstrap`、
  `deploy/docker/go.Dockerfile`、`deploy/compose/launch.yaml`。
- UI: `web/apps/admin-web/src/api/runwayThresholds.ts`、规则面板/页面/Alerts/Settings/router
  测试与 `ui-primitives/FormField`。
- lifecycle/tests/docs: `deploy/scripts/deploy-local.sh` 及其回归测试（infra→threshold
  bootstrap→app 顺序/失败闸）、DB harness guards、
  `cmd/pgdsn-validate`、`docs/runbooks/SWITCH-RUNWAY-THRESHOLDS-TO-DB.md`、模块说明。
- evidence: `docs/evidence/EV-2026-08-30-runway-disposable-db.md`、
  `docs/evidence/screens/XM-C-RUNWAY0-impl-v2/runway-rules-wide.jpg`。

## decisions

- `000017` 依据 release 72d9944 的最大迁移号 000016 + 1 计算；Sprint §7.2 明确批准该
  RUNWAY0 编号，未修改已合入的 000015/000016。
- C3c/C3e 仍不实现：Foundation-B/XM-0030 未在当前基线有批准合入证据；UI 只读/预览并
  明示门禁，符合原规格的最小权限边界。
- 环境参数只作一次性 bootstrap 输入；运行时 API/Worker 不读取 env 阈值，也不在缺行时
  静默使用默认值。
- `deploy-local.sh` 按 `postgres+migrate → --profile tools run --rm
  runway-threshold-bootstrap up → bootstrap SQL → API/worker/web` 顺序运行；两者均保留
  迁移依赖，且可重复执行，避免 API/worker 在 threshold current 写入前启动。

## grid → source mapping

| UI/API 格 | 权威源 | 诚实状态 |
| --- | --- | --- |
| 当前 Critical/Warning/Serious、revision、更新时间 | `GET /api/v1/finance/runway-thresholds` → `finance.runway_threshold_config` + matching history | DB 快照；缺行/不一致为 503 |
| 最近变更 | `GET /api/v1/finance/runway-thresholds/history` → append-only history | 有界降序；不暴露 SQL/DSN |
| 影响计数与对象 observed_at | `GET /api/v1/finance/runway-thresholds/preview` → balance/profit snapshot + active R5 | 区分 evaluation_at、过期观测和 current_inconsistent |
| 渠道/上游 summary 分档 | `/api/v1/finance/{channels,upstreams}/summary` + 同一次 `Current` snapshot | 不跨 revision；未知余额不伪造天数 |
| 规则说明与提交门禁 | `RunwayThresholds.Classify` + Foundation-B 状态 | serious 不通知；无 Action client/scope |

## verification (static / local)

- `go fmt ./...` — PASS（同时同步 sqlc 生成模型）；`go vet ./...` — PASS。
- `go test -p 1 ./...` — PASS（全 Go 包，含 non-metered 过滤、view provider 与 worker
  snapshot metadata 回归）。
- `go tool sqlc generate` 后生成目录无漂移（除基线原有遗漏的
  `FinancePlatformChannelBinding` 生成模型，已一并同步）。
- `bash tests/deploy/deploy-local.test.sh` — PASS（含新 runway bootstrap 顺序断言，完整
  `DEPLOY-LOCAL-TEST-OK`）。
- WSL `pnpm -r run typecheck` — PASS（5 workspace）。
- WSL clean archive `pnpm -r run test` — PASS（ui-primitives 16、ui-admin 232、
  admin-web 63 files / 957、design-tokens 10；共 1,215 tests）。
- WSL admin-web `pnpm exec vitest run src/api/runwayThresholds.test.ts src/components/RunwayThresholdRulePanel.test.tsx src/pages/AlertRulesPage.test.tsx --pool=threads --maxWorkers=1` — PASS（3 files / 9 tests）。
- WSL `pnpm --filter admin-web run build` — PASS；`pnpm --filter ui-storybook run build` — PASS。
- 隔离 DB 证据脚本 — PASS（`EV-2026-08-30-runway-disposable-db.md`）；静态
  `runway-bootstrap-lifecycle` 与 `runway-threshold-db-roles` 检查 — PASS。
- gitleaks v8.28.0（`72d9944..HEAD`，7 commits）— PASS，无泄漏。
- WSL 初次全量 Vitest 在 `/mnt/k` 挂载盘出现 Vitest worker 启动超时；该环境噪声未归因于
  RUNWAY 代码。新片定向测试与 Go 全量均通过；验收线应在其标准 clean archive 门禁中复跑
  全量前端。

## runtime evidence

受影响服务：`migrate`（含 000017）、`platform-api`、`platform-worker`、`web`；一次性
`runway-threshold-bootstrap` 使用 `tools` profile。

验收线已在共享 Docker 栈以 `182c578` 重建并实测（不打印 `.env` 或凭据）：

- `/healthz` — HTTP 200；`/readyz` — HTTP 200。
- `GET /api/v1/finance/runway-thresholds` — HTTP 200；`environment=staging`、
  `critical_days=5`、`warning_days=10`、`serious_days=20`、`revision=1`、
  `source=database`；`updated_by` 为生命周期 bootstrap 身份。
- `GET /api/v1/finance/runway-thresholds/history?limit=20` — HTTP 200；1 条历史、
  `has_more=false`，与 current revision=1 成对。
- `GET /api/v1/finance/runway-thresholds/preview?critical_days=5&warning_days=10&serious_days=20`
  — HTTP 200；`coverage.total=1`、`known=1`、`alert_coverage_complete=true`、
  `counts.unchanged=1`。
- Worker 日志：`sub2api_sync success=true metrics_total=6 metrics_failed=0`；
  `newapi_sync success=true metrics_total=5 metrics_failed=0`；
  `alert_evaluate success=true threshold_revision=1 threshold_source=database`。
  `no_notifier_configured` 为 staging 未配置通知器的预期提示，不影响评估落库。

建议验收命令（后续回归时不要打印 `.env` 或凭据）：

```text
deploy/scripts/deploy-local.sh --sha <release-merged-sha>
curl -sS http://127.0.0.1:8088/healthz
curl -sS http://127.0.0.1:8088/readyz
curl -sS -H 'X-Dev-Principal-ID: dev-operator' -H 'X-Dev-Principal-Type: HUMAN' -H 'X-Dev-Scopes: finance.read,ops.read,registry.read' http://127.0.0.1:8088/api/v1/finance/runway-thresholds
curl -sS -H 'X-Dev-Principal-ID: dev-operator' -H 'X-Dev-Principal-Type: HUMAN' -H 'X-Dev-Scopes: finance.read,ops.read,registry.read' 'http://127.0.0.1:8088/api/v1/finance/runway-thresholds/history?limit=20'
curl -sS -H 'X-Dev-Principal-ID: dev-operator' -H 'X-Dev-Principal-Type: HUMAN' -H 'X-Dev-Scopes: finance.read,ops.read,registry.read' 'http://127.0.0.1:8088/api/v1/finance/runway-thresholds/preview?critical_days=5&warning_days=10&serious_days=20'
docker compose -p xingmang-launch -f deploy/compose/launch.yaml --env-file deploy/compose/.env logs --no-color --since 10m platform-worker
```

截图存放：`docs/evidence/screens/XM-C-RUNWAY0-impl-v2/runway-rules-wide.jpg`，包含
`/alerts?sub=rules` 桌面宽屏的当前值/历史/预览成功状态。记录脱敏 JSON 摘要与 Worker
`threshold_revision` 日志，不记录 token、DSN、口令或完整请求体。

已完成的隔离数据库证据见
`docs/evidence/EV-2026-08-30-runway-disposable-db.md`；它与共享栈重建证据相互独立。

## tests_not_run / risks

- 尚未在共享 `xingmang-launch` 栈上重建或重启（由验收线串行执行，避免与 USER0 并发）；
  因此 runtime evidence 和真实 Docker 镜像 digest 待补。部署时必须先执行迁移、阈值
  bootstrap，再启动 API/worker/web。
- 未联调真实 Sub2API/NewAPI/生产凭据；真实余额接入仍按 REAL0 轨道执行。
- DB 角色最小权限的生产激活仍属于独立 DBR 片；本迁移的触发器不是 superuser ACL 的替代。
- 当前 branch 仅包含 C3a/C3b/C3d-read-cutover；Foundation-B 未合入时不提供阈值写入。

## follow_ups

1. 验收线审读本片后合入 `release/v0.1-launch`，并按部署驱动协议回报
   `MERGED <sha>`；后续部署沿用 `deploy-local.sh` 的分阶段顺序。
2. 后续若 Foundation-B/XM-0030 获批合入，另开 C3c/C3e 分支，不在本片追加写 Action。
