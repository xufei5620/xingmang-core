# XM-B003a-impl：个人 SavedView 持久化

## Status

READY（待验收线审读、复跑并由人工合入；本分支不自动合并、不触碰生产）

- Branch: `ai/codex/XM-B003a-impl`
- Base: `release/v0.1-launch` at `e98080dea8ac65f7bd29860b7948f04611337279`
- Source extraction: approved LOCAL commits `38e89db` (backend) and `d46d7f2` (frontend)
- Scope commit: see the final commit containing this Handoff
- Spec: `docs/superpowers/specs/2026-08-28-saved-view-persistence-design.md`
- Plan: `docs/superpowers/plans/2026-08-28-saved-view-persistence.md`

本片是把 LOCAL 中已批准的 SavedView 实现从集成线拆出的单独可审查切片。除了两项与
规格直接相关的防御修正（主标识列不可被 `defaultHidden` 隐藏；仅提供 `sortAs` 的列仍
可排序；适配器拒绝响应中不匹配当前 `table_key` 的个人视图）及 EOF 空白清理，没有带入
LOCAL 的其他未提交 UI 改动。

## 已实现

### Backend / platform API

- 新增 `ui.saved_view` 关系与 max+1 迁移 `000015_ui_saved_views.{up,down}.sql`；当前
  `release` 的已发布迁移最大号为 `000014`。
- 新增 `savedviews` domain、严格 State v1 校验/规范化、owner 派生、canonical SHA-256
  状态哈希、JSON 字节上限与尾随 JSON 拒绝。
- Store 按 `(Issuer, Subject, IdentityZone, Environment, table_key)` 隔离；Set/Remove
  使用服务端派生 advisory lock、事务内 before/after hash 证据、20/200 配额和并发安全。
- 新增 `ui.saved_view.set@1` / `ui.saved_view.remove@1` L0、HUMAN-only Actions；写入口
  仍是现有 Action Kernel，审计只记录资源 UUID 与 `state_hash`，不记录视图名、搜索词、
  筛选值或列集合。
- 新增 `GET /api/v1/ui/saved-views?table_key=...`，路由要求 `ui.saved_view.manage`，
  owner/Environment 不接受客户端参数；OIDC 默认 staff/admin 映射与 `ui.` 漂移保护已更新。
- `cmd/platform-api` 完成 Store/Action/Query 装配；生成的 sqlc 模型仅为迁移依赖的机械更新。

### Frontend

- `ui-admin` 增加 SavedView v1 wire 类型、列能力 schema、兼容折算、URL 无关的纯逻辑与
  DataTableV2 受控状态。
- DataTableV2 支持内置/个人视图分组、保存/删除 Action 状态、run_id 成功提示、失败诚实
  文案、schema 未就绪时排队、列/筛选/排序/密度兼容和一次性清空分页/选择/展开状态。
- `admin-web` 增加 API client、TanStack Query hook、`dt_*` Router Search Params 编解码、
  `PersistentDataTable` 适配器，并为审计、Sub2API/NewAPI 渠道及上游表提供五个稳定 table key：
  `global.audit.events`、`platform.sub2api.channels`、`platform.newapi.channels`、
  `platform.sub2api.upstreams`、`platform.newapi.upstreams`。
- 未启用的用户/请求/告警表仍不伪装成已持久化；其服务端上下文前置条件按规格保留。
- 任何失败路径均不写 localStorage/sessionStorage/IndexedDB/cookie，也不在 Action 完成前
  宣称保存成功。

## 格 → 数据源 / 状态映射

