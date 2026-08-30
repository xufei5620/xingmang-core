sprint-section: 7

# XM-DBR0：PostgreSQL 角色拆分设计交接

## status

READY（docs-only；待验收线审读并由人工合入；本片没有实现、数据库操作、凭据操作或部署）

## branch / commit / base

- branch: `ai/codex/XM-DBR0-handoff`
- worktree: `K:/星芒统一控制平台/wt-xmDBR0-handoff`
- base: `e64298ebff9bfdcbac5d16f4a813dc7519e23942`（`release/v0.1-launch`，rebase 后）
- handoff commit: 见包含本文件的最终提交
- 开工前动作：已 `git fetch origin --prune` 并读取 `origin/release/v0.1-launch` 的
  `docs/handoffs/ACCEPTANCE-LOG.md` 最新记录；随后将本 docs-only 分支 rebase 到
  `e64298e`，未修改 release 检出。最新日志保留 REAL0-d 合入、CR-0003 BLOCKED、
  RL1/DS0/R210/R215 四片合入及未授权直接合并回退记录；该快照只用于交接，不授权任何
  live 操作。
- 初始分支曾按指派从 `c657a02` 建立；为满足 §7 的完成后基线要求，已在验收前安全
  rebase 到上述最新 release，且只保留本 Handoff 文档差异。

## slice / source documents

- slice: `XM-C-DBR0`（设计与实施计划交接；docs-only）
- spec: `docs/superpowers/specs/2026-08-28-database-role-separation-design.md`
- plan: `docs/superpowers/plans/2026-08-28-database-role-separation.md`
- operating rules: `docs/handoffs/CODEX-SPRINT-2026-08-29.md` §7、
  `docs/handoffs/CODEX-DEPLOY-DRIVEN-MODE.md`

## summary

当前 API、worker、迁移、bootstrap 和运维工具共用 `xingmang` 数据库登录身份；历史证据
显示该身份同时具备 superuser/owner 等能力，因而普通 ACL 不能形成运行时边界。DBR0
冻结一套可验证的目标拓扑与分片门，不改变业务 Query/Action 契约：

1. 由 `xm_migrator` 作为唯一业务对象 owner；常驻进程只继承一个 NOLOGIN capability
   role，登录身份可按 A/B 状态轮换。
2. API、worker、lifecycle、ops、backup 分离故障域；`xingmang` 仅保留为离线
   break-glass cluster admin，不能注入常驻容器。
3. 逐对象声明 table/column/sequence/routine/type/domain/default ACL；PUBLIC 默认拒绝，
   `public` schema 保持 `pg_database_owner` 且只承载 River/迁移特例。
4. append-only 的 ACL 拒绝与既有 rule/trigger 第二防线分别验证；`chain_root` 只允许
   lifecycle 更新 `exported_at`、`export_target` 两列。
5. A/B 凭据轮换必须经过 `steady-a → rotating-a-b → steady-b`，以 digest 链事件证明
   历史；当前快照不能冒充迁移历史。
6. DBR1 只允许独占、随机、loopback 的 PostgreSQL 18 disposable harness；DBR2/3/4
   逐片另批，任何 staging/production/live cutover 仍 NO-GO。

## design contract

### authority and evidence

冲突按以下权威链解决：`PROJECT-CONSTITUTION.md`（尤其条款 1、3、7、11、15、17、21、22、
27）→ `docs/adr/ADR-003-Action唯一写入口.md` →
`docs/adr/ADR-014-SecretProvider与CredentialRef.md` → PostgreSQL 18 权限语义 →
当前 compose/database/query 源码 → PR #97 与 `002_grants_evidence.sql`。规格中的
2026-08-28 staging catalog 是历史只读证据，不能当作当前 staging/production 状态，
也不能授权任何 SQL、迁移或重启。

### role topology and CredentialRefs

