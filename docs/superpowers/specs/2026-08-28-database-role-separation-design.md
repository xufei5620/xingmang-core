# XM-C-DBR0 PostgreSQL 角色拆分设计规格

> **状态：审批稿；只含 Plan / Design，不含实现或部署授权。**
>
> 本文批准后仍不得创建/修改数据库角色、执行 GRANT/REVOKE、转移 owner、
> 改 compose/DSN、生成或轮换真实凭据、运行迁移、重启 staging，亦不得触碰生产。
> DBR1～DBR4 每片必须分别获得明确授权、独立 worktree/分支/PR，并由人类合并。
>
> 基线：`release/v0.1-launch@543087f`。动态 staging 证据采集于 2026-08-28，
> 只能证明当时本机 `xingmang-launch` 的状态；每次实施前必须重新采集。

## 1. 意图与 go/no-go

当前 API、worker、迁移、bootstrap、运维查询共用 PostgreSQL 登录身份 `xingmang`。
该身份既是 superuser，又拥有数据库、业务表、River 表与序列。于是文档中的
`REVOKE UPDATE/DELETE` 对运行身份没有约束力：superuser 与 owner 都能绕过普通 ACL，
append-only 主要依赖 rewrite rule，而 `ops.metric_observation_sample` 连 rule 都没有。

本设计把对象所有权、运行时读写、生命周期操作和运维读取拆成不同故障域，使：

1. API/worker 不再是 superuser、owner、CREATEROLE、CREATEDB、REPLICATION 或 BYPASSRLS；
2. migrator 是唯一业务对象 owner，且凭据不进入常驻运行容器；
3. API 与 worker 只取得现有代码真实需要的对象级权限；
4. bootstrap/审计链根/获批修复使用独立 lifecycle 身份，不复用 migrator；
5. audit/Action/RUNWAY history 的 UPDATE/DELETE/TRUNCATE 在 ACL 层硬拒绝；
6. ops/backup 只读，默认事务只读，凭据短时、独立轮换；
7. 未来迁移不会因 default privileges 漂移重新把权限放大；
8. 每次部署、轮换、回滚都能从 `current_user`、`application_name`、ACL 与恢复演练证明。

当前裁决：

- **DBR0 文档切片：GO**；
- **DBR1 verifier/测试基座：可在人类批准后实施，且只准 disposable PG18：GO WITH APPROVAL**；
- **任何 staging 角色/owner/DSN 变更：NO-GO，直到 DBR2 另批**；
- **任何生产 cutover：NO-GO，须在 staging 证据、备份恢复演练、独立变更单之后另批**。

## 2. 权威链

冲突时按以下顺序裁决：

1. `PROJECT-CONSTITUTION.md`：条款 1、3、7、11、15、17、21、22、27；
2. `docs/adr/ADR-003-Action唯一写入口.md`：Lifecycle Operation 不走普通 Action，
   但必须版本化、人工批准、制品校验和独立审计；
3. `docs/adr/ADR-014-SecretProvider与CredentialRef.md`：凭据只经 CredentialRef；
4. PostgreSQL 18 官方权限语义：
   - <https://www.postgresql.org/docs/18/ddl-priv.html>
   - <https://www.postgresql.org/docs/18/sql-grant.html>
   - <https://www.postgresql.org/docs/18/sql-alterdefaultprivileges.html>
   - <https://www.postgresql.org/docs/18/role-membership.html>
   - <https://www.postgresql.org/docs/18/sql-reassign-owned.html>
5. `deploy/compose/launch.yaml`、`cmd/*/database.go`、`db/queries/*.sql`：当前装配与真实 DML；
6. PR #97 与 `deploy/bootstrap/002_grants_evidence.sql`：当前缺口的可复验来源。

## 3. 当前证据

### 3.1 PR #97

GitHub PR #97：

- 标题：`XM-0050: Codex 冷审遗留三项加固(R009 canonical 碰撞 / R011 限流 / R012 保留期)`；
- 状态：MERGED；
- merged_at：`2026-08-27T23:26:44Z`；
- merge commit：`223d79e35d813eae924beeaf31888a30767585fa`；
- governance / secret-scan / backend / frontend 四项检查均 SUCCESS；
- URL：<https://github.com/xufei5620/xingmang-platform/pull/97>。

PR #97 **没有**拆角色。它新增 SELECT-only `002_grants_evidence.sql`，并明确把
owner/runtime 拆分留为后续部署拓扑任务。

### 3.2 staging catalog 快照

2026-08-28 从本机 `xingmang-launch` 执行 #97 脚本及只读 catalog 查询，得到：

