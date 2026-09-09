# platform-worker

`platform-worker` is the River OSS queue worker for short-lived platform jobs.
It does not run workflows or perform implicit schema changes.

## Local run

Start the PostgreSQL service first:

```bash
read -s POSTGRES_DEV_PASSWORD
export POSTGRES_DEV_PASSWORD
docker compose -f deploy/compose/dev.yaml up -d --wait postgres
```

Run the explicit River lifecycle migration with a DSN that uses the same local
password (the DSN is read but never logged):

```bash
ENVIRONMENT=development \
  DATABASE_URL="postgres://xingmang:${POSTGRES_DEV_PASSWORD}@localhost:5433/xingmang?sslmode=disable" \
  go run ./cmd/platform-worker -migrate
```

Then start the worker:

```bash
DATABASE_URL="postgres://xingmang:${POSTGRES_DEV_PASSWORD}@localhost:5433/xingmang?sslmode=disable" \
  ENVIRONMENT=development \
  go run ./cmd/platform-worker
```

`HEARTBEAT_INTERVAL`, `HEARTBEAT_RUN_ON_START`, and
`HEARTBEAT_FAILURES` are optional development settings. The normal process
uses River's default retry policy and the `default` plus `maintenance` queues.

## 审计归档手工接线（AUD2）

归档不是普通周期任务。当前 release 尚未同时具备 R2-10 集群租约与 DB 角色拆分
证据，因此 `platform-worker` **不会注册或消费** `audit_archive_manual` 的周期任务；
`JobManifest` 仍只有既有六个周期任务。归档配置默认关闭，任何 `scheduled` 模式或
生产环境启用请求都会 fail closed。后续 AUD3/AUD5 只能在独立审批后注入签名 envelope
runner，并沿用同一份手工 seam。

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `XM_AUDIT_ARCHIVE_ENABLED` | `false` | 必须显式置 `true` 才能使用手工 seam；不代表 scheduler 已启用 |
| `XM_AUDIT_ARCHIVE_MODE` | `disabled` | 当前唯一可接受的启用值是 `manual`；`scheduled` 永远拒绝 |
| `XM_AUDIT_ARCHIVE_ENDPOINT` | 空 | 手工 fixture 的 MinIO 地址；只允许 HTTPS，或 development/staging/test 的 loopback HTTP |
| `XM_AUDIT_ARCHIVE_ENDPOINT_ALLOWLIST` | 空 | 精确 `host[:port]` 清单，禁止通配符 |
| `XM_AUDIT_ARCHIVE_BUCKET` | 空 | 显式 bucket 标识；不从 endpoint 推断 |
| `XM_AUDIT_ARCHIVE_CREDENTIAL_REF` | 空 | MinIO object-writer CredentialRef；只校验引用形状，不解析明文 |
| `XM_AUDIT_ARCHIVE_KMS_CREDENTIAL_REF` | 空 | 必须是 `secret://archive/minio-kms` |
| `XM_AUDIT_ARCHIVE_QUALIFICATION_CREDENTIAL_REF` | 空 | 手工 fixture 必须是 `secret://archive/minio-qualification` |
| `XM_AUDIT_ARCHIVE_SECURITY_SINK_CREDENTIAL_REF` | 空 | 预留给 AUD3，必须是 `secret://archive/security-sink`（不在本片读取） |

手工触发只接受已批准 signed envelope 的 SHA-256 摘要，由调用方注入的 runner 负责
验签、Kill Switch、精确 VersionID 与 catalog/CAS 协议；River 参数不携带 envelope
原文、路径、DSN 或任何凭据。MinIO fixture 使用独立项目：

```bash
XM_ARCHIVE_CREDENTIAL_ENV_FILE=/path/to/operator-managed.env \
  docker compose -p xingmang-archive -f deploy/compose/archive.yaml \
  --profile archive-fixture up -d --wait
```

该命令只启动回环、非生产 fixture；`operator-managed.env` 不得提交到仓库，且其中的
root/KMS 值应由 `secret://archive/minio-kms` / 一次性 qualification CredentialRef
在受控环境中装配。完整生产激活、受限读取与 security sink 属后续 AUD3/AUD5，不在本片。

