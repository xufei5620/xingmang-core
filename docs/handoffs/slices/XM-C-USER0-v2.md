sprint-section: 7

# XM-C-USER0-v2 · DailyUsage 与 Key 元数据只读扩展

## status

IN_REVIEW (acceptance evidence pending)

## branch / commit / base

- branch: `ai/codex/XM-C-USER0-v2`
- base: `72d9944` (`release/v0.1-launch`, latest ACCEPTANCE-LOG baseline)
- worktree: `K:/星芒统一控制平台/wt-xmC-USER0-v2`
- commit: see READY line after this handoff is committed

## authorization boundary

- `DAILY_USAGE_APPROVAL` (Sprint §7.2, 2026-08-30) permits the read-only, day-granular
  Fake/core DailyUsage data plane reusing `platform.users.read`.
- `KEY_SCOPE_APPROVAL` (Sprint §7.2, 2026-08-30) permits the metadata-only
  `platform.user_keys.read` scope. Full keys, token hashes, secrets, credential refs,
  copy and export remain forbidden. staff/admin defaults stay conservative; the
  dedicated `key-metadata-reader` RoleScopeMap entry is the explicit production path.
- No Sub2API/NewAPI real reader or real credential was changed or claimed.

## summary

- Added `DailyUsageReader`, CST (+08:00) closed windows, 1..31 day validation,
  deterministic Fake points, known-zero versus unknown-day coverage, and exact-user
  not-found behavior. The Fake declares this optional capability only for Sub2API;
  NewAPI stays detail-only. Real clients remain unavailable until a separate
  evidence/approval slice.
- Added `KeyMetadataReader` with bounded metadata-only fields, opaque/bounded record IDs,
  eight-code-point prefixes, Ref-bound stable cursors, null timestamps, fail-closed status
  normalization and deterministic Fake rows. Credential-like IDs are dropped at HTTP DTO
  serialization rather than reflected.
- Added capability-gated service methods and routes (capabilities are source-specific;
  the Fake currently exposes the new daily/key surfaces only for Sub2API, while a
  NewAPI real client cannot inherit them implicitly):
  `GET /api/v1/platforms/{platform}/users/{canonicalUserId}/daily-usage?day=&days=`
  (reuses `platform.users.read`) and
  `GET /api/v1/platforms/{platform}/users/{canonicalUserId}/keys`
  (requires `platform.user_keys.read`).
- Added user detail UI: Sub2API shows the seven-day coverage table and Fake/source
  freshness banner (anchored to the selected business day); the API Key tab loads only
  prefix/status/time/peak RPM metadata with Ref-bound “加载更多” pagination and no
  full-key action. NewAPI keeps its own unavailable layout and does not inherit the
  Sub2API trend or Key capability.
- Registered the approved scope in development defaults and a dedicated RoleScopeMap role,
  while asserting staff/admin do not receive it by default; updated HTTP permission docs.

## files_changed

- `connectors/platformusers/contract.go`
- `connectors/platformusers/daily_usage.go`
- `connectors/platformusers/daily_usage_test.go`
- `connectors/platformusers/fake_v2.go`
- `connectors/platformusers/key_metadata.go`
- `connectors/platformusers/key_metadata_test.go`
- `connectors/platformusers/contracttest/suite.go`
- `connectors/platformusers/contract_test.go`
- `internal/platform/platformusers/service.go`
- `internal/platform/platformusers/service_test.go`
- `internal/platform/platformusers/permissions.go`
- `internal/platform/httpapi/router.go`
- `internal/platform/httpapi/users_daily_usage.go`
- `internal/platform/httpapi/users_daily_usage_test.go`
- `internal/platform/httpapi/users_keys.go`
- `internal/platform/httpapi/users_keys_test.go`
- `internal/platform/oidcauth/rolemap.go`
- `internal/platform/oidcauth/resolver_test.go`
- `cmd/platform-api/main.go`
- `cmd/platform-api/platformusers.go`
- `web/apps/admin-web/src/api/config.ts`
- `web/apps/admin-web/src/api/users.ts`
- `web/apps/admin-web/src/api/users.test.ts`
- `web/apps/admin-web/src/components/DailyUsagePanel.tsx`
- `web/apps/admin-web/src/components/KeyMetadataPanel.tsx`
- `web/apps/admin-web/src/pages/PlatformUserDetailPage.tsx`
- `web/apps/admin-web/src/pages/PlatformUserDetailPage.test.tsx`
- `web/apps/admin-web/src/router.test.tsx`
- `docs/modules/httpapi/PERMISSIONS.md`
- `docs/modules/httpapi/AUTH-SWITCH.md`
- `docs/handoffs/slices/XM-C-USER0-v2.md`

