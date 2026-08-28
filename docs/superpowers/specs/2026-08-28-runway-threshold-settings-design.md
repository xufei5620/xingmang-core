# XM-C-RUNWAY0 Runway 阈值设置面设计规格

> **状态：审批稿；只含 Plan / Design，不含实现授权。**
>
> 本文批准后仍不得自动创建迁移、scope、Action、Query、前端写面或修改默认角色。
> C3a～C3d 每片必须分别获得明确实施授权、使用独立 worktree / 分支 / PR，并由人类合并。
>
> 基线：`release/v0.1-launch@543087f`（2026-08-28 本文起草时）。实施前必须重新
> `git fetch origin` 并以目标 PR 最新基线复核；本文记录的是接口与不变量，不冻结迁移号。

## 1. 意图与验收边界

当前 runway 阈值由 `platform-api` 与 `platform-worker` 在启动时分别从
`XM_FINANCE_RUNWAY_WARN_DAYS` / `XM_FINANCE_RUNWAY_CRIT_DAYS` 解析。两个进程虽然
共用 `finance.ParseRunwayThresholds`，运行时一致性仍只靠部署人员把 `.env` 配成相同值。
因此现状不能支持 UI 调整、热更新、版本历史、乐观并发、写后确认，也不能证明滚动发布
和多副本期间 API 展示与 R5 告警使用同一阈值。

本设计要让平台管理员能够在最终 IA 指定的位置查看、预览并受控调整三档阈值，同时满足：

1. PostgreSQL 是按 `environment` 隔离的唯一运行时真相源；
2. 每次修改形成不可覆盖的 revision 与历史记录；
3. API 单请求、worker 单轮评估各自只使用一次读取所得的完整快照；
4. 卡片分档和 R5 使用同一套 `<=` 分类器，不再各写边界逻辑；
5. 预览先展示会打开、升级、降级、恢复的上游对象，再允许提交受控 Action；
6. 写动作是 L2、HUMAN-only、新 scope，Foundation-B 前 fail closed；
7. DB 缺行或读取失败不得静默回落默认值并继续判档；
8. `serious` 只影响展示关注色，不创建或投递 R5 告警；
9. 默认角色与前端 `DEFAULT_SCOPES` 不自动获得新 scope；
10. 本文及配套 plan 只请求审批，不授权实现。

## 2. 权威链与当前证据

| 事实 | 当前证据 | 设计后果 |
|---|---|---|
| Action 是平台配置唯一写入口 | `PROJECT-CONSTITUTION.md` 第 2 条、`ADR-003` | 阈值不能由普通 REST PUT、直接 SQL 或前端配置文件写入 |
| L2+ 当前被内核拒绝 | `internal/platform/action/risk.go`、`kernel.go` | 可先注册/展示 Action；Foundation-B 前绝不能执行 |
| 默认阈值为 5 / 10 / 20 | `internal/platform/finance/runway.go` | bootstrap 空 env 时保留现有默认，不改变部署行为 |
| env 只开放 warning / critical | `finance.ParseRunwayThresholds` | bootstrap 继续按现有规则派生 serious；DB 稳态保存三档显式值 |
| API/worker 各自在启动时解析 | `cmd/platform-api/config.go`、`cmd/platform-worker/config.go` | 必须替换为每请求/每轮读取 DB 快照 |
| summary 已回显三档 | `internal/platform/httpapi/finance_summary.go` | 保持字段兼容，增补 revision/source/updated_at |
| 前端不硬编码阈值 | `web/apps/admin-web/src/api/finance.ts` | 继续以服务端快照为准，不复制默认值 |
| 卡片分档用 `<=` | `RunwayThresholds.levelFor` | 作为统一后的边界语义 |
| R5 告警用 `<` | `alerts.Evaluator.runwayFindings` | 现有 5/10 天边界冲突必须在 C3b 修正 |
| 最终原型把告警规则迁出设置页 | `serve_xingmang_v4.py` 最终 `V["gov/settings"]` / `V["g/alerts"]` | 编辑面属于 `/alerts?sub=rules`，`/settings` 只留入口 |

