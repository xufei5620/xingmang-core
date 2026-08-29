# XM-LOCAL-detail-impl · 服务器详情壳 / 蓝图深链 / Tabs a11y

## status

READY

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

## risks

- 这片的 UI 测试在新 worktree 里需要手工补依赖链接后才能跑；功能本身已通过测试验证。
- `Tabs` 的可访问名称依赖 `ariaLabel` 透传到 Radix `List`，后续如果改 Tabs 实现需要保住这条语义。

## follow_ups

- 下一片再拆 `ChannelDetailPage` / `UpstreamDetailPage` / `SupplierCreatePage`，避免和本片重叠。
- 如要继续补详情结构，再单独接 `日期范围 / 筛选 / dev proxy`。
