sprint-section: 7

# XM-DBR1-harness：disposable PostgreSQL 18 角色隔离验证基座

## Status

READY（待验收线本地审读、复跑并由人工合入；本分支没有合并、部署或实时数据库变更）

- Branch: `ai/codex/XM-DBR1-harness`
- Base: `release/v0.1-launch` at `c657a02`（开工前/完成后已读取 `ACCEPTANCE-LOG.md`）
- Commit: `8a52cd2`（`test(database): harden disposable role probes`）
- Slice: DBR1 Task 3（仅 disposable PG18 harness / role checks；DBR2+ 迁移和 live cutover 不在范围）
- Plan: `docs/superpowers/plans/2026-08-28-database-role-separation.md` §DBR1 Task 3
- Spec: `docs/superpowers/specs/2026-08-28-database-role-separation-design.md` §12

## Summary

- `scripts/test-database-roles.ps1` 每次生成独立随机 Compose project、database、volume、
  secret；只使用 `postgres:18@sha256:7341002d…5346d7`，唯一宿主映射为
  `127.0.0.1::5432`，按本次 project/run label 查找资源。
- 启动前拒绝外部 DATABASE/PG/DOCKER DSN、Compose 覆盖、wildcard/fixed port、外部网络、
  数据目录 bind mount 与 tag-only 镜像；启动后核对 RepoDigest、PG major=18、fresh catalog、
  container/volume labels 与 volume 创建时间。
- 内部拼接无密码 loopback DSN，并通过 `pgdsn.Validate(..., true)` 与
  `pgdsn.RequireLoopback`（`cmd/pgdsn-validate --require-managed-password`）后才注入一次性
  secret 给 psql 子进程；fixture 以只读挂载的原始文件执行，不接受外部管理员 DSN。
- fixture 固定 capability/login role 名，明确 schema/table/column/sequence/type/routine grants、
  PUBLIC/default ACL、A/B membership、append-only rule/trigger、River enum 与 ops/backup
  read-only；正向和负向探针都检查 SQLSTATE/数据不变性。
- `finally` 只执行本次随机 project 的 `down --volumes --remove-orphans`，随后按 project/run
  label 枚举资源为零，并确认发现的宿主端口已关闭；任何 teardown 残留都会使脚本非零。
- `cmd/pgdsn-validate` 新增 managed-password 选项与单测，保持原有无密码/loopback 行为兼容。

## Files changed

- `scripts/test-database-roles.ps1`
- `scripts/test-database-roles.test.ps1`
- `tests/security/database-role-cluster.compose.yaml`
- `tests/security/fixtures/database-role-fixture.sql`
- `tests/security/database-role-policy.test.sh`
- `cmd/pgdsn-validate/main.go`
- `cmd/pgdsn-validate/main_test.go`
- `docs/handoffs/slices/XM-DBR1-harness.md`

## Verification

在本分支、最新 release 基线执行：

- `pwsh -NoProfile -File scripts/test-database-roles.ps1` — **PASS**；独占随机 PG18，
  `DBR1 HARNESS PASS: postgres=18 … probes=positive+negative`；teardown label=0、端口关闭。
- `pwsh -NoProfile -File scripts/test-database-roles.ps1 -ValidateOnly` — **PASS**。
- `pwsh -NoProfile -File scripts/test-database-roles.test.ps1` — **PASS**（外部 DSN、远程
  Docker、wildcard/fixed port、tag-only image 均被拒）。
- `bash tests/security/database-role-policy.test.sh` — **PASS**（WSL 环境无 pwsh 时仅提示，
  Windows 门禁已执行 ValidateOnly）。
- `go fmt ./cmd/pgdsn-validate` — PASS（无格式差异）。
- `go vet ./...` — **PASS**。
- `go test -p 1 ./... -count=1` — **PASS**。
- `git diff --check` — **PASS**。
- `gitleaks git --redact --no-banner --log-opts=c657a02..HEAD` — **PASS**（0 leaks）。
- `scripts/check-governance.sh` — **PASS**；linked worktree 使用显式 WSL
  `GIT_DIR/GIT_COMMON_DIR/GIT_WORK_TREE` 调用，Windows 直接 bash 的已知 `.git` 解析提示不计门禁。

## Tests not run / reason

- `pnpm --config.verify-deps-before-run=false -r run typecheck/test` 与 Storybook build：本片无
  前端文件；工作树首次安装在 Windows Defender/文件锁下因 `esbuild` rename `EPERM` 失败，
  因而未把未安装依赖造成的失败冒充为代码失败。验收线如需统一门禁，可在已有前端依赖缓存
  的 Linux/WSL 工作树复跑。
- 未连接 staging/production、共享 `xingmang-launch`、Keycloak 或任何上游；未读取或写入真实
  凭据；未执行 DBR2 role/owner migration、staging cutover、backup/restore。

## Security / scope decisions

1. DBR1 仅创建一次性 disposable cluster；生产角色名称固定以覆盖 policy 形状，只有 Compose
   project/database/volume/fixture 路径随机化。SQL 不做角色名/权限文本替换。
2. fixture 的 `GRANT` 探针使用 PostgreSQL 的真实语义：无 grant option 时 `GRANT` 可能以
   warning 01007 + exit 0 no-op 返回，脚本将该明确 no-op 视为拒绝，并对其它 DDL/DML 要求
   SQLSTATE 42501/55000 或 permission-denied 文本；append-only rule/trigger 另以数据不变检查。
3. volume 时间比较允许 Docker Desktop 时区偏差（24h sanity bound）；随机名称、run label 与
   preflight 空枚举是本次新鲜性/不可复用的主要证据。
4. `--require-managed-password` 是对既有 `pgdsn-validate` 的最小兼容扩展，用于证明内部
   password-free DSN 在注入匿名 secret 前通过同一 `pgdsn` 解析器。

## Risks / follow-ups

- DBR1 只证明 disposable cluster 的 policy/ACL 探针，不代表 DBR2 owner/grant SQL 已获批或
  可在 staging 执行；DBR2 仍须独立 migration approval 与 exact owner manifest。
- CI/验收机若 Docker context 不是 `default` 或系统设置 `DOCKER_HOST`，脚本会 fail-closed；
  仅在确认目标是本机 Docker daemon 后再清除该环境覆盖。
- 角色 policy/verifier（DBR1 Task 1/2）及 DBR2~4 尚未由本片实现；本 Handoff 不宣称 live
  role separation。

## Explicit boundary

`NO LIVE DB CHANGE` · `NO STAGING/PRODUCTION` · `NO REAL CREDENTIALS` · `NO MERGE` ·
`NO DEPLOY`（等待验收线按流程合入）。