| 事实 | 实测结果 | 后果 |
|---|---|---|
| 非系统角色 | 只有 `xingmang` | 无 API/worker/migrator/ops 隔离 |
| 角色属性 | superuser、CREATEROLE、CREATEDB、LOGIN、INHERIT、REPLICATION、BYPASSRLS 全为 true | 任意 ACL 不能形成边界 |
| 数据库 owner | `postgres`、`template0`、`template1`、`xingmang`、遗留 `xm0050_test` 均属 `xingmang` | 不能使用宽泛 `REASSIGN OWNED` |
| 目标 relation owner | `core/action/audit/ops/alerts/finance/public` 下 28/28 张表/序列属 `xingmang` | 应用身份拥有不可 REVOKE 的 owner 能力 |
| default ACL | `pg_default_acl` 0 行 | 未来对象没有权限延续策略 |
| migration | version 13、dirty=false | 当前迁移线完整，但由共享 superuser 执行 |
| 活动连接 | API/worker 等 4 个 idle 连接均为 `xingmang`，`application_name` 为空 | 无法从连接侧区分进程 |

三张追加型表的实测权限：

| relation | owner | 当前 UPDATE | 当前 DELETE | 规则第二防线 |
|---|---|---:|---:|---|
| `action.action_run` | xingmang | 有 | 有 | no_update / no_delete |
| `audit.audit_event` | xingmang | 有 | 有 | no_update / no_delete |
| `ops.metric_observation_sample` | xingmang | 有 | 有 | 无；DELETE 供 retention 合法使用 |

### 3.3 当前 DSN/凭据拓扑

`deploy/compose/launch.yaml` 当前把同一 `${POSTGRES_USER:-xingmang}` 与
`DATABASE_PASSWORD_REF=secret://database/postgres-password` 注入 migrate、bootstrap、
API、worker。API 与 worker 分别在 `cmd/platform-api/database.go`、
`cmd/platform-worker/database.go` 把 ref 映射到固定 `DATABASE_PASSWORD` env。

迁移容器：

1. `cmd/migrate` 经单条命令作用域的 PGPASSWORD 跑平台 migrations；
2. 同一容器再用 `platform-worker -migrate` 跑 River migrations；
3. 两步仍是同一数据库身份。

`cmd/audit-verify` 与 `cmd/platform-shadow` 直接接受 `XM_DATABASE_URL`，目前没有独立
ops CredentialRef 装配。CI 也只提供一个 `XM_TEST_DATABASE_URL` 超级用户 DSN，
因此现有集成测试不能证明最小权限运行。

## 4. 方案比较与选择

### 4.1 选择：owner + 按进程 capability role + 可轮换 login role

稳定权限只授给 NOLOGIN capability role；容器使用 LOGIN identity 并只继承一个
capability role。对象由独立 migrator 拥有。

优点：

- grants 与可轮换登录名解耦；
- API/worker 互相不能继承权限；
- 轮换时可并存 old/new login，不中断旧连接；
- verifier 可以同时检查角色属性、membership 与对象 ACL；
- 不引入 RLS、SECURITY DEFINER 或第二套业务写实现。

### 4.2 否决：只拆 `owner` + 一个共享 `app`

虽然能让 REVOKE 生效，但 worker 将继承 API 的 Action/audit 权限，API 也会继承 River、
retention 和采集写权限。一个进程被攻破仍等于整库应用权限被攻破。

### 4.3 后置：每个写动作只经 SECURITY DEFINER routine

它能把表级 DML 进一步收紧为函数调用，但会形成与现有 Action/Store 并行的第二套写契约，
还需要逐函数 search_path、owner、EXECUTE 与注入审计。本片只做进程级最小权限；
若以后要抵御“已攻破 API 进程直接发 SQL”，另立 ADR/任务。

## 5. 目标角色拓扑

### 5.1 cluster admin

现有 `xingmang` 暂保留为离线 break-glass cluster admin：

- 不再注入 migrate/bootstrap/API/worker/ops/backup；
- 密码独立轮换并离线保管；
- 不作为业务对象 owner；
- 使用必须有变更单、人工批准、独立审计；
- DBR4 只移除运行依赖，不在本任务擅自 DROP/RENAME/降级 bootstrap superuser。

### 5.2 owner/migrator

`xm_migrator`：

- LOGIN；
- 目标 `xingmang` 数据库与业务对象 owner；
- NOSUPERUSER、NOCREATEDB、NOCREATEROLE、NOREPLICATION、NOBYPASSRLS；
- 仅一次性 migrate 容器持有凭据；
- 不授给任何 runtime/ops capability role；
- 每次迁移必须用 exact `application_name=platform-migrate`；
- 迁移结束后不保留常驻连接。

首版选择 login owner，而非额外 NOLOGIN owner + SET ROLE，是为了不把 `options=-c role=...`
放进 DSN，也不改写 golang-migrate/River 的连接建立语义。若以后迁移工具原生支持安全的
per-connection SET ROLE，再单独评估 credentialless owner。

### 5.3 capability roles 与登录身份

