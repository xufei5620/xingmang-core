# XM-LOCAL-controls-impl · 本地预览 API 代理修正（控件残余审计）

## status

READY

## branch / commit / base

- branch: `ai/codex/XM-LOCAL-controls-impl`
- implementation commit: `7b52582`
- base: `e98080d` (`release/v0.1-launch`，当前 D0-b 合入基线)
- worktree: `K:/星芒统一控制平台/wt-xmLOCAL-controls-impl`
- source snapshot audited: `ai/codex/XM-LOCAL-project-completion@50becd4`

## summary

本片先对 LOCAL 快照做了控件归属审计，再只抽取尚未进入 release 的本地预览修正。

- B004 已覆盖共享日期范围控件：`web/packages/ui-admin/src/PeriodControls.tsx`
  已提供日/周/月与显式业务日回显；请求页的自定义时间范围由已合入的
  `RequestPeriodControl` 负责。未重复修改这些组件。
- release 现有 `DataTableV2` 已按“筛选在前、搜索在后”渲染工具条，搜索框使用
  `w-40`；NewAPI 概览/财务及其局部控件留给 C004，用户、渠道、详情页和 SavedView
  均未带入本片。
- 唯一未归属且对本地实时预览有直接影响的改动是 `ee5ddf1`：Vite `/api` 代理
  支持 `XM_DEV_API_TARGET` 覆盖，默认仍为 `http://127.0.0.1:8080`。生产构建
  仍使用同源相对 API，不把目标地址编入产物。

本片不新增 API、迁移、Action、scope 或任何真实凭据操作。

## files_changed

- `web/apps/admin-web/vite.config.ts`
- `docs/handoffs/slices/XM-LOCAL-controls-impl.md`

## 控件归属审计与映射

| 范围 | LOCAL 证据 / 当前归属 | 处理 |
| --- | --- | --- |
| 财务、用户日/周/月 | B004 `PeriodControls` 与各页面 URL/Query 接线；已在 release 基线 | 不重复改 |
| 请求日/周/月/自定义区间 | 已合入 `RequestPeriodControl` + `855557f` 测试 | 不重复改 |
| 通用表格筛选顺序与短搜索 | release `ui-admin/DataTableV2.tsx`：`filters.map` 在 search input 前，搜索 class `w-40` | 不重复改 |
| NewAPI 概览/财务控件 | LOCAL `50becd4`，归 C004 slice | 不带入 |
| 用户、渠道、SavedView、详情页控件 | LOCAL 对应 USER/MAP/B003a/detail slices | 不带入 |
| 本地 API 代理目标 | LOCAL `ee5ddf1`，release 未包含 | 本片实现 |
| `/api` 代理默认目标 | `http://127.0.0.1:8080` | 保持原默认 |
| `/api` 代理本地覆盖 | `process.env.XM_DEV_API_TARGET` | 开发时可覆盖；未设置时回退默认 |

## tests_run

- `node -e 'import("vite").then(async ({loadConfigFromFile})=>{...})'` — PASS：默认目标
  `http://127.0.0.1:8080`，设置 `XM_DEV_API_TARGET=http://127.0.0.1:18088` 后解析为
  `http://127.0.0.1:18088`。
- WSL 临时 archive（`/tmp/xm-local-controls-7b52582-run`，Node 22.22.0 / pnpm 11.24.0）:
  `pnpm install --frozen-lockfile --ignore-scripts --package-import-method=copy --offline` — PASS
- `pnpm -r run typecheck` — PASS（5 个 workspace 项目；仅仓库要求 Node >=24 的 engine warning）
- `pnpm -r run test` — PASS（ui-admin 219、admin-web 881，及 design-tokens/ui-primitives）
- `pnpm --filter ui-storybook run build` — PASS（既有大 chunk warning）
- `pnpm --filter admin-web run build` — PASS（既有大 chunk warning）
- `go fmt ./...` — PASS；本片无 Go 源码改动
- `go vet ./...` — PASS
- `go test -p 1 -count=1 ./...` — PASS
- `$env:GOVERNANCE_BASE_REF='origin/main'; $env:GOVERNANCE_REQUIRE_BASE='0'; D:/Git/bin/bash.exe scripts/check-governance.sh` — PASS
- `gitleaks.exe git --redact --no-banner --log-opts=e98080d..HEAD` — PASS（无泄漏）
- `git diff --check e98080d..HEAD` — PASS

## tests_not_run

- 未启动真实 Vite/平台 API、Docker、服务器或生产环境；代理解析仅验证静态配置。
- 未设置任何真实上游地址、账号、密码、Token 或 CredentialRef。
- 未重复跑 C004/B003a/MAP/USER/详情切片的专属功能测试；这些改动明确由各自切片负责。

## risks

- `XM_DEV_API_TARGET` 是开发者本地环境变量；不得填入带 userinfo、Token 或密码的 URL。
  生产构建不读取该代理配置，但本地日志/浏览器请求仍可能暴露错误目标，需按凭据纪律配置。
- WSL 门禁使用 Node 22，而仓库 engine 要求 Node >=24；验收线应在 Node 24 环境复跑。
- 该变量只改变 Vite dev server 的 `/api` 转发目标，不改变 API 客户端、生产 Nginx
  或后端环境边界；目标服务不可用时仍由页面现有错误态呈现。

## follow_ups

- 验收线审读本 Handoff，在当前 release 上复跑门禁后合入。
- 合入 C004 等独立切片后，继续沿用本地 `XM_DEV_API_TARGET` 以指向对应预览 API；
  不把它写入生产配置。
- 若需要更严格的开发目标校验（仅允许 loopback/内网且拒绝 userinfo），另立小规格后
  再扩展 Vite 配置，当前片保持与 LOCAL 修正一致。
