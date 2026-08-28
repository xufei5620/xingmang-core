# XM-C-MAP0 平台渠道与上游账号绑定设计

> 状态：**待人审，未批准，禁止实施**
> 日期：2026-08-28
> 任务：路线图 C「平台渠道 ↔ 上游账号映射」Design 阶段
> 权威链：`PROJECT-CONSTITUTION.md` → ADR / `BASELINE-v2.1.md` → UI 原型渲染态 →
> `ADMIN-IA.md` → Contracts → 本设计。发生冲突时按该顺序停下并上报。

## 0. 审批声明

本文件和配套实施计划只定义意图、契约、迁移边界与验证方法。它们**不授权**：

- 创建或执行数据库迁移；
- 发布 Sub2API Connector v2；
- 新增 Query、Action 或 scope；
- 运行存量回填；
- 切换利润采集器口径；
- 修改 UI、部署、合并 PR 或触碰生产。

迁移、新 scope、Connector 契约版本、Query/Action 契约均命中
`docs/handoffs/CODEX-PROJECT-HANDOFF.md` 第五节的前置审批门。产品负责人须在
本文件末尾逐项确认；任何一项被否决，本设计先修订，MAP1 不开工。

## 1. 问题与当前证据

### 1.1 原型和 XM-0052 的粒度不同

UI 原型的渠道页一行是被管平台自己的一条渠道：

```text
Sub2API Account.id / NewAPI Channel.id
    → 上游账号
    → 分组 / Key
    → 渠道级收入、成本、毛利、健康、模型、runway 引用
```

XM-0052 / PR #100 暂时把 `ChannelTable` 改成一行一个
`finance.upstream_account`。原因不是原型变了，而是当前
`GET /api/v1/finance/channels/summary` 与
`GET /api/v1/finance/upstreams/summary` 都按 `upstream_account_id` 出行；
平台渠道 ID 与登记簿 UUID 没有正式对应关系。账号粒度让金额真实，但会把同一
上游账号下的多个平台渠道合成一行。

### 1.2 平台渠道身份已经存在，但没有正式绑定实体

真实身份来自被管平台 Connector：

| 平台 | 外部真相源 | 当前 Go 投影 | 身份字段 |
|---|---|---|---|
| Sub2API | `GET /api/v1/admin/accounts` | `sub2api.ChannelBalance` | `Account.id` → `ChannelID` |
| NewAPI | `GET /api/channel/` | `newapi.ChannelStatus` | `Channel.id` → `ChannelID` |

`finance.token_map.own_account_id` 与这个 ID 在收入侧恰好同义：Sub2API 是
`/admin/accounts/{id}/stats` 的账号 ID，NewAPI 是 `quota_data.channel_id`。
但 `token_map` 是「成本侧令牌 ↔ 收入侧账号」事实，不是渠道目录或绑定权威：

- 没有具体 managed service instance；
- 环境只经 `upstream_account` 间接获得；
- 没有渠道生命周期和绑定生效区间；
- 不限制同一渠道在不同 `upstream_account` 下重复出现；
- 不能表示零令牌但已存在的渠道；
- 无法区分同环境两个实例里相同的外部 ID。

所以 `token_map` 只能生成**候选证据**，不能自动成为已确认绑定。

### 1.3 当前两项结构限制

1. `finance.upstream_account.platform_id` 是单值，不能表达一个上游账号同时服务
   多个平台渠道或多个 managed service；它保留作兼容投影，本期不删除。
2. `ops.metric_observation` 当前唯一键是 `(metric_key, environment)`，虽然有
   `source` 字段，却不能同时保留同环境两个相同指标来源。多实例策略因此是本
   spec 的显式审批问题，不能由实现方顺手改变。

## 2. 目标与非目标

### 2.1 目标

1. 定义稳定、无碰撞的平台渠道 canonical identity。
2. 持久化可审计、可并发保护、保留历史的渠道绑定。
3. 从 Connector 渠道目录出行，未映射也不隐藏。
4. 只把 `token_map` 当候选，所有绑定由人确认。
5. 恢复原型行粒度，同时保证收入不重复、成本不蒸发、共享 runway 不被重复合计。
6. 保留现有账号粒度端点兼容期，避免 C001 等已交付页面回归。
7. 把 Sub2API 与 NewAPI 的能力差异、unknown、partial、stale 原样暴露。

### 2.2 非目标

