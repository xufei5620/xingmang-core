# XM-ASSURE1 主动探测（检测任务）设计文档

> **状态：设计稿，待产品负责人拍板。不含实现。**
> 依据：`docs/adr/ADR-019-渠道主动探测通道.md`（决策与理由的权威来源，本文档
> 只展开数据模型/流程/接口细节，不重复 ADR 的推理）；
> `docs/handoffs/slices/XM-ASSURE0-passive-assurance.md` 的 follow_ups；
> `docs/architecture/ADMIN-IA.md` §2.2、§六.3（"检测任务"/"历史记录"子页签的
> 原型形态）。

## 0. 与 ASSURE0 的关系（一句话）

ASSURE0 = 被动、只读、零成本、来自 reqlog 的统计。ASSURE1 = 主动、发真实请求、
有真实成本、来自平台自己新建的探测记录表。两者共享"渠道保障"这个页面壳，
但数据源、写路径、风险等级完全独立，互不依赖、互不冒充。

## 1. 数据模型

新迁移（随 XM-ASSURE1-core 提交，编号待定，当前最新是
`000024_staff_totp`，本设计建议下一个空号）：新增 schema `assurance`，
`core.connector_config` 新增两列。

### 1.1 `core.connector_config` 扩展

```sql
ALTER TABLE core.connector_config
  ADD COLUMN probe_enabled boolean NOT NULL DEFAULT false,
  ADD COLUMN probe_credential_ref text;
```

- `probe_enabled`：平台级 Kill Switch（ADR-019 决策·四·#4）。默认 `false`——
  新平台/新环境天生不允许探测花钱，必须显式打开。
- `probe_credential_ref`：探测专用 API Key 的 `CredentialRef`，与既有
  `credential_ref`（ADR-018 只读 admin 账号）完全分离的字段；校验规则（新增
  到 `ValidateConnectorConfig`）：`mode='real'` 且 `probe_enabled=true` 时必填
  且必须是合法 `secret://` 引用；`mode='fake'` 或 `probe_enabled=false` 时
  允许为空。**不校验它指向的主机**——探测目标主机的允许性由下面 1.2 的
  `target_host` 字段逐条声明并对照现有 `target_allowlist` 校验，`connector_config`
  本身的 `endpoint`/`target_allowlist` 语义不变（那是给只读 admin 通道用的）。

### 1.2 `assurance.probe_declaration`（检测任务声明）

```sql
CREATE SCHEMA IF NOT EXISTS assurance;

CREATE TABLE assurance.probe_declaration (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  platform             text NOT NULL CHECK (platform IN ('sub2api','newapi')),
  environment          text NOT NULL,
  name                 text NOT NULL,              -- 任务名，如"模型指纹"
  prompt_template_key  text NOT NULL,               -- 见 1.2.1 固定模板枚举
  target_host          text NOT NULL,               -- 探测请求实际打的主机，须在 target_allowlist 内
  targets              jsonb NOT NULL,              -- [{channel_id, external_channel_id, model}], 见 1.2.2
  max_tokens           integer NOT NULL CHECK (max_tokens > 0 AND max_tokens <= 512),
  expected_shape       jsonb NOT NULL,              -- 见 1.2.3
  schedule_cron        text,                        -- 可空 = 仅按需触发
  status               text NOT NULL DEFAULT 'active' CHECK (status IN ('active','cancelled')),
  version              integer NOT NULL DEFAULT 1,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           text NOT NULL,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           text NOT NULL,
  cancelled_at         timestamptz,
  cancelled_by         text,
  cancel_reason        text
);
CREATE INDEX ON assurance.probe_declaration (platform, environment, status);
```

#### 1.2.1 固定 Prompt 模板枚举（`prompt_template_key`）

不接受自由文本 Prompt（ADR-019 决策·五）。声明时只能从平台预置的模板里选，
对应原型"检测任务"表格里的任务名：