| capability（NOLOGIN） | 初始 LOGIN identity | 边界 |
|---|---|---|
| `xm_api_runtime` | `xm_api_a` | API Query 与现有获准 L0/L1 Action 所需 DML |
| `xm_worker_runtime` | `xm_worker_a` | River、采集、告警、retention |
| `xm_lifecycle_runtime` | `xm_lifecycle_a` | bootstrap、chain root、获批修复 |
| `xm_ops_read` | `xm_ops_a` | `audit-verify` / `platform-shadow` 只读诊断 |
| `xm_backup_read` | `xm_backup_a` | `pg_dump`/恢复演练读取 |

独立 CredentialRef 名为 `secret://database/cluster-admin`、`migrator`、
`platform-api`、`platform-worker`、`lifecycle`、`ops-readonly`、`backup`，以及只在
独占恢复演练存在的 `secret://database/restore-once`。文档只登记 ref 名，不登记值；
环境之间不得复用密码。`xm_restore_once` 不是 steady runtime role，验收后必须 NOLOGIN
并撤权。所有 LOGIN identity 均应 NOSUPERUSER/NOCREATEDB/NOCREATEROLE/NOREPLICATION/
NOBYPASSRLS，并只继承唯一 capability（INHERIT=true、SET=false、ADMIN=false）。

### object and PUBLIC policy

- API/worker/ops/backup 不拥有对象；owner/migrator 通过显式白名单管理数据库、
  `core/action/audit/ops/alerts/finance` 及 public 中 River/迁移对象。
- `action.action_run`、`audit.audit_event` 对 runtime 无 UPDATE/DELETE/TRUNCATE；
  `audit.chain_root` 的 table ACL 无 UPDATE，lifecycle 仅获两列 UPDATE。
- worker 仅保留 retention 所需的 `ops.metric_observation_sample` DELETE；worker 不得
  UPDATE/TRUNCATE；其它追加型数据由 ACL 与 rule/trigger 双层保护。
- API/worker 的 finance、registry、alerts 权限按现有代码真实 DML 最小授予；API 不获
  River，worker 不获 action/audit 写入。
- `public` schema owner 必须保持 `pg_database_owner`；撤销 PUBLIC 的数据库 CONNECT/
  TEMPORARY、schema 使用及 routine/type 默认开放。River 的表/sequence/routine/type
  只给 worker 的精确权限，`schema_migrations` 对 worker 明确拒绝。
- `ALTER DEFAULT PRIVILEGES` 只对由 `xm_migrator` 创建的未来对象生效；custom schema
  新对象默认 deny，漏列入 exact policy 或 `no-runtime-access` marker 时 fail closed。
- 禁止 `REASSIGN OWNED`；未知数据库、schema、对象、列、sequence、routine、type/domain
  或 default ACL 必须停止并刷新 owner manifest/policy。

### DSN, identity and rotation

每个连接必须显式 user 与 `application_name`（例如 `platform-api`、`platform-worker`、
`platform-migrate`、`platform-lifecycle`、`platform-ops`、`platform-backup`）。
`application_name` 仅是证据字段，不是权限边界；密码通过 audited Docker SecretProvider
注入 pgx/单次 psql 子进程，不进入日志、错误、config dump 或 Handoff。

每个 capability 的 rotation object 必须显式包含九个字段：`state`、`steady_identity`、
`old_identity`、`new_identity`、`approved_change_request`、`started_at`、`deadline`、
`closure_evidence`、`closed_at`。只允许：

- `steady-a`：仅 A 可 LOGIN，其他字段为 null；
- `rotating-a-b`：A/B 同一 capability、权限完全等价，CR 已批准且
  `started_at < deadline`，尚未关闭；
- `steady-b`：仅 B 可 LOGIN，旧连接/membership 清零，保留同一 CR 与时间并有
  `closure_evidence`/`closed_at`。

每次 policy bytes 变化只追加一条固定字段的 state event；trusted previous 必须从当前
base 的 genesis 重放并校验 SHA-256 链。`ValidateTransition` 拒绝跳态、重放、断链、
过期 rotating、跨 capability identity、CR/closure digest 不匹配。DBR0 不实现该函数，
只冻结其输入输出与拒绝语义供 DBR1 实现。

## slice boundaries and approval gates

