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

策略文件是 `contracts/ops/metric-rollup-policy.v1.json`。当前 registry 实测 16
项：14 项 active policy；`invoice.requests.daily` 与 `invoice.amount.daily` 以
CR-0002 的显式 exclusion 保留，未冻结前不会生成或聚合 invoice policy。

## 边界处理

观测时间在未来（时钟漂移）时，`staleness_seconds` 钳到 0，
不显示负数、也不让它看起来「格外新鲜」。
