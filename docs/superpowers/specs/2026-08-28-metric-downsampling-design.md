# 指标原始样本与日粒度降采样设计（XM-C-DS0）

> 状态：**待产品负责人、平台负责人、数据口径负责人共同审批**
>
> 日期：2026-08-28
>
> 性质：路线图 C「90 天原始样本 + 日粒度降采样」的 docs-only Design 制品
>
> 实施授权：**无**。批准本设计不授权迁移、API 契约、DBR、River 自动调度、
> staging 回填或 raw DELETE；后续必须按 DS1～DS4 独立审批。

## 1. 决策摘要与阶段判断

采用“**版本化逐指标 policy + immutable raw + mergeable daily accumulator +
逐样本 append-only receipt**”模型。`id` 只用于稳定排序和诊断，绝不作为
“小于某 ID 的事务都已提交”的证明：

1. `ops.metric_observation_sample` 继续保存至少 90 天 raw，每次采集尝试仍是一行；
2. 新增 `ops.metric_observation_daily`，按 UTC 日、environment、metric、source、
   policy version 保存可合并的日统计与代表 JSON；
3. 新增 `ops.metric_rollup_receipt` 与 per-stream `ops.metric_rollup_state`；批次只读
   `NOT EXISTS receipt` 的 raw，daily merge、receipt INSERT 与成功 state 同事务；
4. 当前 registry 的 **15 个** metric_key 逐个冻结 value kind、primary JSON pointer、
   单位、scale、currency/day 语义与 sum policy；其中两个 invoice key 受 CR-0002
   冻结门约束，CR 未批准则 DS1 STOP；policy 必须与 registry exact coverage；
5. `status=failed` 的旧值不参与数值统计，partial success 与 full success 分开；
6. v1 日桶固定 `[00:00:00Z, next 00:00:00Z)`，history 横轴继续是 `synced_at`；
7. 聚合、receipt 与成功 state 必须同事务；删除逐行要求匹配 receipt，并在同一事务
   满足“先聚合并留证、后删除”，不得由标量 max-ID 推导资格；
8. legacy `hours=1..168` 路径保持 raw-only；新参数 opt-in `raw/day/auto`；`auto`
   固定为“当前 UTC 日 raw、此前完整 UTC 日 daily”，统一排序/limit/cursor/coverage；
9. River unique 只是第一层，per-stream state row lock + receipt unique key 才是并发/
   幂等最终边界；低 ID 可以晚于高 ID 提交，扫描器必须在下一轮发现它；
10. 删除是单向门：没有 backfill、receipt parity、API parity、DBR、kill switch、真实恢复
    演练与 soak，任何 raw DELETE 都是 **NO-GO**。

阶段判定：

| 阶段 | 当前判断 |
|---|---|
| DS0 docs/design | **GO**；本文件与配套计划可评审 |
| DS1 policy/schema + DS2 aggregator | **条件 GO**；仅 disposable PG，须先冻结 CR-0002、15-key policy 与 exact migration diff |
| DS3 API/UI contract | **NO-GO**；须另批响应契约、cursor、coverage 与 UI 语义 |
| DBR / River 多副本 / staging backfill | **NO-GO**；须等各自 gate 与证据 |
| DS4 raw DELETE / production | **硬 NO-GO**；删除门不得由前片批准继承 |

## 2. 当前事实与证据

### 2.1 Raw schema 与写入

- `db/migrations/000005_ops_history.up.sql:8` 创建
  `ops.metric_observation_sample`；字段为 `id,metric_key,source,environment,
  observed_at,synced_at,status,is_partial,watermark,last_error_code,value_json`；
- `id` 是 `GENERATED ALWAYS AS IDENTITY`，提供与时钟无关的唯一排序键；但 sequence/
  identity 在事务提交前分配，**不提供提交顺序**，不能单独充当 closed frontier；
- `db/queries/ops.sql:32` 只有 raw INSERT；`internal/platform/ops/store.go:269`
  的 `Store.UpsertWithSample` 在同一事务更新 latest 并追加 raw；
- `status` 只有 `ok|failed`；`is_partial` 与 status 正交。失败 observation 可能保留
  上次成功的 `value_json`，因此失败行的 value 不能当本轮成功数值；
- `decodeValueJSON` 使用 `json.Decoder.UseNumber`，金额 minor units 不经 float；
- 当前 series 索引为 `(environment,metric_key,synced_at DESC)`，历史排序实际依赖
  `(synced_at,id)`；
- `ops.RegisteredMetricKeys()` 当前精确返回 15 项。除下文 13 项外，还包括
  `invoice.requests.daily` 与 `invoice.amount.daily`；其 connector v1 明确标为 DRAFT，
  具体字段仍受 CR-0002 双方冻结约束。

### 2.2 History API 与消费者