## Sub2API 周期同步（XM-0022）

每 300 秒读一次 Sub2API 只读契约（用户概览 + 当日订单 + 渠道余额），
把结果写进 `ops.metric_observation`，看板据此显示数据新鲜度（规格 §9.1）。

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `XM_SUB2API_MODE` | `fake` | `fake` 用 `sub2api.NewFake`；`real` 走真实只读客户端（XM-0017）。**`production` 环境下 `fake` 启动即拒**，见下 |
| `XM_SUB2API_INSTANCE_ID` | `sub2api-staging` | 写进观测的 `source`，看板必须显示 |
| `XM_SUB2API_SYNC_ENABLED` | `true` | 采集链路的停用开关（宪法 26 条） |
| `XM_SUB2API_SYNC_INTERVAL` | `300s` | 同步周期，下限 1 秒（River 限制） |
| `XM_SUB2API_ENDPOINT` | 空 | real 模式必填，必须 https |
| `XM_SUB2API_TARGET_ALLOWLIST` | 空 | real 模式必填，逗号分隔的**精确**主机清单；留空 = 一个请求都发不出去 |
| `XM_SUB2API_CREDENTIAL_REF` | 空 | real 模式必填，`secret://<scope>/<name>`。**本层只校验形状，不解析明文** |
| `XM_SUB2API_TOKEN` | 空 | 上面那个引用在 env Provider 下的落点（登记表见 `sub2api.go`）。Connector 不认识这个名字 |

### 为什么默认是 fake

真实只读凭据要一个个环境去开。默认设成 `real` 会让每个新环境一上来
就满屏同步失败，什么信息也没给出。默认 `fake` 让上层（看板、告警）先跑起来，
而默认来源标识 `sub2api-staging` 保证这批数字一眼可辨——Fake 数据绝不伪装
成真实来源。进程启动时 `worker_started` 日志里也会带上
`sub2api_mode_default` / `sub2api_source`——注意是 **default**，见下面
「别拿启动日志验证生效模式」。

### 切到 real 会发生什么

配置齐全时工厂现场构造真实客户端（每轮同步现解析 CredentialRef、现建传输层
——凭据会轮换，握着不放的连接不会知道）。完整切换步骤、验证方法与故障对照表
见 `docs/modules/connector/RUNBOOK.md`。

四项连接配置缺任意一项时 `real` **也不会崩**：工厂返回一个分类为
`not_supported` 的错误，错误链里说清缺哪个环境变量，同步任务照常把五条指标
写成 `status=failed`、`last_error_code=not_supported`。配置写错了（endpoint
不是 https、主机不在自己的 allowlist 里）则归 `internal`——运维一看 error_code
就知道该去补配置还是去改配置。

### production 禁止 fake（XM-0031；XM-CRED0 之后分两层）

`XM_ENVIRONMENT=production` 且同步开启、**且这个部署没有接
`core.connector_config`**（`Config.ConnectorConfigs == nil`）时，
`XM_SUB2API_MODE=fake` 会让进程**启动即退出**。生产装配总是接了这张表
（`cmd/platform-worker/main.go`），所以生产走的是第二层：**启动放行**
（模式随时可能被后台切成 real，为一个缺省值拒绝启动没有意义），但**每一轮**
生效模式仍是 fake 时动态工厂返回 `not_supported`（`ErrConnectorProductionFake`），
指标写成 `status=failed`——演示数据一条都进不了生产。

两层各堵一头：启动那层堵「压根没有后台可切」的部署，每轮那层堵「有后台但
没人去切」。理由是这条默认值的失效方向——默认就是 `fake`，「忘了配」的结果
恰好是最危险的那一种：构造出来的用户数、收入、余额会被原样写进
`ops.metric_observation` 与样本表，看板再以正常主数字 + 「数据新鲜」徽章
呈现它们。

