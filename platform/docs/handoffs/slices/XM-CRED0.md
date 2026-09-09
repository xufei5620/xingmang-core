sprint-section: 7.6

# XM-CRED0 · 凭据管理 UI

## status

IN_REVIEW

## branch / commit / base

- branch: `ai/codex/XM-CRED0-ui`
- implementation commits: `207c2f7`, `f4e0cde`, `714e315` (rebased onto the current release before this handoff)
- base / merge-base: `4d8fd2a` (`release/v0.1-launch` at handoff creation)
- worktree: `K:/星芒统一控制平台/wt-xmC-CRED0-ui`
- delivery commit: fill in the final `READY` line after the post-rebase gates pass

## summary

This slice adds the settings-scoped credential management experience at
`/settings?sub=credentials` while leaving the existing platform-level read-only
connection panels unchanged.

- The list is a safe projection of `credential_ref`, `scope`, `updated_at`, and a
  fingerprint prefix. Unknown response fields (including an accidental secret field)
  are discarded before they reach table rows or search values.
- The form accepts a CredentialRef and a one-time pasted value. Add uses the upsert
  Action; editing an existing row starts a rotate flow with the reference read-only and
  the value blank. Successful or failed writes clear the value and mutation variables.
- Each row has a reasoned revoke dialog. All writes go through the existing
  `executeAction` transport and return only an `action_run_id` to the UI.
- Loading, empty, unavailable, permission/error, inline validation, focusable error
  summary, action receipt, and retry boundaries are explicit. No sample credential rows
  or secret values are fabricated.

## decisions

1. Route is `/settings?sub=credentials`, keeping `settings` as the existing IA entry
   because `settings` currently has no navigation sub-tabs; the eventual `/identity`
   page remains out of scope.
2. The approved product short names are mapped to the action-kernel-compatible canonical
   IDs `credential.secret.upsert`, `credential.secret.rotate`, and
   `credential.secret.revoke` (all `@1`). The UI copy may say `credential.upsert`,
   `credential.rotate`, and `credential.revoke`; the mapping is documented here so the
   future backend registration and audit action IDs do not drift.
3. Upsert/rotate use transient `credential_ref` + `secret_value`; revoke uses
   `credential_ref` + `reason`. The UI never sends a fingerprint and never computes one;
   the backend is the source of the fingerprint evidence.
4. `credential.manage` is appended to `DEFAULT_SCOPES` only as a development preview
   hint. It is not production authorization and is intentionally absent from staff/admin
   role defaults until a backend permission mapping is approved.

## 格 → 数据源 → 状态

| 页面格 / 交互 | 数据源 / 契约 | 当前状态 |
| --- | --- | --- |
| CredentialRef 列 | `GET /api/v1/credentials`; `listCredentials` safe projection | UI 已接 Query wrapper；后端路由/登记簿尚未接线，真实环境显示未接入或错误态 |
| scope 列 | Query row `scope`，并与 `secret://<scope>/<name>` 交叉校验 | UI 已接；不一致响应 fail-closed |
| 更新时间列 | Query row `updated_at`; UTC formatter | UI 已接；缺失时显示 `—`，不补当前时间 |
| 指纹列 | Query row `fingerprint`; `fingerprintPrefix` 只显示前 8 位 | UI 已接；服务端尚未生成/返回真实指纹 |
| 空列表 | Query 返回空 `items` | 明确显示「暂无凭据引用」，不填充原型样例 |
| Query 未接入 | `ACTION_NOT_REGISTERED` / `NOT_IMPLEMENTED` error code | 显示「凭据登记簿尚未接入」，表单仍作为 Action 预览边界保留 |
| 新增 / 保存 | `credential.secret.upsert@1`（产品短名 `credential.upsert`）; transient `secret_value` | UI + fake client/test seam；后端 Action/文件写入未接线 |
| 修改 / 轮换 | `credential.secret.rotate@1`（产品短名 `credential.rotate`）; ref read-only, value blank | UI + fake client/test seam；旧值绝不回读 |
| 吊销 | `credential.secret.revoke@1`（产品短名 `credential.revoke`）; reason required | UI + fake client/test seam；后端状态/审计未接线 |
| Action 回执 | existing `ActionResultNote`, `action_run_id` only | UI 已接；不承诺审计追加原子性 |
| Action 错误 | existing `ActionErrorNote` + local secret-string redaction | UI 已接；后端错误契约仍需保持不回显 |
| SecretProvider 目录 | Approved contract: `var/secrets/<env>/<scope>/<name>`, API rw, atomic write, mode 0600; worker read-only, resolve on each use | 仅在本片 decisions/follow-up 记录，未实现或挂载 |
| 审计 projection | ref/scope + fingerprint 前 8 位，永不含值 | 仅 Action 参数/文案边界；后端审计 handler 未接线 |