- `GET /api/v1/metrics/history` 只有 `metric_key + hours`；默认 24、最大 168；
- `ops.MaxSampleLimit=1000`，仓储先保留窗口中最新点，再升序返回；
- 后端 response 已有逐点 `source`、`status`、`is_partial`、`watermark`、
  `last_error_code`，顶层有 `truncated/limit`；
- `web/apps/admin-web/src/api/platform.ts:105` 的 `MetricHistoryItem` 漏了
  `source`，`listMetricHistory` 又只返回 `body.items`，使 UI 丢失
  `truncated/limit`；这不是日表造成的新问题，DS3 必须一并修正；
- UI 的 `toSparkSamples` 以 `synced_at` 为横轴，非 `ok` 点为失败、partial 为虚线；
- 唯一非 UI 消费者是 `alerts.Evaluator.consecutiveFailures`，只读短窗口 raw 并从
  尾部数连续失败。这个规则不得读 daily，也不得因冷热路由改变。

### 2.3 Retention 与容量事实

- `internal/platform/jobs/retention.go` 每 24 小时跑 `retention_prune`；
- `db/queries/ops.sql:69` 直接删除 `synced_at < cutoff` 的 raw，默认 cutoff 90 天；
- 当前删除没有 rollup watermark、daily coverage 或 aggregate-before-delete gate；
- `docs/modules/ops/README.md:258` 明确写明“降采样仍未做”；
- 同文档的“5 个指标、value_json 几十字节”已漂移：当前注册表有 15 个 key，
  channel/model/token/account 数组可能显著放大行。实施前必须在 disposable/获批
  staging read-only snapshot 记录 row count、daily growth、p50/p95/p99 row bytes、
  relation/index size 与每 key/source/environment cadence；本设计不声称当前容量值。

## 3. 方案比较

### 3.1 推荐：显式 daily accumulator 表

优点：每个语义可审计；integer/fixed-point 不经 float；late sample 可 merge；
水位可证明 deletion safety；冷热 Query 可明确 coverage；不引入新数据库扩展。

代价：需要 policy、daily/receipt/state schema、worker、API/UI 与恢复测试，不能当成一个
retention SQL 小改动。

### 3.2 否决：查询时临时 `date_trunc + aggregate`

raw 90 天后被删，临时查询无法回答更老历史；JSON/partial/failed/money 也没有一条
通用 SQL 能诚实聚合。它既不解决长期保留，也不给删除水位。

### 3.3 否决：普通 materialized view 或隐式扩展

整视图 refresh 难以与 raw DELETE 建立逐批 exactly-once 证明；Timescale 等扩展
当前不在 `VERSIONS.lock`/运行栈，不能为一个日表引入新的生产依赖与恢复面。

## 4. Versioned per-metric policy

新增 machine-readable `contracts/ops/metric-rollup-policy.v1.json`。每个 key 必须有：

```text
policy_version, metric_key,
value_kind = gauge | daily_snapshot | additive_delta | document_status,
primary_kind = count | money_minor | fixed_point | none,
primary_json_pointer, currency_json_pointer, business_day_json_pointer,
unit, scale, sum_mode = forbidden | additive,
bucket_timezone = UTC,
expected_interval_seconds (nullable),
full_value_required, partial_value_allowed
```

规则：

- policy key 集合必须与 `ops.RegisteredMetricKeys()` **逐项相等**；缺项、多项、重复项、
  unknown key/version/kind/pointer/scale 全部 fail closed；
- policy 变更发布新版本，不能就地重新解释已删除 raw 的旧 daily row；
- `full_value_required` 明确 full success 缺 primary/value 是否违法；
  `partial_value_allowed` 明确 partial 是否可保留代表 JSON，loader 与 accumulator
  不能靠字段是否碰巧存在来猜；
- `sum_mode=additive` 只允许真正 delta/event 指标。当前 15 个 key **全部禁止把
  同一天多次 snapshot 相加**；`sum_numeric` 在 v1 为 null；
- currency 不唯一时所有 money numeric aggregate 为 null，并记录
  `mixed_currency`；不能把不同币种 minor units 相加；
- fixed-point 只保存 integer + scale，例如 ppm scale=1,000,000；禁止 float；
- JSON arrays 不做通用 element-wise aggregate，只保存代表 JSON 与 policy 指定的
  primary integer。

### 4.1 当前 15 个 key 的 v1 口径

