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
单调 ID 水位**”模型：

1. `ops.metric_observation_sample` 继续保存至少 90 天 raw，每次采集尝试仍是一行；
2. 新增 `ops.metric_observation_daily`，按 UTC 日、environment、metric、source、
   policy version 保存可合并的日统计与代表 JSON；
3. 新增 `ops.metric_rollup_state`，以 raw identity `id` 做 exactly-once 水位；
4. 当前 13 个 metric_key 逐个冻结 value kind、primary JSON pointer、单位、scale、
   currency/day 语义与 sum policy；未知 key fail closed，不能降采样后删除 raw；
5. `status=failed` 的旧值不参与数值统计，partial success 与 full success 分开；
6. v1 日桶固定 `[00:00:00Z, next 00:00:00Z)`，history 横轴继续是 `synced_at`；
7. 聚合、推进水位与删除必须在同一数据库事务内满足“先聚合、后删除”；
8. legacy `hours=1..168` 路径保持 raw-only；新参数 opt-in `raw/day/auto`，
   mixed window 在 UTC 午夜切层，统一排序/limit/cursor/coverage；
9. River unique 只是第一层，state row lock 才是并发/幂等最终边界；
10. 删除是单向门：没有 backfill、水位、API parity、DBR、kill switch、真实恢复
    演练与 soak，任何 raw DELETE 都是 **NO-GO**。

阶段判定：

| 阶段 | 当前判断 |
|---|---|
| DS0 docs/design | **GO**；本文件与配套计划可评审 |
| DS1 policy/schema + DS2 aggregator | **条件 GO**；仅 disposable PG，须先批准逐指标 policy 与 exact migration diff |
| DS3 API/UI contract | **NO-GO**；须另批响应契约、cursor、coverage 与 UI 语义 |
| DBR / River 多副本 / staging backfill | **NO-GO**；须等各自 gate 与证据 |
| DS4 raw DELETE / production | **硬 NO-GO**；删除门不得由前片批准继承 |

## 2. 当前事实与证据

### 2.1 Raw schema 与写入

- `db/migrations/000005_ops_history.up.sql:8` 创建
  `ops.metric_observation_sample`；字段为 `id,metric_key,source,environment,
  observed_at,synced_at,status,is_partial,watermark,last_error_code,value_json`；
- `id` 是 `GENERATED ALWAYS AS IDENTITY`，提供与时钟无关的全序；
- `db/queries/ops.sql:32` 只有 raw INSERT；`internal/platform/ops/store.go:269`
  的 `Store.UpsertWithSample` 在同一事务更新 latest 并追加 raw；
- `status` 只有 `ok|failed`；`is_partial` 与 status 正交。失败 observation 可能保留
  上次成功的 `value_json`，因此失败行的 value 不能当本轮成功数值；
- `decodeValueJSON` 使用 `json.Decoder.UseNumber`，金额 minor units 不经 float；
- 当前 series 索引为 `(environment,metric_key,synced_at DESC)`，历史排序实际依赖
  `(synced_at,id)`。

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
- 同文档的“5 个指标、value_json 几十字节”已漂移：当前注册表有 13 个 key，
  channel/model/token/account 数组可能显著放大行。实施前必须在 disposable/获批
  staging read-only snapshot 记录 row count、daily growth、p50/p95/p99 row bytes、
  relation/index size 与每 key/source/environment cadence；本设计不声称当前容量值。

## 3. 方案比较

### 3.1 推荐：显式 daily accumulator 表

优点：每个语义可审计；integer/fixed-point 不经 float；late sample 可 merge；
水位可证明 deletion safety；冷热 Query 可明确 coverage；不引入新数据库扩展。

代价：需要 policy、daily/state schema、worker、API/UI 与恢复测试，不能当成一个
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

- unknown key、unknown policy version/kind/pointer/scale fail closed；
- policy 变更发布新版本，不能就地重新解释已删除 raw 的旧 daily row；
- `sum_mode=additive` 只允许真正 delta/event 指标。当前 13 个 key **全部禁止把
  同一天多次 snapshot 相加**；`sum_numeric` 在 v1 为 null；