| capability role（NOLOGIN） | login identity 初始名 | 用途 |
|---|---|---|
| `xm_api_runtime` | `xm_api_a` | Query + 当前 API 进程内 L0/L1 Action |
| `xm_worker_runtime` | `xm_worker_a` | River、采集、告警、retention |
| `xm_lifecycle_runtime` | `xm_lifecycle_a` | bootstrap、chain root、获批修复 |
| `xm_ops_read` | `xm_ops_a` | audit-verify、platform-shadow、只读诊断 |
| `xm_backup_read` | `xm_backup_a` | pg_dump/恢复演练读取 |

完整数据库恢复另设**一次性** `xm_restore_once` 登录身份/能力，且不属于 steady
runtime topology：只在独占、空的恢复 cluster 内由获批 restore envelope 创建，
仅执行 data-only restore 与必要 sequence `setval`，验收后立即 `NOLOGIN` 并撤权。
它不能用于 source pg_dump，也不能替代 cluster admin、migrator 或 verifier。

membership 固定为：

```sql
GRANT xm_api_runtime TO xm_api_a
  WITH INHERIT TRUE, SET FALSE, ADMIN FALSE;
```

其余 login 同理。LOGIN roles 全部：NOSUPERUSER、NOCREATEDB、NOCREATEROLE、
NOREPLICATION、NOBYPASSRLS；不直接持有表 grants，不互相 membership。

环境之间不得复用密码。staging 与 production 即使角色名相同，也必须是独立 cluster、
独立 CredentialRef 内容、独立变更单。

## 6. 数据库、schema 与 PUBLIC 基线

目标数据库执行 exact SQL；数据库名从已连接的 `current_database()` 安全引用，
因此 DBR1 可随机 database 而不替换 SQL bytes：

```sql
SELECT format('REVOKE CONNECT, TEMPORARY ON DATABASE %I FROM PUBLIC', current_database()) \gexec
SELECT format(
  'GRANT CONNECT ON DATABASE %I TO xm_migrator, xm_api_runtime, xm_worker_runtime, xm_lifecycle_runtime, xm_ops_read, xm_backup_read',
  current_database()
) \gexec
```

schema 基线：

- `REVOKE ALL ON SCHEMA public FROM PUBLIC`；
- `public` schema owner 保持 PostgreSQL 18 默认的内建虚拟角色
  `pg_database_owner`，**不**改成 `xm_migrator`；目标数据库 owner 是
  `xm_migrator`，所以它经 `pg_database_owner` 语义管理 public；
- verifier 要求 `public.nspowner = pg_database_owner`，且 `pg_database_owner`
  不是 LOGIN、没有显式 membership；其它 custom schema owner 才是 `xm_migrator`；
- `core/action/audit/ops/alerts/finance` 的 CREATE 只归 owner/migrator；
- runtime/ops/backup 只获其所需 schema USAGE；
- API 不需要 `public`（River）USAGE；
- worker 需要 `public` + `core/ops/alerts/finance` USAGE；
- ops/backup 只获要读取 schema 的 USAGE；
- 任何 runtime 不获数据库 TEMPORARY、schema CREATE、table TRIGGER/REFERENCES/MAINTAIN。

函数默认向 PUBLIC 开放 EXECUTE，必须在对象创建事务内撤销；不能只做表权限。

## 7. 逐对象 ACL

### 7.1 core

| object | API | worker | lifecycle | ops | backup |
|---|---|---|---|---|---|
| `core.environment` | SELECT | 无 | SELECT + 首次 INSERT | SELECT | SELECT |
| `core.service` | SELECT/INSERT/UPDATE | 无 | SELECT + seed INSERT | SELECT | SELECT |
| `core.connector` | SELECT/INSERT | 无 | 无 | SELECT | SELECT |
| `core.connection` | SELECT/INSERT/UPDATE | 无 | 无 | SELECT | SELECT |

不授 DELETE/TRUNCATE。已有 FK 的 ON DELETE 语义不构成删除授权。

### 7.2 action/audit

| object | API | worker | lifecycle | ops | backup |
|---|---|---|---|---|---|
| `action.action_run` | INSERT | 无 | 无 | SELECT | SELECT |
| `audit.audit_event` | SELECT/INSERT | 无 | SELECT | SELECT | SELECT |
| `audit.chain_root` | 无 | 无 | table SELECT/INSERT；仅 column UPDATE(`exported_at`,`export_target`) | SELECT | SELECT |

硬约束：

- runtime 对 `action_run` / `audit_event` 无 UPDATE/DELETE/TRUNCATE；
- rewrite rules 继续作为第二道防线，不能因为 ACL 落地而删除；
- lifecycle 无权重写 audit_event；
- `audit.chain_root` 的 table ACL **不得出现 UPDATE**；`root_hash`、`signature`、
  `key_id`、区间与时间列均不可更新。唯一 UPDATE 是 `pg_attribute.attacl` /
  `information_schema.column_privileges` 可证明的 `exported_at`,`export_target`；