| metric_key | value_kind | primary | 日统计 |
|---|---|---|---|
| `sub2api.users.total` | gauge | count `/total_users` | first/last/min/max；sum null |
| `sub2api.users.balance` | gauge | money `/balance_minor_units` + `/currency` | first/last/min/max；sum null |
| `sub2api.revenue.daily` | daily_snapshot | money `/amount_minor_units` | last 代表当日读数；first/min/max 诊断；sum null |
| `sub2api.cost.daily` | daily_snapshot | money `/amount_minor_units` | 同上 |
| `sub2api.channels.balance` | document_status | count `/channel_count` | 数值 first/last/min/max + representative JSON |
| `invoice.requests.daily` | daily_snapshot（CR-GATED） | count `/count`，业务日 `/day` | last + first/min/max；sum null；CR-0002 未冻结前不得生成 v1 policy bytes |
| `invoice.amount.daily` | daily_snapshot（CR-GATED） | money `/amount_minor_units` + `/currency`，业务日 `/day` | last + first/min/max；sum null；CR-0002 未冻结前不得生成 v1 policy bytes |
| `newapi.users.total` | gauge | count `/total_users` | 主计数统计；余额留 representative JSON |
| `newapi.recharge.daily` | daily_snapshot | money `/amount_minor_units` | last + first/min/max；sum null |
| `newapi.subscription.daily` | daily_snapshot | money `/amount_minor_units` | 同上 |
| `newapi.channels.status` | document_status | count `/channel_count` | 主计数 + representative JSON；不聚合逐渠道数组 |
| `newapi.models.usage` | daily_snapshot | count `/total_request_count` | last + first/min/max；不聚合逐模型数组 |
| `finance.cost.daily` | daily_snapshot | money `/total_cost_minor_units` | 单币种才统计；数组只留代表 JSON |
| `finance.revenue.daily` | daily_snapshot | money `/total_revenue_minor_units` | 同上 |
| `finance.profit.daily` | document_status | money `/profit_minor_units`（可缺） | 主金额存在且单币种才统计；coverage counters 留 JSON |

`value_json.day/business_days` 只作为业务元数据；v1 history 时间轴仍是
`synced_at` UTC 日，不能把同一端点偷偷改成业务日口径。需要业务日财务汇总时应读
finance ledger/summary，而不是从 ops snapshots 二次造账。

两个 invoice 行只是当前 DRAFT 形状的审查候选，不构成冻结。DS1 preflight 必须同时
看到 CR-0002 已批准、connector 常量/字段未漂移，以及
`TestPolicyCoversExactlyRegisteredMetrics` 对 15 项精确相等；任一不满足即 STOP，
不能删掉 invoice 行来让测试变绿。

## 5. Success / failed / partial 与 coverage

一日内使用互斥质量桶：

```text
full_success_count    = status=ok     AND is_partial=false
partial_success_count = status=ok     AND is_partial=true
failed_count          = status=failed（无论 is_partial 值）
sample_count          = 三者之和
```

- failed 行即使带旧 value，也不进入 numeric first/last/min/max/sum；
- partial success 不进入权威 full numeric，单独保存 `last_partial_value_json`；
- first/last 的全序为 `(synced_at,id)`；同 timestamp 不得靠 PostgreSQL 任意顺序；
- `error_counts` 是稳定排序的 error code -> count map；
- representative status 不能掩盖计数：只有 full/partial 均为 0 且 failed>0 才是
  failed；有 partial/failed/missing slot 时 daily quality 不能标 full；
- coverage 以**时槽**而非 sample 数计算，避免重复采样把 coverage 推到 100% 以上：

```text
expected_slot_count, covered_slot_count, duplicate_count,
capture_coverage_ppm = covered_slot_count * 1_000_000 / expected_slot_count
```

`expected_interval_seconds` 未知、跨 policy/cadence 漂移或时槽证据不足时，
expected/coverage 为 null；不能用 0 或 100% 假装已知。时槽固定以 UTC epoch 为锚，
slot=`floor(unix_seconds(synced_at)/effective_interval)`；failed 也是一次已覆盖采集尝试，
同 slot 的第二条及以后计入 duplicate。interval 不能整除 86400、一天内发生 interval
切换，或样本没有可信 effective interval 时，该 UTC 日 coverage 为 null 并给 reason。

`expected_interval_seconds` 是**写入该条 raw 时实际生效的 worker cadence**，不是日后
从当前配置或 policy 猜回来的默认值。新 raw 必须显式写 policy version 与 cadence；
迁移前 legacy raw 保持 cadence null。coverage unknown 是诚实的质量事实，但不等于
“未聚合”：只要该 raw 有同 policy hash/version 的逐样本 receipt，且 receipt 指向的
daily accumulator 已通过 invariants，它仍可取得删除资格；不能让 legacy null 永久
堵住 retention，也不能反过来把 unknown coverage 写成 complete。

## 6. Schema

### 6.1 Raw 表前进式补充

`ops.metric_observation_sample` 保留现有字段，新增：

```text
rollup_policy_version smallint NOT NULL DEFAULT 1,
expected_interval_seconds integer NULL,
CHECK rollup_policy_version > 0,
CHECK expected_interval_seconds IS NULL OR expected_interval_seconds > 0
```

