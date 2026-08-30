# 运营指标数据模型

Schema：`ops`。基础迁移：`db/migrations/000004_init_ops.up.sql`；历史样本：
`db/migrations/000005_ops_history.up.sql`；DS1 降采样结构：
`db/migrations/000019_metric_downsampling.up.sql`（部署 forward-only）。

## ops.metric_observation

字段对齐规格 §9.1 与附录 K。

| 列 | 说明 |
|---|---|
| metric_key | 落库判据是形态 `^[a-z0-9][a-z0-9_.-]{0,127}$`，允许点分（`sub2api.revenue.daily`）；HTTP 查询另加白名单（`ops.KnownMetricKey`） |
| source | 数据来源，前端必须显示 |
| environment | FK → core.environment |
| **observed_at** | **可空**——空表示从未成功采集。空 + `last_error_code` 也为空才是「未初始化」；空 + 有错误码是「第一次就失败了」，判 `failed` |
| synced_at | 最近一次同步**尝试**的时刻（无论成败） |
| watermark | 数据水位 |
| status | ok / failed |
| is_partial | 数据是否不完整 |
| last_success / last_error_code | 最后成功时间与错误码 |
| staleness_threshold_seconds | 该指标的新鲜度阈值，必须为正 |
| value_json | 指标值 |

业务唯一键：`(metric_key, environment)`。

## 不存的东西

`staleness_seconds` **不落库**（规格 §9.1 明文要求），查询时按 `now - observed_at` 算。
新鲜度 `state` 同样不落库，是派生值。

## 一致性 CHECK

```sql
CHECK ((status = 'failed') = (last_error_code <> ''))
```

让「静默失败」在库层不可表示：失败必须有错误码，成功必须没有。
领域层 `Validate()` 有同一条规则——两层都拦，纵深防御。

## DS1 降采样元数据与日桶

`ops.metric_observation_sample` 继续一行代表一次采集尝试，并新增：

| 列 | 说明 |
|---|---|
| rollup_policy_version | 写入时冻结的 policy 版本；迁移只为旧行回填 `1`，周期 writer 必须显式传入 |
| expected_interval_seconds | 该 writer 本轮实际 cadence；旧行可为 `NULL`，不能从当前配置倒推 |

日粒度结果写入 `ops.metric_observation_daily`，唯一键是
`(environment, metric_key, source, bucket_day, policy_version)`，桶固定为 UTC
半开区间 `[00:00Z, next 00:00Z)`。金额/计数使用整数（numeric(39,0)），质量桶
`full_success_count + partial_success_count + failed_count = sample_count`；snapshot
的 `sum_numeric` 在 v1 始终为空，混币时所有 numeric 字段为空并保留排序后的
`currency_set`。`ops.metric_rollup_receipt` 与 `ops.metric_rollup_state` 只提供
DS1 schema 基座，逐样本 exactly-once/受限 ACL 属后续 DS2/DBR 片。

策略文件是 `contracts/ops/metric-rollup-policy.v1.json`。当前 registry 实测 22
项：14 项 active policy，8 项显式 exclusion（`invoice.requests.daily` 与
`invoice.amount.daily` 以 CR-0002 gate 保留，未冻结前不会生成或聚合 invoice
policy；`sub2api.requests.*` 与 `newapi.requests.*` 六项以 XM-REQLOG-METRICS
gate 保留——`success_rate_24h` 是滚动 24h 窗口而非按业务日切分的快照，
`trend_7d` 把 7 个日桶打包进一条观测的数组里，两者都不符合现有 ValueKind
「一条样本对应一个标量、可选一个业务日」的形状假设；`*.requests.daily`
本身是标准的 daily_snapshot，是未来激活 policy 的候选，但为免在时间盒内
仓促设计半成品语义，本次随同族其余两项一并 exclusion，见
`docs/handoffs/slices/XM-REQLOG-METRICS.md`）。

## 请求量/成功率指标（XM-REQLOG-METRICS）

原料是记录代理落盘的 `index.jsonl`（只读、不解压 gz 明细），由
`connectors/reqlog.MetricsReader` 按 CST 日历日聚合，`platform-worker` 的
周期任务 `reqlog_metrics`（`XM_REQLOG_MODE=file` 时注册）写入。指标键
命名空间是**平台**（sub2api/newapi）而不是连接器（reqlog）——与
`metering.MetricCostDaily` 实际是 `"finance.cost.daily"` 同一条先例。
每个平台三条：

| metric_key | value_json 形状 |
|---|---|
| `sub2api.requests.daily` / `newapi.requests.daily` | `{day, request_count, success_count, failure_count, avg_duration_ms:int\|null}`，只统计当天目录，success=HTTP 2xx |
| `sub2api.requests.success_rate_24h` / `newapi.requests.success_rate_24h` | `{window_hours:24, request_count, success_count, success_rate_bp:int\|null}`，`[now-24h, now]` 闭区间，rate 是**基点整数**（万分之几），0 条为 null |
| `sub2api.requests.trend_7d` / `newapi.requests.trend_7d` | `{days:[{day, request_count, success_count, missing?:true}, …]}`，固定 7 个元素、按日升序、以今天结尾；没有目录的日子 `missing:true` |

新鲜度阈值 900s（`reqlog.MetricsStalenessThresholdSeconds`），采集周期默认
5 分钟（`XM_REQLOG_METRICS_INTERVAL`）。`XM_REQLOG_MODE` 非 file 时任务不
注册——不写观测也不写 not_supported，前端按缺观测显示「未接入」。

## 边界处理

观测时间在未来（时钟漂移）时，`staleness_seconds` 钳到 0，
不显示负数、也不让它看起来「格外新鲜」。