生产上两个合法出路：**在后台把 `core.connector_config` 那一行切成 `real`**
（不重启容器即生效），或显式 `XM_SUB2API_SYNC_ENABLED=false` 关掉这条采集
链路。`XM_SUB2API_MODE=real` 只改**缺省**：行存在时它不参与判定。
staging / development 不受限制——真实只读凭据要一个个环境去开，这两个环境仍要靠
Fake 把整条采集链路跑通。

#### 别拿启动日志验证生效模式

`worker_started` 里的 `sub2api_mode_default` / `newapi_mode_default` 是环境
变量缺省，后台热切换之后它永远不变（进程启动那一刻还没读过库）。生效模式看
两处，同一条日志里的 `effective_mode_log_events` 也写着这两个事件名：

- 每轮的 `connector_config_applied`：`mode` + `config_source`
  （`database` / `env`）+ `config_version`；
- 每轮 `job_completed` 的 `sub2api_mode` / `newapi_mode`（**本轮生效**），
  配 `sub2api_mode_source` / `newapi_mode_source`。

2026-09-08 的排查里正是有人读了启动日志里的旧字段名 `sub2api_mode`，得出
「生产在跑假数据」的错误结论，而 `core.connector_config` 两行 08-30 起就是
`real`（`docs/handoffs/PLATFORM-ALERT-STORM-2026-09-08.md` 三·1）。字段改名
加 `_default` 后缀就是为了让这种误读在字面上不成立。

`connector_config_source` 同一行里说的是「这个进程的生效模式由谁决定」：
接了 `core.connector_config` 的部署（生产装配无条件接）打 `database`，
只有静态工厂那条路（`ConnectorConfigs == nil`）打 `env`。它是从装配推导的，
不是写死的字面量——否则后一种部署里它会一直宣称 `database`，而每轮
`job_completed` 打的却是 `*_mode_source=env`。

### maintenance 队列的槽位是算出来的

启动日志里有一条 `queue_slots`：

```json
{"event":"queue_slots","queue":"maintenance","max_workers":4,
 "fastest_cadence_seconds":60,"slow_job_threshold_seconds":60,
 "slow_job_count":3,"slow_job_kinds":["finance_cost_sync","newapi_sync","sub2api_sync"]}
```

`maintenance` 队列上跑着心跳、告警评估、留存清理、两条同步采集、成本采集。
它过去是**单槽**（`MaxWorkers` 默认 1，没有环境变量能覆盖），而 XM-OPS-TRUTH
把 `newapi_sync` 的执行期限抬到 120s、`sub2api_sync` 抬到 100s——单槽意味着
上游一变慢，这两个任务就稳定占着唯一的槽，把 60 秒节拍的心跳与告警评估挤到
下一轮。**告警引擎的节拍反而在故障期间变松**，正好是最不该发生的时候。

现在槽位 = 慢任务个数 + 1（慢任务全在跑时仍留一个槽给快节拍的任务）。
「慢任务」是数出来的：这个部署启用了哪些任务（同一套 `XM_*_ENABLED` /
`XM_*_INTERVAL` 解析）× 每个 Worker 自己声明的 `Timeout()`，判据是队列里
最快的那个节拍与 River 默认 1 分钟里更小的那一个。关掉一条采集链路，槽位会
自己少一个；将来谁再声明一个更长的执行期限，槽位会自己多一个。

`XM_MAX_WORKERS` 之类的通用旋钮仍是下限：调大它不会被这里调小。

**副作用要知道**：`maintenance` 上不同种类的任务从此可能**同时**跑。

同一种任务的两轮要重叠，得先有「一轮的执行期限 ≥ 它自己的周期」。三个慢任务
都不满足（120s/100s/120s 对 300s 周期，`TestSlowMaintenanceJobsCannotOverlapThemselves`
钉住这条）。满足的只有心跳与告警评估（60s 期限对 60s 周期），而它们与顺序
相关的写在库层各有闸：审计链走 `pg_advisory_xact_lock`（`db/queries/audit.sql`
的 `LockAuditChain`），告警投递走 `FOR UPDATE SKIP LOCKED`
（`db/queries/alerts.sql`）。

