# 运营指标数据模型

Schema：`ops`。迁移：`db/migrations/000004_init_ops.up.sql`（forward-only）。

## ops.metric_observation

字段对齐规格 §9.1 与附录 K。

| 列 | 说明 |
|---|---|
| metric_key | `^[a-z0-9][a-z0-9_.-]{0,127}$`，允许点分（`sub2api.revenue.daily`） |
| source | 数据来源，前端必须显示 |
| environment | FK → core.environment |
| **observed_at** | **可空**——空表示从未成功采集，是「未初始化」状态的来源 |
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

## 边界处理

观测时间在未来（时钟漂移）时，`staleness_seconds` 钳到 0，
不显示负数、也不让它看起来「格外新鲜」。