## 3. 已选方案与被否决方案

### 3.1 选择：finance 专用 DB 配置，不建通用 Config Edge

新增 finance 自有的 current + history 两表。配置属于平台自己计算 runway 与 R5 的策略，
不是上游业务真相；放在 finance schema 能与 `balance_history`、`profit_daily` 和
`RunwayThresholds` 的领域所有权保持一致。

不新建通用 key/value 配置中心。最终 IA 明确把 Config Edge 标成后置能力；为了三个整数
提前发明任意 key/value、表达式或动态规则引擎，会扩大权限和验证面，且让 DB CHECK 无法
直接表达 `critical < warning < serious`。

### 3.2 否决：env 继续做运行时权威

env 适合不可由产品 UI 修改的部署参数，但无法提供热更新、历史、并发控制或 Action 审计。
API/worker/多副本分别持有启动快照，滚动发布期间天然可能漂移。env 只保留一次性 bootstrap
输入，不再作为升级后服务的运行时 fallback。

### 3.3 否决：复用 core registry

registry 描述 service / connector / connection / capability 等资源目录事实。runway 阈值是
finance 与 alerts 共同消费的运营策略，不是某条 connector 或 upstream account 的属性。
塞进 registry 会把资源发现与告警政策耦合，并诱导后续把任意设置都包装成伪资源。

## 4. 数据模型与动态迁移编号

### 4.1 迁移号只在实施时计算

本文和 plan **不得写死 `000014` 或任何具体迁移前缀**。PR #103、XM-B003 或其它并发片
可能先占用当前最大号。C3a 开工时必须：

1. `git fetch origin` 成功；网络失败即停止编号，不使用陈旧远端状态猜测；
2. 确认 PR #103、XM-B003 及 C3a 声明依赖的合入/分支状态；
3. 从最新 `origin/release/v0.1-launch` 建 worktree，并先纳入所有已批准依赖；
4. 对该 worktree 的 `db/migrations/` 文件名前六位求最大值，加一并补齐六位；
5. 创建同号 `.up.sql` / `.down.sql`；提交前和 PR 更新基线后再次检查碰撞；
6. 若号码被占，未合入的 C3a 必须重命名迁移对并重跑测试，不修改任何已合入迁移。

plan 使用 `${MIGRATION_PREFIX}` 表示上述算法的输出；它是执行期变量，不是待填占位符。

### 4.2 current 表

表名：`finance.runway_threshold_config`。

| 列 | 类型/约束 | 语义 |
|---|---|---|
| `environment` | `text PRIMARY KEY REFERENCES core.environment(id) ON DELETE RESTRICT` | 每环境唯一运行时快照 |
| `critical_days` | `integer NOT NULL CHECK (> 0)` | 最紧展示档及 critical 告警上界 |
| `warning_days` | `integer NOT NULL CHECK (> 0)` | warning 展示档及 R5 是否活跃的上界 |
| `serious_days` | `integer NOT NULL CHECK (> 0)` | 仅展示关注色的上界，不通知 |
| `revision` | `bigint NOT NULL CHECK (> 0)` | 单调递增的乐观并发版本 |
| `updated_at` | `timestamptz NOT NULL` | UTC 生效时刻 |
| `updated_by` | `text NOT NULL CHECK (btrim(updated_by) <> '')` | HUMAN principal 或 bootstrap 身份 |
| `reason` | `text NOT NULL CHECK (btrim(reason) <> '')` | 为什么修改/导入 |
| `request_id` | `text NOT NULL CHECK (btrim(request_id) <> '')` | 与 Action / 生命周期操作证据关联 |

表级 CHECK：`critical_days < warning_days AND warning_days < serious_days`。