| key | 对应原型任务名 | 断言方式 |
|---|---|---|
| `model_fingerprint` | 模型指纹 | 响应里是否出现声明的模型自我标识特征串（每个模型固定一条已知探针问题+预期片段，随实现附一份小型对照表，非用户可编辑） |
| `benchmark_set` | 基准题集 | 固定小题集（≤3 题）算分，与上一次分数比较是否显著下降 |
| `context_length` | 上下文长度 | 固定长度输入 + 要求复述末尾片段，校验是否命中 |
| `min_viable_request` | 最小可用请求 | 最小 Token 请求，只看是否 200 且非空 |

模板集合本身随 XM-ASSURE1-core 实现固化在代码里（非数据库可配置项），新增
模板需要新的切片而非运行时配置——避免探测的"考什么题"变成一个不受控的输入面。

#### 1.2.2 `targets` 结构

```json
[{"channel_id": "chn_xxx", "external_channel_id": "12", "model": "claude-sonnet-4"}]
```

`channel_id`/`external_channel_id` 必须能在 XM-CHAN-FIELDS0 建立的渠道目录
（`contracts/connectors/sub2api.channel-catalog.v3.md`/
`newapi.channel-catalog.v3.md` 描述的字段）里查到，声明时的 Action Handler
必须回查目录做存在性校验，不接受"渠道目录里查无此渠道"的声明。

#### 1.2.3 `expected_shape` 结构

```json
{"min_length": 1, "max_length": 2000, "must_contain": ["..."], "timeout_ms": 30000}
```

均为形状/长度/超时层面的浅层断言，**不做语义正确性判断**——历史记录/检测任务
UI 文案必须明确写"形状与延迟检测，非语义正确性保证"（ADR 决策·六 / 威胁模型
§7.5），不得让运营误以为"通过"等于"这个模型现在完全正确"。

### 1.3 `assurance.probe_run`（一次探测批次）

```sql
CREATE TABLE assurance.probe_run (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  declaration_id   uuid NOT NULL REFERENCES assurance.probe_declaration(id),
  platform         text NOT NULL,
  environment      text NOT NULL,
  trigger          text NOT NULL CHECK (trigger IN ('manual','scheduled')),
  requested_by     text,                     -- principal_id；scheduled 触发为 'worker:platform'
  status           text NOT NULL CHECK (status IN
                     ('pending','running','succeeded','failed','refused','cancelled')),
  refusal_reason   text,                     -- 見 3.4 拒绝原因枚举，仅 status='refused' 非空
  action_run_id    uuid,                     -- 关联 action.action_run，供审计回溯
  started_at       timestamptz,
  finished_at      timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON assurance.probe_run (platform, environment, created_at DESC);
CREATE INDEX ON assurance.probe_run (declaration_id, created_at DESC);
```

`status='refused'` 的行**是一次完整的、值得展示的历史记录**——拒绝执行本身
也要可见（宪法 12 条），不是"这次调用没有发生所以不记"。

### 1.4 `assurance.probe_result`（每个渠道/模型一条结果）

```sql
CREATE TABLE assurance.probe_result (
  id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  run_id                 uuid NOT NULL REFERENCES assurance.probe_run(id),
  channel_id             text NOT NULL,
  external_channel_id    text,
  model                  text NOT NULL,
  status                 text NOT NULL CHECK (status IN ('ok','degraded','failed','timeout')),
  verdict                text NOT NULL,        -- "一致"/"得分下降 12%"/"达标"/"1 个账号冷却" 等展示文案的结构化来源
  latency_ms             integer,
  first_token_ms         integer,
  measured_first_token   boolean NOT NULL DEFAULT false,
  tokens_used            integer,
  http_status            integer,
  error_kind             text,                 -- 结构化错误类别，不含上游原始 body/header
  evidence_ref           text NOT NULL,         -- 形如 "probe-8842"，展示态证据编号
  observed_at            timestamptz NOT NULL,
  created_at             timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON assurance.probe_result (channel_id, model, created_at DESC);
CREATE INDEX ON assurance.probe_result (run_id);
```

**严禁字段**：任何存储被探测模型原始返回文本的字段。威胁模型 §7.1 详述原因；
`verdict`/`error_kind` 必须是探测代码计算好的结构化结论，不得是原始响应的
直接拷贝或截断。

### 1.5 保留期

