sprint-section: 7.5

# XM-DS1 · 指标降采样 DS1 policy/schema/accumulator

## status

READY

DS1 完整 Task 组已交付：版本化 policy、raw metadata/schema、UTC 日桶纯整数
accumulator、writer cadence 传播，以及一次性 PostgreSQL 18 harness。没有启动 DS2
receipt worker、DS3 API/UI、DBR 权限或任何 raw DELETE。

## branch / commit / base

- branch: `ai/codex/XM-DS1-impl`
- worktree: `K:/星芒统一控制平台/wt-xmDS1-impl`
- base: `release/v0.1-launch@37a1be81a7fc205bf1e7649bd1d1e0142dd25093`
- implementation head: `c23aa9957d6b43d4a7099df55db61f92052ece9d` (delivery commit is
  reported on the final READY line after this Handoff is committed)
- GitHub-origin DNS 在 preflight 期间不可用；已先从本地验证的 corrected tip
  `9495e3b` rebase，再从更新的 merged tip `37a1be8` rebase 后 squash。

## scope / summary

- `contracts/ops/metric-rollup-policy.v1.json` + strict loader 冻结 policy v1、UTC
  bucket、整数/fixed-point pointers、full/partial flags 和 snapshot sum forbidden；
  duplicate/unknown/trailing JSON、key/version/pointer/scale drift fail closed。
- 当前 release registry 实测 16 项（DS0 旧表为 15，漏了既有
  `sub2api.channels.status`）。保留既有能力：14 项 active policy 精确覆盖，
  `invoice.requests.daily` / `invoice.amount.daily` 以 CR-0002 显式 exclusion 保留；
  `RollupPolicyFor` 与 accumulator 返回 typed `ErrInvoiceRollupGated`，不会生成
  invoice v1 policy。
- migration `000019_metric_downsampling` 新增 raw policy/cadence 元数据、索引、UTC
  daily accumulator、receipt、per-stream state。只创建 approved table/index/check
  objects；无新增 function/rule/trigger，避免改变 DBR1 object inventory。语义
  error-count/currency 校验在纯 Go validator；append-only ACL/trigger 延后 DBR2/DS4。
- `internal/platform/ops/rollup.go` 使用 `big.Int`，failed/partial 不进入权威 numeric，
  mixed currency 清空 numeric 并保留 currency set，记录 quality/error/slot coverage，
  以 `(synced_at,id)` 全序且拒绝 duplicate/non-increasing ID 与 stream/day/policy mismatch。
- Sub2API、NewAPI、finance periodic writers 显式持久化实际 cadence + policy version；
  zero/manual caller 保持诚实 NULL cadence。

## files_changed

```text
contracts/ops/metric-rollup-policy.v1.json
db/migrations/000019_metric_downsampling.down.sql
db/migrations/000019_metric_downsampling.up.sql
db/queries/ops.sql
docs/modules/ops/DATA-MODEL.md
docs/modules/ops/README.md
internal/platform/jobs/client.go
internal/platform/jobs/cost_sync.go
internal/platform/jobs/newapi_sync.go
internal/platform/jobs/rollup_metadata.go
internal/platform/jobs/rollup_metadata_test.go
internal/platform/jobs/sub2api_sync.go
internal/platform/ops/freshness.go
internal/platform/ops/gen/models.go
internal/platform/ops/gen/ops.sql.go
internal/platform/ops/rollup.go
internal/platform/ops/rollup_policy.go
internal/platform/ops/rollup_policy_test.go
internal/platform/ops/rollup_schema_integration_test.go
internal/platform/ops/rollup_test.go
internal/platform/ops/store.go
scripts/test-metric-rollup.ps1
tests/security/metric-rollup-cluster.compose.yaml
tests/security/metric-rollup-pg.test.sh
docs/handoffs/slices/XM-DS1.md
```

## migration / policy evidence