DEFAULT 1 只用于迁移保存既有行；迁移后 `InsertMetricObservationSample` 必须显式传入
policy version 和 effective cadence，禁止依赖 DEFAULT。新增 retention/late-sample 索引
`(environment,metric_key,id)` 与 `(synced_at,id)`。Raw 仍禁止 UPDATE/TRUNCATE；collector
只 INSERT，唯一合法 DELETE 由 DS4 受控 rollup-retention identity 执行。

### 6.2 Daily 表

```text
ops.metric_observation_daily
  environment text NOT NULL REFERENCES core.environment(id),
  metric_key text NOT NULL, source text NOT NULL,
  bucket_day date NOT NULL, bucket_timezone text NOT NULL DEFAULT 'UTC',
  bucket_start_at timestamptz NOT NULL, bucket_end_at timestamptz NOT NULL,
  policy_version smallint NOT NULL, policy_hash text NOT NULL,
  value_kind text NOT NULL, primary_kind text NOT NULL, sum_mode text NOT NULL,
  unit text NOT NULL, scale bigint NOT NULL,
  currency text NULL, currency_set jsonb NOT NULL DEFAULT '[]',
  first_numeric numeric(39,0) NULL, last_numeric numeric(39,0) NULL,
  min_numeric numeric(39,0) NULL, max_numeric numeric(39,0) NULL,
  sum_numeric numeric(39,0) NULL, numeric_count bigint NOT NULL,
  first_full_value_json jsonb NULL, last_full_value_json jsonb NULL,
  last_partial_value_json jsonb NULL,
  first_full_sample_id bigint NULL, last_full_sample_id bigint NULL,
  last_partial_sample_id bigint NULL,
  first_synced_at timestamptz NULL, last_synced_at timestamptz NULL,
  first_observed_at timestamptz NULL, last_observed_at timestamptz NULL,
  first_watermark text NULL, last_watermark text NULL,
  sample_count bigint NOT NULL, full_success_count bigint NOT NULL,
  partial_success_count bigint NOT NULL, failed_count bigint NOT NULL,
  expected_slot_count bigint NULL, covered_slot_count bigint NULL,
  duplicate_count bigint NOT NULL, coverage_ppm integer NULL,
  coverage_unknown_reason text NULL,
  error_counts jsonb NOT NULL DEFAULT '{}',
  min_sample_id bigint NOT NULL, max_sample_id bigint NOT NULL,
  aggregated_at timestamptz NOT NULL
```

唯一键：`(environment,metric_key,source,bucket_day,policy_version)`。

约束至少包括：

- UTC half-open bucket：`bucket_start_at = bucket_day 00:00:00Z`，
  `bucket_end_at = bucket_start_at + interval '1 day'`，timezone 只能为 `UTC`；
- policy version/scale 为正，policy hash 为 64 位小写 hex；kind/sum mode 在白名单；
- 全部 count 非负，三质量桶之和等于 sample_count，`numeric_count <=
  full_success_count`，`min_sample_id <= max_sample_id`；代表 sample ID/时间/JSON
  必须成组同空或同非空，first/last 按 `(synced_at,id)`；
- expected/covered/coverage 三者同空或同非空；known 时 `covered <= expected`、
  `coverage_ppm` 在 0..1,000,000；unknown 时必须有 reason；
- `currency_set` 是去重排序的字符串数组；money 出现多币种时全部 numeric 为 null，
  单币种 numeric 时 `currency` 必须等于集合唯一元素；
- `sum_mode=forbidden` 时 sum 必须为空；`error_counts` 必须为 object，其计数之和
  等于 `failed_count`（DDL 约束形状，语义和溢出由写前 validator + PG verifier 双检）。

### 6.3 Append-only receipt 表

```text
ops.metric_rollup_receipt
  rollup_name text NOT NULL,
  sample_id bigint NOT NULL,
  environment text NOT NULL,
  metric_key text NOT NULL,
  source text NOT NULL,
  bucket_day date NOT NULL,
  policy_version smallint NOT NULL,
  policy_hash text NOT NULL,
  processed_at timestamptz NOT NULL,
  PRIMARY KEY (rollup_name, sample_id)
```

receipt 不对 raw 建会阻止 DELETE 的 FK；它由“同一事务从已锁定 raw INSERT ... SELECT”
保证来源存在。receipt 禁止 UPDATE/DELETE/TRUNCATE，raw 删除后仍保留，作为 exactly-once
与恢复证据；其容量必须进入 DS4 测量与未来独立归档门，DS0 不发明静默清理。

### 6.4 Per-stream state 表

```text
ops.metric_rollup_state
  rollup_name, environment, metric_key,
  policy_hash,
  highest_receipt_sample_id,      # 仅诊断，不能作为扫描/删除谓词
  processed_sample_count,
  last_attempt_started_at,
  last_success_at,
  last_failure_at,
  status,
  last_error_code,
  updated_at,
  PRIMARY KEY (rollup_name, environment, metric_key)
```