- audit archive/backup 本片不获 DELETE；
- migrator owner 仍能 DDL，这是隔离凭据而不是“连 owner 也不可改”的虚假承诺。

### 7.3 ops

| object | API | worker | lifecycle | ops | backup |
|---|---|---|---|---|---|
| `ops.metric_observation` | SELECT | SELECT/INSERT/UPDATE | SELECT | SELECT | SELECT |
| `ops.metric_observation_sample` | SELECT | SELECT/INSERT/DELETE | SELECT | SELECT | SELECT |
| `ops.metric_observation_sample_id_seq` | 无 | USAGE | 无 | 无 | SELECT |

worker 的 DELETE 是 retention 所需的表级残余风险。首版不加 RLS/SECURITY DEFINER；
测试必须证明 API/ops 不能 DELETE、worker 不能 UPDATE/TRUNCATE。retention 的 cutoff/batch
约束仍由现有代码/SQL 测试保证。

### 7.4 alerts

| object | API | worker | lifecycle | ops | backup |
|---|---|---|---|---|---|
| `alerts.alert` | SELECT/UPDATE | SELECT/INSERT/UPDATE/DELETE | SELECT | SELECT | SELECT |
| `alerts.alert_silence` | SELECT/INSERT | SELECT | SELECT | SELECT | SELECT |

DELETE 只授 worker（resolved-alert retention）；任何 runtime 无 TRUNCATE。

### 7.5 finance

| object family | API | worker | lifecycle | ops | backup |
|---|---|---|---|---|---|
| `upstream_account` | SELECT/INSERT/UPDATE | SELECT | SELECT | SELECT | SELECT |
| `token_map` | SELECT/INSERT/UPDATE/DELETE | SELECT | SELECT | SELECT | SELECT |
| `subscription_cost_batch` / `proxy_asset` | SELECT/INSERT/UPDATE | SELECT | SELECT | SELECT | SELECT |
| `profit_daily` / `amortization_loss` | SELECT | SELECT/INSERT/UPDATE | SELECT | SELECT | SELECT |
| `balance_history` | SELECT | SELECT/INSERT/UPDATE | SELECT | SELECT | SELECT |
| finance sequences | 无 | 逐对象按 INSERT 所需 USAGE | 无 | 无 | 逐对象 SELECT |

API 仍拥有部分表级 DML，因为 Query 与 Action Handler 在同一进程。本角色拆分不声称能防止
“已攻破 API 进程绕过 Action 直接 SQL”；它只把进程故障域彼此隔离。

### 7.6 River/public

`public` 保留给 River 与 `schema_migrations`：

- worker 对 `river_*` tables：SELECT/INSERT/UPDATE/DELETE；
- worker 对每条实际 `river_*` sequence：USAGE；
- worker 对 River routines：EXECUTE；
- worker 对 `public.river_job_state` enum：USAGE；PUBLIC 无 USAGE；
- worker 对 `schema_migrations`：无权限；
- API/lifecycle/ops 对 River：默认无权限（ops 若后续需要任务诊断，另批只读视图）；
- migrator owner 全权执行版本化迁移。

## 8. default privileges 与未来迁移纪律

### 8.1 既有对象与未来对象分开处理

`ALTER DEFAULT PRIVILEGES` 不修改既有对象，且只看**实际创建对象的 current role**；
所以 DBR2 必须同时：

1. 对既有对象执行精确 GRANT/REVOKE；
2. `FOR ROLE xm_migrator` 设置未来 defaults；
3. 保证所有平台/River migration 都以 `xm_migrator` 创建对象；
4. 每次迁移后运行 verifier，不以 SQL 执行成功代替权限正确。

### 8.2 custom schema：默认拒绝

`core/action/audit/ops/alerts/finance` 中未来 table/sequence/routine/type/domain 不自动授 runtime。
每个 migration 必须在同一 up migration 写精确 grants，或写明确的
`no-runtime-access` 注释。漏 grant 的结果是新代码 fail closed，而不是新表自动开放。

全局必须撤销 migrator 新建 routine 默认给 PUBLIC 的 EXECUTE：

```sql
ALTER DEFAULT PRIVILEGES FOR ROLE xm_migrator
  REVOKE EXECUTE ON ROUTINES FROM PUBLIC;

ALTER DEFAULT PRIVILEGES FOR ROLE xm_migrator
  REVOKE USAGE ON TYPES FROM PUBLIC;
```

DBR1 policy/verifier 必须把现有 enum/domain 与 `pg_default_acl.defaclobjtype='T'`
作为独立 object family；逐 schema 检查 PUBLIC/type ACL。当前已知至少包括
`public.river_job_state`。新增 enum/domain 未进入 exact policy 或明确
`no-runtime-access` marker 时 governance fail closed。