- currency 不唯一时所有 money numeric aggregate 为 null，并记录
  `mixed_currency`；不能把不同币种 minor units 相加；
- fixed-point 只保存 integer + scale，例如 ppm scale=1,000,000；禁止 float；
- JSON arrays 不做通用 element-wise aggregate，只保存代表 JSON 与 policy 指定的
  primary integer。

### 4.1 当前 13 个 key 的 v1 口径

| metric_key | value_kind | primary | 日统计 |
|---|---|---|---|
| `sub2api.users.total` | gauge | count `/total_users` | first/last/min/max；sum null |
| `sub2api.users.balance` | gauge | money `/balance_minor_units` + `/currency` | first/last/min/max；sum null |
| `sub2api.revenue.daily` | daily_snapshot | money `/amount_minor_units` | last 代表当日读数；first/min/max 诊断；sum null |
| `sub2api.cost.daily` | daily_snapshot | money `/amount_minor_units` | 同上 |
| `sub2api.channels.balance` | document_status | count `/channel_count` | 数值 first/last/min/max + representative JSON |
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
expected/coverage 为 null；不能用 0 或 100% 假装已知。

## 6. Schema

### 6.1 Raw 表前进式补充

`ops.metric_observation_sample` 保留现有字段，新增：

```text
rollup_policy_version smallint NOT NULL DEFAULT 1,
expected_interval_seconds integer NULL,
CHECK expected_interval_seconds IS NULL OR > 0
```

新增 retention/late-sample 索引 `(synced_at,id)`。Raw 仍禁止 UPDATE/TRUNCATE；
collector 只 INSERT，唯一合法 DELETE 由 DS4 受控 retention 执行。

### 6.2 Daily 表

```text
ops.metric_observation_daily
  environment, metric_key, source,
  bucket_day date, bucket_timezone='UTC',
  bucket_start_at, bucket_end_at,
  policy_version, value_kind, primary_kind,
  unit, scale, currency, currency_set,
  first_numeric numeric(39,0), last_numeric numeric(39,0),
  min_numeric numeric(39,0), max_numeric numeric(39,0),
  sum_numeric numeric(39,0), numeric_count bigint,
  first_full_value_json, last_full_value_json, last_partial_value_json,
  first_synced_at, last_synced_at,
  first_observed_at, last_observed_at,
  first_watermark, last_watermark,
  sample_count, full_success_count, partial_success_count, failed_count,
  expected_slot_count, covered_slot_count, duplicate_count, coverage_ppm,
  error_counts jsonb,
  min_sample_id, max_sample_id,
  aggregated_at
```

唯一键：`(environment,metric_key,source,bucket_day,policy_version)`。

约束至少包括：UTC half-open bucket、非负 counts、三质量桶之和等于 sample_count、
min/max sample ID 正序、coverage 仅在 expected known 时出现、money mixed currency
时 numeric 必须为空、sum_mode forbidden 时 sum 必须为空。

### 6.3 State 表

```text
ops.metric_rollup_state
  rollup_name PRIMARY KEY,
  policy_hash,
  last_sample_id,
  safe_delete_sample_id,
  last_completed_bucket_end,
  last_run_at,
  status,
  last_error_code,
  updated_at
```

v1 只有 `metric_daily_v1`。`safe_delete_sample_id <= last_sample_id`；policy hash
不符、unknown version 或 state 缺失时 delete fail closed。

## 7. Exactly-once、late sample 与补算

每批固定事务：

1. `SELECT metric_daily_v1 state FOR UPDATE`；
2. 冻结 `batch_max_id`，读取 `id > last_sample_id AND id <= batch_max_id`
   的 raw，按 id 升序；
3. 按 environment/key/source/UTC day/policy 分组，严格解 primary integer；
4. merge daily accumulator；incoming `min_sample_id` 必须大于该 daily row 既有
   `max_sample_id`，否则拒绝重复/乱序 merge；