- 不修改 Sub2API、NewAPI、CPA 三方源码，不直写它们的业务表。
- 不实现渠道启停、权重调整、Key 轮换或其他第三方写操作。
- 不实现 M1.5 模型保障 Runner、探针或保障结论。
- 不用绑定表保存渠道名称、健康、模型、余额、金额或凭据明文。
- 不追溯改写 `finance.profit_daily` 历史。
- 不删除 `upstream_account.platform_id` 或 `token_map.own_account_id`。
- MAP1~MAP4 不把新 binding 直接切成 Collector 的写入权威；本期先做读侧投影与
  冲突闸。未来改变核算写入路径须另开独立 spec，因为它会改变新台账事实。
- 不做批量生产绑定 Action；Foundation-B 前只允许单条 L1 人工确认。

## 3. 方案比较

### 方案 A：直接复用 `finance.token_map`

改动少，但把令牌与渠道生命周期混为一体，不能表示零令牌渠道，也没有 service
identity、有效期或跨账号唯一性。否决。

### 方案 B：当前态表 + 只依赖 Action 审计做历史

可以回答「现在绑到谁」，但历史区间查询只能从审计事件重放；绑定变化后，普通
Query 无法直接解释哪个时期绑定到哪个上游。否决。

### 方案 C：时间版本化 binding（采用）

每次重绑关闭旧区间并插入新行；当前唯一性由部分唯一索引保证，Action 审计记录
人、理由和前后态。它不需要新 PostgreSQL 扩展，能独立测试，并保持历史可查询。

## 4. Canonical identity

### 4.1 定义

```text
ChannelRef := (service_id, external_channel_id)
```

- `service_id`：`core.service.id`，指向具体 managed service instance。
- `external_channel_id`：Connector 从上游原样取得的渠道 ID，存为不透明文本。
- 环境不是 identity 的第三段，但 binding 行必须显式带 `environment`，并由复合
  外键证明 service 与 upstream account 都属于同一环境。

不使用下列弱键：

- `external_channel_id`：不同系统和实例可重复；
- `(service_type, external_channel_id)`：同类型多实例可重复；
- `metric_key`：它是观测种类，不是资源身份；
- `upstream_account_id`：一个上游可服务多个渠道；
- `token_map.own_account_id`：缺 service 维度且只是候选证据。

### 4.2 ID 规范化

只执行首尾空白清理；清理后为空即拒绝。NewAPI 当前 ID 虽可解析为整数，公共
模型仍存 text，不做数值重写、大小写转换或前导零删除。Connector 若将来改变
ID 形态，应发布新契约，而不是在 binding 层猜测等价关系。

## 5. 单实例 / 多实例观测策略（审批问题 MI-1）

### 5.1 本计划推荐：MAP1~MAP4 采用单观测实例 fail-closed

Binding 从第一天使用 `service_id`，数据模型天然支持多实例；但本路线图切片不
同时改造全站指标、导航和实例选择器。具体规则：

1. Query 必须接受 `service_id`。
2. UI 从平台类型进入时，后端只在该环境、该 `service_type` 恰有一个 active
   service 时替它补 service；零个返回 `not_connected`，多个返回
   `ambiguous_service`，不选第一条。
3. 最新观测的 `source` 必须等于该 service 的 `instance_id`；不相等时整份渠道
   目录视为不可用于绑定确认。
4. MAP1~MAP4 不修改 `ops.metric_observation` 的唯一键。

### 5.2 被否决的隐式方案

继续按 `(metric_key, environment)` 取任意观测，同时 binding 用 `service_id`，会把
A 实例的渠道清单贴到 B 实例的绑定上。这个错误不报错且 ID 很可能碰巧存在，禁止。

### 5.3 若人审选择真多实例

本 spec 必须退回修订，至少增加：

- `ops.metric_observation` 唯一键迁移为 `(metric_key, source, environment)`；
- ops Query 显式按 source 过滤；
- 历史表、告警 source 归属与前端 `Map<metric_key,...>` 消费者兼容审计；
- 平台页实例选择器及 Router Search Params；
- 对现存重复 key 的确定性回填与回滚计划。

配套实施计划按 5.1 编写；审批若选择 5.3，不能直接执行现计划。

## 6. 数据模型

### 6.1 表