### 8.3 public/River 特例

River bundle 不会写本项目自定义 grants。`public` 被契约化为 River 专用：

- migrator 在 public 创建的未来 table 默认授 worker SELECT/INSERT/UPDATE/DELETE；
- 未来 sequence 默认只授 worker USAGE；
- 未来 routine 默认只授 worker EXECUTE；
- 未来 type/domain 默认撤 PUBLIC USAGE，并只给 exact policy 指定角色；
- `schema_migrations` 始终显式 REVOKE worker；
- governance 禁止普通业务表进入 public。

### 8.4 backup/ops 默认

不自动把未来自有表授给 ops/backup。每个 migration 必须显式决定；verifier/pg_dump 在漏 grant
时失败，避免敏感新表静默进入日常 ops，也避免 backup 静默缺表。

Sequence 权限逐对象声明：业务/worker INSERT 的 `nextval` 只授 `USAGE`；backup
reader 为完整 dump 授 `SELECT`；`UPDATE`/`setval` 只允许 migrator 或获批一次性
restore 身份，并由 restore evidence 证明需要。任何 runtime sequence UPDATE 都是
policy violation，不能以“serial 通常要三个权限”批量放宽。

## 9. 显式 owner 转移

禁止：

```sql
REASSIGN OWNED BY xingmang TO xm_migrator;
```

当前 `xingmang` 还拥有 `postgres`、模板库与 `xm0050_test` 等共享数据库对象；PostgreSQL
`REASSIGN OWNED` 会处理当前数据库对象和该角色拥有的 shared objects，范围超过本任务。

DBR2 必须生成 before/after owner manifest，并只对以下白名单显式 `ALTER ... OWNER`：

- database `xingmang`；
- schemas `core/action/audit/ops/alerts/finance`；`public` owner 必须保持
  `pg_database_owner`，不列入 `ALTER SCHEMA ... OWNER`；
- 上述 schema 的 tables/sequences/routines/types/domains；
- public 中 `schema_migrations`、`river_*` tables/sequences/routines 与
  `river_job_state` type；
- 相关 indexes/constraints 由 table ownership 规则随表核对。

禁止触碰 `postgres`、template databases、`xm0050_test` 或其它数据库。owner 脚本发现未知
schema/object kind 时 fail closed，并要求更新审批包，不能用通配符顺带处理。

## 10. CredentialRef、DSN 与连接身份

### 10.1 稳定引用

| 用途 | CredentialRef |
|---|---|
| cluster admin | `secret://database/cluster-admin` |
| migrator | `secret://database/migrator` |
| API | `secret://database/platform-api` |
| worker | `secret://database/platform-worker` |
| lifecycle | `secret://database/lifecycle` |
| ops | `secret://database/ops-readonly` |
| backup | `secret://database/backup` |
| one-time restore | `secret://database/restore-once`（仅演练窗存在） |

ref 名稳定；轮换时改变登录 role/password 绑定，不把真实值写进仓库、日志、错误或 Handoff。

### 10.2 Provider

API/worker 的数据库装配收敛到共享 `internal/platform/dbconn`：

- 解析 `DATABASE_URL`；
- 使用 `pgdsn.Validate` 防 query 覆盖 host/user/password；
- 解析 `DATABASE_PASSWORD_REF`；
- 默认从 `secrets.NewDockerSecretProvider()` 读取；
- 注入 audited SecretProvider；
- 无 env/file 静默 fallback；
- 明文仅在生成 pgx config 的最短生命周期存在，不进 config dump/log。

迁移与 psql lifecycle 从各自 Docker Secret 文件读取，只把 PGPASSWORD 放在单个子进程环境；
容器 inspect 不得出现其它角色的 secret。

### 10.3 DSN 与 application_name

每个 DSN 显式用户名与 application_name：

```text
postgres://xm_api_a@postgres:5432/xingmang?sslmode=disable&application_name=platform-api
postgres://xm_worker_a@postgres:5432/xingmang?sslmode=disable&application_name=platform-worker
postgres://xm_migrator@postgres:5432/xingmang?sslmode=disable&application_name=platform-migrate
postgres://xm_lifecycle_a@postgres:5432/xingmang?sslmode=disable&application_name=platform-lifecycle
postgres://xm_ops_a@postgres:5432/xingmang?sslmode=disable&application_name=platform-ops
postgres://xm_backup_a@postgres:5432/xingmang?sslmode=disable&application_name=platform-backup
```

`application_name` 是证据/排障字段，不是权限边界。production 的 TLS/host 由其独立部署契约决定；
本设计不把 staging `sslmode=disable` 固化为生产口径。

## 11. 有状态 A/B 凭据轮换

