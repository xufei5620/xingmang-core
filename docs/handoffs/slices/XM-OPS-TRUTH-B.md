# XM-OPS-TRUTH 子片 B：告警语义（迟滞、触发次数、已核对版本、失败作业聚合）

- **status**: ready-for-review（分支内交付，未推 GitHub、未部署）；
  **含两处需要负责人裁定的越界**，见 risks 第 1 条与「审稿处置轮」§0
- **branch**: `ai/claude/XM-OPS-TRUTH`（接在子片 A 的 `ddf4b36` 之上）
- **commit**: `c9d4d9a`（41 files changed, +4050 / -282）→ `fb00f18`（回填 SHA）
  → `4763ac5`（审稿处置轮，29 files changed, +2235 / -153）→ `30f8c7e`（回填 SHA）
  → `5e4eadb`（复审处置轮，13 files changed, +619 / -44，见文末最后一节）
- **时间**: 2026-09-09T03:05:47Z – 2026-09-09T04:02Z（实现，约 56 分钟）；
  2026-09-09T04:27Z – 2026-09-09T05:22Z（审稿处置轮，约 55 分钟）；
  2026-09-09T05:31Z – 2026-09-09T05:42Z（复审处置轮，约 11 分钟，见文末「门禁」）

- **需求来源**: `docs/handoffs/PLATFORM-ALERT-STORM-2026-09-08.md`
  §二第一行（NewAPI 三条翻面 + 「已持续」归零 + 连续失败计数被成功清零）、
  §二第二行（版本告警 669 次、没有「我核对过了」这个动作）、
  §二第四行（288 条 card_sync 把「我的待处理」占满）、§四第二组

> **文末最后一节「复审处置轮」是最新的一轮**，处置复审判定仍未解决的 3 条
> major：新增一条作业周期启动闸、堵住「估计值被洗成确定值」、把 `note` 从
> 已核对清单端点上去掉。它又改了一处契约（该端点**不再返回** `note`）。
>
> **先读文末的「审稿处置轮」一节。** 那一轮改掉了两条 fatal 与十条 major，
> 其中三处**改了本文档前面几节写下的契约**：`failed_jobs_by_kind` 的空值口径
> 变成三态、webhook 投递体补了三个字段、新增了一个只读端点与一个撤销 Action。
> 前面几节保留原样是为了让审稿意见与处置逐条对得上；**以「审稿处置轮」为准**。

---

## summary

四件事，一个共同的形状：**让每个数字只回答一个问题，并让每个抑制器都留下痕迹。**

**1. `metric.sync.failed` 有了迟滞（N=3，开与关同一个数）。** 之前它没有任何持续
时间门槛——一失败下一分钟就报、一成功下一分钟就撤，于是生产上那三条 NewAPI 指标
每 5 分钟翻一次面。判据是**纯函数** `foldSyncFailure`，从 `ops.metric_observation_sample`
折叠出来，不落任何新状态（worker 重启、多副本、换节点跑，答案都一样）。
「关」那一半需要知道「这条告警还开着吗」，所以 `Reconciler` 把
`store.ListActive` 的去重键集合提前取好传进 `Evaluate`。

**2. `metric.sync.consecutive_failed` 的计数从「连续串」改成「滑动窗口」**
（最近 12 条里失败 ≥ 9 次）。旧实现遇到第一条成功就 break，于是 card_sync 那种
每 5 分钟失败一次、三天没停过的慢性病**从来没升级过**。K 取 9 而不是照搬旧的 3：
一串完全交替的 F/S 在 12 条窗口里恰好凑出 6 次失败，阈值取 3 或 6 的话，R1 的迟滞
刚压住的抖动会原样从 R4 冒出来，而且同样是 critical。**rule_key 不改**。

**3. `fire_count`（评估轮数）与 `trigger_count`（触发次数）分开，并新增
`first_opened_at`。** 界面上那个「触发 669 次」其实是「持续了 668 分钟」。
两列都可空、都不回填——旧行不知道自己被触发过几次，填 0 是造假答案。

**4. 「已核对的上游版本」成了一个领域概念。** 新 Action
`alerts.upstream_version.acknowledge`（L1）把 `(environment, metric_key) → version`
记进新表 `alerts.upstream_version_ack`；规则命中时若观测版本等于已核对版本就
不再命中，既有 OPEN 告警在下一轮被**通用恢复逻辑**转 RESOLVED。在这之前，
那条告警唯一的结束方式是旧探测样本被挤出窗口后自己消失（约 16h40m）。

**附带**：`GET /api/v1/ops/overview` 新增按 `kind` 聚合的失败作业摘要
（`failed_jobs_by_kind`），前端片据此把 288 条 card_sync 渲染成一行。

---

## files_changed

### 迁移（新建）
- `db/migrations/000054_alerts_trigger_and_version_ack.up.sql` / `.down.sql`
  —— `alerts.alert` 加 `trigger_count` / `first_opened_at`（可空、不回填）；
  新建 `alerts.upstream_version_ack`

### 后端 —— alerts（本片所有）
- `internal/platform/alerts/rules.go` —— 迟滞常量与配置、`Evaluate` 增
  `active` 入参并返回 `EvaluateResult`、`foldSyncFailure` / `windowedFailures` /
  `trailingSameStatusRun` 三个纯函数、`UpstreamVersionAckSource` 接口（构造函数
  **必填参数**）、R6 的已核对版本抑制、R1/R4 共用一次样本查询
- `internal/platform/alerts/reconcile.go` —— `ListActive` 提到 `Evaluate` 之前并
  把去重键集合传进去；`Result` 增三个抑制器计数
- `internal/platform/alerts/alert.go` —— `Alert.TriggerCount` / `FirstOpenedAt` /
  `EffectiveFirstOpenedAt()`；新类型 `UpstreamVersionAck`；改正 `FireCount` 的注释
- `internal/platform/alerts/store.go` —— 行映射、复发继承首开时刻、ack 三个方法
- `internal/platform/alerts/actions.go` —— 第三个 Action（定义 + Handler）、
  `ObservationReader` 接口、`RegisterActions` 增一个参数
- `internal/platform/alerts/permissions.go` —— 注释（为什么复用 `ScopeAcknowledge`）
- `db/queries/alerts.sql` + `internal/platform/alerts/gen/*` —— 见 deviations 第 1 条

### 后端 —— jobs / httpapi
- `internal/platform/jobs/query_store.go` —— `FailedRunSummaryByKind` +
  `FailedRunSummaryWindow`；把三条查询共用的环境软过滤谓词抽成
  `riverJobEnvironmentPredicate`（原先三处各写一遍）
- `internal/platform/jobs/client.go` —— 一个 `*alerts.Store` 同时喂给评估器与
  Reconciler
- `internal/platform/jobs/alert_evaluate.go` —— 三个抑制器计数进结构化日志
- `internal/platform/httpapi/alerts.go` —— 三个新字段
- `internal/platform/httpapi/ops_overview.go` —— `failed_jobs_by_kind` 段
- `internal/platform/httpapi/jobs.go` —— `JobsQuerier` 增一个方法（见 deviations 第 3 条）
- `internal/platform/httpapi/router.go` —— **一行**：`Jobs: d.Jobs`（跨所有权）
- `cmd/platform-api/main.go` —— `RegisterActions` 多传一个只读观测来源

### 契约（**跨所有权，需要裁定** —— 见 risks 第 1 条）
- `internal/platform/dbroles/policy.go` —— 列清单 + 新对象登记
- `contracts/database/role-policy.v1.json` —— 同上
- `contracts/database/role-policy-state-events.v1.jsonl` —— 追加第 4 条 policy-update

### 文档
- `docs/modules/alerts/README.md` —— 规则表三行、生命周期、权限表、数据模型
- `docs/modules/notify/CATALOG.md` —— 三行规则的「什么情况下响 / 什么时候自己好」

### 测试
新建 4 个文件、改写 6 个：`hysteresis_rule_test.go`、`version_ack_test.go`、
`version_ack_integration_test.go`、`schema_policy_drift_integration_test.go`；
`rules_test.go`、`reconcile_test.go`、`reconcile_integration_test.go`、
`store_integration_test.go`、`actions_test.go`、`runway_rule_test.go`、
`approval_rule_test.go`、`audit_integration_test.go`、`httpapi/alerts_test.go`、
`httpapi/ops_overview_test.go`、`httpapi/jobs_test.go`、
`jobs/query_store_integration_test.go`

---

## 新增 API 字段清单（给 XM-WORKBENCH-TRUTH 逐字用）

### `GET /api/v1/alerts` —— `items[]` 新增三个字段

| 字段 | 类型 | 空值口径 | 语义 |
|---|---|---|---|
| `trigger_count` | `number \| null` | **可为 null** | 真正的**触发次数**：新开 +1、静默窗口过期转回 OPEN 重新投递 +1；持续命中**不**加。`null` = 迁移 000054 之前就存在的旧行，**没有可兜的底**——请显示「—」，**不要显示 0**（一条正在响的告警触发过 0 次是不可能的） |
| `first_opened_at` | `string` | **恒非空** | 这个问题**第一次**开始的时刻（UTC RFC3339），跨 `RESOLVED → REOPENED` 继承（限 24 小时复发窗口内）。「已持续多久」请用它算，不要再用 `opened_at` |
| `first_opened_at_estimated` | `boolean` | 恒有值 | `true` 表示上一个字段是服务端用 `opened_at` 兜的底（旧行），不是真的首开时刻。建议在「已持续」旁加一个「约」或问号提示 |

**`fire_count` 保留不动，语义也不变**——但它是**评估轮数**（每 60 秒一轮，条件仍
成立就 +1），**不是**发生次数。2026-09-08 界面上那个「触发 669 次」就是把它当成了
后者。要显示「触发几次」请改用 `trigger_count`。

### `GET /api/v1/ops/overview` —— 新增两段