```sql
CREATE TABLE finance.platform_channel_binding (
    id                  uuid PRIMARY KEY,
    environment         text NOT NULL,
    service_id          uuid NOT NULL,
    external_channel_id text NOT NULL,
    upstream_account_id uuid NOT NULL,

    valid_from          timestamptz NOT NULL,
    valid_to            timestamptz,

    provenance          text NOT NULL,
    reason              text NOT NULL,
    created_by          text NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT platform_channel_binding_environment_fk
      FOREIGN KEY (environment)
      REFERENCES core.environment(id) ON DELETE RESTRICT,

    CONSTRAINT platform_channel_binding_service_environment_fk
      FOREIGN KEY (service_id, environment)
      REFERENCES core.service(id, environment) ON DELETE RESTRICT,

    CONSTRAINT platform_channel_binding_account_environment_fk
      FOREIGN KEY (upstream_account_id, environment)
      REFERENCES finance.upstream_account(id, environment) ON DELETE RESTRICT,

    CONSTRAINT platform_channel_binding_channel_non_blank
      CHECK (btrim(external_channel_id) <> ''),
    CONSTRAINT platform_channel_binding_reason_non_blank
      CHECK (btrim(reason) <> ''),
    CONSTRAINT platform_channel_binding_creator_non_blank
      CHECK (btrim(created_by) <> ''),
    CONSTRAINT platform_channel_binding_provenance_allowed
      CHECK (provenance IN ('manual', 'token_map_backfill')),
    CONSTRAINT platform_channel_binding_interval_ordered
      CHECK (valid_to IS NULL OR valid_to > valid_from)
);

CREATE UNIQUE INDEX platform_channel_binding_active_key
  ON finance.platform_channel_binding(service_id, external_channel_id)
  WHERE valid_to IS NULL;

CREATE UNIQUE INDEX platform_channel_binding_start_key
  ON finance.platform_channel_binding(service_id, external_channel_id, valid_from);

CREATE INDEX platform_channel_binding_upstream_history_idx
  ON finance.platform_channel_binding(upstream_account_id, valid_from, valid_to);
```

为复合外键增加两条候选键：

```sql
CREATE UNIQUE INDEX service_id_environment_key
  ON core.service(id, environment);

CREATE UNIQUE INDEX upstream_account_id_environment_key
  ON finance.upstream_account(id, environment);
```

二者虽因各自主键已逻辑唯一，仍是 PostgreSQL 复合外键所需的显式 referenced key。

### 6.2 关系基数

```text
一个 ChannelRef --当前时刻--> 0 或 1 个 upstream_account
一个 upstream_account ------> 0 到 N 个 ChannelRef
一个 ChannelRef --跨时间----> N 条互不并发的 binding 区间
```

不建立 `(upstream_account_id, external_channel_id)` 唯一索引：两个不同 service
可以有相同外部 ID，且同一上游可服务它们；只有完整 `ChannelRef` 唯一。

### 6.3 时间与历史

- 时间由服务端 UTC 时钟生成，Action 参数不接受 `valid_from/valid_to`，禁止回填者
  自己伪造生效时刻。
- set/rebind 在一个事务中：锁当前 active 行，关闭为 `valid_to=T`，插入
  `valid_from=T` 的新行。
- remove 只关闭当前行，不删除。
- `upstream_account_id` 不在原行上 UPDATE；目标变化只能产生新区间。
- 本期不用 `btree_gist` 和 exclusion constraint；Action 是唯一业务写入口，部分
  唯一索引负责并发 active 冲突，领域测试负责区间连续性。获批生命周期脚本必须
  复用同一组校验。
- binding 历史只解释绑定决策，不重算旧台账。历史金额以
  `profit_daily` 已冻结的 `platform_id/account_id/upstream_account_id` 为准。

## 7. 并发与 Action 语义

### 7.1 乐观并发

`set@1` 参数：

```json
{
  "service_id": "uuid",
  "external_channel_id": "opaque text",
  "upstream_account_id": "uuid",
  "expected_binding_id": "uuid or omitted",
  "reason": "non-empty text"
}
```

规则：

| 当前态 | expected | 目标 | 结果 |
|---|---|---|---|
| 无 active | 省略 | 任意合法账号 | 插入 |
| 有 active | 省略 | 任意 | `CONFLICT` |
| 有 active | 不等于当前 ID | 任意 | `CONFLICT` |
| 有 active | 等于当前 ID | 同一账号 | 幂等 no-op 回执，审计仍记录请求 |
| 有 active | 等于当前 ID | 新账号 | 同事务 close + insert |