在 DBR3 证明副本编排、连接排空与逐副本 identity evidence 前，本文**不承诺无缝
逐副本轮换**；DBR2 及更早只允许维护窗内受控停止/重启。policy 显式建模：

| state | 允许的 LOGIN/membership | 必填治理字段 |
|---|---|---|
| `steady-a` | 仅 A 可 LOGIN 且继承唯一 capability；B 不存在或 NOLOGIN/无 membership | policy version |
| `rotating-a-b` | A、B 同时可 LOGIN，权限必须完全等价且都只继承同一 capability | approved CR、started_at、deadline、old/new identity |
| `steady-b` | 仅 B 可 LOGIN；A 已 NOLOGIN 且无 membership/活跃连接 | closure evidence、closed_at |

Verifier 对未声明的第二 login、缺/未知 CR、deadline 已过、A/B 权限不等价或 steady
状态仍有双 membership 一律 fail closed。状态迁移只能
`steady-a -> rotating-a-b -> steady-b`，不能把永久双 login 当 steady。

runtime capability role 不变；登录身份 A/B 轮换：

1. 人类创建 `xm_api_b`（或相应 next role），属性最小、密码经安全交互设置；
2. 以 `INHERIT TRUE, SET FALSE, ADMIN FALSE` 加入唯一 capability role；
3. verifier 证明 new role 权限与 old role 等价且不能 SET ROLE migrator/admin；
4. 新 CredentialRef 内容与 DSN username 在一个版本化 compose 变更中准备；
5. DBR3 前在批准维护窗停止对应服务、排空旧连接、切 secret/DSN 后受控重启；
6. 只有 DBR3 已提供 replica identity/connection-drain evidence 时才可逐副本滚动；
7. `pg_stat_activity` 证明 old login 连接为 0；
8. `ALTER ROLE old NOLOGIN`，保留短观察窗；
9. 观察窗后撤 membership、清理旧 secret；
10. 回滚窗口关闭须人工确认。

migrator/lifecycle/ops/backup 是一次性或人工工具，可在维护窗轮换；仍不得复用 runtime 密码。

重要：修改 compose 的 `POSTGRES_PASSWORD_FILE` 不会改变已有 volume 中角色密码；
轮换必须在 PostgreSQL 执行受控 role password 操作并验证新旧连接。

## 12. Verifier 与 disposable PG18 测试

DBR1 harness **自行创建并独占**一个临时 PostgreSQL 18 cluster，不接受外部 admin
DSN，也绝不连接 staging/production/shared developer DB。cluster 使用
`VERSIONS.lock`/获批配置中的完整 `postgres:18@sha256:<digest>`，禁止 tag-only。

### 12.1 harness 安全

- 随机化且只随机化 Compose project、container、network、volume 与 database；
- cluster 内 capability/login/owner 名使用 policy 的**固定生产角色名**，未经字符串
  替换地执行 exact SQL；独占 cluster 消除了角色名碰撞，不再生成 UUID role；
- 创建后验证 Compose project/container/volume labels、实际 RepoDigest 等于 pin、
  `server_version_num` major=18、database fingerprint 与 fresh instance（无 `xm_*`
  roles、无平台 schema/表、volume 创建于本 run）；任一不符在执行 SQL 前 STOP；
- SQL 通过随机 database 的连接上下文/current_database 安全工作；禁止 sed/template
  替换角色、owner、grant 或 policy 内容；
- finally 只执行该随机 project 的 `down --volumes --remove-orphans`，再按 project
  label 证明 container/network/volume 全为 0；销毁的是整个临时 cluster，不在共享
  cluster 内逐个 DROP 角色/库；
- teardown 部分失败继续枚举并报告，但最终非零；不复用 volume、不保留失败现场为
  “下次继续”，需要证据时先导出脱敏日志再销毁；
- 不 TRUNCATE/disable trigger 清理 append-only 表。

### 12.2 正向能力

必须以真实受限 role 证明：

- API Query 与现有 L0/L1 Action 所需 DML；
- worker River heartbeat、sync、finance、alert、retention；
- lifecycle 幂等 staging seed、RUNWAY bootstrap contract、chain-root contract；
- ops audit-verify/platform-shadow 全程 read-only；
- backup pg_dump 全库成功、无漏表。

### 12.3 负向能力

必须断言 SQLSTATE/数据不变：

- 所有 runtime 非 superuser/owner/role admin；
- runtime CREATE/ALTER/DROP/GRANT/TRUNCATE 全拒；
- API/worker/ops UPDATE/DELETE audit_event/action_run 全拒；
- worker UPDATE/TRUNCATE metric sample 拒，合法 retention DELETE 成功；
- API 无 River 权限，worker 无 action/audit_event DML；
- ops/backup `default_transaction_read_only=on` 且任意 DML 拒；
- login role 无法 SET ROLE migrator/admin；
- grants 不来自 PUBLIC 或意外 membership；
- 新 fixture object 验证 default privileges 与迁移显式 grant 门禁。
- chain_root table UPDATE 为 42501、仅两列 UPDATE 成功；`root_hash` 与混合列更新
  均为 42501 且数据不变；verifier 同时对 `pg_attribute.attacl` 与
  `information_schema.column_privileges` 做 exact diff；