5. 推进 `last_sample_id`；完整验证后才推进 `safe_delete_sample_id`；
6. 如本事务执行 retention，只删除 `synced_at < cutoff AND
   id <= safe_delete_sample_id`；
7. COMMIT。任一步失败，daily/state/delete 全部 ROLLBACK。

因此：

- River retry 不重复计数；
- synced_at 很旧但 id 新的 late sample 会先 merge 旧日桶，再具备删除资格；
- 初次 backfill 从水位 0 处理全部现有 raw；
- policy 变更后以新 version 建独立 accumulator，禁止混写；
- raw 尚在 90 天窗口内时可从头重建；raw 已删的历史只能继续 merge 新 late sample，
  不能换算法重算。算法修复需要恢复已验证备份或保留旧版本并前进式发布；
- 不支持 raw UPDATE/更正；需要更正时追加一条有明确 correction policy 的新样本，
  该能力不在 v1，默认拒绝。

并发不能只信 River Unique：state row lock/advisory lock 是最终边界；两 worker
并发时一个等待/退出，不能各自推进水位。

## 8. Retention 的安全删除协议

现有 `PruneMetricSamples` 直接 DELETE 必须在 DS4 被替换为受水位保护的
`RollupAndPruneBatch`。删除资格同时满足：

```text
synced_at < now_utc - raw_retention_days
id <= safe_delete_sample_id
policy version known
daily/state invariants verified
raw delete kill switch enabled
```

单批聚合与删除同事务；批次上限、`SKIP LOCKED`、ctx cancellation 与积压日志保留。
出现 policy unknown、rollup lag、daily constraint、coverage unknown、state rollback
或 backup gate 过期时，本轮删除 0 行并返回显式失败/告警，不能“先删再补”。

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
- `auto`：默认旧完整日读 daily、最近 24h 读 raw；24h 是 v1 固定策略，不能按
  估算点数静默漂移；
- mixed boundary 固定 UTC 00:00：daily 只含 `bucket_end_at <= boundary`，raw
  只含 `synced_at >= boundary`，无 overlap/gap；
