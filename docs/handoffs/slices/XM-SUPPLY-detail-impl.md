# XM-SUPPLY-detail-impl · 渠道 / 上游 / 添加上游详情 UI 壳

## status

READY

## branch / commit / base

- branch: `ai/codex/XM-SUPPLY-detail-impl`
- implementation commit: `0a2463f`
- Handoff commit: `414c880`
- base: `release/v0.1-launch` at `e98080d`
- worktree: `K:/星芒统一控制平台/wt-xmSUPPLY-detail-impl`

## scope

本片只提取 LOCAL 快照 `50becd4` 中的四个详情页 UI 壳，不接入后端、不执行 Action、不写入凭据或业务数据：

- `ChannelDetailPage.tsx`：按平台 + 渠道 ID 展示单渠道经营核算、上游分组、倍率、成本、余额、渠道保障和凭据边界的只读布局。
- `UpstreamDetailPage.tsx`：按平台 + 上游 ID 展示多渠道汇总、充值比例、余额/预计补充、联系人、分组目录、关联渠道和凭据边界的只读布局。
- `SupplierCreatePage.tsx`：添加上游字段蓝图（名称、网址、账号/凭据引用、联系人、充值比例、分组与模型），控件全部禁用，无提交入口。
- `SupplyDetailPages.test.tsx`：验证平台/ID 门禁、只读文案、返回链接、凭据边界和“不发 API / 不提供按钮”。

所有未知值均显示“— / 未接入 / 未知”，页面不从列表缓存拼接经营数字，也不回显密码、Token、私钥或完整 API Key。

## route boundary / dependencies

当前 `release/v0.1-launch@e98080d` 的 `web/apps/admin-web/src/router.tsx` 尚未注册本片详情路由；本片**刻意不修改 router**，避免与 MAP/USER 实现分支同时改平台路由产生冲突。快照 `50becd4` 中的 router hunk 还混有服务器、Action、Jobs 和用户详情改动，未带入本片。

后续集成线应在统一 router 变更中挂载并保留以下路径语义（loader 需先校验平台与 ID）：

- `platforms/:serviceType/upstream/detail/:channelId` → `ChannelDetailPage`
- `platforms/:serviceType/suppliers/:upstreamId` → `UpstreamDetailPage`
- `platforms/:serviceType/suppliers/new` → `SupplierCreatePage`

`SupplierCreatePage` 复用 `ChannelDetailPage` 导出的 `isSupplyPlatform`、`supplyPlatformLabel`、`upstreamDetailPath`；若 MAP/USER 路由整合时拆分模块，需保持这些导出或同步调整 import。未知平台不应回落到另一平台的详情页。

## files_changed

- `web/apps/admin-web/src/pages/ChannelDetailPage.tsx`
- `web/apps/admin-web/src/pages/UpstreamDetailPage.tsx`
- `web/apps/admin-web/src/pages/SupplierCreatePage.tsx`
- `web/apps/admin-web/src/pages/SupplyDetailPages.test.tsx`

## verification

- 详情目标测试：`pnpm --config.verify-deps-before-run=false --filter admin-web test -- src/pages/SupplyDetailPages.test.tsx` — 4/4 PASS。
- 全 workspace frontend tests：`pnpm --config.verify-deps-before-run=false -r --workspace-concurrency=3 run test` — design-tokens 10、ui-primitives 16、ui-admin 219、admin-web 885 全部 PASS；ui-storybook 按约定无单测。
- 全 workspace frontend typecheck：`pnpm --config.verify-deps-before-run=false -r --workspace-concurrency=3 run typecheck` — 5 个 workspace 全部 PASS。
- admin-web production build：`pnpm --config.verify-deps-before-run=false --filter admin-web run build` — PASS；仅保留既有大 chunk warning。
- Storybook build：`pnpm --config.verify-deps-before-run=false --filter ui-storybook run build` — PASS（本机缓存的 esbuild/Storybook 依赖通过 `NODE_PATH` 解析；未改源码或 lockfile）。
- Go：`go fmt ./...`、`go vet ./...`、`go test -p 1 -count=1 ./...` — PASS。
- 治理：`GOVERNANCE_BASE_REF=origin/main GOVERNANCE_REQUIRE_BASE=0 bash scripts/check-governance.sh` — PASS。
- `git diff --check e98080d..HEAD` — PASS。
- gitleaks v8.28.0：`gitleaks git --redact --no-banner --log-opts='e98080d..HEAD'` — 1 commit scanned, no leaks found。

## not_run / risks

- 未把详情路由接入现有 router；需等待 MAP/USER 路由整合线统一挂载并补深链测试。
- 未联调真实渠道/上游详情 API、支付或渠道保障服务；本片没有真实凭据、迁移、Action 或生产写入。
- 详情页当前是字段与状态展示蓝图，不代表对应 ID 在后端真实存在；正式读契约接入后仍须保留缺失/部分/失败新鲜度语义。

## handoff

验收线请审读四个文件及本节路由边界，在不把 LOCAL 整体合入的前提下复跑门禁；后续只在统一 router 变更中接线路由，避免重复声明平台门禁或把 MAP/USER 的动态路径误解析为通用平台页。
