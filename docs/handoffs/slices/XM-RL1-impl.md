sprint-section: 7

# XM-RL1-impl · 共享 GCRA 限流纯模型与内存参考实现

## status

READY（待验收线审读、复跑门禁并人工合入；本片不自动合并、不部署）

## branch / base / commits

- branch: `ai/codex/XM-RL1-impl`
- worktree: `K:/星芒统一控制平台/wt-xmRL1-impl`
- base: `origin/release/v0.1-launch@c657a02`（开工前已 fetch；验收日志最新为 REAL0-d
  merged、CR-0003 blocked）
- implementation commits: `6bef23c`, `687f28a`, `88167a0`
- plan: `docs/superpowers/plans/2026-08-28-shared-rate-limit.md`
- spec: `docs/superpowers/specs/2026-08-28-shared-rate-limit-design.md`

## scope / summary

本片实现 RL1 获批范围内的 transport-independent domain package：

- `Policy`、`BucketKey`、`Decision`、`ReadyState`、`Store` 与整数微秒
  `GCRAState` 契约；所有 SQL-backed 数值在边界处限制为 `math.MaxInt32`。
- `Evaluate` 使用 `gcra-v1` 的 checked integer arithmetic：ceil interval、burst
  tolerance、`max(observed,last_seen)` 防时钟回退、deny-touch、正整数 Retry-After
  向上取整；任何非法 policy/state/overflow 返回 typed error 且不产生 allow。
- v2 canonical key：`0x02 || u32be(key_version) ||` 五个 UTF-8 长度前缀字段；环境与
  principal 枚举精确校验、HTTP method ASCII token 大写、unknown route 固定
  `<unmatched-api-route>`，不接收或回落到原始 URL path。
- `HMACDigest` 只接受精确 32-byte key，输出 `[32]byte` HMAC-SHA256 digest；错误文本不
  包含 key、digest 或身份材料。
- `MemoryStore` 以互斥锁/map 提供未接线参考后端；同一 digest 并发消费精确遵循 GCRA，
  不同 environment/digest 独立，清理按 idle TTL/cleanup interval 且拒绝请求更新
  `last_seen`。

## files_changed

- `internal/platform/ratelimit/types.go`
- `internal/platform/ratelimit/gcra.go`
- `internal/platform/ratelimit/gcra_test.go`
- `internal/platform/ratelimit/key.go`
- `internal/platform/ratelimit/key_test.go`
- `internal/platform/ratelimit/memory.go`
- `internal/platform/ratelimit/memory_test.go`
- `docs/handoffs/slices/XM-RL1-impl.md`

## decisions

- `Ruling: 以内存参考模型保留显式 Initialized 位，同时对非零 TAT/LastSeen 自动视为已
  初始化` — 这样可表示 Unix epoch 的空状态，又兼容仅持久化两个整数的 future store；
  代价是调用方若构造不一致的部分状态，仍需依赖 State 校验/后续 PG decoder 拒绝。
- `Ruling: NewMemoryStore 默认将无效 policy 留在 store 内并 fail-closed，另提供
  NewMemoryStoreWithError` — 保持简单回滚装配的无错误构造器，同时让严格启动路径能够在
  构造时拒绝；代价是调用方必须选择 checked 构造器才能获得 startup error。
- `Ruling: 空 route template 规范化为固定 unknown-route sentinel` — 防止路由未匹配时
  把原始 path 形成无界桶；代价是未匹配端点共享一个保守桶，后续 HTTP 装配需发 bounded
  观测信号。

## contract / privacy boundary

- 无 SQL、migration、pgx/pool、CredentialRef、compose、HTTP middleware、router、环境
  变量或外部网络调用。
- `internal/platform/ratelimit` 未被 `cmd`、`internal/platform/httpapi`、`deploy` 或
  `db/migrations` 导入；release runtime 行为保持不变。
- Store 仅接收固定长度 HMAC digest；canonical bytes 只存在调用方内存，不进入日志或
  数据库。测试 key `00..1f` 仅作为规范 golden，不是凭据。
- 本片不实现 PostgreSQLStore、policy bootstrap、cleanup worker、ready wiring、shadow
  telemetry 或任何 live enforcement；这些属于 RL2/RL3/RL4 且仍需单独审批。

## verification

在本分支已执行：

- `go test -p 1 ./internal/platform/ratelimit -run 'Test(Policy|GCRA)' -count=1` — PASS。
- `go test -p 1 ./internal/platform/ratelimit -run TestMemoryStore -count=1` — PASS。
- `go test -p 1 ./internal/platform/ratelimit -run 'Test(Canonical|HMAC)' -count=1` — PASS。
- `go test -race -p 1 ./internal/platform/ratelimit -count=1` — PASS。
- `go test -p 1 ./internal/platform/ratelimit -count=1` — PASS。
- `go vet ./...` — PASS。
- `go test -p 1 -count=1 ./...` — PASS（全 Go 包）。
- `GOVERNANCE_BASE_REF=origin/release/v0.1-launch GOVERNANCE_REQUIRE_BASE=1 bash
  scripts/check-governance.sh` — PASS。
- `gitleaks.exe git --redact --no-banner --log-opts=c657a02..HEAD` — PASS（3 commits，
  no leaks）。
- `git diff --check origin/release/v0.1-launch...HEAD` — PASS。
- `rg -n 'internal/platform/ratelimit' cmd internal/platform/httpapi deploy db/migrations` —
  无 runtime import（预期）。

验收线应在最终 commit 上复跑完整 Go/frontend/governance/secret 门禁；本片不要求 Docker
或浏览器实机证据，因为没有 runtime effect。

## tests_not_run

- WSL Node `v22.22.0`（仓库要求 Node >=24）下 workspace typecheck 已 PASS；workspace
  test 与 Storybook build 在 `/mnt/k` 挂载盘因 `rolldown`/`oxc-resolver` 缺少对应 native
  optional binding 启动失败（`@rolldown/binding-wasm32-wasi`、`@oxc-resolver/binding-linux-x64-gnu`），
  且本片没有前端文件改动。验收线应在标准 Node 24 clean archive 重新执行前端门禁。
- 未连接任何数据库、真实上游、生产环境或读取凭据；未执行 migration/down、compose、
  deploy 或 HTTP 429/503 接线测试。

## risks / follow-ups

1. `MemoryStore` 只是单进程参考/受控回滚后端；不能宣称跨副本一致，也未接入当前
   `httpapi.RateLimiter`。
2. `Ready` 仅验证内存 policy 与绑定环境；keyring、DB active revision/version、ACL 与
   readiness composite 留给获批后续片。
3. RL2 开工前须以最新 release 动态重算 migration 编号、刷新 DBR policy/owner 证据并
   获得精确 migration/DBR 审批；不得把本片视为 SQL 或 live enforcement 授权。
4. 未来 HTTP adapter 必须在 `RequirePrincipal` 后构造 canonical identity；route 未匹配
   时只能使用 sentinel，不得将 `r.URL.Path` 传入本包。

## acceptance handoff

验收线请审读上述 7 个实现/测试文件与最终 commit，复跑门禁后人工合入 release，并在
`docs/handoffs/ACCEPTANCE-LOG.md` 追加 `MERGED <sha> XM-RL1-impl`。本分支无 PR、无
GitHub Actions 依赖、无部署操作。
