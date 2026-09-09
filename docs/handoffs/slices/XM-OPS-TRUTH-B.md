# XM-OPS-TRUTH 子片 B：告警语义（迟滞、触发次数、已核对版本、失败作业聚合）

- **status**: ready-for-review（分支内交付，未推 GitHub、未部署）；
  **含一处需要负责人裁定的越界**，见 risks 第 1 条
- **branch**: `ai/claude/XM-OPS-TRUTH`（接在子片 A 的 `ddf4b36` 之上）
- **commit**: `c9d4d9a`（41 files changed, +4050 / -282）→ `<本次回填>`
- **时间**: 2026-09-09T03:05:47Z – 2026-09-09T04:02Z（约 56 分钟，实测；
  逐阶段见文末「门禁耗时」）
- **需求来源**: `docs/handoffs/PLATFORM-ALERT-STORM-2026-09-08.md`
  §二第一行（NewAPI 三条翻面 + 「已持续」归零 + 连续失败计数被成功清零）、
  §二第二行（版本告警 669 次、没有「我核对过了」这个动作）、
  §二第四行（288 条 card_sync 把「我的待处理」占满）、§四第二组

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