## files_changed

- `docs/superpowers/plans/2026-08-30-xm-cred0-ui.md`
- `web/apps/admin-web/src/api/config.ts`
- `web/apps/admin-web/src/api/credentials.ts`
- `web/apps/admin-web/src/api/credentials.test.ts`
- `web/apps/admin-web/src/lib/credentialForm.ts`
- `web/apps/admin-web/src/lib/credentialForm.test.ts`
- `web/apps/admin-web/src/components/CredentialManagementPanel.tsx`
- `web/apps/admin-web/src/components/CredentialManagementPanel.test.tsx`
- `web/apps/admin-web/src/pages/CredentialsPage.tsx`
- `web/apps/admin-web/src/pages/SettingsPage.tsx`
- `web/apps/admin-web/src/router.test.tsx`
- `docs/evidence/screens/XM-CRED0-ui/settings-credentials-unavailable.png`
- `docs/handoffs/slices/XM-CRED0.md`

## TDD evidence

- RED: before implementation, the focused Vitest invocation failed with module-not-found
  errors for `./credentials`, `./credentialForm`, and
  `./CredentialManagementPanel`.
- GREEN: the same focused suite now covers API/form/panel/router behavior (159 tests in
  the post-hardening run); all pass after implementation.
- The tests assert no secret field survives the list projection, no value is rendered
  after a write, ref-only revoke payloads, rotate/upsert Action paths, and local
  redaction when an erroneous provider message contains the pasted value.

## tests_run

- `pnpm --config.verify-deps-before-run=false -r run typecheck` — PASS (5 workspace packages).
- `pnpm --config.verify-deps-before-run=false -r run test` — PASS (design-tokens 10,
  ui-primitives 16, ui-admin 232, admin-web 980; Storybook package has no unit tests).
- `pnpm --config.verify-deps-before-run=false --filter ui-storybook run build` — PASS,
  Storybook build completed successfully; existing >500 kB chunk warning only.
- `pnpm --config.verify-deps-before-run=false --filter admin-web run build` — PASS;
  existing >500 kB chunk warning only.
- `D:/Git/bin/bash.exe scripts/check-governance.sh` — PASS on the implementation range.
- `gitleaks git --redact --no-banner --log-opts=<release-base>..HEAD` — PASS, no new
  leaks detected on the implementation commits.
- `git diff --check <release-base>..HEAD` — PASS.
- Local browser preview at
  `docs/evidence/screens/XM-CRED0-ui/settings-credentials-unavailable.png` — desktop
  layout, return link, safe form labels, and explicit backend-unavailable/error boundary
  visually inspected. The proxy was intentionally not connected to a real API.

## tests_not_run

- No Go files changed; `go fmt`, `go vet`, and `go test` were not required for this UI-only
  slice (acceptance line may run its standard full Go gate).
- No real platform API, SecretProvider directory, KMS/vault, connector token resolver,
  Action registry, audit writer, Docker stack, production host, Keycloak, or true
  credential was accessed.
- The browser screenshot shows the Query error boundary because the local API proxy was
  unavailable; it is not evidence of backend Action/SecretProvider integration.

## risks

- The current backend Action kernel enforces three-segment IDs, while the approval log
  uses two-segment product names. This slice deliberately keeps the canonical
  `credential.secret.*` IDs and records the short-name mapping; backend registration and
  audit consumers must confirm this mapping before merge.
- Query and all three Actions are frontend seams only. Until backend handlers are added,
  the page cannot prove file atomicity/mode 0600, audit projection, dynamic resolve, or
  connector file-first/env-fallback behavior.
- Frontend `DEFAULT_SCOPES` is a preview hint, not an authorization decision. A server
  permission mapping is still required, and production staff/admin defaults must remain
  without `credential.manage`.
- The shared shell keeps a fixed navigation rail at very narrow widths; this slice keeps
  `min-w-0` and responsive stacking but does not redesign the global shell.

## follow_ups

- Backend slice: register the canonical `credential.secret.*@1` Actions, enforce the
  `credential.manage` permission, write `var/secrets/<env>/<scope>/<name>` atomically with
  0600 permissions, and emit ref/scope/fingerprint-prefix-only audit projections.
- SecretProvider/connector slice: file provider first, approved env fallback second, and
  dynamic resolve on each worker use without restart; add local fake flow and redacted
  verification evidence.
- Acceptance line: rebase this branch onto the latest release tip, rerun the listed gates,
  then change status to `READY` with the final delivery SHA and merge only from the
  acceptance line.