v1 rollup name 为 `metric_daily_v1`，但 state 按 environment + metric 分流，坏 key/value
只阻塞自己的 stream，不应让全部指标形成全局 head-of-line。`highest_receipt_sample_id`
只能显示进度；任何读取 `id > highest...` 或删除 `id <= highest...` 都是 schema/代码门禁
应拒绝的反模式。policy hash 不符、unknown version、receipt/state/daily 不一致时，对应
stream 的聚合与删除 fail closed。

## 7. Exactly-once、late sample 与补算

identity 不能充当提交水位。反例必须写进测试：Tx A INSERT 得到 id=100 后不提交；
Tx B INSERT id=101 并先提交；rollup 处理 101；随后 A 提交。任何 `last_id=101`
扫描都会永久漏掉 100。v1 因此使用逐样本 receipt，每个 stream 的固定事务为：

1. `SELECT metric_daily_v1/environment/key state FOR UPDATE`；
2. 从 raw 读取该 stream 中 `NOT EXISTS (SELECT 1 FROM receipt WHERE
   rollup_name='metric_daily_v1' AND sample_id=raw.id)` 的最小一批，按 id 升序；
   聚合输入查询**禁止 `SKIP LOCKED`**，也不允许 `id > state.max`；
3. 按 source/UTC day/policy 分组，严格解 primary integer；
4. merge daily accumulator，校验代表值 `(synced_at,id)`、质量计数、policy/hash；
5. 对本批每个 raw `INSERT` append-only receipt；主键冲突视作当前事务的幂等失败，
   重新读/验证后 no-op，绝不能再次 merge；
6. 在同一事务更新 success state/diagnostic count；若本次获批执行 retention，再按
   “cutoff + 本行 matching receipt + matching daily/policy proof”逐行删除 raw；
7. COMMIT。任一步失败，daily/receipt/success-state/delete 全部 ROLLBACK。

若主事务失败，随后开启一个**独立小事务**只写失败观测：`status=failed`、稳定
`last_error_code`、`last_failure_at`。更新条件必须是 state 的 `last_success_at` 早于本次
attempt start，防止一个较老 attempt 的迟到错误覆盖后来成功；该小事务不得写 daily、
receipt、processed count 或任何删除资格。若主 COMMIT 成功但客户端只收到不确定错误，
receipt unique key 会让重试 no-op，失败小事务也会因 success time 条件不成立而跳过。

因此：

- River retry、提交结果不确定都不重复计数；
- synced_at 很旧但 id 新的 late sample 会先 merge 旧日桶，再具备删除资格；
- 低 ID 晚提交的样本会在下一轮 `NOT EXISTS receipt` 扫描中被发现；
- 初次 backfill 处理全部没有 receipt 的现有 raw；
- policy 变更后以新 version 建独立 accumulator，禁止混写；
- raw 尚在 90 天窗口内时可从头重建；raw 已删的历史只能继续 merge 新 late sample，
  不能换算法重算。算法修复需要恢复已验证备份或保留旧版本并前进式发布；
- 不支持 raw UPDATE/更正；需要更正时追加一条有明确 correction policy 的新样本，
  该能力不在 v1，默认拒绝。

并发不能只信 River Unique：per-stream state row lock + receipt unique key 是最终边界；
两 worker 并发时一个等待/退出，不能对同一样本各自 merge。不同 stream 可安全并行；
unknown key 由独立 guard 报错且 raw 留存，不得堵住其它已知 stream。

## 8. Retention 的安全删除协议

现有 `PruneMetricSamples` 直接 DELETE 必须在 DS4 被替换为受逐样本 receipt 保护的
`RollupAndPruneBatch`。删除资格同时满足：

```text
synced_at < now_utc - raw_retention_days
EXISTS matching append-only receipt for this exact raw.id
receipt policy version/hash known and matches raw + daily row
daily/receipt/state invariants verified for this stream
raw delete kill switch enabled
```

单批聚合、receipt 与删除同事务；批次上限、ctx cancellation 与积压日志保留。
v1 在 state row lock 下不使用 `SKIP LOCKED`；即使未来删除阶段单独获批使用，也不能
据“本批不足 batch size”声称已清空，必须另做 `EXISTS eligible receipted raw` backlog
probe，并记录 `eligible_remaining/oldest_eligible_synced_at`。

出现 policy unknown、rollup lag、daily/receipt constraint、state rollback 或 backup gate
过期时，对应 stream 本轮删除 0 行并返回显式失败/告警，不能“先删再补”。
**coverage unknown 本身不禁止删除**：它只表示无法证明采集时槽完整；删除证明来自
该 raw 的 receipt 与 daily parity。二者必须在 API/日志中分开呈现。