| UI 状态/控件 | 来源或写入路径 | 边界 |
|---|---|---|
| 个人视图列表 | `GET /api/v1/ui/saved-views` + `table_key` | HUMAN Principal 的 issuer/subject/identity zone/environment 由服务端派生 |
| 保存视图 | `ui.saved_view.set@1` Action | L0；成功后才 refetch 并展示 `action_run_id` |
| 删除视图 | `ui.saved_view.remove@1` Action | owner/environment 作用域；外部 UUID 与不存在 UUID 不可区分 |
| 搜索、筛选、排序、列、密度 | DataTableV2 `TableViewState` → `dt_*` URL 参数 | URL 显式有效字段优先；不持久化页码、游标、选择、展开、路由 tab |
| 审计摘要 | Action Kernel audit | 仅 `resource_id`、`before/after.state_hash`，不含敏感状态正文 |
| 状态/配额错误 | domain/Store fixed sentinel → 统一 Action error | 不拼接用户输入，避免 marker 泄漏 |

## Verification

以下命令均在本分支执行，依赖使用本地 pnpm store；未修改依赖版本。

- `pnpm --config.verify-deps-before-run=false -r run typecheck` — PASS（5 个 workspace 包）。
- `pnpm --config.verify-deps-before-run=false -r run test` — PASS（ui-admin 232 tests；
  admin-web 901 tests；其余 workspace 通过）。
- `pnpm --config.verify-deps-before-run=false --filter ui-storybook run build` — PASS。
- `go fmt ./cmd/platform-api ./internal/platform/httpapi ./internal/platform/oidcauth ./internal/platform/savedviews` — PASS（无格式差异）。
- `go vet ./...` — PASS。
- `go test -p 1 -count=1 ./...` — PASS。
- `git diff --check` — 在 EOF 空白清理提交后 PASS（需验收线对最终 commit 复跑）。
- `scripts/check-governance.sh` — 使用显式 WSL `GIT_DIR/GIT_WORK_TREE` 与
  `GOVERNANCE_BASE_REF=refs/remotes/origin/release/v0.1-launch`、`GOVERNANCE_REQUIRE_BASE=1`
  执行 PASS；Windows 直接 `bash` 调用无法解析 linked-worktree `.git`，不作为门禁证据。

### 未运行 / 需要验收线补做

- `XM_TEST_DATABASE_URL` 与 `XM_SCRATCH_DATABASE_URL` 均未配置，故 PostgreSQL 集成并发、
  migration up/down/re-up 测试按测试契约跳过；需要在隔离 scratch DB 上补跑并确认 `000015`
  与现有 `core.environment` 迁移链。
- 未执行真实 OIDC/Keycloak、跨浏览器/跨环境刷新、真实 API Action、真实服务器部署或
  GitHub 镜像；这些属于验收/部署阶段，不在本片授权范围。
- 未做生产迁移回退；down 文件仅用于本地开发重置，生产遵循 forward-only/备份恢复。

## 风险与后续

1. `000015_ui_saved_views` 是本片在当前 release（最大已发布号 `000014`）上分配的迁移号；
   合入前验收线需锁定该号，并让后续 RUNWAY 切片避开它（MAP0 继续使用 `000016`），不得
   在并行分支各自重新占号。
2. 部署环境若显式设置 `XM_OIDC_ROLE_SCOPES`，需由负责人把 `ui.saved_view.manage` 纳入
   staff/admin 的授权配置并复核最小权限；代码默认映射不代替人工授权审定。
3. B003a 的数据库迁移必须先在隔离数据库执行 up/down/re-up 与并发集成测试，再考虑合入；
   本片不执行任何外部数据库或生产写入。
4. 合入后应以带 backend 的本地栈做浏览器验收：刷新/第二会话、URL precedence、403/500
   降级、无浏览器存储、1440/1024/800/390 布局，以及 `grossProfit`/`margin` schemaReady
   排序回归。
5. 用户与请求表的服务端 period/cursor 状态仍未纳入 SavedView，后续需单独受控上下文切片，
   不得在本片扩大启用范围。

## 验收交付

验收线请审读本文件列出的 58 个受控变更文件与最终 commit，复跑项目门禁后人工合入；本
分支没有推送 PR、没有自动部署，也没有改变 root checkout 或 `wt-xm-local-complete`。
