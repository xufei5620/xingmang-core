# 渠道主动探测数据模型（检测任务）

Schema：`assurance`。迁移：`db/migrations/000025_assurance_probes.up.sql`
（同一迁移还给 `core.connector_config` 加了 `probe_enabled`/
`probe_credential_ref` 两列）。权威设计来源：
`docs/adr/ADR-019-渠道主动探测通道.md` 与配套规格文档
`docs/superpowers/specs/2026-09-03-xm-assure1-active-probes-design.md`
（本文档只记实现落地后的实际字段/枚举/口径，设计推理见那两份文档，不重复）。

与 `internal/platform/channelassurance`（XM-ASSURE0，被动指标）完全独立的
数据源，互不共用任何 `metric_key`/表（ADR-019 决策·六）。

## core.connector_config 的两个新列

| 列 | 说明 |
|---|---|
| `probe_enabled` | 平台级 Kill Switch，`boolean NOT NULL DEFAULT false` |
| `probe_credential_ref` | 探测专用凭据引用，`text NOT NULL DEFAULT ''`（与设计稿的可空 text 不同——沿用 `credential_ref` 列"空串=未登记"的既有惯例，不引入第二种"没有值"的表示） |

只经 `internal/platform/credentials.Store.SetProbeSwitch` 写入；
`connector.config.set@1` 的 Handler 从不触碰这两列（SQL 的 `ON CONFLICT`
`SET` 子句里没有它们）。

## assurance.probe_declaration（检测任务声明）

| 列 | 说明 |
|---|---|
| `platform` | `sub2api` \| `newapi` |
| `environment` | FK → `core.environment` |
| `prompt_template_key` | `model_fingerprint` \| `benchmark_set` \| `context_length` \| `min_viable_request`，四个代码固化模板（见下） |
| `target_host` | 探测请求实际打的主机；`run@1` 执行时对照该平台 `target_allowlist` 校验 |
| `targets` | jsonb 数组 `[{"channel_id","external_channel_id?","model"}]`；`channel_id` 必须能在 `<platform>.channels.status` 的 ops 观测里查到 |
| `max_tokens` | `1..512`（512 是代码硬顶，`assurance.MaxTokensHardCap`） |
| `expected_shape` | jsonb `{"min_length","max_length","must_contain[]","timeout_ms"}`；`timeout_ms` 声明上限 30000（`assurance.MaxExpectedTimeoutMS`，与 Job 的硬超时对齐，不是两个独立数字） |
| `schedule_cron` | 可空；**本片存储但不接线**，声明了也不会自动触发（诚实起见，Query 层的策略文案会明说"未接线"） |
| `status` | `active` \| `cancelled`；cancelled 是终态，不可通过 declare 复活 |
| `version` | 乐观并发；更新必须带 `expected_version` |

索引：`(platform, environment, status)`。

## assurance.probe_run（一次探测批次）

| 列 | 说明 |
|---|---|
| `trigger` | `manual`（HUMAN 发起）\| `scheduled`（SERVICE 发起；本片无调度器实际产生这个值） |
| `client_run_key` | 可空，供前端"发起检测"按钮连点去重；**本列是实现期新增**，设计稿 §1.3 建表语句遗漏了它，但 §2.4 明确要求这个去重语义 |
| `status` | `pending` → `running` → `succeeded` \| `failed`；或 `refused`（策略闸拒绝，见下）；或 `cancelled`（仅一条路径会产生：Job 执行时刻发现声明已被撤销，见 §"cancelled 何时出现"） |
| `refusal_reason` | `status IN ('refused','cancelled')` 时非空，取值见"拒绝原因" |

索引：`(platform, environment, created_at DESC)`、`(declaration_id, created_at DESC)`。

### cancelled 何时出现

`assurance.probe.cancel@1` 撤销的是**声明**，不是某一条已经入队的 run——
本片没有"撤销单次批次"的 Action。`probe_run.status='cancelled'` 只在一种
情况下产生：Job 的 `Work()` 在真正执行前重新加载声明，发现它在 Action 接受
与 Job 执行之间被撤销了（`refusal_reason='race_declaration_cancelled'`）。

## assurance.probe_result（每个渠道/模型一条结果）

