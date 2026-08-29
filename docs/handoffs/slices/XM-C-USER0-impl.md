# XM-C-USER0-impl · 平台用户只读 v2 核心 / 精确用户详情

## status

READY

## branch / commit / base

- branch: `ai/codex/XM-C-USER0-impl`
- base: `e98080d` (`release/v0.1-launch`, D0-b READY)
- worktree: `K:/星芒统一控制平台/wt-xmC-USER0-impl`
- implementation commits: `53a81d0`（核心实现）、`abbed2e`（前端 fixture 类型修正）
- handoff commit: 本文件所在最新提交（见 READY 行）

## authorization boundary

本片只实施已批准 USER0 CORE Task 1–3：结构化 `UserRef`、canonical 用户 ID
编码、Fake `UserDetailReader`、服务层精确查询、HTTP DTO/路由和 B001 详情页切换。
未发现独立的人类 `DAILY_USAGE_APPROVAL` 或 `KEY_SCOPE_APPROVAL` 证据，因此本片
明确不实现 DailyUsage、Key metadata、任何 Sub2API/NewAPI real reader 或新 scope。
这两项保持后续 BLOCKED，不从 spec merge、CORE 或 fake capability 推定授权。

## summary

- 新增 `UserRef { platform, id }` 与 UTF-8 小写 hex `u-…` canonical codec，严格
  拒绝空值、裸 ID、大小写/奇数 hex、非法 UTF-8 和超过 512 字节的 ID；Go/TS 共用
  golden fixture。
- 新增 `UserDetailReader`/`GetUserQuery`/`UserDetail`/`EvidenceSnapshot`，Fake
  客户端按来源和完整 ID 精确匹配，返回 masked email、统计区间和 Fake freshness；
  不按 username/email/token prefix 关联。
- 服务层只在客户端实现 detail capability 时挂载读取，统一映射 not-found、
  incomplete、取消和上游错误；没有 capability 时返回 `ADVANCED_CONTROLS_REQUIRED`。
- 新增 `GET /api/v1/platforms/{platform}/users/{canonicalUserId}`，在调用查询前
  严格验证路径段，显式 DTO 只输出允许字段；详情页改用 canonical detail Query，
  保留按日/周/月 URL 控件与 Fake/来源/新鲜度展示。原有 v1 列表与有界扫描 helper
  保留兼容，但详情页不再扫描列表。

## files_changed

- `connectors/platformusers/contract_v2.go`
- `connectors/platformusers/contract_v2_test.go`
- `connectors/platformusers/exact_lookup.go`
- `connectors/platformusers/exact_lookup_test.go`
- `connectors/platformusers/fake_v2.go`
- `connectors/platformusers/userref.go`
- `connectors/platformusers/userref_test.go`
- `connectors/platformusers/contract.go`
- `contracts/connectors/platformusers.read.v2.md`
- `contracts/testdata/platform-user-ref-v1.json`
- `internal/platform/platformusers/service.go`
- `internal/platform/platformusers/service_detail_test.go`
- `internal/platform/httpapi/users_detail.go`
- `internal/platform/httpapi/users_detail_test.go`
- `internal/platform/httpapi/router.go`
- `cmd/platform-api/main.go`
- `cmd/platform-api/platformusers.go`
- `web/apps/admin-web/src/api/users.ts`
- `web/apps/admin-web/src/api/users.test.ts`
- `web/apps/admin-web/src/pages/PlatformUserDetailPage.tsx`
- `web/apps/admin-web/src/pages/PlatformUserDetailPage.test.tsx`
- `web/apps/admin-web/src/router.tsx`
- `web/apps/admin-web/src/router.test.tsx`

## 格 → 数据源 / 证据映射

| 运行时格/行为 | 来源 | 状态与边界 |
|---|---|---|
| 用户详情基础事实 | `platformusers.UserDetailReader.GetUser` | CORE Fake 已实现；real 未接入 |
| 用户 ID 路径 | `UserRef` + `u-` UTF-8 hex codec | canonical；512-byte 上限；跨平台隔离 |
| 统计区间 | 服务端 `Period.Normalize` | `day` / `week` / `month`；CST +08:00 |
| 余额/区间充值/区间消费/近 30 天消费 | `UserDetail.User` | `Amount.Known=false` 显示未知，不冒充 0 |
| 来源/观测时间/水位 | `UserDetail.Snapshot` | Fake 明示 `sub2api-fake`/`newapi-fake` |
| 详情 Not Found | `ErrNotFound` | `404` + `ACTION_NOT_REGISTERED` |
| 精确查询未完成 | `ErrLookupIncomplete` | `502` + `EXECUTION_FAILED`，可重试 |
| DailyUsage 趋势 | 未来独立 `DailyUsageReader` | BLOCKED：缺 `DAILY_USAGE_APPROVAL` |
| API Key 元数据 | 未来独立 `KeyMetadataReader` | BLOCKED：缺 `KEY_SCOPE_APPROVAL`，无 `platform.user_keys.read` |
| 充值/开票/请求详情 | payment/invoice/reqlog 各自领域 | 本片不跨域聚合、不新增关联表 |

## tests_run

- `go fmt ./connectors/platformusers ./internal/platform/platformusers ./internal/platform/httpapi ./cmd/platform-api` — PASS
- `go test ./connectors/platformusers ./internal/platform/platformusers ./internal/platform/httpapi ./cmd/platform-api` — PASS
- `go vet ./...` — PASS
- `go test -p 1 -count=1 ./...` — PASS（全部 Go 包）
- `pnpm --config.verify-deps-before-run=false --filter admin-web exec vitest run src/api/users.test.ts src/pages/PlatformUserDetailPage.test.tsx src/router.test.tsx` — PASS（189 tests）
- WSL Ubuntu-24.04 临时 archive：`pnpm install --frozen-lockfile --ignore-scripts --package-import-method=copy --offline`、`pnpm -r run typecheck`、`pnpm -r run test` — PASS（admin-web 47 files / 888 tests；ui-admin 219 tests；Node 22 engine warning）
- WSL 临时 archive：`pnpm --filter ui-storybook run build`、`pnpm --filter admin-web run build` — PASS（保留既有大 chunk warning）
- `D:/Git/bin/bash.exe tests/security/governance-not-hollow.test.sh` — PASS
- `D:/Git/bin/bash.exe scripts/check-governance.sh` — PASS
- `gitleaks git --redact --no-banner --log-opts=e98080d..HEAD` — PASS（3 commits，no leaks found）
- `git diff --check` — PASS

## tests_not_run

- 未访问真实 Sub2API/NewAPI、未新增真实凭据/scope、未执行生产部署或外部写操作。

## risks

- 详情页的 `PlatformUserLookupResult.incomplete` 分支保留用于错误兼容；v2 成功路径不再客户端扫描。
- Fake 仅用于本地演示，数据源带 `-fake`；real 响应形状和字段仍需脱敏证据与独立审批。
- Windows worktree 依赖链接可能触发既有 UI Input 类型漂移；标准 WSL archive 已通过完整 typecheck/build，Node 22 仅有仓库要求 Node >=24 的 warning。

## follow_ups

- 验收线审读并复跑本片全量门禁后，合入 `release/v0.1-launch`。
- 获得独立 `DAILY_USAGE_APPROVAL` 后另开 Task 6 slice；获得 `KEY_SCOPE_APPROVAL` 后另开 Task 7 slice。
- Sub2API/NewAPI real reader 必须在各自真实证据、scope 和 contracttest 审批后独立实现；本片不暗示任何 real 授权。