### 4.3 history 表

表名：`finance.runway_threshold_history`。字段包含 current 表完整快照，主键为
`(environment, revision)`，另有 `change_source`（`bootstrap` / `action`）与创建时刻。
history 只允许 INSERT；数据库触发器拒绝 UPDATE / DELETE。current 更新与 history 插入必须
在同一事务内完成，不能出现“生效了但没有版本历史”。

Action append-only 审计仍是第二条证据链，记录 Action ID、run ID、before/after、reason；
history 不能替代 Action 审计，Action 审计当前可能在业务成功后单独写失败，history 因此也
不能被省略。

### 4.4 env bootstrap 是生命周期操作，不是后门

新增只运行一次的版本化命令 `cmd/runway-threshold-bootstrap`，由部署流程在升级 API/worker
之前执行：

1. 读取 `ENVIRONMENT` 与现有 WARN/CRIT env；
2. 调用现有唯一解析器，空值产生 5/10/20，WARN 超过 20 时沿用 serious 自动让位规则；
3. current 不存在时写 revision 1，并在 history 写 `change_source=bootstrap`；
4. current 已存在且三档相同则幂等成功；已存在但不同则拒绝覆盖并要求人工核对；
5. 输出只含 environment/revision/三档，不输出任何凭据；
6. 产出部署证据后，升级后的 API/worker 只读 DB。

该命令属于宪法第 3 条 Platform Lifecycle Operation：版本化制品、变更单、人工批准、独立
日志；不暴露 HTTP 路由，不允许被日常 UI 或 AI Tool 调用。升级后 DB 缺行时服务 fail
closed，绝不再次用 env 悄悄补值。C3d 验证所有环境 revision 后才删除 compose 中两个旧变量。

## 5. 唯一分类语义

`finance.RunwayThresholds` 保持三个正整数，并新增导出的单一分类方法；summary、preview、
R5 都调用它：

```text
0 < critical < warning < serious

days <= critical  => critical
days <= warning   => warning
days <= serious   => serious
days >  serious   => healthy
```

R5 的告警映射固定为：

| runway level | R5 Finding | 通知严重度 |
|---|---|---|
| `critical` | 有 | `critical` |
| `warning` | 有 | `warning` |
| `serious` | 无 | 无 |
| `healthy` | 无 | 无 |
| unknown reason / 非计量型 | 无 | 无 |

因此 5 天与 10 天均在边界内；默认值下 5 天为 critical，10 天为 warning，20 天为
serious。`serious` 不是 alerts 的 `info` 通知档，UI 不得把三档统称为“三档告警”。

阈值改变只影响此后 summary/preview/评估的分类，不重算余额、消耗或历史毛利。worker 下一轮
评估按新 revision 打开、升级、降级或恢复 R5；Action 不在同步请求内强跑整轮告警。

## 6. Query 契约

所有 Query 使用 `finance.read`；新 scope 只保护写动作。Query 与现有 `/api/v1` 一样继承
Principal、environment 裁剪、NoStore、请求超时和限流。

### 6.1 当前配置

`GET /api/v1/finance/runway-thresholds`

```json
{
  "environment": "staging",
  "critical_days": 5,
  "warning_days": 10,
  "serious_days": 20,
  "revision": 3,
  "source": "database",
  "updated_at": "2026-08-28T10:00:00Z",
  "updated_by": "staff:operator-01",
  "reason": "按近期充值提前量调整"
}
```

`source` 在升级后的运行时只能是 `database`。缺行/DB 失败返回稳定错误
`RUNWAY_CONFIG_UNAVAILABLE`，不得回 5/10/20。

### 6.2 历史

`GET /api/v1/finance/runway-thresholds/history?limit=50&before_revision=<n>`

按 revision 降序返回，`limit` 有严格上限；每行包含三档、revision、source、actor、reason、
request_id、changed_at。这里只显示平台配置历史，不替代全局审计页。