`assurance.probe_run`/`probe_result` 按 `internal/platform/jobs/retention.go`
既有的"分批删除、按环境隔离"纪律接入保留期任务，建议默认 90 天（与团队既有
"历史类明细表"保留期同量级，负责人可调整）。`assurance.probe_declaration` 不
参与保留期清理（声明是配置，不是明细日志；`cancelled` 状态的声明保留供审计
回溯"这条渠道曾经声明过检测"）。

## 2. Action：`assurance.probe.declare@1` / `.cancel@1` / `.run@1` / `.kill_switch.set@1`

四个 Action 均为 **L1**（ADR-019 决策·三对 `run@1` 的推理是本文档的关键前提，
`declare`/`cancel`/`kill_switch.set` 是普通配置写操作，与 L1 分类无争议）。

### 2.1 `assurance.probe.declare@1`

```text
permission: assurance.probe.manage
principal_types: [HUMAN]
environments: [development, staging, production]
idempotency: EXPECTED_DECLARATION_VERSION（更新时必填，防止并发覆盖，模式同 finance.platform_channel_binding 的 expected_binding_id）
write_confirmation: ACTION_RESULT_AND_QUERY_REFETCH
compensation_mode: AUTOMATIC（cancel 即可逆）
params:
  platform: enum[sub2api, newapi], required
  name: string, required
  prompt_template_key: enum[model_fingerprint, benchmark_set, context_length, min_viable_request], required
  target_host: string, required
  targets: array<{channel_id, external_channel_id?, model}>, required, 非空
  max_tokens: integer, required, 1..512
  expected_shape: object, required
  schedule_cron: string, optional
  declaration_id: string, optional（提供则为更新既有声明，否则新建）
  expected_version: integer, 更新时必填
```

Handler 校验顺序：Schema → `platform` 合法 → `targets` 逐条回查渠道目录存在性
→ `target_host` 若该平台已有 `connector_config` 记录则不做交叉校验（探测的
主机允许性在 `run@1` 执行时对照 `target_allowlist` 现查，声明时不提前假设
连接器已配置）→ 写入 `assurance.probe_declaration`（`version` 自增，
`expected_version` 冲突返回 `PRECONDITION_FAILED`，与
`finance.platform_channel_binding.set@1` 同一先例）。

### 2.2 `assurance.probe.cancel@1`

```text
permission: assurance.probe.manage
principal_types: [HUMAN]
compensation_mode: AUTOMATIC（重新 declare 即可恢复）
params:
  declaration_id: string, required
  reason: string, required
```

Handler：declaration 必须存在且 `status='active'`；写 `status='cancelled'`
+ `cancelled_at`/`cancelled_by`/`cancel_reason`；已 cancelled 的声明再次
cancel 返回 `PRECONDITION_FAILED`（不是静默成功——防止"我以为我刚取消了"
的误判）。

### 2.3 `assurance.probe.kill_switch.set@1`

```text
permission: assurance.probe.kill_switch
principal_types: [HUMAN]
compensation_mode: AUTOMATIC（改回原值即可恢复）
params:
  platform: enum[sub2api, newapi], required
  probe_enabled: boolean, required
  probe_credential_ref: string, required when probe_enabled=true, 否则可省略/清空
```

刻意用**独立权限**（`assurance.probe.kill_switch`），不复用
`connector.config.set@1` 的权限点：能批准"这个平台允许探测花钱"的人，未必
需要同时拥有"改连接器怎么连上游、换只读 admin 凭据"的权限——两者是可以分开
授予、分开审计的不同操作（ADR-019 决策·四·#4）。Handler 只更新
`probe_enabled`/`probe_credential_ref` 两列，不触碰 `connector_config` 的
`mode`/`endpoint`/`target_allowlist`/`credential_ref`。`probe_enabled=true`
时校验 `probe_credential_ref` 是合法 `secret://` 引用且该平台当前
`mode='real'`（fake 模式下打开这个开关没有意义，Handler 直接拒绝并提示先切
`mode=real`）。

### 2.4 `assurance.probe.run@1`

