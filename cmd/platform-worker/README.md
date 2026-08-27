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
`sub2api_mode` / `sub2api_source`。

### 切到 real 会发生什么

配置齐全时工厂现场构造真实客户端（每轮同步现解析 CredentialRef、现建传输层
——凭据会轮换，握着不放的连接不会知道）。完整切换步骤、验证方法与故障对照表
见 `docs/modules/connector/RUNBOOK.md`。

四项连接配置缺任意一项时 `real` **也不会崩**：工厂返回一个分类为
`not_supported` 的错误，错误链里说清缺哪个环境变量，同步任务照常把五条指标
写成 `status=failed`、`last_error_code=not_supported`。配置写错了（endpoint
不是 https、主机不在自己的 allowlist 里）则归 `internal`——运维一看 error_code
就知道该去补配置还是去改配置。

### production 环境禁止 fake（XM-0031）

`XM_ENVIRONMENT=production` 且同步开启时，`XM_SUB2API_MODE=fake` 会让进程
**启动即退出**，不是降级也不是告警。

理由是这条默认值的失效方向：默认就是 `fake`，所以「忘了配」的结果恰好是最
危险的那一种——构造出来的用户数、收入、余额被原样写进 `ops.metric_observation`
与样本表，看板再以正常主数字 + 「数据新鲜」徽章呈现它们。只有让进程起不来，
这个疏忽才必然在上线前被发现；一条启动日志会淹没在噪声里。

生产上两个合法出路：配 `XM_SUB2API_MODE=real`（连接配置齐全就走真实客户端；缺配置
时每周期写一条 `SyncFailed` + `last_error_code=not_supported`，那是诚实的失败，
不是假数据），或显式 `XM_SUB2API_SYNC_ENABLED=false` 关掉这条采集链路。
staging / development 不受限制——真实只读凭据要一个个环境去开，这两个环境仍要靠
Fake 把整条采集链路跑通。

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