配置拆成两个开关：

```text
XM_METRIC_DOWNSAMPLING_ENABLED=false   # rollup/backfill
XM_METRIC_RAW_DELETE_ENABLED=false     # raw deletion；独立硬门
```

前者开启不允许推导后者也开启。

## 9. Hot/Cold Query 与 API 兼容

### 9.1 Legacy contract

只带 `hours` 的请求保持：

```text
GET /api/v1/metrics/history?metric_key=<key>&hours=1..168
```

- raw-only；原 item 字段、升序与 1000 limit 不变；
- alert consecutive-failure 继续直接调用 raw repository，不走 auto；
- 可在顶层 additive 增加 coverage，但不能删除/改名现有字段。

### 9.2 Opt-in range contract

新增互斥参数：

```text
from=<RFC3339>&to=<RFC3339>
resolution=raw|day|auto
limit=<1..1000>&cursor=<opaque>
```

`hours` 与 from/to 不能并用，非法组合 400；调用方不显式 opt-in 就不会收到 daily。

- `raw`：最多覆盖可用 90 天，按 cursor 分页；
- `day`：只读 daily；
- `auto`：冻结 `evaluated_at`，boundary 是 `evaluated_at` 所在**当前 UTC 日 00:00Z**；
  `bucket_end_at <= boundary` 的完整旧日读 daily，`synced_at >= boundary` 的当前日读
  raw，无 overlap/gap。它不是“严格最近 24h raw”；
- 示例：`evaluated_at=2026-08-28T15:20Z`、请求 `[2026-08-25T00:00Z,
  2026-08-28T15:20Z)`，25/26/27 日返回 daily，28 日 00:00–15:20 返回 raw；
- 合并全序：`(sort_at,resolution_rank,source,id-or-bucket-day,policy_version)`；
- 全局 limit 在 merge 后应用，保留最新点并按全序升序返回；`truncated=true` 时
  `next_cursor` 指向本页**最旧** tuple，下一页使用严格开区间 `tuple < cursor_tuple`
  读取更旧数据，边界点不重不漏；
- cursor 编码固定为 `v1.<base64url-no-padding(canonical-json)>`，长度上限 2048；payload
  含 env/key/from/to（请求区间为 `[from,to)`）、resolution、evaluated_at、UTC boundary、
  policy-set hash、方向 `older` 与完整排序 tuple。未知字段/版本、非 canonical 编码、
  绑定不一致或 policy hash 漂移返回 400 `INVALID_CURSOR`，不能静默重置第一页；
- daily item 是 discriminated union，带 `resolution=day`、bucket、aggregate；
  raw item 带 `resolution=raw`。Legacy hours 仍只产生旧 raw shape。

### 9.3 Coverage

顶层 coverage 至少包含：

```text
complete,
requested_from/to, effective_from/to,
raw_available_from/to,
daily_available_from/to,
resolution_segments[{from,to,resolution,complete}],
sources,
policy_versions[], policy_hashes[],
receipt_count, highest_receipt_sample_id, receipt_verified_at,
expected/covered/full_success/partial_success/failed counts,
unknown_reasons[],
truncated, limit, next_cursor
```

`highest_receipt_sample_id` 仅诊断，不能作为查询或删除水位。每个 availability
端点都必须成对给出 from/to，未知值为 null 并进入稳定 reason。segment 是 half-open
`[from,to)`，相邻段必须首尾相接。

`complete` 只表示请求窗口所需 tier 的**可用性**完整，不代表所有采集尝试成功，也
不因分页截断变 false；`truncated/next_cursor` 单独表达传输分页。库查询失败时不得返回
半页；可用历史本来就缺失时返回 200 + `complete=false`/明确 reason。unknown coverage
返回 null/明确 reason；daily 不计算相对“现在”的 freshness，保留代表 sample/time/
watermark 与质量计数。daily 的 `numeric(39,0)` 全部编码为十进制字符串，raw legacy
JSON number shape 不变。

### 9.4 DS3 前端边界

DS3 选择**range API-only**，不在未获 IA/产品批准时新增历史页、range controls 或
cursor UX。现有 `MetricSparkline` 继续只调用 legacy hours；DS3 只修现存诚实性：
TypeScript raw item 补 `source`，client 返回完整 legacy page，不再丢 `truncated/limit`，
截断在 sparkline 旁可见，并把错误注释从“observed_at 升序”改为
“`(synced_at,id)` 升序”。day/auto UI 是独立 `DS3-UI` NO-GO，必须另批路由、controls、
分页和 BigInt 展示；DS3 API 合并不得宣称前端已消费长期历史。

## 10. River、DBR 与运行边界