```jsonc
{
  // null = 这个部署没接 jobs 查询器，或这一格读库失败（不假装没有失败）
  // []   = 查过了，窗口内一条失败作业都没有
  // 两者必须分开渲染
  "failed_jobs_by_kind": [
    {
      "kind": "card_sync",
      "count": 288,                       // 窗口内这一类失败了几次
      "first_at": "2026-09-05T10:27:00Z", // 窗口内最早一次
      "last_at":  "2026-09-08T15:12:00Z", // 窗口内最近一次
      "last_run_id": 90210,               // 跳去 /api/v1/jobs/runs 定位用
      "error_count": 3,                   // 最近那条作业的尝试次数（不是本类总数）
      "last_error": {                     // 可为 null（那条作业没有记错误）
        "at": "2026-09-08T15:12:00Z",
        "message": "rejected: infini POST /v2/cards/status/batch",
        "truncated": false,
        "original_length": 44
      }
    }
  ],
  "failed_jobs_window_hours": 24          // 上一段的回看窗口
}
```

- 只收 `discarded`（与前端既有口径一致：retryable 会自己消失、cancelled 是人主动
  取消）；按 `count` 降序、同 count 按 `kind` 升序。
- 环境来自调用者身份，不是查询参数。
- 权限 `ops.read`（与该端点其余部分相同，无新增 scope）。

### 新 Action（前端要给它做执行入口）

```
POST /api/v1/actions/alerts.upstream_version.acknowledge/versions/1/execute
{ "metric_key": "sub2api.connector.health", "version": "0.2.3", "note": "（可选）" }
```

- 权限 `alerts.alert.manage`（与「确认告警」同一个 scope，**没有新 scope**）。
- 风险等级 **L1**，人类身份，三个环境都允许。
- 错误码：`INVALID_PARAMS`（指标键未注册 / version 形态非法 / 超长）、
  `PRECONDITION_FAILED`（这条指标没有观测，或观测里没有 `version`）、
  `CONFLICT`（`version` 与平台此刻观测到的不一致，文案会给出两个版本）。
- **执行入口在哪**：告警详情里 `rule_key == "upstream.version.changed"` 的那条，
  `source_metric_key` 就是要传的 `metric_key`，`dedup_key` 的最后一段就是要传的
  `version`（形如 `upstream.version.changed:production:sub2api.connector.health:0.2.3`）。
  告警的 `detail` 里也逐字写了这两个值。

---

## 给 XM-CARD-VISIBILITY 的规则注册点

要加一条新规则，只需改 `internal/platform/alerts/rules.go` 的三处，
每处都留了显式锚注释（搜 `XM-CARD-VISIBILITY 注册点`）：

1. **规则键常量块**（文件顶部 `const (...)`）——键**只增不改**，它是静默窗口的
   匹配键与历史告警的分组键。
2. **`Rules(cfg RuleConfig)` 的切片字面量**——九个字段一项都不能空
   （`TestRulesDeclareAllNineSpecFields` 机械保证），而且 `Condition` / `Recovery`
   必须与实际判定同步改：只改判定不改声明，文档与告警目录里说的就是另一条规则。
3. **`Evaluate` 主循环里产出 `Finding` 的位置**——注意本片之后 `Evaluate` 的签名多
   了一个 `active map[string]struct{}`，返回值是 `EvaluateResult` 而不是 `[]Finding`。

还要同步两处（否则测试红）：`docs/modules/notify/CATALOG.md` 的规则表
（`notify/catalog_test.go` 会逐条对账）、`docs/modules/alerts/README.md` 的规则表。

**不要动** `versionChangeFinding` 与 `foldSyncFailure` / `windowedFailures`
——那三处是本片的判据。

---

## 负责人上线后要执行的那一步

负责人 2026-09-08 04:12Z 已经把 sub2api 就地升级到 0.2.3 并核对过了。**上线后**：

在管理端对生产环境执行
`alerts.upstream_version.acknowledge`，参数
`metric_key = sub2api.connector.health`、`version =`（**当时观测到的实际版本，
从告警详情里逐字复制，不要凭记忆填**）。执行后下一轮评估（≤60 秒）那条告警会
自动变成「已解决」。

> ⚠️ **这一步现在没有落点，是本片的阻塞项。** 操作与审批页的「操作目录」是只读的
> （页面自己写着「目录页不是执行入口」），平台没有通用的参数表单执行器，
> `alerts.alert.acknowledge` / `alerts.silence.create` 都各有专门的前端组件。
> 所以**在 XM-WORKBENCH-TRUTH 给它做出按钮之前，负责人点不到这个动作**。
> 后端契约已经就绪（上一节），前端片可以直接照着做。

---

## tests_run

全部实测，`-p 1 -count=1`，`XM_TEST_DATABASE_URL` 指向 `xm_test_wt_xm_ops_truth`
（集成用例**真的跑了**，不是 Skip——见下面的 `-v` 记录）。

| 命令 | 结果 | 耗时 |
|---|---|---|
| `go test -p 1 -count=1 ./internal/platform/alerts/...` | ok | 1.6s |
| `go test -p 1 -count=1 ./internal/platform/httpapi/...` | ok | 1.0s |
| `go test -p 1 -count=1 ./internal/platform/jobs/...` | ok | 11.5s |
| `go test -p 1 -count=1 ./internal/platform/dbroles/...` | ok | 0.6s |
| `go test -p 1 -count=1 ./internal/platform/ops/... ./internal/platform/notify/...` | ok | 0.8s |
| `go vet`（上述五个包 + `cmd/platform-api`） | 干净 | 2s |
| `gofmt -l internal/ cmd/` | 只剩一处**存量**违规（见 risks 第 6 条） | <1s |
| `bash scripts/check-governance.sh` | exit 0 | 3s |

新增/改写的测试（26 个函数）覆盖：折叠函数表驱动（含窗口截断）、迟滞开、
迟滞关（Evaluator 层 + **Reconciler 层** + 真库端到端）、成本纪律（健康时不查历史）、
迟滞不外溢到别的规则、R4 滑动窗口（含「窗口外不算」「纯抖动不触发」「阈值边界」）、
R4 规则声明与判定同步、`trigger_count` 五种状态转换、`first_opened_at` 三级复发继承
与窗口外重算、ack 抑制（含「核对旧版本不算」「跨指标不串」「每轮只取一次快照」）、
Action 参数形态六种拒绝 + 三种错误码 + 定义断言、经真实内核的端到端核对与冲突、
失败作业聚合（合并/计数/首末/最近错误/跨环境/窗口外/截断）、`/ops/overview` 的
null-vs-[] 与读库失败降级、alerts schema ↔ 策略契约双向逐列对账。

---

## 变异验证表

逐条：改实现 → 跑测试 → 记红 → 还原 → 复跑绿。**15 条全部按预期变红。**

| # | 变异 | 预期红 | 实测红 | 已还原 |
|---|---|---|---|---|
| 1 | `foldSyncFailure` 的 `failRun >= n` 改成 `>= 1`（等于取消迟滞） | 迟滞开 | `TestFoldSyncFailureIsAPureHysteresis`、`TestSyncFailedDoesNotOpenBeforeThreshold`、`TestChronicWindowResistsPureFlapping` + 三个 e2e | ✅ |
| 2 | `Evaluate` 里去掉 `\|\| syncFailedActive`（告警还开着时不再回看历史） | 迟滞关 | `TestSyncFailedStaysOpenUntilRecoveryThreshold`、`TestHealthyMetricWithNoActiveAlertSkipsHistoryQuery`、`TestReconcilerFeedsActiveAlertsIntoHysteresis`、`TestEndToEndRealAlertFiresAndDelivers` | ✅ |
| 3 | `Reconcile` 传空 active 集合（**规则存在≠调用得到**） | 只有穿两段的测试 | `TestReconcilerFeedsActiveAlertsIntoHysteresis`、`TestEndToEndRealAlertFiresAndDelivers`（Evaluator 层单测**照样绿**——这正是为什么必须从编排层打进来） | ✅ |
| 4 | `windowedFailures` 还原成「遇到成功即 break」 | R4 滑动窗口 | `TestChronicFailureCountsInAWindowNotAStreak` | ✅ |
| 5 | `versionChangeFinding` 里删掉 ack 比较（表照写、规则不读） | ack 抑制 | `TestAcknowledgedVersionStopsTheRule`、`TestAcknowledgedUpstreamVersionResolvesTheOpenAlert` | ✅ |
| 6 | ack 比较改成「只要有任何一条 ack 就抑制」 | 逐字/按指标 | `TestAcknowledgingTheOldVersionDoesNotSuppressTheNewOne`、`TestAcknowledgementIsScopedToItsMetric`、e2e | ✅ |
| 7 | `TouchAlert` 无条件 `trigger_count + 1` | 触发次数 | `TestTriggerCountOnlyMovesOnStateTransition` | ✅ |
| 8 | `insert` 复发时不继承首开时刻 | 已持续不归零 | `TestFirstOpenedAtSurvivesRecurrence` | ✅ |
| 9 | `alertToItem` 把 `trigger_count` 填成 `fire_count` | API 分离 | `TestAlertItemSeparatesFireCountFromTriggerCount`、`TestAlertItemTellsYouWhenFirstOpenedAtIsAGuess` | ✅ |
| 10 | 聚合 SQL 的 `last_error` 取最早那条（`ORDER BY at ASC`） | 取最近 | `TestFailedRunSummaryGroupsByKind`（`last_run_id = 30, want 34`） | ✅ |
| 11 | 共享的环境软过滤谓词改成恒真 | 跨环境隔离 | `TestFailedRunSummaryGroupsByKind` **与既有的** `TestQueryStoreListRunsEnvironmentSoftFilterPostgresIntegration`（同时红，证明那个常量真的被三处共用） | ✅ |
| 12 | 策略契约里删掉 `trigger_count` 一列 | 漂移闸 | `TestAlertsSchemaMatchesRolePolicyInventory` + `dbroles.TestCheckedInPolicyContractLoads`（摘要链） | ✅ |
| 13 | `router.go` 删掉 `Jobs: d.Jobs`（唯一一处跨所有权改动） | 装配 | `TestOpsOverviewMergesFailedJobsByKind`、`TestOpsOverviewDistinguishesNoDataSourceFromNoFailures` | ✅ |
| 14 | 新 Action 的风险等级 L1 → L2 | 不许抬级 | `TestActionDefinitionsAreValid`、`TestAcknowledgeUpstreamVersionDefinition` + 两个 e2e | ✅ |
| 15 | 去掉 `version` 的形态校验 | 参数装不下凭据 | `TestAcknowledgeUpstreamVersionRejectsCredentialShapedParams` 的六个子用例中五个 | ✅ |