```text
permission: assurance.probe.run
principal_types: [HUMAN, SERVICE]   -- SERVICE 供 §3 的定时调度以 worker:platform 触发；刻意不含 AI（见 §7.6）
compensation_mode: NOT_POSSIBLE（探测一旦发出真实请求，花费已经发生，无法"撤销"这次调用本身；可撤销的只是后续调度，走 cancel）
idempotency: OPTIONAL_CLIENT_RUN_KEY（调用方可传 client_run_key 去重快速连点，见 3.3）
params:
  declaration_id: string, required
  client_run_key: string, optional
```

Handler 执行顺序（**任何一步不满足都写一条 `status='refused'` 的
`probe_run` 行并正常返回，不是 Action 层错误**——"决定不跑"本身是一次成功
的 Action 执行，返回值里带 `refused` 状态和原因，供前端和历史记录展示）：

1. 查 `declaration`，必须 `status='active'`，否则 `refused`
   reason=`declaration_cancelled`；
2. 查 `core.connector_config`（platform+environment）：
   - 不存在或 `mode='fake'` → **直接放行到第 6 步**，走假连接器，不检查
     Kill Switch/预算/白名单（fake 模式零成本，见 ADR 决策·四）；
   - `mode='real'` 时依次检查：
     a. 全局 Kill Switch `XM_ASSURE_PROBE_ENABLED` 为真，否则 `refused`
        reason=`global_kill_switch_off`；
     b. `probe_enabled=true`，否则 `refused` reason=`platform_kill_switch_off`
        （即 UI 上的"未启用"状态）；
     c. `probe_credential_ref` 非空，否则 `refused`
        reason=`probe_credential_missing`；
     d. `declaration.target_host` 落在 `target_allowlist` 内，否则 `refused`
        reason=`target_host_not_allowlisted`；
3. 查今日（业务日边界，同 `OrderSummary.Day` 的时区口径）已产生的
   `probe_run`（`trigger` 不限、`status` 非 `refused`/`cancelled`）计数，
   达到该平台每日预算上限则 `refused` reason=`daily_budget_exhausted`；
4. 查同一 `declaration_id` 最近一次非 `refused` 的 `probe_run.created_at`，
   距今不足冷却间隔（建议 60s，负责人可调）则 `refused`
   reason=`cooldown_not_elapsed`；
5. 查该 `platform+environment` 是否已有 `status IN ('pending','running')`
   的 `probe_run`，存在则 `refused` reason=`platform_run_in_progress`
   （并发闸，见 §3.2 关于为什么不能只靠 River `UniqueOpts`）；
6. 全部通过：插入 `probe_run(status='pending', trigger, requested_by)`，
   入队 River 任务 `assurance_probe`（携带 `run_id`），返回
   `{run_id, status:"pending"}`。

`client_run_key` 去重：若同一 `declaration_id` + `client_run_key` 已存在一条
`pending`/`running` 的 `probe_run`，直接返回该 run 而不新建（防止前端"发起
检测"按钮被连点两次造成两条重复历史）。

## 3. River 任务 `assurance_probe`

```go
const AssuranceProbeJobKind = "assurance_probe"
const QueueAssuranceProbe = "assurance_probe" // 新队列，与 QueueMaintenance 隔离：
                                               // 探测是外部可见、有真实成本的动作，
                                               // 不该被内部维护任务的排队延迟影响，反之亦然
type AssuranceProbeArgs struct { RunID string }
func (AssuranceProbeArgs) Kind() string { return AssuranceProbeJobKind }
func (AssuranceProbeArgs) InsertOpts() river.InsertOpts {
    return river.InsertOpts{
        MaxAttempts: 1,          // 不自动重试——重试可能对已经花过一次钱的探测再收一次费（ADR 决策·五）
        Queue:       QueueAssuranceProbe,
    }
}
```

### 3.1 为什么不用 River `UniqueOpts` 做并发闸