### 6.3 影响预览

`GET /api/v1/finance/runway-thresholds/preview?critical_days=5&warning_days=10&serious_days=20`

预览是 Query，不写 current/history/action/audit，不启动 worker，不改变告警。它对当前环境的
runway 快照与活动 R5 告警做纯比较，返回：

- current/proposed 阈值与 current revision；
- `observed_at`、runway coverage 与 unknown reasons；
- `would_open` / `would_escalate` / `would_deescalate` / `would_resolve` / `unchanged` 计数；
- 有变化的对象：account_id、显示名、days、old_level、new_level、alert_transition；
- 订阅型与 unknown 不编影响结论，只进入 coverage/reasons。

对象列表分页/限量；读者已有 `finance.read`，泄漏面不超过现有 summary。预览的
`observed_at` 必须显示，因为余额与日均会在预览后继续变化。Action 绑定 current revision 和
明确参数，不声称冻结易变的对象集合。

### 6.4 summary 的一致快照

`GET /api/v1/finance/upstreams/summary` 保留现有 `runway_thresholds` 三个字段，并增补
`revision`、`source`、`updated_at`。handler 每次请求只读配置一次，同一个值同时：

1. 传给 `SummaryStore.UpstreamSummaries`；
2. 用于每行分类；
3. 原样回显到响应。

禁止计算后再次读取配置拼响应，否则一次请求内也可能出现 revision 漂移。

## 7. L2 Action 契约与权限

稳定 ID：`finance.runway_threshold.set`，版本 `1`。

| 项 | 设计 |
|---|---|
| Risk | `L2` |
| Permission | 新 scope `finance.runway_threshold.manage` |
| Principal | 仅 `HUMAN` |
| Environment | development / staging / production，目标始终取 Principal.Environment |
| Params | 三个 `int`、`expected_revision` `int`、非空 `reason` `string` |
| Approval | 走最终获批的 XM-0030 / Foundation-B L2 策略 |
| Idempotency | Foundation-B 审批/执行单至多一跑；Store 另用 expected revision 防并发覆盖 |
| Write confirmation | 事务提交后重新 Query，精确核对三档与 `revision=expected+1` |
| Compensation | `MANUAL`；提交一张新 L2 Action，把上一 revision 的值作为下一 revision 写回 |

Action Handler 必须：

1. 复用唯一 Validate/Classifier，拒绝非正或非严格递增；
2. 从 context 取 HUMAN Principal/environment，不接受 `environment` param；
3. `UPDATE ... WHERE environment=? AND revision=?`，0 行返回 revision conflict；
4. 同事务 INSERT history；
5. `RecordResource`、`RecordBefore`、`RecordAfter`、`RecordReason`；
6. commit 后重新读取并比较，失败则返回写后确认失败并进入人工核对，不能报成功；
7. 回执包含 action_run/approval 信息、旧/新 revision、审计入口。

Foundation-B 未获批或未落地时，定义可以注册、Query/预览可以上线，但执行必须返回
`ADVANCED_CONTROLS_REQUIRED`。UI 只能显示门禁，不允许出现直接保存旁路。

### 7.1 新 scope 不默认授予

- 不加入 `web/apps/admin-web/src/api/config.ts` 的 `DEFAULT_SCOPES`；
- 不修改默认 Keycloak RoleScopeMap，不碰真实 Keycloak；
- 不复用 `finance.read`、`finance.upstream_account.manage` 或泛化 `finance.config.manage`；
- 只有后续独立、人工批准的角色映射变更才能授予；
- 前端是否显示/启用按钮只是体验，服务端 Action 内核是最终裁决者。

## 8. API/worker 热更新与多副本一致性

定义 `finance.RunwayThresholdProvider`：