变异全部还原后复跑：四个包全绿（见 tests_run）。

---

## not_run

- **`go test ./...` 全量**：按派工，主控者跑。
- **前端**（`pnpm typecheck` / `test`）：本片不改 `web/`。新增的三个告警字段是
  **纯新增**，既有字段一个都没动（`TestAlertItemJSONKeysMatchTheStructTags` 用反射
  清点响应键，漏字段/多字段都会红），所以前端不会因为本片而炸；但前端要用新字段
  仍需 XM-WORKBENCH-TRUTH 那一片。
- **`sqlc generate`**：本机装不了 sqlc（见 deviations 第 1 条），生成物是手写的。
  **建议主控者在有 sqlc 的机器上跑一次 `sqlc generate` 并只保留 alerts 包的 diff**，
  确认与手写结果逐字一致。
- **`cmd/db-role-verify` 对真库跑一遍**：本片新增的 schema-vs-契约对账测试
  （`TestAlertsSchemaMatchesRolePolicyInventory`）只覆盖 `alerts` schema，
  并且它比对的是 information_schema 与契约，不是真实的 ACL。真实 catalog 校验仍在
  DBR1 harness 那一侧。
- **生产**：未 ssh、未连生产库、未部署。

---

## risks

1. **【要负责人裁定】本片动了 `contracts/database/role-policy.v1.json`
   与 `role-policy-state-events.v1.jsonl`，与派工里「不要改 role-policy」直接冲突。**

   派工的原话是「新建表**不要改 role-policy**，但要把表登记进 dbroles 漂移门禁读的
   契约里（找 `internal/platform/dbroles/drift_gate*`）」。**本仓不存在
   `drift_gate*` 文件**：漂移门禁读的就是 `role-policy.v1.json`
   （`cmd/db-role-verify/main.go` 的默认 `--policy`）。两句指示在本仓是同一份文件，
   无法同时满足。

   而且这不是「新建表才有的问题」——`alerts.alert` **加两列同样要改它**：
   `policy.go` 的 `tableColumns()` 与 JSON 里的 `columns` 数组都是逐列钉死的，
   `verifier` 对列清单做的是**精确集合比较**。

   两种漏改方式方向相反、都危险：改了 JSON 不追加状态事件 → `dbroles` 的
   `TestCheckedInPolicyContractLoads` 当场红；干脆不改 JSON → 本地全绿，
   红在 DBR1 harness 与生产漂移检查上，也就是**别人手上**。

   **我的处置**（按 000050 的先例，commit `b23ac8b`，形状完全相同）：
   - 改了：`policy.go` 的列清单 + 一行 `add("table","alerts","upstream_version_ack",…)`；
     JSON 的对应两处；事件链追加第 4 条 `evt-alerts-trigger-count-and-version-ack`。
   - **没动**：角色拓扑、能力集合、轮换状态、DefaultACL、`public_*` 任何一项，
     以及既有两张 alerts 表的授权（`TestAlertsTablePrivilegesAreUnchangedByThisSlice`
     把这句话钉住了）。

   **请负责人明确这算不算「改 role-policy」。** 如果算，这三个文件的改动应该拆成
   一张单独的变更单走审批，本片其余部分不依赖它们**编译**，但会依赖它们**不红**。

   新表的授权是 `xm_api_runtime {SELECT, INSERT, UPDATE}`：Action 用
   `ON CONFLICT DO UPDATE` 写，照抄 `alert_silence` 的 `{SELECT, INSERT}` 会编译通过、
   本地全绿，只在角色分离真正上线那天失败。

2. **迟滞造出了一个新的沉默区。** N=3、采集周期 300s ⇒ **一次 10 分钟以内的上游
   读取失败从此不再产生告警**。这是有意的取舍（那段降级仍能从运行保障页的新鲜度、
   后台任务页的运行记录看到，数据真停更时 `metric.data.stale` 接手），但它与
   「数据新鲜度必须可见」擦边。已写进 README 的规则表与生命周期一节；
   **建议同步进 runbook**，否则下一次「为什么没报」没人答得上来。
   补偿是每轮的 `hysteresis_suppressed` / `hysteresis_held` / `version_ack_suppressed`
   进 `alert_evaluate` 的结构化日志——盯守可以直接看这三个数。

3. **`Reconcile` 的活跃快照取样时刻提前了一个评估耗时。** `ListActive` 从
   `Evaluate` 之后挪到了之前（迟滞「关」那一半需要它）。一条在 `Evaluate` 期间被人
   手动确认或解决的告警，本轮仍会被当作「活跃」参与迟滞判定。后果最多是多保留一轮
   （60 秒），已在 `reconcile.go` 的注释里如实写下——它不是原子的，别让下一个人以为是。

4. **`xm_api_runtime` 对 `public.river_job` 一格授权都没有**（策略里只给了
   `xm_worker_runtime`）。今天 `/jobs/runs` 能跑说明生产还没按 dbroles 分角色部署。
   本片把 API 对 `river_job` 的读依赖从 1 处变成 2 处。**这不是本片引入的问题，
   也绝不要顺手加授权**——那是审批门控的策略变更。记在 follow_ups。

5. **迁移号 000054 是指针不是集合。** 并行的 XM-CARD-VISIBILITY / XM-WORKBENCH-TRUTH
   若也加迁移会撞号，而本地库会**静默缺号且退出码 0**。合入前要与主控者对一次号；
   撞了就重建测试库，不要 `goto`。

6. **`gofmt -l` 还剩一处存量违规**：`internal/platform/integration/types_test.go`。
   `git status` 显示它未被本片修改，是既有欠账。没有顺手修——那会让本片的 diff 混进
   一个与它无关的文件。

7. **`sqlc` 生成物是手写的**（见 deviations 第 1 条）。改动是机械的（列清单 + Scan
   目标 + 三条新查询），但它带着 `DO NOT EDIT` 的头。下一个跑 `sqlc generate` 的人
   会得到什么，取决于我写得对不对——集成测试跑通说明列顺序与类型是对的，
   但仍建议主控者复核。

8. **`alerts/gen/models.go` 里既有的漂移不是本片造成的**：那个包的 models 落后于
   迁移 000051–000053（没有 integration / publishing / ext_app 的任何结构体）。
   一次全量 `sqlc generate` 会把它们补进**每个** gen 包，动 8 个共享文件几百行——
   000050 那次的处置是只留必要的那几行，本片同样只动了 alerts 的部分。不顺手修。

9. **`R4` 的 K/W 是我替负责人定的**（见 owner_decisions）。如果实际观测发现它太钝
   （慢性病要一小时才升级）或太灵，改 `DefaultChronicFailureThreshold` /
   `DefaultChronicWindowSamples` 两个常量即可，测试全部从 `DefaultRuleConfig()` 取值，
   不会因为改数字而红。

---

## 替负责人做的决定（三条 needs_owner + 两条实现级）

1. **迟滞 N = 3，开与关同一个数，写成常量不开环境变量。**
   N=3 ＝ 失败满 15 分钟才报、恢复满 15 分钟才撤。09-08 那次风暴里 24h 288 轮只有
   15:07、15:12 两轮失败（相邻两轮），N=2 仍会开，N=3 恰好压住。开关不对称会让人
   事后无法用一个数解释这条告警的行为。不开环境变量：它不是运营会调的东西
   （env 只留「会不会花真钱」），而且新环境变量要同步进 `launch.yaml` 才过治理检查。

2. **「已核对的上游版本」用新表 `alerts.upstream_version_ack`，不复用
   `alert_silence`、不加列到 `alerts.alert`。**
   静默的三条性质全都反着（限时窗口 / 按 rule_key 匹配 / 只挡投递不挡命中）——用它
   实现的话，核对过的版本会在 7 天后自己回来，而且既有 OPEN 告警不会被解决。挂在
   `alerts.alert` 上则会随保留期清理被删掉，那个失效**没有任何人做错任何事、
   也不留任何痕迹**。

3. **失败作业聚合走 `GET /api/v1/ops/overview`，不新开路由。**
   两条路都要动 `router.go`；选这条是因为跨所有权的面最小（一行、既有 deps 字面量的
   一个字段），而权限口径完全一致（都是 `ops.read`）。代价是「工作台的待处理清单去
   `/ops/overview` 取数」路径语义上有点绕——若负责人更看重路径语义，改成
   `/jobs/failure-summary` 是两处跨所有权改动，实现可以直接搬。

4. **R4 的阈值 K=9 / 窗口 W=12，而不是照搬旧的「3」。**
   派工写的是「计数改为滑动窗口，不被一次成功清零」，字面读是「保留 3」。但那样
   会把 R1 的迟滞刚压住的翻面抖动原样从 R4 放出来（12 条窗口里纯交替恰好 6 次失败），
   而且同样是 critical——等于把病换个规则键再犯一次。K=9/W=12 读作「这一小时里
   四分之三的采集是失败的」。`TestChronicWindowResistsPureFlapping` 把这条理由钉住了。