`connector_probe` 任务能用 `ByPeriod` 去重，是因为它的 `ConnectorProbeArgs`
除可选的测试用 `RunID` 外没有任何区分字段——所有生产触发的参数完全相同。
`AssuranceProbeArgs.RunID` 对每次探测批次都不同（它就是主键），`UniqueOpts`
按参数去重在这里**保证不了**"同平台同时只有一个在跑"，因为两个不同批次的
`RunID` 天生不相等。并发闸因此必须在 Action Handler 层（步骤 5）和 Job
执行开始时各查一次 `probe_run` 表状态，是应用层锁而非 River 层锁——这一点
在实现时必须写进代码注释，避免后来者以为"River UniqueOpts already handles
this"。

### 3.2 Work() 执行顺序

1. Context 取消检查；
2. 重新加载 `probe_run` + `declaration` + 当前 `connector_config`——**在执行
   时刻再检查一次 Kill Switch/预算**（Action 通过到 Job 真正执行之间存在
   竞态窗口：有人可能在这段时间关闭了开关或预算刚好被另一个并发请求耗尽），
   任一条件此刻不再满足，`probe_run` 更新为 `status='refused'`（追加
   `refusal_reason`，前缀 `race_`），Job 视为**成功完成**（拒绝执行不是
   Job 失败）；
3. 构造探测客户端：fake 模式用内存假实现（立即返回声明模板对应的固定"健康"
   响应，供 XM-ASSURE1-core/ui 两片验证全链路，零网络调用）；real 模式用
   `probe_credential_ref` 解出的 Key 对 `target_host` 发 HTTP 请求；
4. `probe_run.status='running'`，`started_at=now()`；
5. 对 `targets` 逐条（不并发，顺序执行，避免同一批次对同一渠道的多个模型
   同时开多条连接放大瞬时成本）：
   - 单次请求超时 30s（模型推理可能比健康检查慢得多，但仍需要一个硬顶，
     防止任务无限挂起吃满 Job 槽位）；
   - 记录 `probe_result`：`status`/`verdict` 按 `expected_shape` 断言计算，
     `first_token_ms`/`measured_first_token` 只在真实测到时填，
     `error_kind` 用结构化分类（复用 `connector.ErrorKind` 的分类习惯），
     从不落原始响应文本（威胁模型 §7.1）；
   - 单个 target 失败不中断批次，继续下一个（与 `connector_probe`
     "sub2api 失败不影响 newapi" 同一条纪律的批内版本）；
6. 全部 target 处理完（无论各自 ok/degraded/failed/timeout），
   `probe_run.status='succeeded'`，`finished_at=now()`——**这里的"succeeded"
   指"批次跑完并写全了结果"，不代表所有渠道都健康**，与
   `connector_probe.probe()` "probeOK 计的是探测本身成功，不是
   healthy=true" 同一措辞纪律；
7. 任何一步的**存储写失败**（不是探测本身的失败）才让 `probe_run` 落
   `status='failed'`，Job 返回 error 走一次显式的 `error` 级日志（复用
   `recordAudit` 的"审计缺口必须刺眼"精神），因为 `MaxAttempts=1` 不会自动
   重试去重新发一次已经可能发生过的付费调用。

### 3.3 定时调度（`schedule_cron` 非空的声明）

由 River 的周期调度（与现有 `XM_*_INTERVAL` 机制同构）在到期时以
`principal_id='worker:platform'`、`principal_type=SERVICE`、`trigger=scheduled`
调用 `assurance.probe.run@1`——**调度器本身只是按时调用同一个 Action**，不
绕过 Handler 的任何一步检查（Kill Switch/预算/冷却/并发闸对定时触发同样
生效，定时任务一样会在预算耗尽的日子被拒绝并诚实记录）。

## 4. Kill Switch 语义（汇总）

| 情形 | `probe.run` 行为 | UI 呈现 |
|---|---|---|
| `mode='fake'` | 总是放行，走假连接器 | 正常显示探测结果，无需任何开关 |
| `mode='real'`，`XM_ASSURE_PROBE_ENABLED` 未设/false | 拒绝，`global_kill_switch_off` | "全局检测开关已关闭"（运维可见，不下钻到具体平台） |
| `mode='real'`，全局开、`probe_enabled=false` | 拒绝，`platform_kill_switch_off` | 检测任务行显示"未启用"pill，"发起检测"按钮禁用+提示 |
| `mode='real'`，两个开关都开、缺 `probe_credential_ref` | 拒绝，`probe_credential_missing` | "探测凭据未登记"，指向治理段密钥引用 |
| 全部满足但预算耗尽 | 拒绝，`daily_budget_exhausted` | "今日探测预算已用完，明日 00:00（业务日）重置" |
| 全部满足但冷却中/已有在跑批次 | 拒绝，`cooldown_not_elapsed` / `platform_run_in_progress` | 按钮短暂禁用+倒计时或"检测进行中" |