- 新建独立 `metric-rollup-worker` 进程、专用 `metric-rollup` queue、独立 pgx pool 与
  `XM_METRIC_ROLLUP_DATABASE_*` CredentialRef；现有 `platform-worker`/collector 不能取得
  raw DELETE 或 daily/receipt/state DML。kind `metric_daily_rollup` 建议每小时；
- DS2 只交付 disabled-by-default 独立 worker/manual CLI/disposable tests；在 R2-10
  集群租约/重复探测证据前不激活多副本 periodic；
- River `ByArgs+ByQueue+ByPeriod` 是第一层，state row lock 是最终幂等层；
- raw retention 由独立 rollup process 的 `metric_raw_retention` 每日调用同一 service；
  现有 platform-worker retention 只继续处理 resolved alerts；rollup 落后时 delete 失败；
- 每轮日志：policy hash、input ID range、days touched、rows aggregated、late rows、
  watermark、raw deleted、lag/error；不记录 value_json/money/source secrets。

DBR 目标权限：

| identity | exact capability |
|---|---|
| collector | latest 所需 DML + raw INSERT；无 raw UPDATE/DELETE |
| API | raw/daily/receipt/state SELECT；无 DML |
| rollup/retention worker | raw SELECT/DELETE、daily/receipt/state SELECT/INSERT/UPDATE；receipt 无 UPDATE/DELETE，raw 无 UPDATE/TRUNCATE |
| migrator | schema/owner/migration only |
| backup | raw/daily/receipt/state 与相关 sequences SELECT |

当前 main 的 shared superuser/owner 状态不满足生产 gate。DBR policy 必须通过新
version + 独立 CR 精确加入 daily/receipt/state/sequence/River queue grants及独立
CredentialRef；DS0 不继承 DBR0 的任何
实施授权，也不能先用 broad worker grant 上线。

## 11. Backup、恢复与回滚

- 恢复包由 source `backup` identity 生成数据库 dump，并附 committed policy bytes、
  SHA-256、migration/image digest manifest；policy 文件不是数据库行，不能声称被 pg_dump；
- target `migrator` identity 只跑 exact migrations；一次性 `restore` identity 只执行
  data-only restore/sequence set，完成即撤销。三者凭据、权限与日志必须分离；
- raw、daily、receipt、state 与 identity sequence 全部进入一致快照；
- restore 顺序为 schema/migration -> raw/daily/receipt/state data + sequence -> policy
  checksum verifier -> rollup parity
  -> history raw/day/auto parity；
- 真实 disposable PG 恢复必须证明 integer >2^53、receipt、quality counts、
  source segments、cursor 与 safe-delete invariant；
- 恢复后验证 sequence next value 严格大于 raw max(id)，并在回滚 fixture 事务里实际
  INSERT 一行证明不会冲突；禁止 `--clean/--create/--disable-triggers`；
- DS1/DS2 rollback：关闭 rollup，旧 API/raw/retention 保持原状；
- DS3 rollback：关闭 range/auto 路由，恢复 legacy hours raw；schema 保留；
- DS4 rollback：先关闭 raw delete。已经删除的 >90d raw 无法由 binary rollback
  复原，只能从已验证 backup 恢复；因此 delete activation 是不可逆审批门；
- 不执行生产 down migration，不 DROP daily/receipt/state，不清空 receipt/state 伪装重跑。

## 12. 真实 PostgreSQL 验证矩阵

必须在 pinned PostgreSQL 18 disposable cluster 证明。PG gate 使用显式 build tag/
命令；缺 DSN **Fatal 而非 Skip**，并在测试开头验证：镜像为 `VERSIONS.lock` 的完整
digest、`server_version_num` major=18、loopback、专用数据库名与 database comment
sentinel、fresh migration version/dirty。共享/业务库立即拒绝：

1. migration up/no-op、约束/索引/owner/ACL/default ACL；
2. `2^53+`、int64 边界、numeric sum overflow、money/fixed-point 无 float；
3. full/partial/failed（含失败携带旧值）不互相污染；
4. UTC 23:59:59.999999 / 00:00、闰日、同 timestamp 不同 ID；
5. source 切换、currency 混合、JSON arrays、15-key exact registry、invoice CR gate、
   unknown policy/key fail closed；
6. retry/concurrent worker、低 ID 未提交而高 ID 先提交、commit-result unknown、每个
   事务边界 fault injection；
7. backfill、receipt 幂等、late sample 更新旧桶、interval change/legacy cadence null、
   policy version split；
8. aggregate-before-delete、逐行 receipt proof、rollup lag 时 0 DELETE；coverage unknown
   仍诚实为 null，但已有 receipt 的 raw 可按其它 gate 删除；
9. raw/day/auto 边界无 overlap/gap、全局排序/limit/cursor/truncated；
10. legacy hours 与 alerts consecutive-failure parity；
11. DBR 正负权限；
12. 三身份 full dump/restore、policy checksum、sequence next-value 后
    daily/receipt/state/history parity。

