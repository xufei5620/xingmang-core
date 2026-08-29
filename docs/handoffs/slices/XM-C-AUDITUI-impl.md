# XM-C-AUDITUI-impl · 审计归档 / 审计子页 UI

## status

READY

## branch / commit / base

- branch: `ai/codex/XM-C-AUDITUI-impl`
- base: `e98080d`（DEPLOY0-b 已合入）
- worktree: `K:/星芒统一控制平台/wt-xmC-AUDITUI-impl`
- implementation commit: `8a5a757`
- test-hardening commit: `13616c4`

## summary

本片把 `/audit` 变成显式子页结构，只做只读展示，不碰写路径或新权限：

- `/audit` 仍是审计记录默认页，保留现有事件查询、哈希链、展开摘要和加载更多；
- `?sub=evidence` 进入诚实的未接入状态页，说明对象存储、manifest 与冷读接口尚未建立；
- `?sub=chain` 进入 CLI-only 状态页，明确审计链验证走 `audit-verify`，不在浏览器里做全链扫描；
- 未知 `?sub=` 不静默回落到事件列表，而是显示未接入提示并提供返回审计记录入口。

实现上沿用仓库现有的 `Tabs` / `PageState` / `DataTableV2` / `ApiStateView` 约定，避免引入新的页面协议；路由仅挂载审计记录默认页，不增加普通 Action、对象存储访问或写入接口。

## files_changed

- `web/apps/admin-web/src/pages/AuditPage.tsx`
- `web/apps/admin-web/src/pages/AuditPage.test.tsx`
- `web/apps/admin-web/src/router.test.tsx`

## tests_run

- `D:/Node/node.exe .../vitest.mjs run --environment jsdom web/apps/admin-web/src/pages/AuditPage.test.tsx` — PASS (`4 passed`)
- `D:/Node/node.exe .../vitest.mjs run --environment jsdom -t '审计事件页|最近活动来自审计记录' web/apps/admin-web/src/router.test.tsx` — PASS (`2 passed, 130 skipped`)
- `bash tests/security/governance-not-hollow.test.sh` — PASS
- `C:/Users/58439/AppData/Local/Temp/xm-gitleaks-bin/gitleaks.exe git --redact --no-banner --log-opts=HEAD^..HEAD` — PASS (`1 commits scanned, no leaks found`)
- `git diff --check` — PASS
- WSL clean archive（Node 22.22.0 / pnpm 11.24.0）`pnpm install --frozen-lockfile --ignore-scripts --package-import-method=copy --offline` — PASS；`pnpm -r run typecheck` — PASS（5 个 workspace）；`pnpm -r run test` — PASS（ui-admin 219、admin-web 878，design-tokens 10）；Storybook build — PASS；admin-web production build — PASS。
- WSL 临时 git snapshot `GOVERNANCE_REQUIRE_BASE=1 bash scripts/check-governance.sh` — PASS；Windows/WSL gitleaks `e98080d..HEAD` — PASS（3 commits scanned, no leaks）。
- Go `go fmt ./...`、`go vet ./...`、`go test -p 1 -count=1 ./...` — PASS。

## tests_not_run

- 未联调真实对象存储、生产环境或真实凭据；本片只读 UI 不触发这些外部系统。

## risks

- `Tabs` 会把子页挂在同一条 `/audit?sub=` 语义下；若后续 ui-admin tabs 语义变化，router 断言需要继续收窄到活跃面板。
- 未知子页只做前端拒绝，不改变后端审计查询契约。
- 这片仍是只读展示，没有接入对象存储、manifest 或恢复链路。

## follow_ups

- 继续把审计归档的只读 UI 与后续 `AUD2+` 冷读/恢复设计解耦；
- Node 22 与仓库 Node >=24 的 engine warning、既有大 chunk warning 不影响本次门禁结果；验收线仍可在 Node 24 环境复跑。