## 5. Query：检测任务表 / 历史记录

### 5.1 `GET /api/v1/platforms/{platform}/assurance/probes`（检测任务）

权限复用 `request.read`（与 ASSURE0 的 `保障概览`/`历史记录` 端点同一个
scope，理由同 ASSURE0 交接文档："能复用就不新开一个 scope"——检测任务表的
敏感度与被动指标同级，都是"这个平台今天调用多不多/健不健康"，不含
Prompt 具体输出）。返回声明列表 + 每条声明最新一次 `probe_run`/其结果聚合，
供原型"检测任务"表格：

```json
{
  "platform": "sub2api",
  "probes": [{
    "declaration_id": "...",
    "name": "模型指纹",
    "channel_names": ["Claude 官方 API"],
    "target_models": ["claude-sonnet-4"],
    "policy_text": "按需 · 无定时" | "按需 + 每 6h",
    "last_run_at": "2026-09-03T02:30:00Z" | null,
    "last_run_status": "ok" | "degraded" | "failed" | "refused" | "never_run",
    "last_run_verdict": "一致" | null,
    "kill_switch_state": "enabled" | "disabled" | "not_applicable_fake",
    "can_run_now": true,
    "cannot_run_reason": null | "platform_kill_switch_off" | "..."
  }],
  "freshness": {...}
}
```

### 5.2 `GET /api/v1/platforms/{platform}/assurance/probe-history`（历史记录，主动部分）

同样复用 `request.read`。返回 `probe_result` 明细流（分页），供原型"历史
记录"表格。**这个端点与 ASSURE0 既有的
`GET .../assurance/history`（被动近 7 天聚合）是两个独立端点**，前端"历史
记录"子页签分别调用，各自渲染各自的卡片（见 §6），不合并成一次请求。

## 6. UI 状态（对齐原型列结构，`docs/architecture/ADMIN-IA.md` 引用的
`serve_xingmang_v4.py` 渲染态）

### 6.1 检测任务子页签（`V["s2/model"]("probes")`）

原型列：任务 / 渠道 / 目标模型 / 策略 / 最近一次 / 结果，本设计追加"发起
检测"操作列：

- 结果列的 pill 状态：`ok`（一致/达标）/ `warn`（下降/冷却/degraded）/
  `bad`（failed/timeout）/ 新增 `muted`"未启用"（`kill_switch_state=disabled`
  时，不与业务结果混在同一套颜色语义里）/ `muted`"从未运行"
  （`last_run_status=never_run`）；
- "发起检测"按钮：`can_run_now=false` 时禁用并 tooltip 显示
  `cannot_run_reason` 的人话版本（"该平台检测未启用，请在治理段打开开关"等，
  不显示原始 `reason` 枚举字符串）；
- 页面顶部保留一条说明条，把原型"这是目标布局"的 `warnbar` 换成"检测结果
  为形状与延迟检测，不构成语义正确性保证"的常驻说明（不是"未接入"，因为
  此片之后后端是真的）。

### 6.2 历史记录子页签（`V["s2/model"]("history")`）

渲染**两张独立卡片**，都带各自的新鲜度/覆盖率说明：

1. "被动聚合（近 7 天）"——ASSURE0 既有实现原样保留，本片不改一行；
2. "主动检测历史"——新卡片，原型列：时间 / 渠道 / 模型 / 检测项 / 结果 /
   证据，`证据`列展示 `evidence_ref`（`probe-8842` 形态），结果 pill 同
   §6.1。

两卡之间有清晰的标题分隔与说明文字，禁止合并成一张"看起来是同一份数据"的
表格（ADR-019 决策·六）。

