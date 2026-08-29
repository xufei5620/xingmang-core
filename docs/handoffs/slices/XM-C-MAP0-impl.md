# XM-C-MAP0-impl · 平台渠道绑定与上游分组映射

## status

READY

## branch / commit / base

- branch: `ai/codex/XM-C-MAP0-impl`
- worktree: `K:/星芒统一控制平台/wt-xmC-MAP0-impl`
- base: `e98080d` (`release/v0.1-launch`, 当前 D0-b 合入头)
- implementation head: `2dbf709`
- spec approval: `c882507`（XM-C-MAP0 docs-only approval）
- migration: `000016_finance_platform_channel_binding`；`000015` 预留给并行
  B003a SavedView slice，故本片保持连续后的 000016，不假设 000013 空闲。

## summary

本片从 LOCAL 的已批准实现中只抽取渠道映射相关内容，并以 release 为基线重放：

- MAP1：Sub2API 完整渠道目录 v2（保留无余额概念账号、nullable balance、独立
  completeness/coverage 证据）与 NewAPI v2 completeness envelope；真实/Fake/factory/
  Worker 均静态暴露 v2，legacy 余额/状态由同一次目录读取的纯适配器产生。
- MAP2：新增时间版本化 `finance.platform_channel_binding` 表、环境复合外键、
  canonical ChannelRef、活动唯一键/历史索引、候选四态（unmapped/candidate/
  conflict/orphan）、L1 HUMAN-only set/remove Actions、`finance.read` Query 与
  `CONFLICT`/`PRECONDITION_FAILED` 错误映射。绑定只写人工确认记录，不改 token_map、
  platform_id 或历史 profit_daily。
- MAP3/4 读侧骨架：提供 `/api/v1/platforms/{platform}/channels`、ChannelRef 行键、
  共享 runway/经营字段的显式 null/状态占位，以及 Sub2API/NewAPI 的渠道目录表。
  该端点目前遵循 fail-closed UI 约定；raw-ledger 历史经济投影与真实 runway join
  仍是后续独立 projection slice，不能把占位字段当作已接通数据。
- 仅保留本片所需的 `ChannelTable`/`ChannelsPanel`/`NewApiChannelsPanel` 接线和
  `ManagedChannelTable`；没有引入 USER0、SavedView 持久化或 PersistentDataTable 文件。
  `ManagedChannelTable` 使用 release 已有 `DataTableV2`。

## files_changed

### Connector / contracts

- `connectors/sub2api/channel_directory.go`, `channel_directory_test.go`
- `connectors/sub2api/{client.go,contract.go,fake.go,upstream.go,client_contract_test.go}`
- `connectors/newapi/channel_directory.go`, `channel_directory_test.go`
- `connectors/newapi/{client.go,contract.go,fake.go,upstream.go}`
- `contracts/connectors/sub2api.read.v2.md`
- `contracts/connectors/newapi.channel-directory.v2.md`

### Binding / HTTP / persistence

- `db/migrations/000016_finance_platform_channel_binding.{up,down}.sql`
- `db/queries/finance.sql`
- `internal/platform/finance/channel_binding*.go`
- `internal/platform/finance/channel_projection*.go`
- `internal/platform/finance/gen/{finance.sql.go,models.go}`
- `internal/platform/httpapi/{channel_bindings.go,channel_bindings_test.go,platform_channels.go}`
- `internal/platform/httpapi/{response.go,response_test.go,router.go}`
- `cmd/platform-api/main.go`
- generated model mirrors under `internal/platform/{action,alerts,audit,ops,registry}/gen`
- `internal/platform/jobs/{sub2api_sync.go,sub2api_sync_test.go,newapi_sync.go,newapi_sync_test.go}`
- `internal/platform/{oidcauth/resolver_test.go,ops/freshness.go,ops/metrickeys_test.go}`
- `internal/platform/finance/permissions.go`
- `contracts/actions/finance.platform_channel_binding.{set,remove}.v1.json`
- `docs/modules/{connector,finance}/README.md`, `docs/modules/httpapi/PERMISSIONS.md`

### Admin UI