`remove@1` 必须给 `expected_binding_id` 和 `reason`；找不到或已变化返回
`CONFLICT`，不静默成功。

并发创建在“无 active 行可锁”时依靠部分唯一索引仲裁；唯一冲突映射成稳定的
Action `CONFLICT`，不把 PostgreSQL 错误原文透传。

### 7.2 前置校验

单条 Action 为 L1、仅 HUMAN：

1. 环境从 Principal 取得，参数无 environment。
2. service、upstream account 均存在且与 Principal 同环境。
3. service 未 retired，`service_type` 为 `sub2api` 或 `newapi`。
4. 最近一次成功且未过期的完整渠道目录包含该 ChannelRef。
5. 目录 partial/truncated、从未成功、source 不匹配时禁止新建/重绑。
6. 当前 token-map 证据若指向多个上游或与目标相反，返回冲突；先修证据，不能
   让“已确认绑定”和“正在参与核算的映射”公开打架。
7. 不修改 `token_map`、`upstream_account.platform_id` 或第三方系统。

## 8. 候选四态与人工确认

候选是 Query 的纯投影，不落 binding 表。对 Connector 渠道目录、当前 binding、
同环境 `token_map` 和 `upstream_account.platform_id` 做 union 后，每个未确认对象
恰为以下四态之一：

| 状态 | 判据 | UI / Action |
|---|---|---|
| `unmapped` | 已观测渠道；无同 ID token-map 证据 | 显示未映射；允许人手选账号 |
| `candidate` | 已观测渠道；恰好一个 distinct 上游候选；无非空 platform mismatch | 显示证据与“待确认”；绝不自动写 |
| `conflict` | 多个 distinct 上游候选，或 `platform_id` 非空且指向别的 service | 金额/毛利 fail closed；先人工消歧 |
| `orphan` | token-map/binding 有 ChannelRef 证据，但完整最新目录没有该渠道 | 保留调查；不删除、不自动解绑 |

补充规则：

- `platform_id IS NULL` 不否决唯一候选，但响应必须带
  `platform_assignment_missing=true`。
- partial/truncated/stale/failed 目录不能证明渠道不存在；此时既有对象为
  `inventory_unknown` 新鲜度状态，不转换成 orphan。
- 只有最近成功、完整、source 匹配的目录才能产生“没看到该渠道”的 orphan 判据。
- `candidate` 必须经 `set@1` 人工确认；候选算法、定时任务、AI 均无写权限。
- production 批量确认不在本期；未来批量 Action 至少 L2，需预览、幂等、写后确认。

## 9. Connector 渠道目录门

### 9.1 Sub2API：必须先发布 read contract v2

v1 `ChannelBalance` 不是完整目录：真实客户端对 `/admin/accounts` 解码后，会跳过
没有 quota limit 的账号；订阅型/无余额概念账号因此从 `sub2api.channels.balance`
消失。MAP1 必须新增不依赖余额的 v2 目录：

```go
type ManagedChannel struct {
    Snapshot
    ChannelID         string
    Name              string
    Status            string
    BalanceMinorUnits *int64 // nil = 没有余额概念，不是 0
    Currency          string
}

type ChannelDirectoryReader interface {
    Channels(context.Context) ([]ManagedChannel, error)
}
```

要求：

- `/admin/accounts` 每条有合法 ID 的账号都出行；无 quota 只让余额为 nil。
- 保留 v1 `ChannelBalances()` 与 `sub2api.channels.balance` 兼容期。
- 新指标 `sub2api.channels.status` 承载完整数组；新鲜度取最旧成员，partial/truncated
  可见。
- v2 不增加写方法、不解凭据字段、不调用 `/cn-providers/.../balance` 写/探测路径。

### 9.2 NewAPI：v1 足够建立身份，能力不足必须照实

`newapi.ChannelStatus` 已列 `/api/channel/` 的 ID、名称、类型、enabled、余额三态、
`model_count`、错误率和延迟，可直接作为 MAP1 目录。现有限制原样保留：

- `balance_updated_time=0` → 未配置，不是 0；
- 余额可能旧；
- 错误率最多覆盖部分渠道；
- 只有模型数，没有模型名；
- 没有 assurance 结论。

Mapping 不等待 NewAPI v2，也不得用 `model_count` 冒充“已验证模型数”。