| slice | 交付 | 本片关系 | 运行/审批边界 |
|---|---|---|---|
| DBR0 | 本设计 + implementation plan | 本片只交接文档 | GO（docs-only） |
| DBR1 | machine policy、verifier、disposable PG18 harness、CI | 后续独立分支 | 需 DBR0 后另行明确批准；只连独占 PG18 |
| DBR2 | owner/migrator/lifecycle SQL、default ACL、CredentialRef assembly | 后续独立分支 | exact migration/credentials/staging 分别审批；不得套用本文档批准 |
| DBR3 | API/worker 独立 DSN 与受限角色 E2E | 后续独立分支 | 依赖 DBR2 staging evidence；Codex 不执行 canary/cutover |
| DBR4 | ops/backup、restore、legacy lockdown | 后续独立分支 | restore/lockdown/production 各有独立门；不得自动生产 |

批准 DBR0 **不**批准 DBR1 代码、任何 GRANT/REVOKE/owner 转移、迁移、凭据设置、
staging 重启、legacy lockdown 或 production cutover。当前 live 状态统一为 NO-GO。

### DBR1 disposable harness contract (future slice)

DBR1 必须从 `VERSIONS.lock` 的完整 PostgreSQL 18 RepoDigest 启动一次独占随机 Compose
project/container/network/volume/database；唯一宿主映射是 `127.0.0.1::5432`，端口只
能按本次 project label 查询。生产角色名固定，SQL/policy 字节不得模板替换；不接受
外部 admin DSN、`DOCKER_HOST`/query host 覆盖、wildcard/fixed port、bind mount 或共享
network。首次连接前在内存构造无密码 loopback DSN，并通过 `pgdsn.Validate` 与
`pgdsn.RequireLoopback`，再单独注入一次性测试 secret。finally 只清理本次 project，
并证明 container/network/volume/端口全部消失。DBR1 的正负 ACL、append-only、default
ACL、sequence/type/domain 与 teardown 证据属于 DBR1 Handoff，不在本片伪造。

### RUNWAY / AUDIT dependencies

- RUNWAY policy/ACL 只能纳入最新获批 merge SHA 的实际对象；必须证明 SHA 是当前 base
  ancestor。`finance.runway_threshold_config` / `history` 的 runtime ACL 与 history
  append-only 约束须显式进入 DBR policy；live activation 仍等待 DBR2+DBR3。
- AUDIT archive 申请的四个专用 capability 在 policy v1 中保持 unknown/fail-closed，
  不能塞入 `xm_lifecycle_runtime`。只有 AUD design 以获批 SHA 进入当前 base 且独立 CR
  批准 exact grants 后，才能发布新 policy/version；ops/backup 不获 audit DELETE。

## files_changed

- `docs/handoffs/slices/XM-DBR0-handoff.md`

没有修改实现代码、迁移、compose、contracts、权限脚本、CI、前端或外部仓库。

## data-source mapping

| 交接内容 | 权威来源 | 读取/展示边界 |
|---|---|---|
| go/no-go 与禁止项 | DBR0 spec §§1、13、15–17；plan Global Constraints | 设计评审，不授权 live |
| 角色与 capability | spec §5；plan Goal/Architecture | 名称/边界说明，不创建 role |
| 对象 ACL/default ACL | spec §§6–9；plan DBR1 Task 1–3 | exact policy 输入，不执行 GRANT/REVOKE |
| CredentialRef/DSN | spec §10；plan DBR2 Tasks 7–9 | 只记录 ref 名，不读取值 |
| A/B rotation | spec §11；plan DBR1 Task 1 | 只冻结状态机/事件契约，不轮换凭据 |
| disposable harness | spec §12；plan DBR1 Task 3–4 | 未来独占 PG18，本文不运行 harness |
| cutover/restore | spec §13；plan DBR2–4 | 人工维护窗/独立批准，本文不执行 |
| RUNWAY/AUDIT gates | spec §14；plan Dependency Gates | 依赖检查，不吸收未知 capability |

## decisions

1. 初始按任务从 `c657a02` 建立；完成前依 §7 fetch 并 rebase 到 `e64298e` 最新
   release，验收线合入前仍需按当时最新 release 再审。