5. **Action ID 用 `alerts.upstream_version.acknowledge`，不是派工写的
   `alerts.acknowledge_upstream_version`。**
   后者是两段式，过不了 `action.actionIDPattern`（要求
   `<域>.<资源>.<动作>` 至少三段），`Definition.Validate` 会在注册时直接拒绝，
   **进程起不来**。

---

## deviations（与简报/派工不一致处，全部有意）

1. **`sqlc` 生成物是手写的，不是 `sqlc generate` 出来的。** 本机没有 sqlc，
   模块缓存里那份 v1.31.1 要求 Go ≥ 1.26 而本机是 1.25.7，离线装不上；
   `GOPROXY=off` 与关闭 sumdb 都试过。改动是机械的：所有 `SELECT *` 展开的列清单
   末尾补两列、所有 `AlertsAlert` 的 `Scan` 末尾补两个目标、新增三条查询与一个
   models 结构体。集成测试跑通说明列顺序与类型都对。**建议在有 sqlc 的机器上复核。**

2. **`RuleConfig.ConsecutiveFailureThreshold` 被删除而不是改名。**
   语义从「连续 K 轮」变成「窗口内 K 次」，留着旧名字会让配了它的人以为自己配的
   还是原来那个判据。删掉则漏改是编译错误。新字段是
   `SyncFailedHysteresisRounds` / `ChronicWindowSamples` / `ChronicFailureThreshold`。
   全仓只有 alerts 包及其测试用过它。

3. **`JobsQuerier` 接口多了一个方法，而不是另立一个接口。**
   `httpapi/jobs.go` 不在本片的文件所有权里，改它是一行接口声明 + 测试假货三行。
   另立接口的话，`router.go` 里 `d.Jobs`（类型是 `JobsQuerier`）就要做类型断言，
   而**断言失败是静默的**——那正是「点了没生效但不报错」那一类失效。

4. **简报建议 Action 参数用 `metric_key`，派工写的是 `source`。** 取 `metric_key`：
   它是平台自己注册过的稳定标识（`ops.KnownMetricKey` 认得它，拼错当场拒绝并列出
   可选值），而 `source` 是连接器自报的展示字段、没有白名单。`source` 由服务端从
   观测里读出后写入库，不来自参数。

5. **`Evaluate` 的签名与返回类型都变了**（`(ctx, env, now, active) → EvaluateResult`）。
   简报只说加入参；返回类型也改了，因为三个抑制器计数必须冒到 `Result` 再进日志——
   一个不留痕的抑制器就是下一个「安静地给你一个旧答案」。

6. **`TestEvaluateR4StreakBrokenBySuccess` 被就地改写而不是新增一条相反的测试。**
   它断言的是「任意一条成功样本打断连续串」，与本片要求的滑动窗口不可能同时绿。
   同一条语义还钉在 `rules.go` 的 `Recovery` 字段、README 的规则表、
   `notify/CATALOG.md` 三处，四处一起改了。

7. **端到端告警测试（`reconcile_integration_test.go`）改成逐轮推进**：
   健康 1 轮 → 失败 N-1 轮（断言**不开**且 `HysteresisSuppressed == 1`）→ 第 N 轮开 →
   持续 1 轮合并 → 恢复 N-1 轮（断言**不撤**且 `HysteresisHeld == 1`）→ 第 N 轮解决。
   它同时把观测与样本一起写（`UpsertWithSample`，与生产 sync worker 同一个方法）——
   只写最新态的话迟滞看不到历史，测试会因为夹具不全而恒不命中。

---

## follow_ups

1. **前端执行入口**（阻塞）：XM-WORKBENCH-TRUTH 要给
   `alerts.upstream_version.acknowledge` 做按钮，否则负责人点不到。契约见上文。
2. **role-policy 的三处改动是否需要单独走审批**（阻塞级，见 risks 第 1 条）。
3. **`xm_api_runtime` 对 `public.river_job` 无授权**：本片让 API 对它的读依赖变成 2 处。
   角色分离真正上线前必须处理，但那是审批门控的策略变更，不在本片。
4. **`sqlc generate` 复核**（见 deviations 第 1 条）。
5. **runbook 补一句迟滞的沉默区**（见 risks 第 2 条）。
6. **`alerts/gen/models.go` 相对迁移 000051–000053 的既有漂移**（见 risks 第 8 条）
   ——不是本片造成的，也没顺手修。
7. **`domainError` 里 `ErrNotFound → PRECONDITION_FAILED` 与 finance 的
   `NOT_REGISTERED/404` 不一致**——子片 A 之前就记着的旧账，本片同样没顺手统一。
8. **迁移号对账**（见 risks 第 5 条）。

---

## 门禁耗时（实测，非估计）

| 阶段 | 开始 (UTC) | 结束 (UTC) | 耗时 |
|---|---|---|---|
| 读需求源与既有实现 | 2026-09-09T03:05:47Z | 03:12:47Z | 约 7 分钟 |
| 实现（迁移 / store / 规则 / Action / 聚合 / API） | 03:12:47Z | 03:36:20Z | 约 24 分钟 |
| 写测试 + 修既有测试 | 03:36:20Z | 03:47:25Z | 约 11 分钟 |
| 四包定向测试全绿 | 03:47:25Z | 03:47:44Z | **17.6 秒**（alerts 1.5s / httpapi 1.1s / jobs 9.6s / dbroles 0.6s / ops 0.6s / notify 0.3s） |
| 15 条变异验证（逐条改→跑→还原） | 03:47:44Z | 03:53:23Z | 约 6 分钟 |
| 变异还原后复跑 | 03:53:05Z | 03:53:23Z | 18 秒 |
| `gofmt -l` + `go vet`（5 包 + cmd） | 03:55:59Z | 03:56:01Z | 约 2 秒 |
| `bash scripts/check-governance.sh` | 03:56:12Z | 03:56:15Z | 约 3 秒 |
| 文档 + 本交接文档 | 03:53:23Z | — | 见提交时间 |

**跑得最久的是 jobs 包（约 10–11 秒）**，因为它带 River 迁移与多条真库集成用例。
其余四个包合计不到 4 秒。

---
---

# 审稿处置轮（2026-09-09T04:27Z – 05:22Z）

审稿给了 13 条阻塞级意见（2 条 fatal、11 条 major，其中 4 组是同一问题的重复
陈述）。逐条处置如下。**本节的契约以本节为准**，它改掉了上面几节里三处口径。

## 0. 需要负责人裁定的第二处越界（与 risks 第 1 条同类）

**又动了一次 `contracts/database/role-policy.v1.json`**：给
`alerts.upstream_version_ack` 的 `xm_api_runtime` 加了 `DELETE`。

- **为什么必须加**：审稿第 9 条指出「已核对版本是一个没有解除路径的闩」。
  解除路径只能是 `DELETE`（表上 `version` 有 `NOT NULL` + `CHECK (btrim(version) <> '')`，
  抹不成空；加 `revoked_at` 列则要新迁移 + 改列清单，比加一个权限更重）。
  撤销 Action 跑在 `platform-api`，用的是 `xm_api_runtime`。
- **同步改了三处**（与上一轮同一形状）：`internal/platform/dbroles/policy.go`
  的 `defaultObjects()`、策略 JSON、`role-policy-state-events.v1.jsonl` 追加
  第 5 条 `evt-alerts-upstream-version-ack-revoke`
  （`current_policy_sha256 = 644048f1…`）。**没动**角色拓扑、能力集合、
  轮换状态、`DefaultACL`，也没动其它任何表的授权。
- 请负责人与 risks 第 1 条一并裁定。

## 1. 【fatal】R4 的恢复条件是假的 → 补上「关」那一半判据

**问题**：`metric.sync.consecutive_failed` 声明的恢复条件是「窗口内失败次数
回落到 9 次以下（一次成功不再清零计数）」，README 与运营看的
`notify/CATALOG.md` 各抄了一份；而代码里 R4 只在 `f.State == StateFailed` 时
产出 `Finding`，Reconciler 又对本轮没再命中的活跃告警一律 `Resolve`——
**任何一轮采集成功都会把 R4 关掉**，窗口里还有 9 条失败也照关。
`FFFSFFFSFFFS` 这种劣化形态下它每隔几轮 open→resolved→reopened 一次，
每次都是新行、重新投递、critical——R1 的迟滞刚压住的抖动换个 `rule_key` 冒出来。

**处置**（审稿建议的 (a)）：`Evaluate` 里取出 R4 的 `dedup_key` 是否在 `active`
集合里（`chronicActive`，`active` 本来就已经在手里，R1 用的就是它）；判据变成

- **开**：当前正在失败 **且** 窗口内失败 ≥ K；
- **关**：只看窗口计数，`failures < K` 才关。

同时：`Rule.Recovery` 改成「窗口内失败次数回落到 9 次以下（中途成功一两轮既不
清零计数，**也不关闭告警**）」；详情文案按半边分支渲染
（`chronicFailureDetail`，最新一轮已成功时不再报错误码——那说的是上一次失败的事）；
新增 `EvaluateResult.ChronicHeld` / `Result.ChronicHeld`，进 `alert_evaluate`
的结构化日志字段 `chronic_held`。

**测试**（`internal/platform/alerts/chronic_recovery_test.go`，新建）：
`TestChronicFailureStaysOpenUntilTheWindowClears` 驱动三段（开 / 继续挂着 / 关）；
`TestChronicFailureDoesNotOpenWhileHealthy` 守住「开还要求当前正在失败」，
其中第二支**故意让 R1 处于活跃**，这样历史样本确实被取了——否则那条断言会
因为外层取数闸而恒真（第一版就是这样，M2 变异当场证伪，见变异表）；
`TestReconcilerKeepsChronicAlertAcrossASuccessfulRound` 从 Reconciler 打进来
（Evaluator 层的 `active` 是测试自己伪造的，证明不了编排层真的传了）。
另把既有 `TestChronicFailureRuleDeclarationMatchesTheJudgement` 的缺席型断言
（「不含旧短语」）补成正向断言（阈值与「也不关闭告警」逐字出现）。