## 10. Query 与权限

### 10.1 Binding Query

```http
GET /api/v1/finance/platform-channel-bindings
    ?service_id=<uuid>
    &include_history=false
```

权限：`finance.read`。返回 confirmed bindings、候选四态和 orphan；不返回凭据明文，
不混入健康指标。

### 10.2 渠道投影 Query

```http
GET /api/v1/platforms/{platform}/channels
    ?service_id=<uuid>
    &from=YYYY-MM-DD
    &to=YYYY-MM-DD
```

权限：同时需要 `ops.read` 与 `finance.read`。缺任意一项均 403，错误指出缺哪个
scope。响应以 Connector 目录为骨架：

```text
ChannelRef + name + observed
  LEFT JOIN active binding / candidate state
  LEFT JOIN channel economics
  LEFT JOIN connector health
  LEFT JOIN model/assurance projection
  LEFT JOIN upstream-account runway reference
```

当前 `/finance/channels/summary` 保留账号粒度兼容期；C001 跨账号合计继续用它，
不能在 MAP3 偷换响应粒度。

### 10.3 Action 与 scope

```text
finance.platform_channel_binding.set@1
finance.platform_channel_binding.remove@1
scope: finance.platform_channel_binding.manage
risk: L1
principal: HUMAN only
compensation_mode: MANUAL
```

新 manage scope 不加入 `oidcauth.DefaultRoleScopeMap` 的 staff 或 admin。生产授权只
能由人工审定的 `XM_OIDC_ROLE_SCOPES` 明确给出；“admin 应该什么都有”不是理由。

## 11. 渠道级经营联接与去重

### 11.1 骨架与 unknown

渠道目录永远是主表，binding/finance 都是 LEFT JOIN：

- 无 binding：渠道仍显示；finance 与 runway 为 null，标 `unmapped`。
- binding 存在但上游账号 disabled：显示绑定和 disabled，不隐去。
- 目录无成功观测：页面显示未初始化/失败，不拿 finance 账号反造渠道。
- 未知金额保持 null，绝不写 0。

### 11.2 利润查询

对 ChannelRef 的窗口只读取冻结事实：

```text
profit_daily.platform_id = core.service.instance_id
profit_daily.account_id  = external_channel_id
profit_daily.upstream_account_id = confirmed binding target
```

逐业务日规则：

1. 成本可对该渠道日内的令牌成本行求和，但有任一未知即覆盖不全。
2. 使用收入是账号级事实，每 `(service, channel, business_day)` 只能确认一次。
3. `revenue_known_rows == 0` → 收入未知；`== 1` → 可用；`> 1` →
   `duplicate_revenue_conflict`，不得求和。
4. 出现同一 ChannelRef、同一天、不同 `upstream_account_id` 的已知收入，同样冲突。
5. 禁止 `SUM(DISTINCT revenue_minor)`：不同合法事实金额相同并不等于重复。
6. 任一冲突、覆盖不全、混币种时，渠道金额/毛利/毛利率按既有纪律返回 null，并
   回冲突/覆盖证据；不返回偏低或放大的“部分合计”。
7. `gross_profit = usage_revenue - supply_cost`，收入 ≤ 0 时 margin 为 null；金额
   scale-6、无 float。

当前 Collector 已处理“同一上游账号内，多令牌供给同一 own account”的收入一次
原则；跨 upstream-account 的重复证据由本 Query fail closed。MAP1~MAP4 不改变
Collector 写入语义，避免一个 UI 映射切片顺手改变新财务事实。

### 11.3 健康与模型

- 健康按相同 ChannelRef 从同一目录观测联接。
- Sub2API v2 只承诺账号状态与可空余额，不承诺成功率/延迟。
- NewAPI v1 可给 enabled/error-rate/latency/model-count，partial 和观测时间随值返回。
- Sub2API 模型、NewAPI 模型名、“已验证/已配置”、assurance 均没有现成真相源；
  返回 null + `not_integrated:M1.5`，不推导。
- M1.5 的模型保障产物必须以同一 ChannelRef 为外键/关联键，不能另造 URL/name 键。

### 11.4 Runway 与共享上游

Runway 继续按 `upstream_account_id` 计算：余额 ÷ 该上游全部映射渠道的近 7 个完整
业务日日均消耗。投影到渠道行时：