```go
type RunwayThresholdSnapshot struct {
    Thresholds RunwayThresholds
    Revision   int64
    Source     string
    UpdatedAt  time.Time
}

type RunwayThresholdProvider interface {
    Current(ctx context.Context, environment string) (RunwayThresholdSnapshot, error)
}
```

- API：每个相关请求调用一次 `Current`；首版不做进程缓存。
- worker：每轮 alert evaluation 开始调用一次 `Current`，整轮把同一 snapshot 传给 Evaluator。
- 多副本：所有副本读同一个 PostgreSQL current row；不依赖本地内存广播。
- DB 错误/缺行：本轮整体失败且不调用 Reconciler，已有告警不能因“空 Findings”被恢复。
- 下一轮：自然读到新 revision，热更新上限是 worker 评估间隔，无需重启。
- 日志：记录 environment/revision，不记录理由全文或任何凭据。

首版不引入 Redis、LISTEN/NOTIFY、轮询缓存或 Config Edge；单行主键查询的代价低于新增一致性
协议的复杂度。若将来读压成为实测瓶颈，再以 revision-aware 缓存另立任务。

## 9. UI 规格：规则页内联，不用右侧 Drawer

最终位置：`/alerts?sub=rules`。页面先落实 IA v3 的外层子页签；现有“活跃告警 / 含已解决”
成为 `sub=alerts` 内部筛选，不与外层五页签混用。

R5 规则行下方使用**内联完整区块**；影响确认使用居中 `Dialog`，或从规则行进入独立 full-page
路由。明确禁止右侧 Drawer，避免与现有详情/操作侧栏模式混淆，也避免窄屏双层抽屉。

### 9.1 格 → 源 → 状态

| 格 | 数据源 | 必须覆盖的状态 |
|---|---|---|
| R5 规则说明 | 代码中的规则声明 | live；写清 serious 不通知 |
| 当前三档 | current Query | loading/live/error/permission，不用本地默认值 |
| revision/source/更新时间 | current Query | live；DB 失败明确不可用 |
| 三个输入 | current Query + 表单草稿 | pristine/dirty/invalid；严格递增即时提示 |
| coverage/reasons | preview Query | loading/live/empty/error/stale-observation |
| 影响计数 | preview Query | open/escalate/de-escalate/resolve/unchanged |
| 受影响对象表 | preview Query | 只列变化对象；分页；显示 observed_at |
| 提交入口 | Action catalog + scope + F-B 状态 | 缺 scope 禁用；F-B 前门禁；不得直接保存 |
| 审批/执行回执 | L2 Action / Approval | request_id/run_id/status/审计入口 |
| 写后有效值 | current Query + summary refetch | revision 与三档完全一致才显示成功 |
| 最近变更 | history Query | bootstrap/action 来源、actor、reason、revision |

### 9.2 设置页只留入口

`/settings` 不承载第二份阈值表单。它按最终原型显示治理入口：
“告警规则与静默 → 告警与故障”，链接 `/alerts?sub=rules`。财务 summary 可以显示当前三档与
同一链接，但不能提供另一个编辑实现。

### 9.3 权限和门禁文案

- 有 `finance.read`、无 manage scope：可看当前值/历史/预览，编辑区禁用并明确缺少 scope；
- 有 manage scope、F-B 未就绪：仍禁用提交，说明 `ADVANCED_CONTROLS_REQUIRED`；
- F-B 就绪：预览完成且表单未变化后，按钮文案为“提交审批”，不是“保存”；
- 403/409/5xx 原地保留草稿和预览，不把失败伪装成已应用；
- 审批执行后再 refetch current + summary，未验证一致不显示成功绿色状态。

## 10. 历史与审计边界

本片能证明“每个 revision 谁在何时因何改成了什么”，并能通过 Action audit 查执行链。
现有 `alerts.alert` 是当前/episode 状态，会在重复命中时刷新 detail，并不是完整 transition log。
因此本片不宣称能够逐次重放一条活动告警的每次升降级。