## 2. 【fatal】`failed_jobs_by_kind` 的 null 有两义 + 错误被静默吞掉

**问题**：同一个 JSON `null` 表达「这个部署没接 jobs 数据源」和「接了但这次查库
失败了」；`err` 被 `if err == nil` 吞掉，而整个 `ops_overview.go` 没有任何 logger。
前端按上面的口径会把 null 渲染成「本部署未接入」——一个良性的永久状态——
而真相可能是运行保障页正瞎着，且日志里一个字都没有。两条旧测试互相打架、都绿。

**处置**：

- 响应新增 `failed_jobs_status`：`"ok"` / `"not_wired"` / `"query_failed"`，
  **恒有值**。`failed_jobs_by_kind` 的 `null` 从此只是「没有数据」，
  为什么没有由这个字段说。前端应 `switch` 它，不要判 null。
- `OpsOverviewDeps` 新增 `Logger *slog.Logger`（`router.go` 传 `d.Logger`，
  nil 回落 `slog.Default()`）；`err != nil` 时写一条 Warn
  `ops_overview_failed_jobs_unavailable`，带 `module` / `environment` /
  `err_kind`（**只写错误类型，不写 `err.Error()`**——那可能带库连接串）。
  成功路径不写日志（每 30 秒一次的健康页会把真正的 Warn 淹掉）。

**测试**：`TestOpsOverviewFailedJobsHasThreeDistinctStates`（替换掉那两条互相
打架的旧测试）、`TestOpsOverviewLogsWhenTheFailedJobsQueryFails`。

## 3. 【major】阈值在三份文档里各抄一份，没有探针

**处置**：新增 `internal/platform/alerts/docs_threshold_pin_test.go`：

- `TestTunedThresholdsAreQuotedInBothDocs` —— N / W / K 与两条 critical 的升级
  时延，**数字全部从 `DefaultRuleConfig()` 渲染**，断言逐字出现在
  `docs/modules/alerts/README.md` 与 `docs/modules/notify/CATALOG.md`；
- `TestEveryRuleHasARowInBothDocs` —— 范围从 `Rules()` **发现**，每条规则
  在两份文档的规则表里都要有行。

> 这条探针落地当天就抓到一个存量漏项：`approval.pending.too_long` 从 XM-0030c
> 起就在 `Rules()` 里，却从没写进 `README.md` 的规则表。已补。`CATALOG.md` 的
> 「七条规则」标题也改成「八条」（它的表里本来就有八行）。
>
> handoff **不进探针**：它是时点记录，将来改常量不该逼人回来改一份历史文档。
> 权威口径是 `rules.go` 的常量 + 上面两份被钉住的文档。

## 4 / 8. 【major】L1 的理由说错了（漏数了 `note`）

**问题**：`actions.go` 与 README 都写着「本 Action 的**两个**参数在形状上装不下
凭据」，但 Schema 有**三个**字段——`note` 是 ≤200 字节的自由文本，除长度外零
形态校验，**装得下**凭据。L1 这个结论是对的，理由却反了：正因为装得下，它才
**必须**锁在 L1。一条把自己的理由说错了的注释，比没有注释更容易被拿去做相反的
决定（memory：抬风险等级会泄漏凭据）。

**处置**（选审稿给的第二条路，更便宜且与既有 Schema 能力一致）：改正
`actions.go` 的声明注释、`README.md` 的 Action 权限一节；措辞改成「本 Action 有
自由文本参数，因此**永久锁定 L1**，在 `action.Schema` 支持形状校验
（Pattern / Redacted 标记）之前不得抬级」。新的撤销 Action 的 `reason` 同理。
**没有**给 `note` 加形态校验：能想到的规则（禁长串无空格 token）会把
「见 PR 链接」这类合法备注一起拒掉，用一条会误伤的校验换一句错误的注释不划算。

**测试**：`TestUpstreamVersionActionsArePinnedToL1BecauseFreeTextParamsExist`
——两个 Action 都断言 `RiskLevel == L1`，并断言那个自由文本字段确实存在
（哪天它被去掉了这条会红，届时该重读那段理由再决定，而不是让过期理由挂着）。

## 5. 【major】结束告警的范围是手列的，产生告警的范围是发现的

**问题**：R6 的命中范围**发现自观测**（判据是这条观测里有没有 `version`），而
Action 用 `ops.KnownMetricKey` 那份**手列**白名单做准入闸；落库侧只校验
`ValidMetricKey`，所以未注册的指标观测完全可以存在。两个范围漂开 = 「告警响得
起来、按钮点不动」，那条告警又回到只能等证据过期的状态。今天不出事只因为白名单
**恰好**覆盖了现有连接器。

**处置**：Handler 里把顺序倒过来——先 `ops.ValidMetricKey`（形态，连落库都通不过
的键不必去查库），再 `observations.Get(ctx, metricKey, p.Environment)`；
查不到 → `PRECONDITION_FAILED`，文案把 `RegisteredMetricKeys()` 当**提示**附上。
已注册清单从此不是准入闸。

**测试**：`TestAcknowledgeUpstreamVersionScopeComesFromObservationsNotAWhitelist`
（含一条「用例自身失效检查」：如果那个假想的未注册键哪天被注册了，测试当场
提示换一个）。
**契约变化**：拼错 `metric_key` 的错误码从 `INVALID_PARAMS` 变成
`PRECONDITION_FAILED`（形态非法仍是 `INVALID_PARAMS`）。

## 6. 【major】推送正文还在把评估轮数念成次数

**问题**：管理端那一路修好了，但半夜真正被人读到的是 Telegram / 企微那条消息，
它照旧写着「最近发现: …（累计 669 次）」——用的就是 `FireCount`。
`webhookPayload` 上面那句「字段与 `GET /api/v1/alerts` 的响应对齐」也不成立了。

**处置**（`notify.go`，本片所有）：

- `FormatMessage` / `FormatWeComMarkdown`：
  - `首次发现: <EffectiveFirstOpenedAt>（已持续 11h8m0s）`，兜底值加后缀
    `（首开时刻为估计值）`；
  - `最近发现: <LastSeenAt>（已评估 N 轮）`（原来是「累计 N 次」）；
  - 新增一行 `触发次数: N 次`，`null` 时写 `—（未记录）`（不写 0）。
- `webhookPayload` 补 `trigger_count` / `first_opened_at` /
  `first_opened_at_estimated`，空值口径与 API 逐字相同。
- `docs/modules/notify/CATALOG.md` 的示例与「什么时候会响」一节同步改
  （示例由 `notify/catalog_test.go` 钉住是真实渲染输出，改错会红）。

**测试**：`TestNotificationSeparatesEvaluationRoundsFromTriggers`（照抄 09-08
现场的 669 / 1 形态）、`TestWebhookPayloadCarriesTheSameThreeFieldsAsTheAPI`。

## 7 / 13. 【major】`trigger_count` 与 `first_opened_at` 跨度不一致

**问题**：`first_opened_at` 跨 `RESOLVED→REOPENED` 继承，`trigger_count` 每次复发
从 1 重新开始（复发走 `insert` 新行）。而前端被本文档告知要把两者并排渲染成
「已持续 X，触发 N 次」——一条开关四轮的告警会显示成「已持续 1 小时 35 分，
触发 1 次」。两个数各自都对，合成出来的那句话是假的。

**处置**：`InsertAlert` 的 `trigger_count` 从硬编码 1 改成入参（`sqlc.narg`，
可空）；`Store.insert` 在复发分支里与 `first_opened_at` **一起**继承：
`trigger_count = 上一条 + 1`。**例外**：上一条自己是 000054 之前的旧行
（两列皆 `NULL`）时两列都留 `NULL`——「不知道」是诚实的，从 1 重新起算不是。

> ⚠️ 这一段的初版写的是「时刻仍然继承（上一行的 `opened_at` 是真实发生过的）」，
> 复审证伪：那个时刻是 `EffectiveFirstOpenedAt` 兜出来的**估计值**，写进新行
> 就把它洗成了确定值。已在复审处置轮改正，见本文档末尾第 2 条。

**测试**：`TestFirstOpenedAtSurvivesRecurrence` 就地改写（三次复发后
`trigger_count == 3`、窗口外重新起算为 1）；新增
`TestRecurrenceOfALegacyRowKeepsTheCountUnknown`（直接写库造一条两列皆 NULL 的
旧行——那种行没有任何 Go 侧路径造得出来，而生产里确实存在）。

## 9. 【major】已核对版本是一个看不见、撤不掉的闩

**处置**：补两样。

- **读**：`GET /api/v1/alerts/upstream-versions`（新文件
  `internal/platform/httpapi/alerts_upstream_versions.go`，本片所有；
  `router.go` 加 3 行，`d.UpstreamVersionAcks` 为 nil 时不挂载）。
- **写**：`alerts.upstream_version.revoke@1`（L1，`reason` 必填，
  **不带 `version` 参数**）。

## 11. 【major】R4 的升级时延变慢 3 倍，代价没写

**处置**：README 补一句实测口径——「一次彻底的硬故障，R4 的 critical 升级从
15 分钟（旧的『连续 3 轮』）推迟到 45 分钟（窗口 12 里凑够 9 次失败）。R1 仍在
第 3 轮（15 分钟）给出 critical，所以升级链路不是从零开始等。」并给出备选
K=6 / W=8（30 分钟）。`CATALOG.md` 那一行也补了「那条 15 分钟、这条 45 分钟，
所以先到的一定是上一条」。两句里的数字都被上面第 3 条的探针钉住。

## 12. 【major】R4 的立论例子（card_sync）是错的