Skipped integration test、共享开发库、同进程内存 fake 或 SQLite 都不能作为删除证据。

## 13. 分片与审批门

| 分片 | 交付 | 当前授权 |
|---|---|---|
| DS0 | 本 design + implementation plan | docs-only；GO |
| DS1 | 15-key policy、schema、integer/quality accumulator、真实 PG migration tests | conditional GO；仅 disposable PG；CR-0002 + exact policy/diff 先批 |
| DS2 | receipt exactly-once store、late/backfill、manual CLI、disabled 独立 River worker | conditional GO；仅 disposable PG；不接 API/staging/delete |
| DS3 | raw/day/auto repository、HTTP/cursor/coverage + legacy 前端诚实性；range UI 不在本片 | NO-GO；独立 API contract 审批 |
| DS4 | receipt-protected aggregate-and-prune、DBR、River activation、backup/restore、staging soak | NO-GO；raw delete 另有最终硬门 |

审批不继承：DS1 green 不授权 DS2；DS2 green 不授权 DS3；DS3 merge 不授权 DS4
或开启删除。任何物理删除都必须看到 exact-head CI、真实 PG 恢复证据与人类批准。

## 14. 明确不做与 STOP 信号

本设计不：

- 改 finance ledger 的业务日/金额真相；
- 把 JSON arrays 通用展开或按元素聚合；
- 引入 Timescale/新数据库扩展；
- 把历史 daily 点称作 freshness；
- 用 float 保存/计算金额、计数或比例；
- 扩大 history 为任意导出端点；
- 删除 audit、alerts 未解决事件或其它表；
- 在 DS0 创建迁移、worker、API 或配置。

以下任一出现立即 STOP：registry/policy 不等于 15 项或 invoice CR 未冻结；unknown
policy/key；sum snapshot；mixed currency money；
failed value 进入 numeric；partial 冒充 full；非 UTC v1 日桶；watermark 回退；
以标量 ID 跳过 raw；daily/receipt/state 未验即删 raw；auto overlap/gap；丢
source/truncated；River unique 被当作唯一并发锁；rollup 复用 collector DB 身份；
broad DB role；PG gate skip；restore 未通过却启用 delete；staging/production 命令
未经独立批准。

## 15. 本设计审批请求

请逐项批准或否决：

1. 15-key policy v1、“当前所有 snapshot 的 sum 均禁止”及 invoice CR-0002 gate；
2. UTC synced-at 日桶，而非业务日重算；
3. raw/daily/receipt/state schema 与 numeric(39,0) 字符串 API；
4. per-sample receipt exactly-once + late/low-ID-late-commit merge + same-tx
   aggregate/receipt/state/before-delete；
5. legacy hours 保持、range opt-in raw/day/auto、当前 UTC 日 boundary、严格 cursor/
   coverage，以及 DS3 range API-only 边界；
6. DS1/DS2 仅 disposable PG conditional GO；
7. DS3 API/DBR 与 DS4 staging/delete 保持 NO-GO，分别另批。

## 16. Round-1 独立复核销项

| ID | 销项位置 | 必须有的执行证据 |
|---|---|---|
| C1 | §2.1、§4.1：15-key + invoice CR gate | registry exact coverage test |
| C2 | §6.3、§7：append-only receipt/NOT EXISTS | 低 ID 晚提交、unknown commit、retry PG tests |
| C3 | §5、§6.1、§8：写入 effective cadence，unknown coverage 与 deletion proof 分离 | interval change + legacy-null + receipted-delete tests |
| C4 | §9.2：当前 UTC 日 raw | 非午夜固定示例、边界无重无漏 test |
| I1 | §4：full/partial policy 字段 | strict loader tests |
| I2 | §9.2/§9.3：完整 coverage 与严格 cursor | multi-page tuple parity tests |
| I3 | §9.4：range API-only | legacy source/truncated UI tests + 明确 DS3-UI STOP |
| I4 | §10：独立 rollup process/pool/CredentialRef | DBR 正负权限 tests |
| I5 | §12：fail-on-skip pinned PG | version/digest/sentinel/migration assertions |
| I6 | §11：三身份恢复 | policy checksum + sequence next-value + restore parity |
| I7 | §7：失败小事务/防迟到覆盖 | fault + concurrent later-success test |
| M1 | §9.4：修正 synced-at 注释 | frontend test/review |
| M2 | §7/§8：禁止 SKIP LOCKED 生成水位，显式 backlog probe | locked/backlog PG test |
| M3 | §6/§9：DDL 约束、代表 ID、十进制字符串与 cursor 编码 | negative DDL/API fixtures |

本表只说明设计已吸收 review；不替代 DS1～DS4 的审批、实现或第三方 round2。