- existing/default type/domain ACL 完整，PUBLIC 对 `river_job_state` USAGE 为 false；
- 每条 sequence 的 runtime USAGE/backup SELECT 与禁止 runtime UPDATE/setval 逐对象证明。

### 12.4 append-only 两层分别证明

ACL probe 用 runtime role 断言 permission denied；rule/trigger probe 用具有相应 DML 权限的
专用测试角色断言 rewrite/trigger 仍阻止变更。只测 runtime ACL 不能证明第二防线存在。

## 13. 部署与回滚

### 13.1 staging 前置

以下缺一即 STOP：

1. DBR1 verifier 合入且 disposable PG18 全绿；
2. DBR2 精确角色/owner/ACL/default ACL diff 获迁移审批；
3. 角色属性、owner manifest、PUBLIC diff、未知对象清单完成审阅；
4. 七个 steady CredentialRef 与本次恢复演练的一次性 restore ref 由人类配置，未进入仓库/日志；
5. 全量逻辑备份完成，hash 校验；
6. 在独立 PG18 恢复并跑 migration version、audit chain、核心 Query；
7. staging 变更单与维护窗单独批准；
8. 记录原 image digest、compose/env checksum、角色/ACL/application_name 证据。

### 13.2 有序切换

1. 创建 capability/login roles，旧服务仍用 `xingmang`；
2. 显式 owner 转移到 migrator，应用 ACL/default ACL；
3. verifier 全绿；
4. no-op 重跑 migrate（migrator）与 bootstrap（lifecycle）；
5. 切一个 API replica，验证 Query、一个获准 L1 Action、action_run/audit_event；
6. 切一个 worker，验证 River、sync、finance、alert、retention；
7. 切 ops/backup，完成 audit-verify 与分离身份的 pg_dump/restore；
8. 滚动剩余 replicas；
9. soak 后确认无 `xingmang` 应用连接；
10. 移除共享 secret，cluster admin 轮换并离线保存。

### 13.3 快速回滚

legacy admin 未锁定前：

- 按进程恢复上一版 compose/DSN/CredentialRef 与 image digest；
- 单 replica 启动并验证后再扩；
- owner 可以暂留 migrator，因为 legacy superuser 仍可运行；
- 不为“回滚”执行生产 down migration或删除新角色/历史数据。

若 ACL 错误，使用审批包中的显式 inverse-grant/owner manifest；禁止宽泛 REASSIGN OWNED。
旧 admin secret 已锁定后，任何回滚都属于 break-glass，需要新批准。

### 13.4 恢复验收

恢复后的 disposable 环境必须证明：

1. source `xm_backup_a` 只做 pg_dump/sequence SELECT，不获 target CREATE/DML；
2. cluster admin 只预建空 cluster/database、固定 roles，并把 database owner 设为
   migrator；它不执行 data restore；
3. `xm_migrator` 只运行 exact migrations/owner/ACL/default ACL；
4. 临时 `xm_restore_once` 只做 data-only COPY/INSERT 与获批 sequence setval，完成后
   NOLOGIN/撤权；
5. `xm_ops_a`/DBR verifier 只读验收，不复用 backup/migrator/restore credential；

- migration version/dirty；
- 所有对象 owner/ACL/default ACL；
- audit chain 全链；
- API/worker 正负能力；
- RUNWAY current/history（若已合入）；
- pg_dump 包含全部业务/审计/River对象；
- CredentialRef/age 私钥与数据库备份不在同一存放位置。

## 14. 与 RUNWAY/AUDIT 的依赖

### 14.1 RUNWAY

RUNWAY 不引用待审分支或易漂移 head。每次 DBR policy/SQL packet 必须从批准 CR/Task
读取 `RUNWAY_APPROVED_MERGE_SHA`，并证明该 SHA 是当前 base 的 ancestor；未提供、
未 merge、SHA 改变或对应 migration/object diff 不一致都 **STOP**。只有该次最新获批
merge 中实际存在的对象才进入 policy：

- `finance.runway_threshold_config`：API SELECT/UPDATE、worker SELECT、lifecycle SELECT/首次 INSERT；
- `finance.runway_threshold_history`：API SELECT/INSERT、lifecycle SELECT/首次 INSERT；
- history 对 runtime 无 UPDATE/DELETE/TRUNCATE；
- schema trigger 是第二防线；DB 角色 ACL 是生产激活前置。