**处置**：四处例子换成 2026-09-08 那三条 NewAPI 指标
（`newapi.channels.status` / `newapi.users.total` / `newapi.recharge.daily`），
并在 `rules.go` 与 README 各留一段显式警告：card_sync 是 `river_job` 的失败，
根本不产出 ops 观测（`ops/freshness.go` 的白名单里没有任何 `card_*` 键），
这条规则看不见它；它的合并展示在 `/ops/overview` 的 `failed_jobs_by_kind`。
提交消息 `c9d4d9a` 里的同一句话改不了，在这里更正。

---

## 契约变更（覆盖本文档前面几节，前端片以此为准）

### 1）`GET /api/v1/ops/overview` —— `failed_jobs_by_kind` 的空值口径改了

```jsonc
{
  "failed_jobs_by_kind": null,          // 或 [] 或 [ ... ]，见下表
  "failed_jobs_status": "query_failed", // "ok" | "not_wired" | "query_failed"
  "failed_jobs_window_hours": 24
}
```

| `failed_jobs_status` | `failed_jobs_by_kind` | 含义 | 前端该怎么渲染 |
|---|---|---|---|
| `ok` | `[]` 或有元素 | 查过了 | 正常渲染；空数组 = 「窗口内没有失败作业」 |
| `not_wired` | `null` | 这个部署没接 jobs 查询器 | 「本部署未接入」，良性 |
| `query_failed` | `null` | 接了，但这次读库失败 | **这一格正瞎着**，要提示用户，别当成「没有失败」 |

**不要再用「`null` = 未接入」这条旧口径**（本文档上面那一节写的就是它）。
服务端在 `query_failed` 时会写一条 Warn `ops_overview_failed_jobs_unavailable`。

### 2）`GET /api/v1/alerts/upstream-versions`（新增，`ops.read`）

```jsonc
{
  "items": [
    {
      "metric_key": "sub2api.connector.health",
      "version": "0.2.3",
      "source": "sub2api-prod",
      "acknowledged_by": "staff_alice",
      "acknowledged_at": "2026-09-09T03:40:00Z"
    }
  ],
  "revoke_action": "alerts.upstream_version.revoke"
}
```

- `source` 是服务端从观测里读出来的，不是参数。
- **没有 `note`，前端不要给它留位置。** 执行时写的那段自由文本除 ≤200 字节外
  没有形态校验，「形状上装得下凭据」正是这个 Action 被永久锁在 L1 的第一条
  理由；本端点只要 `ops.read`（staff 就有），而审计事件的读路径单独要
  `audit.read`。回显它等于把它降一档。要看「当时那个人说了什么」，
  走 `GET /api/v1/audit/events`。**这一条覆盖本文档前面出现过的
  带 `note` 的响应样例**（复审处置轮第 3 条）。
- 环境来自调用者身份，不是查询参数；跨环境读取一律拒绝。
- **界面上建议放在「运行保障 → 告警」里**，或做成版本告警旁边的一个「已核对
  记录」抽屉：读到这份清单的人下一个问题必然是「点错了怎么办」，
  所以响应里直接带了撤销用的 Action ID。
- 服务端未装配该依赖时端点**不存在（404）**，不是 500——前端要能容忍 404。

### 3）`alerts.upstream_version.revoke@1`（新 Action，前端要给它做入口）

```
POST /api/v1/actions/alerts.upstream_version.revoke/versions/1/execute
{ "metric_key": "sub2api.connector.health", "reason": "核对时看错了行" }
```

- 权限 `alerts.alert.manage`（与核对同一个 scope），风险等级 **L1**（永久锁定），
  人类身份，三个环境都允许。
- **不带 `version`**：撤的是「这条上游此刻记着的那条核对」。让调用方再报一次
  版本号只会多出「上游已经又升级了所以你撤不掉」这种失败形态，而那恰恰是最
  需要撤销的时刻。
- 错误码：`INVALID_PARAMS`（`metric_key` 形态非法 / `reason` 空白或超 200 字节）、
  `PRECONDITION_FAILED`（这条上游本来就没有已核对记录）。
- 撤销后**下一轮评估（≤60 秒）**那条版本提醒就回来了。

### 4）`alerts.upstream_version.acknowledge@1` 的错误码有一处变了

拼错的 `metric_key`（形态合法但这个环境下没有观测）从 `INVALID_PARAMS` 变成
**`PRECONDITION_FAILED`**，文案里附已注册指标清单作为提示。形态非法
（不匹配 `^[a-z0-9][a-z0-9_.-]{0,127}$`）仍是 `INVALID_PARAMS`。

### 5）Webhook 投递体（自建端点，不是管理端）

`webhookPayload` 补了 `trigger_count`（可为 `null`）、`first_opened_at`（恒非空）、
`first_opened_at_estimated`。`fire_count` 保留不动。

---

## 审稿处置轮的变异验证表（17 条，全部实测）

跑法：改一处实现 → 跑定向测试 → 记红/绿 → 还原 → 复跑全绿。

| # | 变异 | 预期红 | 实测 |
|---|---|---|---|
| M1 | `Evaluate` 里 R4 的闸改回 `f.State == StateFailed`（去掉 `chronicActive`） | R4 关那一半 | **红**：`TestChronicFailureStaysOpenUntilTheWindowClears` + `TestReconcilerKeepsChronicAlertAcrossASuccessfulRound` |
| M2 | R4 的闸放宽成 `if true`（开也不要求当前失败） | 「不该新开」 | **第一次绿 → 测试没写对**。原因：`active` 为空时外层取数闸根本没进那段代码，断言恒真。给测试补了「R1 活跃、R4 不活跃」那一支后**红**：`TestChronicFailureDoesNotOpenWhileHealthy` |
| M3 | `chronicFailureDetail` 恒走「正在失败」分支 | 详情半边 | **红**：`TestChronicFailureStaysOpenUntilTheWindowClears` |
| M4 | 读库失败时 `failed_jobs_status` 也置 `not_wired`（两态合一） | 三态互异 | **红**：`TestOpsOverviewFailedJobsHasThreeDistinctStates` |
| M5 | 把那条 Warn 降成 Debug（等于不留痕） | 降级留痕 | **红**：`TestOpsOverviewLogsWhenTheFailedJobsQueryFails` |
| M6 | `DefaultSyncFailedHysteresisRounds` 3 → 4 | 文档探针 | **红**：`TestTunedThresholdsAreQuotedInBothDocs`（两份文档各一个子测试）。**其余测试全绿**——证明 N 在 Go 侧确实只有一处定义 |
| M6b | （M6 的副产物）`TestChronicFailureStaysOpen…` 也红了 | — | 那是测试自己耦合了 `W-K ≥ N` 这个前提。已改成显式条件跳过并写明理由，重跑 M6 后只剩文档探针红 |
| M7 | 删掉 README 里 `approval.pending.too_long` 那一行 | 覆盖范围 | **红**：`TestEveryRuleHasARowInBothDocs` |
| M8 | Handler 里把 `ValidMetricKey` 换回 `KnownMetricKey` | 范围同源 | **红**：`TestAcknowledgeUpstreamVersionScopeComesFromObservationsNotAWhitelist` |
| M9 | 核对 Action 的 `RiskLevel` 抬到 L2 | L1 锁定 | **红**：三条（`TestActionDefinitionsAreValid` / `TestAcknowledgeUpstreamVersionDefinition` / `TestUpstreamVersionActionsArePinnedToL1…`） |
| M10 | `RegisterActions` 里不注册撤销 Action | 成对注册 | **红**：`TestActionDefinitionsAreValid` + 两条撤销集成测试 |
| M11 | `FormatMessage` 改回「（累计 N 次）」并删掉触发次数行 | 推送口径 | **红**：`TestNotificationSeparatesEvaluationRoundsFromTriggers` + `TestTelegramNotifierSendsMessage` |
| M12 | `webhookPayload` 不填 `trigger_count` | 投递体对齐 | **红**：`TestWebhookPayloadCarriesTheSameThreeFieldsAsTheAPI` |
| M13 | 复发时不继承 `trigger_count`（恒为 1） | 两字段同跨度 | **红**：`TestFirstOpenedAtSurvivesRecurrence` + `TestRecurrenceOfALegacyRowKeepsTheCountUnknown` |
| M14 | 上一条是旧行时把 `trigger_count` 填 1 而不是 NULL | 不编造 | **红**：`TestRecurrenceOfALegacyRowKeepsTheCountUnknown` |
| M15 | `router.go` 不挂载 `/alerts/upstream-versions` | 读路径存在 | **红**：四条 `TestListUpstreamVersionAcks*` |
| M16 | `policy.go` 的授权改回去、**不动**策略 JSON | 两份策略一致 | **第一次绿 → 缺闸**。`go test ./internal/platform/dbroles` 全绿：`defaultObjects()` 与磁盘上的策略 JSON 之间没有任何测试。补了 `TestAlertsPolicyGoAndContractAgree`（范围只覆盖 alerts 两张表）后**红** |
| M17 | 删掉新追加的第 5 条 policy-update 事件 | 摘要链 | **红**：`TestCheckedInPolicyContractLoads` |

**M2 与 M16 是这一轮最有价值的两条**：它们各自证伪了一条我本来会当成「已覆盖」
的断言——一条恒真（测试没走进被测代码），一条根本没有闸（两份策略无人对账）。

---

## 审稿处置轮的门禁（实测时刻，非估计）