| 列 | 说明 |
|---|---|
| `status` | `ok` \| `degraded` \| `failed` \| `timeout` |
| `verdict` | 结构化结论文本（如"模型自称一致"/"基准题集 2/3 分（较上次持平）"）；**从不含原始响应文本** |
| `error_kind` | 复用 `internal/platform/connector.ErrorKind` 的分类习惯；`timeout` 由 Job 直接判 ctx 是否超时，不经这套分类 |
| `evidence_ref` | 形如 `probe-8f3a1c2d`：`probe-` + 结果行 uuid 去掉连字符的前 8 位十六进制，纯派生、无额外查询 |
| `first_token_ms` / `measured_first_token` | 只在真实测到时才填；fake 模式的健康响应会给出一个明确标注为模拟值的数字，real 模式（非流式请求）恒 `measured_first_token=false` |

严禁字段：任何存储被探测模型原始返回文本的列（威胁模型 §7.1）。

**基准题得分的存储**：表里没有单独的分数列。`benchmark_set` 模板的得分
编码进 `verdict` 的固定前缀（`"基准题集 %d/%d 分（...）"`），
`assurance.ParseBenchmarkScore` 负责从历史 `verdict` 里把分数解析回来，供
"与上一次比较"的判断（`assurance.Store.LatestBenchmarkScore`）。

## 四个固定模板（`internal/platform/assurance/templates.go`）

模板集合固化在代码里，不是数据库可配置项——新增模板需要新的代码切片。

| key | 断言方式 |
|---|---|
| `model_fingerprint` | 响应是否含该模型的期望自称片段（`modelFingerprintHints` 小型厂商前缀对照表，未命中退化为用声明的 model 字符串本身） |
| `benchmark_set` | 固定 3 题，按子串匹配判分；与上一次分数比较（同一 `(channel_id, model)`），显著下降（差 >1）才判 `degraded` |
| `context_length` | 固定长输入 + 要求复述末尾 32 字符，核对是否命中 |
| `min_viable_request` | 最小 Token 请求，只看响应非空 |

`expected_shape.must_contain`/长度区间对**任意模板**都生效，作为通用附加断言。

## 判定顺序（`assurance.Store.checkGates`）

`assurance.probe.run@1` 接受时与"检测任务"表的 `can_run_now` 走同一份代码：

1. 声明必须 `active`；
2. `core.connector_config` 不存在或 `mode='fake'` → 直接放行（fake 模式零成本，跳过 3-7）；
3. 全局 Kill Switch（`XM_ASSURE_PROBE_ENABLED`）；
4. 平台 Kill Switch（`probe_enabled`）；
5. `probe_credential_ref` 非空；
6. `target_host` 在 `target_allowlist` 内；
7. 当日预算未耗尽（默认 50/平台/日，`XM_ASSURE_PROBE_DAILY_BUDGET` 覆盖，业务日按 UTC+8 固定偏移切分）；
8. 冷却已过（默认 60s，`assurance.DefaultCooldown`，本片未做成环境变量）；
9. 该平台没有其它 `pending`/`running` 的批次。

Job 执行时刻的复检（`RecheckAtExecution`）只做 1、3-7（Kill Switch/凭据/
白名单/预算），不做冷却/并发——那两项在"正在执行的就是被复检的这条 run
自己"这个场景下复检没有意义。**预算复检必须排除被复检的这条 run 自己**
（它此刻已经以 `pending` 落库，会被朴素的计数查询算进去），否则任何"预算
恰好用完这一条"的批次都会在执行时刻把自己算作超额而错误拒绝——这是实现
期发现并修掉的一个真实 bug，测试见 `TestRecheckAtExecutionExcludesItselfFromBudget`。

## 拒绝原因枚举

`declaration_cancelled` / `global_kill_switch_off` / `platform_kill_switch_off`
/ `probe_credential_missing` / `target_host_not_allowlisted` /
`daily_budget_exhausted` / `cooldown_not_elapsed` / `platform_run_in_progress`；
Job 执行时刻复检命中的同一批原因会加 `race_` 前缀。

## Query 形状（`internal/platform/assurance/service.go`）

- `Service.ProbeList(ctx, platform, environment)` → 检测任务表：每条 active
  声明 + 最新批次汇总（多个结果取"最坏"：`failed`/`timeout` > `degraded` >
  `ok`）+ `kill_switch_state`（`enabled`/`disabled`/`not_applicable_fake`）+
  `can_run_now`/`cannot_run_reason`。
- `Service.ProbeHistory(ctx, platform, environment, cursor, limit)` → 主动
  探测历史，按 `created_at` 倒序游标分页（`assurance.DefaultProbeHistoryLimit`
  =50，`MaxProbeHistoryLimit`=200）。

HTTP 形状见 `internal/platform/httpapi/assurance_probes.go`
（`GET /api/v1/platforms/{platform}/assurance/probes` /
`.../assurance/probe-history`，权限复用 `request.read`）。