若产品要求完整重放，另立 append-only `alerts.alert_transition` 设计；不能为了本 UI 顺带扩大
alerts 数据模型。R5 detail 至少带本轮使用的 threshold revision 与三档，使当前态可关联到
config history。

## 11. 分片与依赖

| 片 | 独立交付 | 依赖/门禁 |
|---|---|---|
| C3a | 动态编号迁移、current/history Store、生命周期 bootstrap、current/history Query | 本规格人工批准；迁移单独批准 |
| C3b | 唯一 `<=` classifier、R5 边界修正、preview Query、API/worker 每请求/每轮 DB 快照 | 本规格人工批准；契约语义变化单独批准 |
| C3c | L2 Action contract、新 scope、expected revision、审计、写后确认 | **Foundation-B / XM-0030 先获批并落地**；scope 映射另批 |
| C3d | `/alerts?sub=rules` 内联 UI、Settings 入口、bootstrap/回滚 runbook、移除 env 运行时旋钮 | C3a+b；提交审批能力还依赖 C3c |

四片各自 worktree/分支/PR；任一片不能借本设计稿绕过自己的审批点。C3d 可以先交付只读/预览/
门禁 UI，但不得因为按钮已画出就宣称写能力可用。

## 12. 失败与恢复语义

| 场景 | 行为 |
|---|---|
| bootstrap 缺 DB 或 env 非法 | 生命周期操作失败；旧服务继续，不切换 |
| bootstrap 发现 DB 已有不同值 | 拒绝覆盖，人工核对来源 |
| current 缺行/DB 不可达 | Query 503；summary 不判档；worker 本轮不 reconcile |
| 三档非正/不递增 | UI 与服务端拒绝；DB CHECK 最后一闸 |
| expected revision 过期 | 409 conflict；强制重新读取与预览 |
| Action 审批参数被改 | Foundation-B params hash 拒绝执行 |
| 写后读取不等于预期 | 不报成功；记录 run/audit，进入人工核对 |
| 新阈值误配 | 用上一 revision 值提交新的 L2 Action；历史不删除 |
| 多副本同时读取 | 各请求/轮次持有自己的完整 revision；下一请求/轮次收敛 |

## 13. 安全边界与明确不做

- 不修改上游 Sub2API/NewAPI/CPA 源码或数据库；
- 不触碰生产、Keycloak 或真实角色映射；
- 不把新 scope 加进默认角色、默认 dev scopes 或任意 AI 身份；
- 不提供直接 SQL、普通 PUT/PATCH、任意配置 key/value 或自动审批旁路；
- 不让 MACHINE/AI 执行该 Action，也不让 AI 投审批票；
- 不在 URL、日志、history、audit 或前端保存凭据；本配置本身不含凭据；
- 不同步重跑整轮告警，不重算历史毛利，不将 serious 变成通知；
- 不使用右侧 Drawer；只用 inline/full-page/dialog；
- 不在本文固定迁移号；
- 不自行合并或部署。

## 14. 审批请求

请人类逐项确认以下设计决定：

1. DB current + append-only history 为唯一运行时权威，env 只作生命周期 bootstrap；
2. `<=` 为统一边界，5/10/20 均包含在对应档；
3. serious 只展示、不触发 R5 通知；
4. Query/preview 复用 `finance.read`；写使用新 scope `finance.runway_threshold.manage`；
5. `finance.runway_threshold.set@1` 为 HUMAN-only L2，Foundation-B 前不可执行；
6. 默认角色、默认前端 scopes 和 Keycloak 本轮均不授新 scope；
7. UI 位于 `/alerts?sub=rules`，Settings 只留入口，禁止右侧 Drawer；
8. 迁移前缀按实施时最新目标基线动态 `max+1`；
9. C3a～C3d 分 PR，逐片另行授权。

**批准本文仅批准设计进入下一道评审，不等于授权任何一片开始实现。**