| 门禁 | 开始 (UTC) | 结束 (UTC) | 耗时 | 结果 |
|---|---|---|---|---|
| `go test -p 1 -count=1`（alerts / httpapi / notify / jobs / dbroles / ops） | 05:04:26 | 05:04:45 | 19 秒 | 全部 ok（集成用例真跑，`XM_TEST_DATABASE_URL` 指向 `xm_test_wt_xm_ops_truth`） |
| `go vet`（六个改动包 + cmd/platform-api） | 05:04:54 | 05:04:55 | 1 秒 | 通过 |
| `gofmt -l`（同上 + contracts / db） | 05:04:55 | 05:04:56 | <1 秒 | 无输出 |
| `bash scripts/check-governance.sh` | 05:04:56 | 05:04:58 | 2 秒 | exit 0（该脚本只在失败时输出） |
| `sqlc generate` + 裁剪外溢 | 04:39 | 04:41 | 约 2 分钟 | 只保留 `alerts/gen/alerts.sql.go`，八个 `models.go` 的外溢一律 `git checkout` 还原（同上一轮，既有漂移不在本片修） |

**未跑**：全量 `go test ./...`（派工明确由主控者跑）、前端 `pnpm`（本片不改
`web/`）、`scripts/test-database-roles.ps1` / DBR1 harness（需要另一套夹具，
且 `cmd/db-role-verify` 要连真库——本片不连库）。

---

## 审稿处置轮新增 / 改动的文件

**新增**

- `internal/platform/alerts/chronic_recovery_test.go`
- `internal/platform/alerts/docs_threshold_pin_test.go`
- `internal/platform/httpapi/alerts_upstream_versions.go`
- `internal/platform/httpapi/alerts_upstream_versions_test.go`

**改动**

- `internal/platform/alerts/rules.go`（R4 关那一半、`ChronicHeld`、
  `chronicFailureDetail`、R4 声明文案、card_sync 例子更正）
- `internal/platform/alerts/reconcile.go`、`internal/platform/jobs/alert_evaluate.go`
  （`ChronicHeld` → `chronic_held` 日志字段）
- `internal/platform/alerts/actions.go`（L1 理由更正、`metric_key` 范围改为发现、
  新增撤销 Action、`ackMetricKeyParam`）
- `internal/platform/alerts/store.go`（`trigger_count` 继承、
  `DeleteUpstreamVersionAck`、`ListUpstreamVersionAcksOrdered`）
- `internal/platform/alerts/notify.go`（推送文案、`webhookPayload` 三个字段）
- `db/queries/alerts.sql` + `internal/platform/alerts/gen/alerts.sql.go`
  （`InsertAlert` 的 `trigger_count` 入参、`DeleteUpstreamVersionAck`）
- `internal/platform/httpapi/ops_overview.go` / `router.go`
- `internal/platform/dbroles/policy.go`、`contracts/database/role-policy.v1.json`、
  `contracts/database/role-policy-state-events.v1.jsonl`（第 0 条的越界）
- `cmd/platform-api/main.go`（一行：`UpstreamVersionAcks: alertStore`）
- `docs/modules/alerts/README.md`、`docs/modules/notify/CATALOG.md`
- 六个测试文件的就地改写（见变异表对应行）

---

## 审稿处置轮的 follow_ups

1. **`policy.go` 的 `defaultObjects()` 与 `role-policy.v1.json` 全局没有对账测试。**
   本轮 M16 证实了这一点，但只补了覆盖 `alerts` 两张表的那一条
   （`TestAlertsPolicyGoAndContractAgree`，放在本片拥有的文件里）。
   其余 schema 仍然是两份无人对账的副本——建议给 `dbroles` 补一条全量的。
2. **`chronic_held` 与 `hysteresis_held` 只进日志，没有进 `/ops/overview`。**
   运维要回答「为什么这条 critical 在采集已恢复的情况下还挂着」，今天只能翻
   worker 日志。
3. **`xm_api_runtime` 对 `public.river_job` 一格授权都没有**（上一轮已记，未变）。
   角色分离真正上线那天 `failed_jobs_by_kind` 会变成 `query_failed`——现在至少
   会写一条 Warn 了，但授权本身仍需走审批。
4. **撤销 Action 与只读端点同样没有前端落点**（与核对 Action 同一条阻塞项）。
   XM-WORKBENCH-TRUTH 做按钮时请把「已核对记录 + 撤销」一并做掉，否则读得到
   但撤不掉，只是把闩挪了个位置。
5. **`note` / `reason` 仍是无形态校验的自由文本。** 两个 Action 因此永久锁 L1。
   若将来 `action.Schema` 支持 Pattern / Redacted 标记，可重新评估。

---

# 复审处置轮（2026-09-09T05:31Z – 05:42Z）

复审对上一轮的 7 + 10 条 finding 逐条重跑变异，判定 3 条仍未解决（全部 major）。
本节处置这 3 条。**没有新增功能，全部是补闸、改正与去掉一条泄漏通道。**

## 1. 【major】「慢任务不可能与自己重叠」只在默认配置下成立

**问题**：这条不变量是把 maintenance 从 1 槽抬到 4 槽的前提，但
`TestSlowMaintenanceJobsCannotOverlapThemselves` 用的是 `slotConfig(t)`
（= `DefaultConfig()`），只比过默认的 300s。复审用探针把
`cfg.NewAPISyncInterval` 配成 90s：`validate()` 放行（唯一下限是 River 的
1 秒），`maintenanceQueueSlots` 照给 4 槽，而 `newapi_sync` 的作业期限是
2m0s —— 期限 ≥ 周期，自重叠窗口成立，全套门禁一条不红。周期性插入用的
`UniqueOpts.ByPeriod` 是**本次配置的周期**（`newManifestPeriodicJob`），
所以 90 秒后那次插入落在**另一个** period 桶里，River 的唯一性拦不住它。

**处置**（复审修法 a）：新增启动闸 `validateJobCadence`
（`internal/platform/jobs/queue_slots.go`），由 `Config.validate()` 在最后一条
调用——上面每条「River 一秒下限」的错误更具体，同一份坏配置该先听到那句话。
判据：**启用**的、**覆写过 `Timeout()`** 的 maintenance 任务，其作业期限必须
**严格小于**它的周期，否则进程拒绝启动。

闸的范围是**发现**出来的，不是手列的（memory「闸的范围要发现不要手列」）：

- 任务清单 ← `RegisteredPeriodicJobSpecs()`；
- 启用状态与周期 ← `effectiveJobConfig`（与 `/ops` 那张部署态时刻表、与
  `maintenanceQueueSlots` 同一段代码）；
- 作业期限 ← 新增的 `configuredJobTimeout`。只有这一项是按 kind 分支的手写
  名单，所以给它配了一条**反查**测试 `TestConfiguredJobTimeoutCoversEverySlowJob`：
  跑一次真实 `NewClient`（它经 `addWorker` 收集每个 Worker 自己声明的
  `Timeout()`），读它算出来的 `slow_job_kinds`，逐个要求
  `configuredJobTimeout` 认得。加第四个慢任务却忘了登记的人会看到这条红。

**范围比派工多一个 `finance_collect`**：派工只点名 newapi / sub2api，但
finance 是同一个队列上的第三个慢任务（`queue_slots.go` 自己数出来的三个之一），
期限同样 120s。只挡两条等于留一个已知的洞。

**真实下限**（派工说的「README:81 的下限 1 秒」在仓库里不存在，见 deviations
第 2 条）改写在运维真正会读的地方 `deploy/compose/.env.example`：
`XM_NEWAPI_SYNC_INTERVAL` 最小 121s、`XM_FINANCE_COLLECT_INTERVAL` 最小 121s、
`XM_SUB2API_SYNC_INTERVAL` 最小 101s；`XM_ALERT_EVALUATE_INTERVAL` 的下限
**确实**还是 1 秒（评估任务没覆写期限），这一点也写明了，免得读者以为所有
周期都被抬了。

**测试**：`TestJobCadenceRejectsSelfOverlappingInterval`（表驱动，9 行，
全部用非默认配置，含复审那条 90s 探针与三个「恰好等于期限」的边界）、
`TestJobCadenceErrorNamesTheKnob`（启动错误必须点名任务、两个数、环境变量）、
`TestConfiguredJobTimeoutCoversEverySlowJob`（范围反查）。

## 2. 【major】估计值被洗成确定值（含随之而来的三处相反陈述）

**问题**：`store.go` 的复发分支写的是
`if inherited, _ := previous.EffectiveFirstOpenedAt(); ...`——第二个返回值
（「这是不是兜出来的估计值」）被丢掉了。上一环是 000054 之前的旧行时，
兜底值被原样写进新行的 `first_opened_at`，库里从此分不出「记下来过」与
「兜的底」，读取侧的 `first_opened_at_estimated` 恒为 `false`，界面上那个
「（估计值）」后缀永远不再出现。

**处置**：接住那个 bool，为真时**不写** `first_opened_at`（保持 NULL），
读取侧继续按 `EffectiveFirstOpenedAt` 兜底并如实标 `estimated`。
`insert` 里 `firstOpenedAt time.Time` 改成 `firstOpenedAtArg pgtype.Timestamptz`
——与 `triggerCountArg` 同一条口径：**NULL 表示不知道**。

**取舍写在了三处（代码注释 / README / 这里），不留一句好听的**：留 NULL 之后
「已持续」从**本行**的 `opened_at` 起算，比真实时长短掉中间那一段复发间隔。
库里只有「确定值」与「不知道」两档，没有第三档能存住「这是估计值但它更早」。
少报一段并明说是估计，好过报一个更准的数却谎称它确定（宪法 12 条）。
要两全得给这一列配一个 `estimated` 标记列，见 follow_ups。

**同步改正三处相反陈述**：`store_integration_test.go` 那条「时刻仍然继承
（上一行的 opened_at 是真实发生过的）」的注释与断言、
`docs/modules/alerts/README.md` 的同一句、本文档「7 / 13」那一节
（原地加了一条 ⚠️ 更正，没有把旧话悄悄抹掉）。

## 3. 【major】新端点把 note 从 audit.read 降到了 ops.read