因此：RUNWAY design/C3a disposable 开发可以并行审批；**staging/production 激活必须等待
DBR2+DBR3 权限证据**。若 RUNWAY migration 先合入，DBR owner/ACL manifest 必须显式发现并
覆盖两表；若 DBR 先合入，RUNWAY migration 必须带精确 grants 并通过 verifier。

### 14.2 AUDIT archive

审计归档 docs commit `d642875` 定义四种专用 DB capability，不能合并回宽泛
`xm_lifecycle_runtime`：

| conditional capability | exact DB 边界 |
|---|---|
| `xm_audit_archive_source_reader` | 指定列 SELECT `audit_event`,`chain_root`,`action_run`,`core.environment` |
| `xm_audit_anchor_writer` | fixed tip read + INSERT `chain_root`；无 table/column UPDATE、DELETE |
| `xm_audit_archive_catalog_writer` | 仅 archive catalog SELECT/INSERT/advisory lock；无 audit_event/action_run DML |
| `xm_audit_restore_writer` | 仅 fingerprint 匹配的 empty isolated target，fixed-column INSERT + tx-bound verify；无 UPDATE/DELETE/DDL |

这些角色不进入 DBR policy v1。只有 AUD design 已以获批 merge SHA 进入当前 base、
独立 CR 批准 exact grants 后，才发布新的 policy version 并扩展 provisioning/verifier；
旧 policy 遇到这些未知 role/object 必须 fail closed，不能忽略或借 lifecycle 权限运行。
ops/backup 仍不获 audit DELETE；hot-row lifecycle 仍是独立 ADR/审批。

## 15. 分片与审批门

| 片 | 交付 | 依赖 | live 状态 |
|---|---|---|---|
| DBR0 | 本 design + implementation plan | 当前证据 | docs only；GO |
| DBR1 | role policy/verifier + disposable PG18 harness + CI | DBR0 人工批准 | 不接 staging；可另批实施 |
| DBR2 | owner/migrator/lifecycle、PUBLIC/default ACL、角色 provisioning/runbook | DBR1 全绿；迁移与凭据分别批准 | staging execution 仍 STOP |
| DBR3 | API/worker shared dbconn、独立 secrets/DSN/application_name、受限角色 E2E | DBR2 staging evidence | 逐进程 staging STOP |
| DBR4 | ops/backup、恢复演练、旧共享身份下线、长期 drift evidence | DBR3 soak | legacy lockdown/production 各自 STOP |

审批相互不继承：

1. 批准 DBR0 文档 ≠ 批准 DBR1 代码；
2. 批准 DBR1 ≠ 批准任何数据库迁移；
3. 角色/owner/ACL SQL 有独立迁移审批；
4. 七套 CredentialRef/真实凭据有独立人工配置与轮换审批；
5. staging 执行有独立变更单/维护窗；
6. production 以最新 base、备份恢复、staging soak 重新审批；
7. Codex 不执行 live cutover、不合并、不部署。

## 16. 明确不做与残余风险

- 不修改生产、Keycloak 或上游系统；
- 不把数据库角色等同 Keycloak RoleScopeMap；本片没有新应用 scope；
- 不加 RLS、不按 `environment` 行级隔离；生产环境仍靠独立部署/身份，不继承 staging；
- 不把 API 拆成 read/write 两个进程；API 仍持 Action 所需 DML；
- 不把 worker retention DELETE 收紧成 SECURITY DEFINER；表级 DELETE 是已记录残余风险；
- 不自动化 cluster admin/break-glass；
- 不因 role split 宣称已有生产备份、TLS、HA 或灾备；
- 不清理 `xm0050_test` 或其它观测到的数据库；清理另需授权；
- 不执行任意 SQL/容器命令作为平台功能。

## 17. 审批请求

请人类逐项确认：

1. 采用 migrator owner + 五个 capability role + 可轮换 login role，而非共享 app；
2. 现有 `xingmang` 仅作离线 break-glass，不供常驻服务；
3. 逐对象 ACL 矩阵，特别是 API 部分 DML 与 worker retention DELETE 残余风险；
4. custom schema default deny、public 专供 River 的 default privilege 特例；
5. 禁止宽泛 REASSIGN OWNED，owner 转移只走精确白名单 manifest；
6. 七个 steady CredentialRef、一次性 restore ref 与 Docker Secret Provider 路径；
7. A/B login 的 steady/rotating/steady policy 状态；DBR3 前仅受控重启；
8. DBR1 只跑 loopback disposable PG18；
9. DBR2 migration、credentials、staging、production 四类 STOP 相互独立；
10. DBR2+DBR3 前置于 RUNWAY live activation，DBR4 前置于审计归档 live 设计；
11. DBR0/DBR1 可批准推进，但当前 live cutover 保持 NO-GO。

**批准本文只批准设计进入评审，不授权实现、数据库变更、凭据操作或部署。**