### 6.3 保障概览（`overview`，KPI 行）

原型 KPI 行"受保障渠道/模型映射/24h 检测/需复核"里的"24h 检测"格此前无
数据源；本片之后可以接：`24h 检测` = 近 24h `probe_result` 行数，`需复核` =
`status IN ('warn','bad')` 的最新结果渠道去重数。是否在 ASSURE1-ui 这一片
就接这两格、还是继续显示"未接入"，留给该切片依据当时的验收节奏决定，本
设计不强制。

## 7. 威胁模型

### 7.1 探测凭据泄漏

`probe_credential_ref` 全程只以 `CredentialRef` 形式流转（ADR-014），明文
不落库、不进日志、不进前端、不进 AI 上下文。**探测响应体本身也是一个新的
敏感面**：它是真实模型对平台自己 Prompt 的真实输出，理论上可能包含被上游
（或者一个行为异常的渠道）刻意构造的、意图在管理后台里造成 XSS 或界面欺骗
的文本。缓解：`probe_result` 表结构上就不设"原始响应文本"字段（§1.4），
只落结构化的 `verdict`/`error_kind`/数值字段；即使未来需要展示片段用于
排障，也必须走既有的 `esc()` 转义纪律（`web/apps/admin-web` 现有约定），
不新开一条"探测结果原样渲染"的例外通道。

### 7.2 失控成本

四层闸：固定小 Prompt（不可自由文本）+ `max_tokens` 硬上限 + 每平台每日
预算硬顶 + 并发 1/同平台 + 触发冷却 + 失败不自动重试。任一层被绕过都还有
其余层兜底；纯代码层 Handler 校验（不依赖前端）。

### 7.3 上游侧影响

探测账号在 Sub2API/NewAPI 自己系统里应当是一个可被识别的专用账号（如有
展示名/标签能力，建议负责人在开通探测账号时打上明显标识），使被管平台自己
的运营也能把探测流量和真实客户流量分开——这是账号侧的运营约定，不是平台代码
能强制的，作为交接文档的开放问题之一交给负责人。

### 7.4 Kill Switch 竞态

Action 接受时检查一次，Job 真正执行时再检查一次（§3.2 步骤 2），覆盖
"接受之后、执行之前"这段窗口被关闭开关或用光预算的情况。

### 7.5 断言的误导性

`expected_shape` 只做浅层断言，UI 文案必须明示这一点（§1.2.3），不得让"一致
/达标"pill 被解读成"模型完全正确"的强保证。

### 7.6 AI 主体的谨慎排除

`assurance.probe.run@1` 的 `principal_types` 故意只含 `HUMAN`/`SERVICE`，
不含 `AI`——虽然 L1 一般不禁止 AI 身份（ADR-009 讲的是"AI 复用 Action、不
设后门"，不是"AI 不能碰 L1"），但探测会花真钱、打真实第三方相邻的产品接口，
在还没有运行时间证据之前，本设计选择保守：**先不让 AI 身份触发探测**，
之后如果确有 AI 值守探测的场景，需要产品负责人单独评估并修改
`PrincipalTypes`（这本身是一次显式的 Action 定义变更，不是运行时开关）。

## 8. 测试矩阵

### 8.1 Handler/内核

- `declare`：合法声明成功；`targets` 含渠道目录不存在的 `channel_id` 拒绝；
  `max_tokens` 超 512 拒绝；`expected_version` 冲突返回
  `PRECONDITION_FAILED`；更新已 cancelled 的声明允许（重新声明视为新建，
  还是必须先"复活"？——本设计选择**不允许**，必须走新的 declare 不带
  `declaration_id`，取新 id，cancelled 是终态，测试断言这一点）。
- `cancel`：合法取消成功；重复取消 `PRECONDITION_FAILED`；取消不存在的
  `declaration_id` 返回 `NOT_FOUND` 类错误。
- `kill_switch.set`：`probe_enabled=true` 但未带 `probe_credential_ref`
  拒绝；`mode='fake'` 时尝试打开拒绝；成功路径只改两列、不改
  `mode`/`endpoint`。