- base migration max `18`; DS1 migration `000019`。
- up hash `0d9dc5221ad630078bf84c7fe07c1b0f57a8ed0a`
- down hash `739aa8fe2db930e2b4e56fb4dad966abefdb76fb`
- `db/queries/ops.sql` hash `fe28021b91e65e45613101c3bd8517b74a055acc`
- policy contract hash `6c5b66612341010eaf47ce7a3ed5cb0a03e5a1b6`
- binary diff digest (two migrations + ops query) `33273f75306e715217c3357d6f21b40ba95c9668`

## tests_run

- TDD RED: `go test -p 1 ./internal/platform/ops -run 'TestPolicy|TestAccumulator' -count=1 -v`
  initially failed with missing `ValueKind`/`RollupPolicy`/`RawRollupSample` interfaces;
  this feature-missing failure was observed before implementation.
- policy/accumulator/invoice-gate/integer-quality-coverage/writer-cadence targeted tests — PASS。
- `go tool sqlc generate` — PASS (only `internal/platform/ops/gen/*` retained)。
- `go test -p 1 ./...` — PASS。
- `go vet ./...` — PASS。
- `go test -race -p 1 ./internal/platform/ops ./internal/platform/jobs` — PASS。
- `git diff --check` — PASS。
- `D:\Git\bin\bash.exe scripts/check-governance.sh` with
  `GOVERNANCE_BASE_REF=release/v0.1-launch GOVERNANCE_REQUIRE_BASE=1` — PASS。
- `gitleaks git --redact --no-banner --log-opts='37a1be8..HEAD'` — PASS, one commit，no leaks。
- `bash tests/security/metric-rollup-pg.test.sh` — PASS (static boundary checks)。
- `pwsh -NoProfile -File scripts/test-metric-rollup.ps1 -ValidateOnly` — PASS。
- Full disposable harness `pwsh -NoProfile -File scripts/test-metric-rollup.ps1` — PASS：
  random Compose project、locked PG18 image、actual RepoDigest、project/container/volume
  labels、fresh catalog、database comment sentinel、10 个 schema/cadence tests 和
  label-verified teardown 全通过。脱敏 run 例：project `xm-ds1-d1b3c29f285a`，loopback
  port `19331`，database `xm_rollup_d1b3c29f285a`。

## tests_not_run

- `pnpm -r run typecheck`、`pnpm -r run test`、Storybook build：未运行（DS1 无 web 变更，
  DS3 UI 仍 NO-GO）。
- 生产/staging、真实 upstream/credentials、DBR grants、DS2 receipt/backfill、DS3 API、
  DS4 retention/delete、backup/restore：均未运行；本片仅 schema/纯算法/一次性 PG。
  本片含迁移，合入后由验收线决定是否按节流规则重建栈；Codex 未部署。
- `git fetch origin` 因 GitHub DNS 失败；未据此推断远端状态，基线以本地 corrected release
  commit 记录为准。

## risks / decisions

- CR-0002 仍为“待开票线确认”；invoice 两指标显式 gated，不能静默聚合。
- active registry/policy 为 14+2（16 total），记录了 DS0 后 `sub2api.channels.status`
  漂移且不删除既有能力。
- daily/receipt/state 仅为 DS1 schema 基座；DBR1 policy 未编辑，DBR2/DS4 必须先加入
  精确 grants/append-only runtime ACL 才能启用 worker。
- `XM_METRIC_DOWNSAMPLING_ENABLED` / `XM_METRIC_RAW_DELETE_ENABLED` 未新增或开启；
  本片不存在 raw 删除路径。

## follow_ups

1. 验收线审读 exact head/diff 并追加 `MERGED <sha>`。
2. 若 release 在合入前移动，按新 exact tip 重新计算 migration number 与 diff digest。
3. DS2 仅在 DS1 merge 后按最新 acceptance tail 开工；DS3/DS4 审批独立。