## 格 → 数据源 / 证据映射

| 运行时格/行为 | 来源 | 状态与边界 |
|---|---|---|
| 用户每日消费趋势 | `DailyUsageReader.DailyUsage` | Fake/core 已实现；按日 CST；真实来源未接入 |
| 趋势覆盖率 | `SeriesCoverage` | expected/covered/complete；缺日保持 unknown，不补零 |
| 每日趋势新鲜度 | `DailyUsageSeries.Snapshot` | source/watermark/observed_at/is_partial 必带；Fake source 带 `-fake` |
| API Key 元数据 | `KeyMetadataReader.ListKeyMetadata` | 仅 prefix/status/created/last-used/peak RPM；不含完整凭据 |
| Key 列表权限 | `platform.user_keys.read` | 独立路由 scope；403 显示缺少该 scope，不回落 v1 前缀 |
| Key 游标 | Ref-bound opaque `km-<hex>-<offset>` | 跨用户/非法游标拒绝；页大小默认 50、上限 200 |
| 角色授权 | `oidcauth.DefaultRoleScopeMap` | 专门 `key-metadata-reader` 映射；staff/admin 默认不含 |
| 开发预览授权 | `web/apps/admin-web/src/api/config.ts` | 默认携带 scope 仅用于本地 Fake 演示；生产仍由 OIDC 决定 |

## tests_run

- `go test -p 1 ./...` — PASS
- `go test ./connectors/platformusers ./internal/platform/platformusers ./internal/platform/httpapi ./internal/platform/oidcauth` — PASS
- WSL Ubuntu-24.04 临时 archive：`pnpm -r run typecheck` — PASS (5 workspace projects; Node 22 engine warning only)
- WSL archive：`pnpm --filter admin-web exec vitest run` — PASS (60 files / 948 tests)
- WSL archive：`pnpm --filter ui-admin exec vitest run` — PASS (16 files / 232 tests)
- WSL archive：`pnpm --filter ui-storybook run build` — PASS (existing large chunk warning only)
- WSL archive：`pnpm --filter admin-web run build` — PASS (existing large chunk warning only)
- TDD RED evidence: before implementation, targeted DailyUsage/Key/HTTP tests failed with
  missing reader/type/handler symbols; after implementation the same tests pass.
- `git diff --check` — PASS (after final commit)

## tests_not_run

- Shared `xingmang-launch` Docker stack was intentionally not restarted by this slice;
  acceptance line must rebuild the affected API/web service on the running stack.
- No real Sub2API/NewAPI endpoint, credential, production system, or external write was
  accessed.

## acceptance evidence to capture

The acceptance line should run the branch on the running local stack and attach:

1. `docs/evidence/screens/XM-C-USER0-v2/` screenshots of Sub2API user detail overview,
   seven-day trend (including the unknown day), and the API Key metadata tab;
2. redacted `curl` responses for both new endpoints with dev headers, plus a 403 response
   proving missing `platform.user_keys.read` is fail-closed;
3. platform-api and platform-worker log excerpts showing the request IDs/health without
   credential material; then update this Handoff status to `READY` and add the final SHA.

## risks

- DailyUsage Fake intentionally demonstrates one unknown day; this is not real upstream
  coverage and must remain visibly labeled until a separate real-reader approval.
- Key metadata status normalization is conservative (`unknown` for unrecognized values),
  and malformed/credential-like record IDs or prefixes fail the whole page rather than
  being silently omitted.
- Development `DEFAULT_SCOPES` includes the approved metadata scope for local preview; do
  not copy that list into production OIDC role configuration. staff/admin defaults remain
  without the scope.

## follow_ups

- Acceptance line: deploy on the running `xingmang-launch` stack, capture the evidence above,
  then mark this handoff READY and merge into `release/v0.1-launch`.
- Real DailyUsage/Key readers require separate source evidence and approval; this slice does
  not infer or enable them.
