# XM-LOCAL-detail-impl · 服务器详情壳 / 蓝图深链 / Tabs a11y

## status

BLOCKED

## branch / commit / base

- branch: `ai/codex/XM-LOCAL-detail-impl`
- base: `release/v0.1-launch`（`e98080d`）
- implementation commit: `2036956`
- worktree: `K:/星芒统一控制平台/wt-xmLOCAL-detail-impl`

## summary

本片只做最小独立回迁：

- `server` 蓝图新增只读详情深链入口，表格保留空态但可点到 `/platforms/server/detail/:serverId`。
- 新增 `ServerDetailPage` 详情壳，所有字段均为只读预览，不请求 API，不回显凭据。
- `PlatformDetailPage` 的主页签与子页签补上可访问名称。
- `ui-primitives` 的 `Tabs` 组件新增 `ariaLabel` 支持，供平台详情页把 tablist 名称传到实际元素上。

本片不包含 `ChannelDetailPage` / `UpstreamDetailPage` / `SupplierCreatePage`，它们保留给下一片。

## files_changed

- `web/apps/admin-web/src/blueprints/BlueprintView.tsx`
- `web/apps/admin-web/src/blueprints/BlueprintView.test.tsx`
- `web/apps/admin-web/src/blueprints/index.ts`
- `web/apps/admin-web/src/blueprints/server.ts`
- `web/apps/admin-web/src/blueprints/types.ts`
- `web/apps/admin-web/src/pages/PlatformDetailPage.tsx`
- `web/apps/admin-web/src/pages/PlatformDetailPage.test.tsx`
- `web/apps/admin-web/src/pages/ServerDetailPage.tsx`
- `web/apps/admin-web/src/pages/ServerDetailPage.test.tsx`
- `web/apps/admin-web/src/router.tsx`
- `web/packages/ui-primitives/src/Tabs.tsx`

## tests_run

- `node ...vitest.mjs run --environment jsdom BlueprintView.test.tsx PlatformDetailPage.test.tsx ServerDetailPage.test.tsx` — PASS
- `go test -p 1 -count=1 ./...` — PASS
- `D:/Git/bin/bash.exe -lc '... tests/security/governance-not-hollow.test.sh'` — PASS
- `git diff --check` — PASS
- `gitleaks git --redact --no-banner --log-opts=HEAD~1..HEAD` — PASS
- `node ...vitest.mjs run --environment jsdom web/packages/design-tokens/src web/packages/ui-primitives/src web/packages/ui-admin/src web/apps/admin-web/src` — FAIL
  - 多个 workspace 既有测试在 `CommandPalette`、`DataTableV2`、`RequestsPanel`、`PlatformUsersPanel`、`ChannelsPanel`、`ProxyAssetDialog`、`AlertRules`、`Nav`、`PageState`、`Sparkline`、`ContextStrip`、`OverviewPage` 等处失败；这些失败覆盖本片之外的大量组件/页面，导致无法完成 sprint 要求的 full-gate 复核。

## risks

- full-gate 复核当前被 workspace 里一批既有失败挡住；需要先清理这些既有回归，才能把本片重新标成 READY。
- `Tabs` 的可访问名称依赖 `ariaLabel` 透传到 Radix `List`，后续如果改 Tabs 实现需要保住这条语义。

## follow_ups

- 下一片再拆 `ChannelDetailPage` / `UpstreamDetailPage` / `SupplierCreatePage`，避免和本片重叠。
- 先修复 full workspace test 的既有失败，再补本片的最终 READY 复核。