连接池按 `pgxpool` 默认 `MaxConns`（`max(4, CPU 核数)`）配，并发变高时可能
出现短暂的连接等待——同步任务的大部分时间花在上游 HTTP 上、并不握着连接，
但真正需要时应显式设池大小，见 handoff 的 follow_ups。

### 失败也要写

上游读取失败时，任务**仍然**为每个指标写一条观测：`status=failed` +
`last_error_code=<connector.ErrorKind>`。数据静静停更是规格 §9.1 明令禁止
的失败模式——看板必须能诚实显示「正在失败」，而不是让人盯着一个不再变化
的数字自己猜。

失败观测会先读回旧行，保住 `observed_at` / `last_success` / 上次已知值：
`ops.Store.Upsert` 是整行覆盖（`ON CONFLICT DO UPDATE SET observed_at =
EXCLUDED.observed_at …`），不先读就写会把「半小时前成功过」抹成「从未采集」。
旧的 `observed_at` 留着，staleness 才会随时间自然增长。

三组读取的失败是**分开记**的：渠道余额超时不会把已经读到的当日收入一起
抹成失败。

### 任务什么时候算失败

只有**写库失败**才返回 error 让 River 重试。上游读取失败已经作为观测落库了，
再让 River 重试只会和 300 秒的周期重复排队，而且重试会把刚写好的失败态
原样覆盖一遍。真正需要重试的是「话根本没说出口」。

The integration test is deliberately opt-in and loopback-only so a shell's
production `DATABASE_URL` cannot be mutated by `go test`:

```bash
XM_RUN_INTEGRATION=1 \
  DATABASE_URL="postgres://xingmang:${POSTGRES_DEV_PASSWORD}@localhost:5433/xingmang?sslmode=disable" \
  go test ./internal/platform/jobs -run TestRiverWorkerPostgresIntegration -count=1 -v
```

The repository CI uses its dedicated `XM_TEST_DATABASE_URL` (loopback port
5432), which enables the same integration test without the local opt-in marker.

For non-development environments, do not put a password in `DATABASE_URL`.
Provide a password-free URL plus an explicit `DATABASE_PASSWORD_REF`; the
existing audited `SecretProvider` path then resolves the internally mapped
`DATABASE_PASSWORD` value. Inline passwords are accepted only with the explicit
`ENVIRONMENT=development` local exception above.

`DATABASE_URL` query parameters are checked against an **allowlist**
(`internal/platform/pgdsn`). pgx reads the query string as connection settings
*after* it fills in host and userinfo, so `?password=` and `?host=` silently
override what the URL appears to say — a check that parses the URL itself and
then hands the same string to pgx is no check at all. Only `sslmode`,
`sslcert`, `sslkey`, `sslrootcert`, `connect_timeout`, `application_name`,
`target_session_attrs` and the `pool_*` settings are accepted; anything else is
rejected in every environment. The loopback guard on the integration test asks
pgx which hosts it will actually dial rather than reading the URL.

## 接入配置与凭据来源（XM-CRED0）

用户拍板：凭据只在管理后台填，不再用服务器 `.env`。worker 侧因此有两条变化，
都**不需要重启**：

1. **凭据文件优先、env 兜底。** Sub2API / NewAPI 的只读 token 与 NewAPI 收入库
   口令都经一条 `secrets.NewChain(file, env)` 解析：先读 `XM_SECRET_ROOT`
   下的 `<scope>/<name>` 文件（由 platform-api 写入、worker 只读挂载），
   文件不存在才落到旧的 `XM_*_TOKEN` 登记表；文件存在但为空、或读取出 IO
   错误则**原地失败**，不拿 env 的值盖过去（fail closed）。每一环各自留审计，
   日志里看得出这次是文件命中的还是 env 兜底的。没配 `XM_*_CREDENTIAL_REF`
   时链里只有文件一环——引用可以来自下面那张表。