- 每行引用同一个 account-level runway；
- `shared_channel_count > 1` 时显示“共享余额 / 共 N 渠道”；
- 顶部余额、最紧 runway、coverage 以 distinct `upstream_account_id` 计算；
- 禁止按渠道行重复相加余额、成本或 coverage 分母；
- 订阅型保持 `not_applicable`，不进 runway coverage 分母。

## 12. 存量回填与迁移纪律

1. 执行 MAP2 时动态读取当时最高 `.up.sql`，使用下一连续编号；本文不冻结
   `000013`，因为 A/B 并行切片可能先占号。
2. 迁移只建表、约束、索引，不插入 confirmed binding。
3. 首轮 Query 从 Connector + token_map 产生候选四态。
4. development/staging 逐条 L1 Action 人工确认；production 不自动确认。
5. 首次确认的 `valid_from` 是批准时刻，不回填到 token_map 的 `created_at`，因为
   后者只证明令牌映射当时存在，不证明渠道绑定自那时起一直正确。
6. 冲突、orphan、partial/stale 都保留证据，不清理 token_map，不自动解绑。
7. 绑定变化不 UPDATE 旧 `profit_daily`、`balance_history` 或审计。
8. down migration 仅本地开发重置；生产回滚按 forward-only 修复/恢复流程。

## 13. MAP1~MAP4 依赖与交付边界

```text
MAP0 本 spec/plan（人审）
  └─ MAP1 完整渠道目录（Sub2 v2；New v1 适配；单实例 fail-closed）
       └─ MAP2 binding schema + candidates + L1 Actions + Query
            └─ MAP3 渠道级读侧经营投影 + duplicate/conflict 闸 + shared runway
                 └─ MAP4 UI 回到原型渠道行粒度
```

每片独立 worktree、分支、PR、自验证；上片未由人合并，下片不得假设其接口存在。
MAP1~MAP4 都不得自行合并。若 MI-1 选择真多实例，依赖图先增加独立 metrics/source
迁移片并修订计划。

## 14. 验收矩阵

### 数据库 / 领域

- 同一 ChannelRef 不能有两条 active binding。
- 一个 upstream account 可以服务多个 ChannelRef。
- service/account 跨环境在数据库层被拒绝。
- rebind 关闭旧区间、插入新区间，remove 不删除历史。
- stale `expected_binding_id`、并发首次创建均稳定返回 conflict。
- unknown 不创建伪账号或伪 binding。

### 候选 / Query

- `unmapped/candidate/conflict/orphan` 四态均有测试。
- partial/truncated/stale/failed 不会自动制造 orphan 或解绑。
- source 与 service.instance_id 不一致 fail closed。
- 零/多 active service 不静默选第一条。
- 未映射渠道仍出行；orphan 仍在管理 Query 可见。

### 财务 / runway

- 多令牌同渠道：收入一次、成本完整。
- 跨上游重复收入：金额 null + conflict，不放大。
- 缺侧、混币、历史未归属继续 fail closed。
- 共享 runway 在 N 个渠道行可引用，但合计只算一个 upstream account。
- binding 变化不改变旧台账查询结果。

### Connector / UI

- Sub2API 无 quota 账号仍在 v2 目录，余额 nil 不等于 0。
- NewAPI 未配置余额、已耗尽余额、非法余额三态不合流。
- Sub2API/NewAPI 列能力差异诚实可见。
- 1024px 页面 body 不横滚，横滚只在表容器。
- 金额仍只经 `formatScaledMinorUnits`，无样例数字硬编。

## 15. 人审必须明确答复

批准不能只写“看起来可以”，须逐项答复：

1. **MI-1**：批准本期“单观测实例 fail-closed”，还是要求现在扩为真多实例？
   本计划推荐前者；选择后者必须退回修订。
2. 是否批准新增迁移与时间版本化 `finance.platform_channel_binding`？
3. 是否批准 Sub2API read contract v2 与新完整目录指标？
4. 是否批准新增两个 Query、两个 L1 HUMAN Action 及
   `finance.platform_channel_binding.manage`？
5. 是否同意新 scope 默认不授予任何默认角色？
6. 是否同意存量只生成候选、production 不自动确认？
7. 是否同意 MAP1~MAP4 只做读侧渠道经营投影，Collector 权威切换另立 spec？

只有七项全部批准，配套计划才成为可执行计划；否则本 MAP0 保持 Design 状态。