- `run`：fake 模式全链路成功（无需任何 connector_config 记录）；
  `mode='real'` 六种拒绝原因各自命中（`declaration_cancelled`/
  `global_kill_switch_off`/`platform_kill_switch_off`/
  `probe_credential_missing`/`target_host_not_allowlisted`/
  `daily_budget_exhausted`/`cooldown_not_elapsed`/
  `platform_run_in_progress`，共八种，逐一构造前置状态断言精确原因，不能
  互相顶替）；预算边界值（恰好等于上限 vs 上限+1）；`client_run_key` 去重
  返回同一 `run_id` 而非新建。

### 8.2 River 任务

- Work() 对每个拒绝原因在执行时刻的"竞态复检"分支（§3.2 步骤 2）；
- 单 target 超时不影响其余 target 继续处理，`probe_run` 仍以 `succeeded`
  收尾，`probe_result` 里对应一行 `status='timeout'`；
- 存储写失败让 Job 返回 error 且不重试（断言 `MaxAttempts=1` 生效，同一
  `run_id` 不会被 River 重新入队执行第二次）；
- `assurance_probe` 用独立队列，不与 `maintenance` 队列的任务互相阻塞
  （集成测试起两条队列各插一个长任务，断言互不等待）。

### 8.3 HTTP/Query

- 检测任务/历史记录两个端点各自的 `request.read` scope 校验（缺 scope
  403）；
- 历史记录端点返回结构里被动卡片与主动卡片字段互不覆盖，其中一个数据源
  为空（如从未探测过）时另一个仍正常渲染；
- 检测任务表返回的 `cannot_run_reason` 与实际 Kill Switch/预算状态一致
  （集成测试逐一构造六种状态断言接口输出）。

### 8.4 前端

- "发起检测"按钮在各 `cannot_run_reason` 下的禁用态与 tooltip 文案；
- "未启用"pill 与"从未运行"pill 视觉上与 ok/warn/bad 结果 pill 可区分
  （不同色系，不复用 warn 的黄色去表示"没启用"这种非结果状态）；
- 历史记录两张卡片各自独立渲染、独立空态；
- **安全测试**：构造一个 fake 探测结果里 `verdict`/`error_kind` 含
  `<script>`/HTML 特殊字符的用例，断言渲染为字面文本而非被解析执行
  （复用现有 `esc()` 测试模式）。

### 8.5 治理/门禁

- `gitleaks`：确认没有任何测试 fixture 把 `probe_credential_ref` 写成
  看起来像真实密钥的字面量（复用团队记忆里 gitleaks generic-api-key
  误报的处理方式：常量抽出 + 明显占位符命名）；
- `bash scripts/check-governance.sh` 通过。

## 9. 上线路径（Rollout）

1. **XM-ASSURE1-core**：迁移（`assurance` schema + `connector_config` 两列）、
   四个 Action、`assurance_probe` River 任务、fake 模式探测客户端。**全程
   fake 模式验证**，不需要任何真实凭据，不产生真实费用。real 模式的 HTTP
   客户端代码同样在本片写出并单测（对着测试用的假 HTTP Server 验证请求构造/
   超时/错误映射），但不针对任何真实厂商执行。
2. **XM-ASSURE1-ui**：检测任务/历史记录子页签接入真实 Query/Action，移除
   原型遗留的"这是目标布局"`warnbar`。真实浏览器验证走 ASSURE0 同款证据
   纪律（截图 + Playwright MCP），全部在 fake 模式下完成。
3. **XM-ASSURE1-real**：前提是负责人已经（a）在选定平台的真实产品里开通
   一个专用探测账号/Key；（b）通过 `connector.config`/新
   `kill_switch.set@1` 把 `probe_credential_ref` 登记好；（c）打开该平台
   `probe_enabled`；（d）在部署环境设置 `XM_ASSURE_PROBE_ENABLED=true`。
   本片的工作主要是**证据收集**（对一个真实渠道跑一次真实探测、人工核对
   结果），而非新增代码，产出一份 `docs/evidence/EV-<日期>-assure1-real-verify.md`。