2. **接入配置每轮从 `core.connector_config` 读。** 客户端工厂每轮同步都查
   `(platform, environment)` 这一行（30s 缓存）：行存在即以行里**非空**的
   `mode` / `endpoint` / `target_allowlist` / `credential_ref` 为准，
   `XM_SUB2API_*` / `XM_NEWAPI_*` 只作缺省；行里留空的字段仍用 env 缺省。
   库读不到（表还没建、库瞬时不可用）则本轮完全按 env，进入故障时记一条
   `connector_config_unavailable`，恢复时记 `connector_config_recovered`；
   生效配置变化时记 `connector_config_applied`（只打端点主机与凭据引用）。

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `XM_SECRET_ROOT` | `/run/xm/secrets` | 凭据文件根目录，必须是绝对路径且不是根目录。只打路径进日志，不读内容 |

`production` 的 fake 闸随之分两层：启动时因为模式可能随时被后台切成 real 而
放行；但**每一轮**生效模式仍为 fake 时工厂返回 `not_supported`
（`ErrConnectorProductionFake`），五条指标写成 `status=failed`，演示数据一条
都写不进生产。`worker_started` 日志里的那两个字段从此叫 `*_mode_default`，
名字本身就说明它只是缺省值；生效模式看 `connector_config_applied` 与每轮
`job_completed` 的 `*_mode` / `*_mode_source`。

## 保留期清理（XM-R012）

River 周期任务 `retention_prune`，默认每 24 小时一轮，跑在 `maintenance` 队列。
它删两类数据：

| 目标 | 条件 | 默认保留 | 环境变量 |
| --- | --- | --- | --- |
| `ops.metric_observation_sample` | `synced_at` 早于 cutoff | 90 天 | `XM_METRIC_SAMPLE_RETENTION_DAYS` |
| `alerts.alert` | **已解决**且 `resolved_at` 早于 cutoff | 180 天 | `XM_ALERT_RETENTION_DAYS` |

开关是 `XM_RETENTION_ENABLED`（默认 `true`），周期是 `XM_RETENTION_INTERVAL`。
天数必须为正——填 `0` 会让 worker **拒绝启动**：0 天最自然的读法是「不保留」，
也就是把整张表删空，而想表达「不清理」的人该去关上面那个开关。两种意图差得
太远，不能让一个手滑的 0 去猜。

**审计事件不在清理范围内，一条都不删。** 这不是尚未实现：宪法 11 条要求审计
append-only 并在库外锚定；删掉中间任意一条都会断链，`VerifyChain` 会立刻报
`sequence_gap`；而且 `audit.audit_event` 上的 `audit_event_no_delete` 规则
（迁移 000003）让 `DELETE` 变成静默空操作——写了也删不掉，只会报告「删了 0 行」
并显示成功。因此这里**没有**审计保留天数这个变量：给一个删不掉东西的旋钮，
比不给更误导。审计的容量问题走归档（导出 + 链根锚定后转冷存储），另立任务。
完整理由见 `internal/platform/jobs/retention.go` 文件头。

**未解决的告警永远不删**，与年龄无关。一条至今没人处理的老告警恰恰是最不该
被删的那种，而按 `created_at` 之类的条件筛会把它删掉且没有任何报错。

### 运行中看什么

每轮完成打一条 `job_completed`，带 `metric_samples_deleted`、
`resolved_alerts_deleted`、两个保留天数，以及固定的
`audit_events=never_pruned`（把「没清审计是有意为之」写进日志，
免得半年后翻日志的人以为是漏了）。

积压很大时（首次启用可能有上百万行）单轮最多跑 500 批、每批 2000 行，
删不完会打一条 `retention_batch_limit_reached` 的 Warn 然后正常结束——
明天那轮接着删。**持续**出现这条 Warn 才说明保留期或清理频率需要调整。

### 权限

清理任务需要 `ops.metric_observation_sample` 与 `alerts.alert` 上的 `DELETE`。
这与旧文档里「对应用账号 `REVOKE UPDATE, DELETE`」的说法冲突，**以本节为准**：
`UPDATE` 永远不该有（改一条已记录的样本等于篡改历史），`DELETE` 只允许按时间
窗口批量发生。现状证据查询见 `deploy/bootstrap/002_grants_evidence.sql`。