2. DBR0 维持设计规格声明的 `GO (docs only)`；DBR1 仅为 `GO WITH APPROVAL`，所有
   角色 SQL、CredentialRef 值、staging/production 均保持 NO-GO。
3. 采用 migrator-owner + 五个 capability role + A/B login，而不是共享 `app` role；
   采用显式 owner manifest，不使用宽泛 `REASSIGN OWNED`。
4. policy v1 对未来未知 AUDIT capability fail closed；RUNWAY 对象只按批准 merge SHA
   动态纳入，不能引用待审分支 head。

## verification

以下命令在本分支执行，均只读检查文档/提交结构，未连接数据库：

- Handoff 结构检查：确认首行 `sprint-section: 7`、必需章节
  `status`/`branch`/`base`/`files_changed`/`tests_run`/`tests_not_run`/`risks`/
  `follow_ups` 存在，且 `NO LIVE DB CHANGE`、`NO MERGE`、`NO DEPLOY` 边界明确 — PASS。
- 文档引用检查：spec/plan/Sprint §7/ACCEPTANCE-LOG 路径存在，DBR1–4、RUNWAY、AUDIT
  边界均可定位 — PASS。
- `git diff --check e64298e..HEAD` — PASS。
- `bash tests/security/governance-not-hollow.test.sh` — PASS。
- `GOVERNANCE_BASE_REF=e64298e GOVERNANCE_REQUIRE_BASE=1 bash scripts/check-governance.sh`
  （通过显式 worktree Git 元数据调用）— PASS。
- `gitleaks git --redact --no-banner --log-opts=e64298e..HEAD` — PASS（本片提交增量，
  未发现 secret）。

## tests_run

- 见上方 verification；本片只有 Markdown Handoff，未运行 Go、前端、Docker 或 DBR harness。
- `git status --short` 在最终提交后应为空；验收线请对最终提交重跑上述 governance、
  gitleaks 与 diff 检查。

## tests_not_run

- `go fmt`/`go vet`/`go test`、pnpm typecheck/test、Storybook、sqlc：无实现变更，未运行。
- DBR1 disposable PostgreSQL 18 harness、role verifier、fixture SQL、迁移 up/down：本片
  明确禁止运行；DBR1 需另分支、另批准。
- 共享 `xingmang-launch`、staging、production、Keycloak、Sub2API/NewAPI、外部服务器：
  未连接、未重启、未读取或写入。
- 所有真实密码、Token、CredentialRef 值、备份密钥：未读取、未生成、未进入日志/文档。

## risks

- 规格里的 staging catalog 是 2026-08-28 历史快照；任何 DBR1/DBR2 实施前必须重新采集
  并在最新 base 上重算对象、迁移和 RepoDigest。
- 本片已基于 fetch 后的 `e64298e`；后续 release 若新增对象或 policy/approval，当前
  Handoff 需在合入前刷新，不能把旧快照当授权。
- DBR1/DBR2/DBR3/DBR4 尚未由本片实现；没有 disposable ACL/restore/identity 证据，
  不能宣称已完成数据库角色隔离或生产安全加固。
- RUNWAY/AUDIT 对象与角色存在动态依赖；未知对象必须 fail closed，而不是静默放行。

## follow_ups

1. 验收线审读本 Handoff，并按最新 release 验证 `git merge-base`、ACCEPTANCE-LOG、
   RUNWAY/AUDIT 依赖后再决定是否合入。
2. 若批准 DBR1，使用独立分支完成 machine-readable policy、transition verifier、
   disposable PG18 harness 与 teardown/ACL 证据；DBR0 文档批准不自动授予该权限。
3. DBR2 先做 exact owner/ACL/default-ACL review packet，再分别请求 migration、credential
   和 staging approval；严禁把 schema 文档当作执行授权。
4. DBR3/DBR4 按 plan 顺序继续，并在任何 live 变更前提供备份恢复、连接身份和维护窗证据。

## explicit boundary

`NO LIVE DB CHANGE` · `NO STAGING/PRODUCTION` · `NO REAL CREDENTIALS` · `NO MERGE` ·
`NO DEPLOY`（本片等待验收线审读/人工合入；文档批准不延伸为实施授权）。
