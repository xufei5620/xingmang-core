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
| `XM_SUB2API_MODE` | `fake` | `fake` 用 `sub2api.NewFake`；`real` 是 XM-0017 的落点。**`production` 环境下 `fake` 启动即拒**，见下 |
| `XM_SUB2API_INSTANCE_ID` | `sub2api-staging` | 写进观测的 `source`，看板必须显示 |
| `XM_SUB2API_SYNC_ENABLED` | `true` | 采集链路的停用开关（宪法 26 条） |
| `XM_SUB2API_SYNC_INTERVAL` | `300s` | 同步周期，下限 1 秒（River 限制） |
| `XM_SUB2API_CREDENTIAL_REF` | 空 | XM-0017 预留。**当前只校验引用形状，不解析明文** |

### 为什么默认是 fake

真实只读账号还没开出来（XM-0017）。默认设成 `real` 会让每个新环境一上来
就满屏同步失败，什么信息也没给出。默认 `fake` 让上层（看板、告警）先跑起来，
而默认来源标识 `sub2api-staging` 保证这批数字一眼可辨——Fake 数据绝不伪装
成真实来源。进程启动时 `worker_started` 日志里也会带上
`sub2api_mode` / `sub2api_source`。

配成 `real` 不会崩：客户端工厂返回一个分类为 `not_supported` 的错误，
同步任务照常把五条指标写成 `status=failed`、`last_error_code=not_supported`。

### production 环境禁止 fake（XM-0031）

`XM_ENVIRONMENT=production` 且同步开启时，`XM_SUB2API_MODE=fake` 会让进程
**启动即退出**，不是降级也不是告警。

理由是这条默认值的失效方向：默认就是 `fake`，所以「忘了配」的结果恰好是最
危险的那一种——构造出来的用户数、收入、余额被原样写进 `ops.metric_observation`
与样本表，看板再以正常主数字 + 「数据新鲜」徽章呈现它们。只有让进程起不来，
这个疏忽才必然在上线前被发现；一条启动日志会淹没在噪声里。

生产上两个合法出路：配 `XM_SUB2API_MODE=real`（XM-0017 之前它会每周期写一条
明确的 `SyncFailed`，那是诚实的失败），或显式 `XM_SUB2API_SYNC_ENABLED=false`
关掉这条采集链路。staging / development 不受限制。

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