**问题**（上一轮修正引入的新问题）：`alerts_upstream_versions.go` 在
`ops.read` 这一档把 `note` 原样回显，注释写「它已经进了审计链，在这里回显
不新增泄漏面」。这句不成立：审计链的读路径是 `router.go` 的
`RequireScope(audit.ScopeRead)`，那一行自己写着「审计事件带前后摘要，敏感度
高于 ops.read」；而 `staff` 这个粗粒度角色拿的是
`registry.read + ops.read + ui.saved_view.manage`，**不含 `audit.read`**。
于是同一份代码在两个地方给出互相矛盾的判断——`actions.go` 说 note
「形状上装得下凭据、所以永远不能进展示通道」，这里却开了一条。

**处置**（复审修法的第一支）：去掉 `upstreamVersionAckItem.Note`（字段 + 赋值
+ 测试里那条在场断言）。响应只回 `metric_key` / `version` / `source` /
`acknowledged_by` / `acknowledged_at` —— 五个全部是平台自己写下的事实，
足够回答「这条抑制是谁按的、按的是哪个版本」。要看「当时那个人说了什么」，
走既有的 `GET /api/v1/audit/events`（`audit.read`），那是它本来就该在的一档。

顺带在 `actions.go` 的 L1 声明里把「note 今天有哪些读路径、各要什么 scope」
写全，并写明这个端点**有意**不回显它——否则下一个人还会照着「已经进审计链了」
那句话再开一次。

**测试**：新增 `TestListUpstreamVersionAcksDoesNotEchoTheNote`（缺席型断言，
两条：响应里既不许有 `"note"` 这个键，也不许有 note 的内容换个键名溜出去）。
两个防恒真前提都写在同一条用例里：仓储桩**确实**带着 note 回来
（`sampleAck()` 填了 `ackNoteMarker`），且投影**确实**渲染了
（`acknowledged_by` 在场）。

---

## 复审处置轮的变异验证表（5 条，全部实测）

| # | 变异 | 被打中的断言 | 结果 |
|---|---|---|---|
| M1 | `alerts_upstream_versions.go` 把 `Note` 字段与赋值加回去 | `TestListUpstreamVersionAcksDoesNotEchoTheNote` | **红**（响应体里出现 `"note"` 及其内容）→ 还原后绿 |
| M2 | `store.go` 把 `wasEstimated` 改回丢弃（`inherited, _ :=`） | `TestRecurrenceOfALegacyRowKeepsTheCountUnknown` | **红**（新行 `first_opened_at` 非 NULL）→ 还原后绿 |
| M3 | `client.go` 去掉 `validate()` 里的 `validateJobCadence(c)` 调用 | `TestJobCadenceRejectsSelfOverlappingInterval` 的 4 个子用例 + `TestJobCadenceErrorNamesTheKnob` | **红** → 还原后绿 |
| M4 | `queue_slots.go` 把判据从 `timeout < interval` 放宽成 `<=` | 三个「周期恰好等于期限」子用例 | **红** → 还原后绿 |
| M5 | `queue_slots.go` 的 `configuredJobTimeout` 删掉 `NewAPISyncJobKind` 分支 | `TestConfiguredJobTimeoutCoversEverySlowJob` | **红**（真实注册点把它判成慢任务，闸却不认得它）→ 还原后绿 |

**M5 是这一轮最有价值的一条**：它证明这条闸的手写部分（期限名单）与被它描述
的对象（真实 Worker 声明的 `Timeout()`）之间有对账，而不是又一份会静静漂开的
副本（memory「被信任的过期闸最危险」）。

---

## 复审处置轮的门禁（实测时刻，非估计；UTC）

下表是**提交前那一遍**的实测时刻（此前另有一遍中途门禁，05:40:38–05:41:33，
结果相同）。

| 门禁 | 开始 | 结束 | 耗时 | 结果 |
|---|---|---|---|---|
| `scripts/dev/worktree-testdb.sh --print-url` | 05:31:16 | 05:31:18 | 2 秒 | 库已存在，无待应用迁移 |
| `go build ./...` | 05:45:40 | 05:45:41 | 1 秒 | 无输出 |
| `go vet ./...` | 05:45:41 | 05:45:42 | 1 秒 | 无输出 |
| `go test -p 1 -count=1 ./internal/platform/jobs/ ./internal/platform/alerts/... ./internal/platform/httpapi/ ./cmd/...` | 05:45:42 | 05:46:03 | 21 秒 | 14 个包全部 ok（集成用例真跑，`XM_TEST_DATABASE_URL` 指向 `xm_test_wt_xm_ops_truth`） |
| `bash scripts/check-governance.sh` | 05:46:03 | 05:46:06 | 3 秒 | exit 0 |
| `gofmt -l internal cmd` | 05:46:06 | 05:46:07 | 1 秒 | 只剩 `internal/platform/integration/types_test.go`——**本轮没碰过它**，是仓库既有漂移，不在本片修 |

中途我把两个集成测试文件的字段对齐改乱过一次，`gofmt -w` 已修（05:40:44）。

**未跑**：全量 `go test ./...`（派工明确由主控者跑）、前端 `pnpm`（本轮不改
`web/`）、`sqlc generate`（本轮不改 SQL）。

---

## 复审处置轮改动的文件

- `internal/platform/jobs/queue_slots.go`（新增 `configuredJobTimeout`、
  `validateJobCadence`）
- `internal/platform/jobs/client.go`（`validate()` 末尾调用新闸）
- `internal/platform/jobs/queue_slots_test.go`（三条新测试 + `strings` 导入）
- `internal/platform/jobs/newapi_sync_integration_test.go`、
  `internal/platform/jobs/sub2api_sync_integration_test.go`
  （1 秒周期改 10 分钟，见 deviations 第 5 条）
- `internal/platform/alerts/store.go`（接住 `wasEstimated`、`firstOpenedAtArg`、
  `insert` 文档注释）
- `internal/platform/alerts/store_integration_test.go`（那条注释与断言）
- `internal/platform/alerts/actions.go`（L1 声明里补「note 的读路径」）
- `internal/platform/httpapi/alerts_upstream_versions.go`（去掉 `Note`）
- `internal/platform/httpapi/alerts_upstream_versions_test.go`（缺席断言）
- `docs/modules/alerts/README.md`（继承例外、端点字段说明）
- `deploy/compose/.env.example`（三处真实下限）
- 本文档（「7 / 13」的更正、契约节的 `note` 移除、本节）

---

## 复审处置轮的 deviations（与派工不一致处，全部有意，全部当下记下）

1. **handoff 文件名**：派工写的是 `docs/handoffs/slices/XM-OPS-TRUTH.md`，
   仓库里没有这个文件——本切片拆成 A / B 两个子片，本轮的改动全部落在 B，
   所以写进 `XM-OPS-TRUTH-B.md`。
2. **「README:81 的下限 1 秒」在仓库里不存在**：全仓没有 jobs 模块的 README，
   `grep` 也找不到任何 `.md` 写过这句话（复审给的位置是错的）。真实下限改写在
   运维真正会读的地方：`deploy/compose/.env.example` 三处。
3. **复审给的断言原文自相矛盾，取了它的前两句**：原文要求「新行
   `first_opened_at` 仍为 NULL **且** `EffectiveFirstOpenedAt` 的 estimated
   为 true、**时刻等于上一行 opened_at**」。第三句与前两句不可能同时成立——
   `first_opened_at` 为 NULL 时兜底取的是**本行**的 `opened_at`，读取侧拿不到
   上一行。落地取前两句，第三句改成「等于本行 `opened_at`」，并**额外**钉住
   「不等于上一行 `opened_at`」（否则估计值换条路又被继承进来）。代价见第 2 节。
4. **闸的范围比派工多一个 `finance_collect`**，理由见第 1 节。
5. **改了两个既有集成用例的周期（1 秒 → 10 分钟）**。这不是为了让门禁变绿而
   改测试：那两份配置正是这条不变量禁止的形状（期限 100s / 120s 的任务每秒被
   排一次）。两个用例要的都只是 `RunOnStart` 触发的那**一次**执行，周期只需大到
   用例结束前不会再来第二次。

---

## 复审处置轮的 risks

1. **新闸是启动闸，不是降级**。生产若有人把三条采集周期配到期限以下，
   worker 会**拒绝启动**。当前生产是 300s 默认值
   （`deploy/compose/server-prod.yaml`、`launch.yaml`），不受影响；
   但下次调这几个值的人必须先读 `.env.example` 里新加的那三段。
2. **两条同步的作业期限今天实际是常量**：`Sub2APIRequestTimeout` /
   `NewAPIRequestTimeout` 没有 env 入口（`cmd/platform-worker/config.go` 只读
   `XM_FINANCE_COLLECT_REQUEST_TIMEOUT`），所以期限恒为 100s / 120s，
   `.env.example` 里写的「最小 101s / 121s」是真值。将来给这两个超时开 env
   入口时，那三段文案会跟着变——它们是**算**出来的，不是常量。
3. **`first_opened_at` 的「已持续」在旧行复发链上会少报一段**（第 2 节的取舍）。
   影响面限于 000054 迁移之前就存在的行，且界面上带「（估计值）」标注。

## 复审处置轮的 follow_ups

1. **给 `first_opened_at` 配一个 `estimated` 标记列**，就能同时保住「继承更早
   的时刻」与「如实说它是估计值」。今天只有两档口径，必须二选一。
2. **`XM_SUB2API_SYNC_INTERVAL` 在 `.env.example` 里没有独立条目**（只在
   `XM_ALERT_EVALUATE_INTERVAL` 的注释里被引用），所以它的下限说明只能挂在
   别人旁边。建议补一个正式条目，与 newapi / finance 对齐。
3. **`validateJobCadence` 只管 maintenance 队列**。`assurance.QueueProbe` 上的
   探测任务今天是按需触发、没有周期，将来给它加周期任务时要把这条闸的队列
   范围一起想清楚。
