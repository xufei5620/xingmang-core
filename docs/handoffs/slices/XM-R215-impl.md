sprint-section: 7

# XM-R215-impl：Connector 静态预算与纯护栏

## Status

READY（待验收线审读、复跑并人工合入；本片不部署、不连接真实源站）

- Branch: `ai/codex/XM-R215-impl`
- Base: `release/v0.1-launch` at `c657a02`（开工前已 fetch；含 REAL0-d 重交版）
- Scope commit: see the final commit containing this Handoff
- Spec: `docs/superpowers/specs/2026-08-29-r2-15-connector-budget-design.md`
- Plan: `docs/superpowers/plans/2026-08-29-r2-15-connector-budget.md`
- Acceptance log read before completion: latest signal is `MERGED bca4036` and the
  `XM-CR-0003-blocked` note; no newer signal was present at handoff time。

## 交付范围

本片只实现 R215-1 的静态策略与纯函数护栏，不改变现有 Connector/Worker 行为：

- `contracts/connectors/budgets.v1.json`：版本化 v1 literal policy。每项能力有有限的
  requests/pages/rows/bytes/cost/wall-clock/concurrency、route cost、retry 与 poll bounds；
  unknown field/route/cost/cursor/coverage/version/scope 以及 write/destructive capability
  fail closed。合同中已声明但尚未有安全路由的 `sub2api.groups.read`、
  `sub2api.models.usage_read`、`metering.upstream.balance_read` 明确标记为 disabled，
  不会被误当成可请求能力。
- `internal/platform/connector/budget_policy.go`：`RouteSpec`/`RegisteredRouteSpecs`、
  strict loader/validator、默认策略和能力查找。route 只含稳定 ID、capability、方法与
  路径模板，不含 host/query/cursor/credential。
- `internal/platform/connector/budget.go`：无 I/O 的 `BudgetGuard`/`BudgetRun`。请求前检查
  context/deadline/route/attempt/cost，并先调用可注入的 `BudgetAuthority`；只有通过后才
  增加计数。响应和分页分别累计 bytes/rows/pages，检测 per-route 与 run cap、重复/停滞
  cursor；`Finish` 生成排序、无敏感内容的 `RunEvidence`，重复 Finish 幂等。
- `internal/platform/connector/limited_body.go`：Content-Length、chunked 和解压后 reader
  的硬字节限制；越界立即关闭底层 body，错误不带响应前缀。提供 context 检查包装。
- `internal/platform/connector/backoff.go`：纯 `ParseRetryAfter`（delta-seconds/HTTP-date）、
  deterministic SHA-256 jitter 与 `NextPollState`。退避不睡眠、不启 ticker、不访问 DB/网络；
  超 policy 的 Retry-After 进入 manual suspend，不静默截断。
- `connectors/{sub2api,newapi,metering}/budget_routes.go`：各 Connector 的静态 route 清单，
  仅用于盘点/策略审查，不参与请求构造。metering 的 NewAPI 收入数据库通道不伪造 HTTP 路由。
- `docs/evidence/TEMPLATE-connector-budget-inventory.md` 与本 README 的 R2-15 小节：盘点
  证据格式、无敏感字段边界及 R215-2/3/4 后续分工。

## 关键决定

1. Sub2API 的 accounts 路径同时支撑账号与渠道余额；策略用不同稳定 route ID 表示两种
   capability，便于分别核算，不把 host/path 误当作预算共享键。
2. metering 包覆盖 Sub2API 与 NewAPI 两种来源；静态清单按实际 HTTP 路由拆分，
   NewAPI 使用计费收入的只读 DB 通道不在本片擅自发明 endpoint。
3. v1 最小轮询间隔等于既有 300 秒 cadence；默认策略只允许保持或变慢。所有 retry
   attempts 计入 request/cost 上限。
4. source authority 采用纯 `BudgetAuthority` 注入点；R215-1 不持有数据库连接、不解析
   CredentialRef、不发 HTTP。PostgreSQL reservation/poll-state 留给 R215-2 的单独批准。
5. disabled capability 仍进入 policy/inventory，避免“清单缺席=无限制可用”的静默升级。

## TDD 与验证证据

先加入测试并运行 `go test ./internal/platform/connector`，测试按预期因缺少
`PollPolicy`/`BudgetGuard`/`LimitedBody` 等实现而失败；随后按 Red→Green→Refactor
补齐最小实现。最终在 rebase 到 `c657a02` 后重新执行：

- `go test -p 1 -count=1 ./...` — PASS
- `go vet ./...` — PASS
- `go test ./internal/platform/connector ./connectors/...` — PASS
- `gitleaks protect --staged --redact` — PASS（无泄漏）
- `gitleaks git --log-opts='c657a02..HEAD' --redact` — PASS（1 commit，无泄漏）
- 显式 WSL linked-worktree 门禁：
  `GIT_DIR=/mnt/k/星芒统一控制平台/xingmang-platform/.git/worktrees/wt-xmR215-impl`
  `GIT_WORK_TREE=/mnt/k/星芒统一控制平台/wt-xmR215-impl`
  `GOVERNANCE_BASE_REF=refs/heads/release/v0.1-launch`
  `GOVERNANCE_REQUIRE_BASE=1 bash scripts/check-governance.sh` — PASS
- `git diff --check` — PASS。

Windows 直接 `bash scripts/check-governance.sh` 会把 linked-worktree 的 Windows `.git`
路径误解析为 WSL 路径；该命令未作为证据，已用上面的显式 `GIT_DIR/GIT_WORK_TREE` 调用
完成同一门禁。没有运行前端、Docker、迁移或真实源站测试，因为本片明确不触碰这些边界。

## 不在本片 / 后续

- 没有把 guard 接入 Sub2API、NewAPI、metering 或 River job；当前运行请求数不变。
- 没有 migration、PostgreSQL SourceBudgetAuthority、lease、PollState 持久化、DBR 或
  source concurrency；这些属于 R215-2 且需要单独 exact diff/审批。
- 没有重试睡眠、手动刷新、预算编辑、reset circuit、UI Drawer 或真实 credential。
- R215-3 才逐个 Connector 做 fake/shadow integration；R215-4 才能在负责人批准的
  staging 窗口启用 enforcement。真实源站验证保持 `not_run`。

## 验收交付

请验收线审读本文件列出的 14 个受控文件，按最新 release 基线复跑 Go/gitleaks/governance
门禁后人工合入。该分支不自动合并、不推送 GitHub PR、不部署生产。