- `web/apps/admin-web/src/api/platformChannels.{ts,test.ts}`
- `web/apps/admin-web/src/components/ManagedChannelTable.{tsx,test.tsx}`
- `web/apps/admin-web/src/components/{ChannelTable.tsx,ChannelsPanel.tsx,NewApiChannelsPanel.tsx}`
- `web/apps/admin-web/src/pages/PlatformDetailPage.tsx`

## 格 → 数据源 / 证据映射

| UI/HTTP 格 | 数据源 | 诚实性闸门 |
|---|---|---|
| 渠道行 / ChannelRef | Connector `*.channels.status` observation 的 `channel_id` + service UUID | 只接受匹配 service instance 的成功目录；空目录不由 finance 账号反造 |
| 上游映射状态 | `finance.platform_channel_binding` + `token_map` candidate evaluator | 四态与 evidence status 分开；冲突/孤儿不隐藏 |
| 目录完整性 | `inventory_completeness`（reported/fetched/truncated/evidence） | `coverage_partial` 不合并进 complete；未初始化/失败为 unknown |
| 余额/模型/健康 | Sub2 v2 nullable balance；NewAPI v1 channel status | 未接入字段为 null/「未接入」，不写 0 或伪造 assurance |
| 经营核算 / runway | 后续 raw-ledger/projection store；本片响应字段 | 当前明确 `null` + `economics_state`/coverage 占位，不宣称已接通 |
| set/remove Action | `/api/v1/actions/...` + binding migration | L1、HUMAN-only、manage scope 默认角色不授予、审计 receipt |

## tests_run

- `go fmt ./...` — PASS
- `go vet ./...` — PASS
- `go test -p 1 -count=1 ./...` — PASS（全部 Go 包）
- `go test ./internal/platform/finance ./internal/platform/httpapi -run 'TokenEvidence|Binding|PlatformChannels|Projection' -count=1` — PASS
- `pnpm --config.verify-deps-before-run=false --filter admin-web test` — PASS（49 files / 885 tests）
- `pnpm --config.verify-deps-before-run=false -r run typecheck` — PASS
- `pnpm --config.verify-deps-before-run=false -r run test` — PASS（ui-admin 219 tests、
  admin-web 885 tests、design-tokens 10、ui-primitives 16）
- WSL 临时 archive（Node 22.22.0，`pnpm install --frozen-lockfile --ignore-scripts
  --package-import-method=copy --offline`）执行 workspace typecheck、workspace test、
  Storybook build、admin-web build — PASS
- `GOVERNANCE_BASE_REF=release/v0.1-launch GOVERNANCE_REQUIRE_BASE=1
  bash scripts/check-governance.sh`（Git Bash）— exit 0
- `gitleaks.exe git --redact --no-banner --log-opts='e98080d..HEAD'` — PASS（no leaks found）
- `git diff --check` — PASS
- `git diff --name-status release/v0.1-launch..HEAD` — 无删除；仅列上述 MAP connector/
  binding/projection/UI/docs 文件，未带 USER0、SavedView 或 D0 资产越界变更。

## tests_not_run

- 未连接真实 Sub2API/NewAPI、真实 PostgreSQL 或执行迁移；绑定集成测试因未设置
  `XM_TEST_DATABASE_URL` 按设计 skip。
- 未在真实服务器执行部署、远程 Git 推送、OIDC/Keycloak 或第三方 API 写入。
- 未做浏览器视觉验收；WSL 构建只证明静态产物可构建。

## risks / follow_ups

- 本片的 MAP3/4 是 LOCAL 已批准实现的 fail-closed 读侧骨架：`economics`、
  `runway`、模型 assurance 等未有真实来源时保持 null；后续应从人审合并后的 MAP
  projection slice 接入 repeatable-read raw ledger、历史 binding 匹配、重复收入冲突
  与 distinct upstream runway，不能在 UI 层补算。
- `TokenEvidence` 现在带 active service count 与 system type，仍需真实目录/服务数据
  才能形成 candidate；production 不自动确认。
- 迁移 000016 只建表/约束/索引，不插入存量 binding；生产回滚遵循 forward-only 纪律。
- 验收线需审读本 Handoff 与 `e98080d..2dbf709` 差异，复跑门禁后再合入 release；
  本片没有 merge 或 deploy 权限。