- 合并全序：`(sort_at,resolution_rank,source,id-or-bucket-day,policy_version)`；
- 全局 limit 在 merge 后应用，保留最新点；`truncated=true` 时提供 opaque
  `next_cursor`，cursor 绑定 env/key/from/to/resolution/policy hash；
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
policy_version/hash,
rollup_watermark_id, safe_delete_sample_id, rollup_verified_at,
expected/covered/full_success/partial_success/failed counts,
truncated, limit, next_cursor
```

`complete` 只表示本请求窗口被所需 tier 完整解析，不代表所有采集尝试成功。
unknown coverage 返回 null/明确 reason；daily 不计算相对“现在”的 freshness，保留
first/last observed/synced/watermark 与质量计数作为来源/新鲜度证据。

## 10. River、DBR 与运行边界

- 新 kind `metric_daily_rollup`，maintenance queue，建议每小时；
- DS2 只交付 disabled-by-default worker/manual CLI/disposable tests；在 R2-10 集群级
  所有权证据前不激活多副本 periodic；
- River `ByArgs+ByQueue+ByPeriod` 是第一层，state row lock 是最终幂等层；
- retention 仍每日，但 DS4 后调用同一 rollup service；rollup 落后时 delete 失败；
- 每轮日志：policy hash、input ID range、days touched、rows aggregated、late rows、
  watermark、raw deleted、lag/error；不记录 value_json/money/source secrets。

DBR 目标权限：

| identity | exact capability |
|---|---|
| collector | latest 所需 DML + raw INSERT；无 raw UPDATE/DELETE |
| API | raw/daily/state SELECT；无 DML |
| rollup/retention worker | raw SELECT/DELETE、daily/state SELECT/INSERT/UPDATE；无 raw UPDATE/TRUNCATE |
| migrator | schema/owner/migration only |
| backup | raw/daily/state 与相关 sequences SELECT |

当前 main 的 shared superuser/owner 状态不满足生产 gate。DBR policy 必须通过新
version + 独立 CR 精确加入 daily/state/sequence grants；DS0 不继承 DBR0 的任何
实施授权，也不能先用 broad worker grant 上线。

## 11. Backup、恢复与回滚

- raw、daily、state、policy version/hash 全部进入逻辑备份；
- restore 顺序为 schema/migration -> raw/daily/state data -> verifier -> rollup parity
  -> history raw/day/auto parity；
- 真实 disposable PG 恢复必须证明 integer >2^53、watermark、quality counts、
  source segments、cursor 与 safe-delete invariant；
- DS1/DS2 rollback：关闭 rollup，旧 API/raw/retention 保持原状；
- DS3 rollback：关闭 range/auto 路由，恢复 legacy hours raw；schema 保留；
- DS4 rollback：先关闭 raw delete。已经删除的 >90d raw 无法由 binary rollback
  复原，只能从已验证 backup 恢复；因此 delete activation 是不可逆审批门；
- 不执行生产 down migration，不 DROP daily/state，不清空水位伪装重跑。

## 12. 真实 PostgreSQL 验证矩阵

必须在 pinned PostgreSQL 18 disposable cluster 证明：

1. migration up/no-op、约束/索引/owner/ACL/default ACL；
2. `2^53+`、int64 边界、numeric sum overflow、money/fixed-point 无 float；
3. full/partial/failed（含失败携带旧值）不互相污染；
4. UTC 23:59:59.999999 / 00:00、闰日、同 timestamp 不同 ID；
5. source 切换、currency 混合、JSON arrays、unknown policy/key fail closed；
6. retry/concurrent worker、每个事务边界 fault injection；
7. backfill、水位单调、late sample 更新旧桶、policy version split；
8. aggregate-before-delete、rollup lag/unknown coverage 时 0 DELETE；
9. raw/day/auto 边界无 overlap/gap、全局排序/limit/cursor/truncated；
10. legacy hours 与 alerts consecutive-failure parity；
11. DBR 正负权限；
12. full dump/restore 后 daily/state/history parity。

Skipped integration test、共享开发库、同进程内存 fake 或 SQLite 都不能作为删除证据。

## 13. 分片与审批门

| 分片 | 交付 | 当前授权 |
|---|---|---|
| DS0 | 本 design + implementation plan | docs-only；GO |
| DS1 | policy、schema、integer/quality accumulator model、真实 PG migration tests | conditional GO；仅 disposable PG；exact policy/diff 先批 |
| DS2 | exactly-once store、watermark、late/backfill、manual CLI、disabled River worker | conditional GO；仅 disposable PG；不接 API/staging/delete |
| DS3 | raw/day/auto repository、HTTP/cursor/coverage、前端 page/source 修复 | NO-GO；独立 API/UI contract 审批 |
| DS4 | aggregate-and-prune、DBR、River activation、backup/restore、staging soak | NO-GO；raw delete 另有最终硬门 |

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

以下任一出现立即 STOP：unknown policy/key；sum snapshot；mixed currency money；
failed value 进入 numeric；partial 冒充 full；非 UTC v1 日桶；watermark 回退；
daily/state 未验即删 raw；auto overlap/gap；丢 source/truncated；River unique 被当作
唯一并发锁；broad DB role；restore 未通过却启用 delete；staging/production 命令
未经独立批准。

## 15. 本设计审批请求

请逐项批准或否决：

1. 13-key policy v1 与“当前所有 snapshot 的 sum 均禁止”；
2. UTC synced-at 日桶，而非业务日重算；
3. raw/daily/state schema 与 numeric(39,0) 字符串 API；
4. state-row exactly-once + late merge + same-tx aggregate-before-delete；
5. legacy hours 保持、range opt-in raw/day/auto/cursor/coverage；
6. DS1/DS2 仅 disposable PG conditional GO；
7. DS3 API/DBR 与 DS4 staging/delete 保持 NO-GO，分别另批。
