# XM-INV-STRANDED-EVENTS —— 四个角度的原始提案与八份对抗审稿（工作流原件）

> 由 2026-09-08 设计工作流的 journal 原样导出，供 L1/L2 实现者按提案与审稿条件施工。综合计划见 XM-INV-STRANDED-EVENTS-DESIGN.md。


---

## 提案：XM-INV-DEAD-CONTAINMENT：死信按账号兜住，流级只对「无人认领」的死信 fail-closed

**角度**：影响面——一个账号的孤儿事件不该让同一来源实例下其他所有客户开不了票。只处理「事件已判死」之后的放大链（dead → EVENTS_DEAD 流级致命 → 五流 AND → 全实例 source_unavailable / ErrSourceUnavailable / readyz 503）；不处理孤儿事件的产生、不处理判死前的 pending 窗口、不处理错误分级。

代码里对「一条死信是否拖住整条流」已经有两个互相矛盾的定义：扫描周期完整性（backend/internal/postgresstore/consumption.go:718-731）说「failed/dead 且该 payload_hash 上已有 open 冻结 → 不拖住周期」，而流级健康（source_sync.go:1122-1124 EVENTS_DEAD 致命）说「任何 dead → 整条流不就绪」，后者经 ListFundingLots/ListUserEligibilitySummaries 的五流 AND（backend/internal/application/service.go:613-616、:1276-1286）与 Submit/ConfirmManualIssue 的 assertSourceFreshTx（source_sync.go:1455-1456；调用点 store.go:380、requests.go:579）打到该来源实例下的每一个客户。09-07 的 3 条死信都在 2222 名下、2222 有 3 个开放冻结（EVENT_DEAD 冻结的作用正是让该账号本来就开不了票：freezeEligibilityTx consumption.go:2392-2402 置 frozen + 作废预留，Submit/Confirm 的额度 UPDATE 又要求 eas.eligibility_status='active'，store.go:503-509 / requests.go:352-357），所以流级闩对 2222 是多余的、对另外 6 个用户是纯放大。方案：把周期完整性那条「open 冻结兜住」的关联抽成唯一一份 SQL 片段，四处健康查询与周期完整性查询都从它渲染（沿用 SER-BUSY 把 transientRequeueMarkers 渲染进 SQL 的单一定义手法，source_sync.go:1170-1200）；死信分成「已兜住」（有 open 冻结匹配 payload_hash）与「未兜住」两类，只有未兜住的才是流级致命 EVENTS_DEAD，已兜住的降为新的非致命原因 EVENTS_DEAD_CONTAINED（加进 nonFatalStreamHealthReasons 这唯一一处，runtime.go 的两个 allowed 集合改为从它派生而不再手抄）；readyz 对未兜住死信照旧 503，对已兜住死信返回 200 但 body 带 degraded 列表并按 5 分钟节流打 Error 日志；同时给 ResolveEligibilityFreeze 加一道显式关卡：凡 source_revision_hash 仍对应 processing_status='dead' 的事件，冻结不得解——这是把今天靠「流不就绪 → ErrEligibilitySourceStale」偶然成立的顺序（eligibility_operations.go:239-250）改成为自己负责的规则，也是「不会被遗忘」的保证：EVENT_DEAD 冻结留在管理员冻结队列里，只有 ingest-requeue-dead / ingest-acknowledge-unreplayable 两个修复工具能让它可解。未兜住的死信（无落库事实、无应用层 hint、payments/identities 流）行为完全不变，仍 fail-closed 且已有恢复路径。

### 机制

1) 单一定义的兜住谓词。source_sync.go 新增常量 sourceEventOpenFreezeSQL = `EXISTS (SELECT 1 FROM eligibility_freezes ef WHERE ef.source_revision_hash=sie.payload_hash AND ef.status='open')`（要求宿主查询里 ingest 行别名为 sie）。这正是 tryPublishEconomicScanCyclesTx 今天用 LEFT JOIN ... ef.id IS NOT NULL 表达的关联（consumption.go:727-728），改成同一片段后周期完整性与流级健康在物理上只剩一处定义。
2) 计数拆分。四个查询各多返回一列 dead_events_contained：SourceIngestHealth（source_sync.go:1039-1044）、sourceReadinessHealthQuery（:1225-1258，仿 busy_within_grace 先在 classified 子查询算成布尔列 dead_contained 再 FILTER，保持 source_ingest_events_readiness_active_idx 索引路径）、SourceHealth（:1338-1361）、assertSourceFreshTx（:1420-1436）。结构体加字段 SourceStreamHealth.ContainedDeadEvents、SourceIngestHealth.DeadContained；DeadEvents/Dead 仍是总数（前端与日志继续显示总数）。
3) 判定。evaluateSourceStreamHealth（:1087-1131）改为：uncontained := DeadEvents-ContainedDeadEvents；uncontained>0 → 追加 EVENTS_DEAD（致命，语义不变）；ContainedDeadEvents>0 → 追加 EVENTS_DEAD_CONTAINED；nonFatalStreamHealthReasons（:1071）加入 EVENTS_DEAD_CONTAINED，并导出 NonFatalStreamHealthReasons() 供 cmd/api 派生。Ready 计算逻辑不变——所以五流 AND、assertSourceFreshTx、ResolveEligibilityFreeze 的 assertSourceFreshTx 三个消费者不用改一行就自动只对未兜住死信 fail-closed。
4) readyz。runtime.go:762 的一致性证据检查与 :765-766 的 errSourceStreamDeadEvents 改看 uncontained；:799-800 的 errSourceIngestDeadEvents 改看 health.DeadContained<health.Dead（即 Dead-DeadContained>0）；readySourceStreamAllowedReasons/notReadySourceStreamAllowedReasons（:677-680）改为 init 时从 postgresstore.NonFatalStreamHealthReasons() 派生（后者 ∪ {EVENTS_PENDING}）。readinessProbe.evaluate（cmd/api/readiness.go:98-150）返回 (httpapi.ReadinessOutcome, error)，全部检查通过后若 Ingest.DeadContained>0 则 Degraded=[\"source_ingest_dead_events_contained\"]，并经一个仿 eligibilityProofPendingWarner（runtime.go:636-670）的节流器每 5 分钟打一条 level=ERROR msg=\"readiness degraded\" check=... contained_dead=N。httpapi 侧 Config.Readiness 签名随之改（server.go:55、:77），/readyz 200 body 变为 {\"status\":\"ready\",\"degraded\":[...]}（空时省略），degraded 名称必须通过 readinessCheckNamePattern（httpapi/readiness.go:64-67）否则丢弃并记 rejected——沿用 READYZ-DETAIL 的闭合词汇表纪律。
5) 解冻关卡。ResolveEligibilityFreeze（eligibility_operations.go:218）在 SOURCE_REFUND 短路（:276-281）之后加：EXISTS(SELECT 1 FROM eligibility_freezes ef JOIN source_ingest_events sie ON sie.payload_hash=ef.source_revision_hash WHERE ef.id=$1 AND sie.processing_status='dead') → 返回新哨兵 domain.ErrEligibilityDeadEventUnrepaired（包 ErrInvalidState，与 :379-381 三个同族并列），httpapi handleDomainError（server.go:1247-1260）在泛型 ErrInvalidState 之前映射为 409 ELIGIBILITY_DEAD_EVENT_UNREPAIRED。按 hash 不按 freeze_reason 关卡：EVENT_PAYLOAD_DRIFT 等同 hash 冻结也算兜住（tryPublish 就是这么算的），解掉它同样会让死信失去兜住。
6) 「不被遗忘」的四条线：(a) 冻结队列 GET /api/v1/admin/eligibility-freezes 里那张 EVENT_DEAD 行因关卡 5 无法被「顺手解掉」，只能先修复；(b) readyz 200 body 的 degraded 字段 + 5 分钟一条 Error 日志（仓库没有 prometheus/metrics，运维告警本来就是 grep 日志 + 看 readyz check，PRODUCTION-RUNBOOK.md:2050-2088）；(c) 管理端 source-health（httpapi/operations.go:13）每流 dead_events/contained_dead_events 与 EVENTS_DEAD_CONTAINED 原因；(d) 既有的 source_ingest_event.dead 审计与 \"source event marked dead\" Error 日志不变。
7) 顺带修一个结构性隐患：tryPublish 的 LEFT JOIN 在同一 payload_hash 有两张 open 冻结（例如 EVENT_PAYLOAD_DRIFT + EVENT_DEAD，唯一索引 eligibility_freezes_one_open_trigger 0009:530-532 按 reason 区分所以允许并存）时会把 checkpointRecords 数成 2，撞 :758-761 的 invalidBalanceSnapshot → 整个来源 SOURCE_GAP 冻结。改 EXISTS 后消失。未在生产观察到，标为「结构上可达」。

### 代码改动

- `K:/发票/wt-XM-INV-RC104/backend/internal/postgresstore/source_sync.go`：新增 const sourceEventOpenFreezeSQL 与 const eventsDeadContainedReason="EVENTS_DEAD_CONTAINED"；nonFatalStreamHealthReasons（:1071）加入该原因并新增导出函数 NonFatalStreamHealthReasons() map[string]bool（返回副本）；SourceStreamHealth 加 ContainedDeadEvents int64 `json:"contained_dead_events"`（:112-143），SourceIngestHealth 加 DeadContained int64（:82-87）；SourceIngestHealth()（:1037-1049）、sourceReadinessHealthQuery（:1225-1258，classified 子查询加 dead_contained 布尔列并给 FROM source_ingest_events 起别名 sie）、SourceHealth()（:1338-1361）、assertSourceFreshTx()（:1420-1436）四个查询各加 count FILTER (WHERE processing_status='dead' AND <sourceEventOpenFreezeSQL>) 并 Scan 进新字段；evaluateSourceStreamHealth()（:1119-1124）改为 uncontained>0→EVENTS_DEAD、Contained>0→EVENTS_DEAD_CONTAINED；导出薄包装 EvaluateSourceStreamHealth(item *SourceStreamHealth, policy SourceFreshnessPolicy) 供 cmd/api 做穿两段的链式测试。
  - 为什么：这是放大点本身：四个计数位点与一个判定函数，任何一处漏改就是「闸的判据是别处逻辑的副本」；谓词从周期完整性抽出后两边物理上同源。
- `K:/发票/wt-XM-INV-RC104/backend/internal/postgresstore/consumption.go`：tryPublishEconomicScanCyclesTx() 完整性查询（:718-731）去掉 LEFT JOIN eligibility_freezes ef，FILTER 改为 NOT (sie.processing_status IN ('failed','dead') AND <sourceEventOpenFreezeSQL>)；注释指向 source_sync.go 的单一定义。
  - 为什么：让流级健康与周期完整性共用一份谓词；同时消除同 hash 两张 open 冻结把 checkpointRecords 数成 2 触发 invalidBalanceSnapshot（:758-761）的隐患。
- `K:/发票/wt-XM-INV-RC104/backend/cmd/api/runtime.go`：readySourceStreamAllowedReasons/notReadySourceStreamAllowedReasons（:677-680）改为 init() 里从 postgresstore.NonFatalStreamHealthReasons() 派生（后者再并入 EVENTS_PENDING）；validateSourceRuntimeReadiness()：:762 的 item.DeadEvents != 0 改为 item.DeadEvents-item.ContainedDeadEvents != 0，:765 同；validateSourceIngestRuntimeReadiness()（:799-800）改为 health.Dead-health.DeadContained > 0；新增 shouldWarnContainedDead(health, now, lastAt) 纯函数与 containedDeadWarner（仿 :636-670）。
  - 为什么：规则与调用点是两段代码：不改 :762 的话，Ready=true 且 DeadEvents>0 会被判「inconsistent health evidence」直接 503；派生集合是为了不再造第二份 nonFatal 副本。
- `K:/发票/wt-XM-INV-RC104/backend/cmd/api/readiness.go`：新增 const readinessDegradedSourceIngestDeadContained="source_ingest_dead_events_contained"；readinessProbe 加 containedDead *containedDeadWarner 字段；evaluate()（:98-150）签名改为 (httpapi.ReadinessOutcome, error)，所有检查通过后若 sourceHealth.Ingest.DeadContained>0 则填 Degraded 并调用 warnIfPresent。
  - 为什么：readyz 不隐瞒：已兜住死信不再 503，但必须在 200 上留下可告警的闭合词汇。
- `K:/发票/wt-XM-INV-RC104/backend/internal/httpapi/server.go 与 backend/internal/httpapi/readiness.go`：新增 type ReadinessOutcome struct{ Degraded []string }；Config.Readiness（server.go:77）与 Server.readiness（:55）改为 func(context.Context) (ReadinessOutcome, error)；/readyz 处理器（:176-189）成功时对 Degraded 逐项用 readinessCheckNamePattern 过滤（不合法的丢弃并以 readinessRejectedCheck 记日志），body 写 {"status":"ready","degraded":[...]}（空则省略）；readinessOutcomeLog 增加 degraded 状态：从 degraded 转为干净时打一条 "readiness recovered"。
  - 为什么：运维手册把 readyz 的 check 名当告警键（PRODUCTION-RUNBOOK.md:2054-2088），200 上的 degraded 字段延续这个习惯；名字仍走同一字符集护栏，不可能泄漏 id/主机。
- `K:/发票/wt-XM-INV-RC104/backend/internal/postgresstore/eligibility_operations.go`：ResolveEligibilityFreeze()（:218）在 SOURCE_REFUND 短路（:276-281）后新增查询：该冻结的 source_revision_hash 是否仍对应任一 processing_status='dead' 的 source_ingest_events 行；是则返回 domain.ErrEligibilityDeadEventUnrepaired。
  - 为什么：今天「先修复后解冻」只靠流不就绪偶然成立（:239-250 ErrEligibilitySourceStale）；本方案让流就绪后这个顺序必须由规则自己负责，否则解冻会把死信变回未兜住、流级放大从另一扇门回来。
- `K:/发票/wt-XM-INV-RC104/backend/internal/domain/types.go`：在 :378-381 同族旁新增 ErrEligibilityDeadEventUnrepaired = fmt.Errorf("%w: a dead source event still correlates to this freeze; requeue or acknowledge it first", ErrInvalidState)。
  - 为什么：命名的失败，而不是泛型 ErrInvalidState；types_test.go:53-62 的哨兵族测试需同步加一行。
- `K:/发票/wt-XM-INV-RC104/backend/internal/httpapi/server.go handleDomainError（:1247-1260）`：在 ErrEligibilityProjectionPending 等专有分支之前加 case errors.Is(err, domain.ErrEligibilityDeadEventUnrepaired) → 409 ELIGIBILITY_DEAD_EVENT_UNREPAIRED。
  - 为什么：CR-0007 的顺序纪律：专有哨兵要排在泛型 ErrInvalidState 之前才赢。
- `K:/发票/wt-XM-INV-RC104/backend/migrations/0032_eligibility_freezes_open_revision_index.sql`：CREATE INDEX IF NOT EXISTS eligibility_freezes_open_revision_idx ON eligibility_freezes(source_revision_hash) WHERE status='open' AND source_revision_hash IS NOT NULL；注释仿 0031（只读新增、可先于代码、回滚可留）。
  - 为什么：assertSourceFreshTx 跑在每次 Submit/Confirm 事务里，谓词对每条 dead 行做一次 open 冻结查找；现有索引（0009:530-534、0010:28-32）没有一条以 source_revision_hash 开头。
- `K:/发票/wt-XM-INV-RC104/web/src/App.tsx 与 web/src/lib/http-api.ts`：sourceReasonLabels（App.tsx:5755-5767）加 EVENTS_DEAD_CONTAINED: "存在死信事件（已按账号冻结兜住）"；:6048 显示改为「待处理 N / 死信 M（兜住 K）」；http-api.ts:230 类型加 contained_dead_events?: number（可选，兼容旧服务端），:1769 计数校验与 :1844 映射同步。
  - 为什么：未知原因会落到 "未识别的安全阻断原因"（App.tsx:6061-6062）——不崩但误导；总数/兜住数分开显示是运维看板层面的「不隐瞒」。
- `K:/发票/wt-XM-INV-RC104/docs/PRODUCTION-RUNBOOK.md、docs/ELIGIBILITY-OPERATIONS.md、docs/SOURCE-SYNC-PROTOCOL.md、docs/CONFIGURATION.md`：RUNBOOK :2074-2088 表加一行 degraded=source_ingest_dead_events_contained（含 grep 'readiness degraded' 告警示例）并注明 source_ingest_dead_events/source_stream_dead_events 现只对未兜住死信触发；ELIGIBILITY-OPERATIONS :570-586 改写「重投→解冻」段落为显式关卡与 409 码；PROTOCOL :435-437 与 CONFIGURATION :560-563 的「queued/dead 事件即失败」改为「queued 事件或未兜住的 dead 事件」。
  - 为什么：文档里现在写的是流级 fail-closed 语义，不改会让下一位读者把降级当回归。

### 防住什么
- 同一来源实例下、与死信无关的账号在事件判死之后继续被 ListFundingLots 标 source_unavailable、被 ListUserEligibilitySummaries 标 SOURCE_NOT_READY、被 Submit/ConfirmManualIssue 以 ErrSourceUnavailable 拒绝——前提是死信已被 open 冻结兜住（09-07 的 3 条按 9ca5afcb 能发布这一事实推断都已兜住；需用户用只读查询核实，见 data_integrity_risks 第 1 条）。
- readyz 因已兜住死信而永久 503：容器 healthcheck（docker-compose.prod.yml:313-317）不再被一条已隔离的死信标为 unhealthy；roll-forward.sh:121-122 的 readyz==200 部署门禁不再被它卡住（RC104 这类修复本身不再需要绕过门禁部署）。
- 同一来源上其他账号的冻结无法解（ResolveEligibilityFreeze 的 assertSourceFreshTx :239 → ErrEligibilitySourceStale）——这是今天流级闩的第二个放大面，随 Ready 语义一起收窄。
- 管理员把 EVENT_DEAD（或同 hash 的其它）冻结「顺手解掉」从而让已兜住死信变回未兜住、流级放大从解冻这扇门回来——被新的 ErrEligibilityDeadEventUnrepaired 关卡挡住，且失败有名字。
- 周期完整性与流级健康对「这条死信是否拖住流」给出两个答案——谓词只剩一份 SQL 定义，四个健康查询与 tryPublish 共同渲染。
- 同 payload_hash 两张 open 冻结把 tryPublish 的 checkpointRecords 数成 2、触发 invalidBalanceSnapshot 与全源 SOURCE_GAP 冻结（结构上可达，未在生产观察到）。
- nonFatalStreamHealthReasons 与 runtime.go 的 allowed 集合再次漂开——后者改为派生，配 T3 的变异。

### 防不住什么
- 孤儿事件的产生本身：supersede 把周期置 blocked、绑定过不了 verifyFactBatchContextTx/validateFactMetadata、泛型 PROJECTION_FAILED 8×5 分钟判死——整条链一字未动，事件照样会死、该账号照样冻结、修复仍要人跑 requeue/acknowledge 工具。
- 判死之前的 35 分钟（8 次 × 5 分钟）以及任何投影积压期：EVENTS_PENDING 在 evaluateSourceStreamHealth 里对流级 Ready 仍是致命的（source_sync.go:1119-1120、:1125-1131），且 SourceHealth/assertSourceFreshTx 没有 busy 宽限（:1345、:1427）。09-07 事故 07:08→08:32 那段所有客户被拒的现象本方案完全不解决；这需要错误分级角度（确定性失败第一次就冻结并终止，把 pending 窗口压到 0）或把宽限下沉到流级 Ready，两者都不在本角度。
- 未兜住的死信：MarkSourceEventFailed 只在 payload_hash 关联到已落库事实（source_sync.go:846-856）或应用层给了 hintAccountID（:861-876）时才冻结；解密失败、JSON 失败、payments/identities 流的死信没有冻结 → 仍是 EVENTS_DEAD 流级致命、readyz 仍 503。这是刻意保留的 fail-closed（无法归因就不能按账号隔离），恢复路径仍是两个修复工具；缩小这个集合（例如 funding_lots.source_revision_hash 关联）是另一个切片。
- 未兜住死信经周期完整性→水位停滞→15 分钟后 ECONOMIC_WATERMARK_STALE 的间接放大：同样保留、同样一致（未兜住 = 处处致命）。
- 「不被遗忘」只能落到日志 grep、readyz body、冻结队列与管理端健康页：仓库没有 metrics/pager 基础设施，本方案不发明一个。若运维不看这四处，一条已兜住的死信可以静默数天——但该账号本人自始至终是冻结的，账本不会因此失真。
- 被冻结账号本人的可用性：2222 仍 0 张额度、仍冻结，直到重投成功或写掉并解冻；本方案不改善他的处境。

### 数据完整性风险
- 前提需核实（我不能连生产库，请用户自行跑只读查询）：SELECT sie.stream_id,sie.event_id, EXISTS(SELECT 1 FROM eligibility_freezes ef WHERE ef.source_revision_hash=sie.payload_hash AND ef.status='open') AS contained FROM source_ingest_events sie WHERE sie.processing_status='dead'。若任一条 contained=false，本方案对那条不生效（仍 503、仍全实例不可用），且说明 09-07 的推断有误。
- 兜住判定是活的（每次查询看 ef.status='open'），不是冻在 ingest 行上的缓存：冻结一旦不再 open，同一条死信立即变回未兜住、流级 fail-closed 自动回来。这是刻意选择（避免「被信任的过期闸」），代价是解冻关卡必须存在（code_changes 第 6 项），否则管理员一次解冻就等于把放大面重新打开——虽然方向是安全的（拒绝更多），但不是我们要的行为。
- Submit 事务是 READ COMMITTED（store.go:367），assertSourceFreshTx 读 eligibility_freezes 与后面额度 UPDATE 读 eas.eligibility_status 是两个快照。理论窗口：解冻在两者之间提交。实际不可达：ResolveEligibilityFreeze 自己也调 assertSourceFreshTx（eligibility_operations.go:239）并取同样五把 pg_advisory_xact_lock(hashtextextended(source+stream,5))（source_sync.go:1414），所以解冻与提交在同一来源上互斥；再加上新关卡，dead 期间根本解不了。列在这里是因为它靠两把锁的巧合成立，若有人把 ResolveEligibilityFreeze 的 assertSourceFreshTx 拿掉，这个窗口就真的开了——关卡 6 是它的第二道保险。
- ResolveEligibilityFreeze 是 SERIALIZABLE（:228），新增的 EXISTS 读 source_ingest_events 会在该表上加 SIREAD 谓词锁；与它并发的 MarkSourceEventFailed 是默认隔离级（source_sync.go:790 pool.Begin），不会互相 40001；投影事务（consumption.go:352/474 Serializable）里的 tryPublish 已经在读 eligibility_freezes，改 LEFT JOIN→EXISTS 不改变谓词锁足迹。新增 40001 面：无。
- tryPublish 从 LEFT JOIN 改 EXISTS 对 incomplete!=0 的判定严格等价（任一行有无匹配冻结的真值不变），只改变 manifestRecords/checkpointRecords 在「同 hash 多张 open 冻结」时的计数——从错误的重复计数变为正确计数；不存在让本应 blocked 的周期发布的方向。T7 的变异专门守这一点。
- 幂等/写路径：本切片不写任何业务行；唯一 DDL 是可重复执行的 CREATE INDEX IF NOT EXISTS；关卡 6 只读不写。金额、时间字段均未触碰。
- readyz body 新增字段：degraded 名称经 readinessCheckNamePattern（无数字/点/冒号/斜杠）过滤，无法携带 id、主机或路径。

### 迁移
仅一条只加索引的迁移 0032_eligibility_freezes_open_revision_index.sql：CREATE INDEX IF NOT EXISTS eligibility_freezes_open_revision_idx ON eligibility_freezes(source_revision_hash) WHERE status='open' AND source_revision_hash IS NOT NULL。无新表（不触发开票库 permissions 重放）、无列变更、无数据回填；可先于代码部署、代码回滚后可保留。eligibility_freezes 是小表（open 行为个位到两位数），索引主要保护每次 Submit/Confirm 事务里 assertSourceFreshTx 的五次谓词求值不退化为对 open 冻结的顺序扫描；tryPublish 今天已经在无索引下做同一关联，所以不加索引也正确、只是慢。前端 http-api.ts 的 contained_dead_events 设为可选字段，旧服务端响应仍能解析。

### 测试计划

- **postgresstore/source_sync_test.go：TestContainedDeadIsNonFatalWhileUncontainedDeadStaysFatal（纯单测，表驱动，仿 :77 那个）——(a) DeadEvents=1,Contained=1 → Ready=true，Reasons 恰为 [EVENTS_DEAD_CONTAINED]；(b) DeadEvents=1,Contained=0 → Ready=false，含 EVENTS_DEAD 不含 CONTAINED；(c) DeadEvents=2,Contained=1 → Ready=false 且两个原因都在。旧实现下 (a) 是 Ready=false，所以这不是旧实现也绿的正向断言。**：证明 evaluateSourceStreamHealth 只对未兜住死信 fail-closed，且兜住的仍被报告而非抹掉。
  - 变异必红：把 nonFatalStreamHealthReasons 里的 EVENTS_DEAD_CONTAINED 删掉 → (a) 红；把致命分支改回 item.DeadEvents>0 → (a) 红；把 CONTAINED 追加删掉 → (a)(c) 红（缺席型：证明原因不是被吞掉）。
- **postgresstore/source_readiness_integration_test.go 扩展 + 新 TestDeadEventContainmentIsConsultedByEveryHealthSurface（真库）——插 1 条 dead 事件 + 1 张 status='open'、source_revision_hash=payload_hash 的 EVENT_DEAD 冻结；对四个面逐一断言：SourceReadinessHealth（Ingest.Dead=1, Ingest.DeadContained=1, item.Ready=true, Reasons=[EVENTS_DEAD_CONTAINED]）、SourceHealth（同）、assertSourceFreshTx 返回 nil、SourceIngestHealth（Dead=1, DeadContained=1）；然后 UPDATE 冻结 status='resolved' 再跑四个面，全部翻回致命（Ready=false/EVENTS_DEAD/ErrSourceUnavailable）；保留原有 EXPLAIN 断言 source_ingest_events_readiness_active_idx 与无 seq scan。**：证明 四个计数位点都消费同一谓词，且判定是活的（冻结不 open 立即回到 fail-closed）；readiness 查询仍索引受限。
  - 变异必红：任选一个查询把 `AND <sourceEventOpenFreezeSQL>` 从 contained 计数里去掉 → 只有那个面的断言红（四个面各自独立断言，缺一面就发现不了）；把谓词里的 ef.status='open' 去掉 → 第二阶段（resolved 后应翻回致命）红；DROP INDEX source_ingest_events_readiness_active_idx → EXPLAIN 断言红（已有）。
- **cmd/api/main_test.go：TestReadyzAcceptsContainedDeadAcrossRuleAndConsumer（穿两段）——构造 SourceStreamHealth{DeadEvents:1,ContainedDeadEvents:1,其余健康}，先经 postgresstore.EvaluateSourceStreamHealth（规则），再把结果原样送进 validateSourceRuntimeReadiness（调用点）→ nil；同一报告加 PendingEvents=1 → Ready=false 且 Reasons=[EVENTS_DEAD_CONTAINED,EVENTS_PENDING] → 仍 nil（与 EVENTS_PENDING 单独容忍一致）；readinessProbe.evaluate 用 Ingest{Dead:1,DeadContained:1} → 无错误且 outcome.Degraded==["source_ingest_dead_events_contained"]。**：证明 规则（postgresstore）与消费者（cmd/api）对新原因的一致；readyz 200 但不隐瞒。
  - 变异必红：runtime.go:762 保留 item.DeadEvents != 0 → 「inconsistent health evidence」红；readySourceStreamAllowedReasons 改回字面量 {ECONOMIC_RESCAN_ACTIVE:true} → 红（这就是「别造第二份副本」的守卫）；validateSourceIngestRuntimeReadiness 改回 health.Dead>0 → 红；evaluate 不填 Degraded → Degraded 断言红。
- **cmd/api/readiness_test.go 扩展 TestReadyzStillFailsClosedOnUncontainedDead——Ingest{Dead:2,DeadContained:1} → check=source_ingest_dead_events；流项 DeadEvents=1,Contained=0,Ready=false → check=source_stream_dead_events；并保留 :154-165、:208-220 两个既有用例（改为 Contained=0 明示）。**：证明 未兜住死信的 fail-closed 与 READYZ-DETAIL 的两个 check 名一字不变。
  - 变异必红：把 :799 改成 health.DeadContained>0（判反）→ 第一段红；把 :765 的减法改成只看 ContainedDeadEvents → 第二段红。
- **application/rescan_grace_integration_test.go 同款新测试 TestContainedDeadEventLeavesOtherAccountsInvoiceable（真库）——同一来源两个账号 A、B 各有额度；让 A 的一条 usage 事件真正经 RunOnce 判死（复用 service_integration_test.go:163-307 的单位不匹配路径，使 EVENT_DEAD 冻结经 hint 开出）；断言 ListFundingLots(B) 不是 source_unavailable、Submit(B) 成功走过 assertSourceFreshTx；ListFundingLots(A) 为 frozen、Submit(A) 因额度 UPDATE 的 eas.eligibility_status='active' 谓词失败；ListUserEligibilitySummaries(B) 无 SOURCE_NOT_READY、(A) 有 ACCOUNT_FROZEN。旧实现下 B 的三处全是不可用 → 红。**：证明 放大面确实从实例级降到账号级，且账号级保护（冻结 + 额度谓词）独立成立、不是靠流闩。
  - 变异必红：assertSourceFreshTx 的谓词去掉 → Submit(B) 红；SourceHealth 的谓词去掉 → ListFundingLots(B)/Summaries(B) 红；把 A 的冻结手动 resolved 后再跑 → B 三处必须重新变为不可用（fail-closed 回归），若判定被改成缓存列 → 这一步红。
- **postgresstore/eligibility_operations_integration_test（新）TestResolveEligibilityFreezeRefusesWhileACorrelatedEventIsStillDead——EVENT_DEAD 冻结 + 同 hash dead 事件 → Resolve 返回 errors.Is(err, domain.ErrEligibilityDeadEventUnrepaired)；变体：只有 EVENT_PAYLOAD_DRIFT 冻结（同 hash）也被拒；跑 AcknowledgeUnreplayableIngestEvent（或直接把事件置 processed）后再 Resolve，错误不再是该哨兵（可以是 ErrEligibilityEvaluationUnmatched 等下游关卡）。httpapi/eligibility_operations_test.go:116 表加一行 → 409 ELIGIBILITY_DEAD_EVENT_UNREPAIRED；domain/types_test.go 哨兵族加一行。**：证明 「先修复后解冻」由规则自己负责，不再靠流不就绪偶然成立；关卡按 hash 不按 reason。
  - 变异必红：删掉关卡 → 第一断言红；关卡改成 freeze_reason='EVENT_DEAD' 才查 → DRIFT 变体红；关卡漏掉 processing_status='dead' 条件（任何事件都拒）→ acknowledge 后那段红。
- **postgresstore/policy_anchor_integration_test.go 扩展 TestScanCycleFrozenDeadEventPublishesWhilePureTransientDeadEventHoldsCycle（:505）——新增 balances 流用例：一条 balance_checkpoint 事件 dead，同 payload_hash 同时有 EVENT_PAYLOAD_DRIFT 与 EVENT_DEAD 两张 open 冻结，周期 scan_snapshot_row_count=1 → 周期必须 published 而非 blocked，且不产生 SOURCE_GAP 冻结。旧实现（LEFT JOIN）下 checkpointRecords=2≠1 → blocked → 红。**：证明 tryPublish 与流级健康共用的 EXISTS 谓词在完整性判定上等价，并消除重复计数隐患。
  - 变异必红：把 tryPublish 改回 LEFT JOIN ... ef.id IS NOT NULL → 红；把共享片段的 ef.status='open' 去掉并让测试里 DRIFT 冻结为 resolved、EVENT_DEAD 不存在 → 原有「纯瞬时死信拖住周期」用例红。
- **cmd/api/runtime_test.go：TestShouldWarnContainedDeadRateLimits（纯函数表驱动，仿 shouldWarnProofPending）——DeadContained=0 → 不告；=1 且 lastAt 零 → 告；4m59s 后 → 不告；5m 后 → 告。**：证明 已兜住死信在日志里每 5 分钟出现一次、既不刷屏也不沉默。
  - 变异必红：把 >= 改成 > 或把 interval 改成 0 → 边界用例红；把 DeadContained<=0 的早退去掉 → 第一用例红。
- **httpapi/readiness_test.go 扩展——Readiness 返回 Degraded=["source_ingest_dead_events_contained"] → 200 且 body.degraded 相等；返回 Degraded=["bad:name/1"] → 200、body 无 degraded、日志 check=rejected_check_name；Degraded 为空 → body 没有 degraded 键（缺席型：用 json 解码到 map 断言键不存在）。**：证明 200 路径的闭合词汇护栏与 503 路径一致。
  - 变异必红：处理器不过滤 pattern → 第二用例红；空时也写 "degraded":[] → 第三用例红（这个缺席断言靠第二用例的存在证明不是恒真）。

**规模** M。后端实现约 200-250 行（source_sync.go 约 90、consumption.go 约 10、runtime.go/readiness.go 约 60、httpapi 约 40、eligibility_operations.go+domain 约 25）；测试约 450-550 行（9 组）；迁移 1 个只加索引；前端 <15 行；文档 4 处约 40 行。可拆两刀：第一刀（谓词+四查询+evaluate+runtime 两处判定+派生集合+解冻关卡+索引）就已把放大面收掉且 readyz 不再 503；第二刀（ReadinessOutcome/degraded body/节流日志/前端/文档）是「不隐瞒」的可观测性，可以随后合。 · **依赖** 部署前由用户核实生产 3 条死信都被 open 冻结兜住（data_integrity_risks 第 1 条的只读查询）；否则本方案对那几条不起作用，需先跑 ingest-acknowledge-unreplayable/requeue。, 不依赖其他角度即可独立合入；但收益与其它角度互补而非替代：错误分级角度决定判死前 pending 窗口有多长（本方案不碰 EVENTS_PENDING）；孤儿事件根因角度决定还会不会有新死信；hint 覆盖面（MarkSourceEventFailed :861-876）决定「未兜住」集合有多大。, XM-INV-READYZ-DETAIL 的闭合词汇护栏（httpapi/readiness.go:64-67）与 check 名不变——本方案复用而非替换。, 不要求改 Sub2API/NewAPI；不要求改 agents/；不改任何冻结的协议字段。 · **回滚** 纯代码回滚：revert 提交即可。索引 eligibility_freezes_open_revision_idx 可保留（只读附加，与 0031 同性质）。回滚方向是「拒绝更多」：已兜住死信重新变为流级致命、readyz 回到 503、ResolveEligibilityFreeze 回到靠 ErrEligibilitySourceStale 偶然挡住——没有任何数据需要回退：本切片不写业务行，在收窄期间为其他账号签发的发票其事实链本就完整（死信只属于被冻结的那个账号），无需作废。前端字段是可选的，服务端先回滚前端不崩；服务端先升前端未升则 EVENTS_DEAD_CONTAINED 显示为「未识别的安全阻断原因」但功能正常。若只回滚第二刀（readyz body），第一刀独立成立。


### 审稿：复发与运维：把 2222 绑定 + 例行对账重叠（40001 已修）在改后代码上 — refuted=False would_ship=True

- [major] **重放到「判死之后」这一段。改后：孤儿在 verifyFactBatchContextTx 拒绝 blocked 周期（consumption.go:1024-1027）→ ErrConflict 被 wrapWithAccountHint 包上账号（source_processor.go:679/711/759）→ 8 次判死 → hint 路径开 EVENT_DEAD 冻结、source_revision_hash=claim.PayloadHash（source_sync.go:861-880）→ 兜住 → 流 Ready、readyz 200 degraded、另 6 个用户能开票。这一步成立。但接下来运维按 RUNBOOK:2074 那行跑 ingest-requeue-dead：工具的 ReplayBlocked 只看绑定存在/哈希一致/周期状态（ingest_requeue_dead_repair.go:514-527），不看 validateFactMetadata 的 watermarkAt>observed_at+5min（consumption.go:324-325），于是判「可重投」→ attempt_count=0 置 queued（:427-431）→ 第一次就因水位非法失败 → 8×5 分钟 EVENTS_PENDING 对流级 Ready 仍致命（source_sync.go:1119-1131，SourceHealth/assertSourceFreshTx 无 busy 宽限）→ 全实例再次不可开票约 40 分钟，readyz 因 created_at 早于 15 分钟直接 503 source_ingest（runtime.go:808）→ 再判死 → ON CONFLICT 复用同一冻结 → 又兜住。ingest-acknowledge-unreplayable 因 ReplayBlocked=false 拒绝写掉（ingest_unreplayable_acknowledge.go:126-129）。新关卡 5 又让 ResolveEligibilityFreeze 拒绝。终态：该账号永久冻结、readyz 永久 degraded、每 5 分钟一条 Error 永久打，且每一次按手册修复都是一次自伤的 40 分钟全实例停摆。**
  - 方案把关卡 5 的正当性建立在「只有两个修复工具能让它可解」上，但对生产此刻这 2 条 usage 死信（以及每次重放必然产生的同类孤儿——CLAIM-BINDING 重绑到新周期后 scan_ceiling_at 必然比原 observed_at 晚超过 5 分钟）两个工具一个误判一个拒绝，恢复路径实际不存在。does_not_prevent 写的「静默数天」实为无界；depends_on 未把修工具列为前置。这正是「引入 fail-closed 却没有恢复路径」——虽然对该账号的结果与今天一样（今天靠 ErrEligibilitySourceStale 也解不了），但方案把偶然变成了规则，并且把「修复动作会引发全实例停摆」这一点留给运维踩。
  - 依据：ingest_requeue_dead_repair.go:514-527、:427-431；ingest_unreplayable_acknowledge.go:126-129；consumption.go:324-325、:1024-1027；source_processor.go:262、:679、:711、:759；source_sync.go:861-880、:1119-1131；runtime.go:808；docs/PRODUCTION-RUNBOOK.md:2074 与 ELIGIBILITY-OPERATIONS.md:568-586（「created_at 不动，readyz 会以另一原因 503」）
- [minor] **runtime.go:762 的一致性证据检查从 item.DeadEvents!=0 改成 uncontained!=0 之后，一条 Ready=true、ContainedDeadEvents=1 但 Reasons 里没有 EVENTS_DEAD_CONTAINED 的流会静默通过 readyz——今天这个检查的作用正是抓「有死信却 Ready」这种自相矛盾的报告。**
  - 方案在 T1 用 postgresstore 单测守「原因不被吞掉」，但 cmd/api 这一层的诚实闸被同时放松了；两段代码是两处，规则那边红不代表消费者这边会拒。应保留「Contained>0 ⇒ Reasons 必含 EVENTS_DEAD_CONTAINED」的断言。
  - 依据：runtime.go:757-763（现有 if item.PendingEvents != 0 || item.DeadEvents != 0 || !reasonsWithinSet(...)）；方案 code_changes 第 3 项
- [minor] **测试计划的变异描述有两处对不上。T4 第一条：把 :799 改成 health.DeadContained>0，用 Ingest{Dead:2,DeadContained:1} 跑——变异后谓词仍为真、仍返回 errSourceIngestDeadEvents、check 仍是 source_ingest_dead_events，T4 照样绿；真正能抓它的是 T3 里 {Dead:1,DeadContained:1}→nil。T2「去掉某一面的 AND <谓词> → 只有那个面的断言红」：去掉后 contained==dead==1，第一阶段（期望 contained=1、Ready）照样绿，只有第二阶段（冻结 resolved 后应翻回致命）才红。**
  - 两条都不是恒真，但归因写错了；按方案的自述去做变异验证，会得出「已验证」的假结论。缺席型/反向断言必须写明是哪一句把它变红。
  - 依据：runtime.go:799-801；方案 test_plan 第 4、2 条；readiness_test.go:154-165 现有用例只设 Ingest.Dead=1
- [minor] **重放的收益是按「整个事故」全有或全无：同一次对账重叠产生的孤儿里只要有一条拿不到 hint——解密失败、strictJSON 失败、verifiedExternalAccount 失败、payments/identities/cutover_manifest/subscription_purchase 这些没有 wrapWithAccountHint 的实体（只有三处包 hint）——它就是未兜住死信，整条流回到 EVENTS_DEAD 致命，方案对那次事故等于没生效。**
  - 方案在 does_not_prevent 里写了 payments/identities，但没把「一条未兜住就整体失效」的全有或全无性质说清；下一次复发能否受益取决于孤儿的实体类型分布，不是确定的。
  - 依据：source_processor.go:325-363（dispatch）、:679/:711/:759（仅三处 hint）；source_sync.go:846-880（无 hint 且无落库事实则不冻结）

**签字条件**：
- 把修复工具补齐列为硬前置或同切片合入：ingestRequeueDeadReplayBindingTx（ingest_requeue_dead_repair.go:483-527）的 ReplayBlocked 必须额外套用 validateFactMetadata 的水位规则（replay 绑定所在批次的 scan_ceiling_at 不得晚于 sie.observed_at+5 分钟），使 ingest-requeue-dead 拒绝重投、ingest-acknowledge-unreplayable 允许写掉生产这 2 条 usage 事件；否则关卡 5 是一道没有钥匙的锁。
- RUNBOOK 新增的 source_ingest_dead_events_contained 一行不得直接指向 ingest-requeue-dead；要写明：先 dry-run 看 replay_binding 与水位判定，重投一条兜住的死信意味着该来源实例约 40 分钟 EVENTS_PENDING 全实例不可开票且 readyz 503 source_ingest。
- 部署前由用户跑方案 data_integrity_risks 第 1 条的只读查询，确认 3 条死信 contained=true；任一为 false 先走写掉/重投再上线。
- 保留 runtime.go:762 的诚实闸：Ready=true 且 ContainedDeadEvents>0 时 Reasons 必须含 EVENTS_DEAD_CONTAINED，否则仍报 inconsistent health evidence，并在 T3 加对应变异。
- 修正测试计划的变异归因：T4 第一条改用 Ingest{Dead:1,DeadContained:1}→nil（或明确写「由 T3 守」）；T2 写明是「resolved 后翻回致命」那一段把去掉谓词的变异变红。

**残余风险**：
- 前提未核实：生产 3 条死信是否都有 open 冻结且 source_revision_hash=payload_hash，只能由用户跑方案给的只读查询；若 balance_checkpoint 那条不在任何已发布周期里，「9ca5afcb 能发布」的推断不覆盖它。
- 判死前的 ~40 分钟 pending 窗口每次复发都会全实例停摆一次（EVENTS_PENDING 对 ListFundingLots/Submit 的五流 AND 仍致命，SourceHealth/assertSourceFreshTx 无 busy 宽限）；方案明说不管，但意味着「新客户绑定 + 例行对账」这个常规操作仍必然带来一次全实例停摆，只是从 24 小时缩到 40 分钟。
- 关卡 5 没有任何受控旁路：一旦修复工具对某类事件误判（如现在的水位规则），该账号只能靠直连库改 SQL 解冻，而仓库纪律是不允许这么做的。
- 「不被遗忘」的实现是每 5 分钟一条永远打的 Error 加 readyz 200 上的 degraded 字段；在恢复路径缺失的情况下它会退化成噪声底，运维学会忽略之后，第二个被兜住的账号只是让 contained_dead 从 1 变 2，不会触发新的注意；唯一可靠的「看见」是管理员冻结队列。
- 四个健康查询是手工各加一段 FILTER，谓词虽然只剩一份 SQL 片段，但「每个查询都渲染了它」没有编译期保证，只靠 T2 的四面各自断言；未来新增第五个健康面时同样会漏。
- tryPublish 从 LEFT JOIN 改 EXISTS 对 incomplete 严格等价我复核了（任一行有无匹配冻结的真值不变），但 manifestRecords/checkpointRecords 从「可能虚高」变「准确」的方向只会让更多周期正确 blocked 或正确 published，没有反向风险；仍建议 T7 同时覆盖 manifestRecords 这一支。

### 审稿：数据完整性与财务正确性（重复计数/漏计/时间倒流/跨周期串账/Serializa — refuted=False would_ship=True

- [major] **已兜住的死信是 balance_checkpoint、且它所在周期发布（本方案第 7 项把 DRIFT+DEAD 双冻结从「blocked+全源 SOURCE_GAP」改成发布后，这一形状新增可达）。被冻结账号 A 仍会被 finalizeSourceAccountsTx 排进投影作业（targets 只排除 syncing/catchup，不排除 frozen），prepareEligibilityProjectionJob 在无锁半程里跑 ensureBalanceCarryForwardProofTx，对该周期算出 has_real_checkpoint=false → 为 (A, cycle) 写入一条不可变的 carry-forward 证明（「余额自上一检查点未变」）。之后无论 requeue 是否修好绑定，真实检查点 INSERT 都被触发器 reject_real_checkpoint_after_carry_forward 永久拒绝——事件再次判死，A 的真实余额在该周期永远进不了账本。**
  - 影响面只限被冻结的 A（其他账号不受影响，评估器会因 expected≠actual 保持 A 冻结，所以不会开错票），且这是 POLICY-ANCHOR 2.5 已有的行为、投影作业本来就不看流就绪——但方案 rollback 段写的「账本不会因此失真」「修复仍是 requeue/acknowledge」对这一形状不成立：requeue 永远失败，而 acknowledge 的 ReplayBlocked 护栏只看周期状态、会把它当可重投而拒绝写掉，账号进入死锁。第 7 项把今天「响亮的全源停摆」换成「安静的单账号永久缺口」，方向是合理的，但必须在文档里承认，并给出兜底。
  - 依据：consumption.go:579-586 targets 不排除 frozen；:3305-3311 prepare 半程对任何账号跑 ensureBalanceCarryForwardProofTx；:3637-3653 has_real_checkpoint 只看已落库的 balance_reconciliation_checkpoints；:3720-3733 INSERT balance_carry_forward_proofs；0014_balance_carry_forward_proof.sql:155-176 reject_real_checkpoint_after_carry_forward、:178-179 immutable 触发器；投影路径无 assertSourceFreshTx/SourceHealth（grep consumption.go 仅 975/990/1150 的 ErrSourceUnavailable 属 cutover）；ingest_unreplayable_acknowledge.go:128-131 护栏。
- [minor] **解冻关卡（第 5 项）的 EXISTS 走 ef→sie 方向按 payload_hash 查 source_ingest_events；该表没有任何以 payload_hash 开头的索引（仅 unprocessed_idx / source_pending_idx / dependency_wait_idx / readiness_active_idx(source,stream,status,created_at) 部分索引）。ResolveEligibilityFreeze 是 SERIALIZABLE，若规划器选 Seq Scan 会对整表加关系级 SIREAD 谓词锁。**
  - 「新增 40001 面：无」略过于绝对：与它并发的 Serializable 写者只有 requeue/acknowledge 两个修复工具（MarkSourceEvent*/CommitSourceBatch/Claim 都是默认隔离，pool.Begin），所以实际 40001 概率极低，但不是零；且 40001 在 Resolve 的 HTTP 路径会落到 handleDomainError 的 default 分支变成未分类 500。另 source_ingest_events 大到 0013 要求 CONCURRENTLY 预建索引，方案不能顺手在迁移里对它 CREATE INDEX。另：方案说 474 行是 tryPublish 的投影事务，实际 474 是 AdvanceSourceEconomicWatermark、不调用 tryPublish；tryPublish 的 Serializable 调用方只有 RegisterCutoverManifest（352→436）。
  - 依据：eligibility_operations.go:228 Serializable；0004:252-253、0005:56-57、0009:109-110、0013:76-78 四个索引定义；source_sync.go:296/790/737-740/957 默认隔离；ingest_requeue_dead_repair.go:374、ingest_unreplayable_acknowledge.go:88 Serializable；server.go:1258-1270 default 分支；consumption.go:436 与 source_sync.go:470/756/886/940 为 tryPublish 全部调用点。
- [minor] **「不隐瞒」的第 4 项每 5 分钟打一条 level=ERROR 「readiness degraded」。一条已兜住、且因 requeue/acknowledge 都不可用而长期停留的死信（正是当前生产 2222 的两条 usage）会每天新增 ~288 条 ERROR，永久触发运维按 ` ERROR ` 前缀盯守的通用错误计数。**
  - 这把方案要消除的「一条死信放大成全实例告警」从 readyz 搬到了告警通道，且与仓库既有惯例不一致：同类的 eligibilityProofPendingWarner 用的是 slog.Warn。不是账本风险，但会让『降级』在运维面上重新变成『事故』。
  - 依据：runtime.go:659-669 warnIfStale 用 slog.Warn；httpapi/readiness.go:110-115 readinessFailureLogInterval 同为 5 分钟；记忆条目「盯守通用错误计数要匹配 ` ERROR ` 前缀」。
- [minor] **RepairPreAnchorUsageEligibility 用直接 SQL 把 EVENT_DEAD 冻结置 resolved，绕过新的 ResolveEligibilityFreeze 关卡；同一事务里它把对应事件 requeue 成 queued。**
  - 方向安全：事件回到 pending → EVENTS_PENDING 对五流 AND/assertSourceFreshTx 仍致命，不会出现「死信未兜住且流就绪」；但方案第 6 项「只有两个修复工具能让它可解」的叙述漏了这扇门，文档要写全，否则下一位读者会以为关卡是唯一入口。
  - 依据：eligibility_repair.go:244-248 requeue、:364-368 直接 resolved；source_sync.go:1118-1120 EVENTS_PENDING 致命、:1345/:1427 无 busy 宽限。
- [minor] **deploy/postgres/verify-source-readiness-index.sh 内嵌了一份 sourceReadinessHealthQuery 的副本供生产 EXPLAIN 验证；它已经落后于当前查询（没有 busy_within_grace 的 classified 子查询、没有 sesc 联接），本方案再加 dead_contained 列后差距更大。**
  - 这是「被信任的过期闸」形状：脚本验证的是一条已不再运行的查询。属既有欠账、非本方案造成，但方案改到同一条查询时应一并处理或明确标注脚本已过期。
  - 依据：deploy/postgres/verify-source-readiness-index.sh:192-221 vs source_sync.go:1225-1258。
- [minor] **tryPublish 与四处健康查询共用的谓词 `ef.source_revision_hash=sie.payload_hash AND ef.status='open'` 不带账号也不带来源/流，仅靠 64 位内容哈希唯一性把冻结与死信绑在一起。**
  - 结构上与今天 tryPublish、MarkSourceEventFailed 的关联、requeue 工具的 --account 过滤一致，冻结的 source_revision_hash 在所有能匹配死信的写入点都等于触发载荷的 payload_hash，且载荷内含 external_user_id、跨账号碰撞需字节级相同，实际不可达；列出是因为方案把它从「周期完整性」抬到「放开 Submit 的闸」，赌注变大了，测试 T2/T5 应加一条「另一账号同哈希冻结不算兜住」的负向断言以固定语义（当前实现下会被算作兜住）。
  - 依据：consumption.go:2372-2380 freezeEligibilityTx(revision)；source_sync.go:878-881 EVENT_DEAD revision=claim.PayloadHash；source_processor.go:660-667/697-704/745-752 SourceRevision: claim.PayloadHash；ingest_requeue_dead_repair.go:318-331 同一关联。

**签字条件**：
- 合入前用户跑方案 data_integrity_risks 第 1 条的只读查询，确认三条死信 contained=true；否则先处理未兜住的那条。
- 对「已兜住的 balance_checkpoint 死信落在已发布周期」这一形状二选一并写进 ELIGIBILITY-OPERATIONS/RUNBOOK：(a) 在 ensureBalanceCarryForwardProofTx 的 has_real_checkpoint 里把「本周期映射到该账号 open 冻结所指死信」也视为已有检查点（返回 errBalanceCarryForwardProofPending，让冻结账号的投影挂起而不是写入不可变的假证明），或 (b) 明文承认该账号在该周期的真实余额永久不可重投、唯一出路是 acknowledge，并修 AcknowledgeUnreplayableIngestEvent 的 ReplayBlocked 护栏使其对这种事件能写掉。删掉 rollback 段「账本不会因此失真」的绝对表述。
- 解冻关卡的查询写成 `sie.processing_status='dead' AND sie.payload_hash=ef.source_revision_hash`，并在 T6 里加 EXPLAIN 断言它走 source_ingest_events_readiness_active_idx 的部分索引扫描而非 Seq Scan on source_ingest_events；不要在迁移里对 source_ingest_events 建新索引。
- 「readiness degraded」节流日志改 Warn 级（与 eligibilityProofPendingWarner 一致），或在 RUNBOOK 的通用错误计数盯守里显式排除该 msg；两者择一并写进 :2074-2088 那张表。
- 文档第 6 项补上 RepairPreAnchorUsageEligibility 这扇直接 resolved 的门（eligibility_repair.go:364-368），说明它同事务 requeue 因而方向安全。
- T2 或 T5 增加负向用例：同 payload_hash 的 open 冻结若在另一账号名下（人为构造），当前语义下也算兜住——用断言把这一语义钉死并在注释里说明依赖内容哈希唯一性，避免日后有人把它当 bug 改成账号匹配而不知会 tryPublish。
- 顺手更新或明确标注 deploy/postgres/verify-source-readiness-index.sh:192-221 的查询副本已过期；修正方案文本里对 consumption.go:474 的误引（474 不调用 tryPublish）。
- 合入前重新核对迁移编号 0032 未被其他在飞分支占用。

**残余风险**：
- 前提未经我核实（我不能连生产库）：09-07 三条死信是否都被 open 冻结兜住。方案已把只读核查列为 depends_on 第 1 条；若任一条 contained=false，本方案对它无效且推断链有误。
- 那两条 usage 死信目前 requeue 必失败（重投绑定水位非法）、acknowledge 又被 ReplayBlocked 护栏拒绝：本方案落地后它们会作为「已兜住」无限期停留——readyz 永久 degraded、2222 永久冻结——直到工具本身被修。这是运维缺口而非账本风险，但方案的「恢复路径仍是两个修复工具」在当下并不成立。
- 已兜住死信为 usage/credit 时：finalized_through 会越过缺失事实的 event_time；日后重投成功走「迟到事实→重投影」路径可以吸收（LATE_FINALIZED_EVENT 只在额度低于已开票时触发），我判断可恢复，但没有对这条路径跑过变异验证。
- Submit 是 READ COMMITTED：assertSourceFreshTx 读到的健康快照与后面额度 UPDATE 读到的 eas 状态是两个快照；冻结（MarkSourceEventFailed，默认隔离，不取五把流锁）可以在两者之间提交。方向安全（UPDATE 会因 eas 已 frozen 失败），且与今天相同；但反向（Submit 的预留未提交时冻结做 invalidateAccountReservations）留下一条冻结账号带预留的窗口，Confirm 再查 eas 才挡住——既有行为，未被本方案改变。
- T2 在真库上要证明 count(*) FILTER (WHERE ... AND EXISTS(相关子查询)) 在 SourceHealth/assertSourceFreshTx 的 LEFT JOIN+GROUP BY 形状里能被 PostgreSQL 接受并走 0032 索引；我对语法可行有把握，对规划器是否对 eligibility_freezes 走索引没把握（小表可能 Seq Scan，既有 EXPLAIN 断言只盯 source_ingest_events，不会误红）。
- 本仓库同时有 50+ 条 ai/claude 分支在飞；我抽查的 5 条都停在 0031，但 0032 编号仍可能被其他切片先占（记忆：迁移版本号是指针不是集合），合入前要再核。

---

## 提案：XM-INV-CYCLE-HANDOVER：被 supersede 周期上的事件在后端交接到后继周期（原批次原水位）

**角度**：后端侧——被放弃周期上的事件要能安全迁移到后继周期并真正落地（不依赖代理重送、不改验证器的信任语义、不引入新闩）

核心判断：孤儿事件的正确水位是它**原批次**的 scan_ceiling_at，而不是任何新批次的。验证器 verifyFactBatchContextTx（consumption.go:1008-1028）按 (event_id,batch_id,scan_cycle_id) 三元组查映射，只要求 b.scan_ceiling_at == 传入水位、m.payload_hash == revision、c.cycle_status ∈ {receiving,processing,published}——它**不要求 b.scan_cycle_id == m.scan_cycle_id**。因此后端在 supersede 时可以为旧周期 C 上每条未完成事件写一条新映射 (C', E, **原 batch B**, 原 payload_hash)：B 仍是代理签名过的真实批次，其 ceiling T、sequence、signing_key 都是原值，validateFactMetadata 的 T ≤ observed_at+5min 与首送时一样成立（consumption.go:322-323），事实落地时 stream_watermark_at=T、observed_at=首送观测——**零时间倒流、零伪造观测**；唯一"新"的信息是"C' 对 E 负责"，这是接收端的完整性记账决定，用新列 migrated_from_scan_cycle_id + 审计事件公开。这同时修正了 C' 的完整性语义：tryPublish（consumption.go:729-741）按映射行计数，C' 在继承的事件落地前不再发布水位。对代理真实重送的情况，映射行新增 observed_at 列记录第二次观测时间，认领时用 COALESCE(m.observed_at, sie.observed_at)，并把 5 分钟判据（同一 Go 常量渲染成 SQL）加进 claimBindingSelect 臂 1 的 WHERE，使"当前有效绑定"的定义与运行时校验完全一致；修复工具复用同一段 SQL 与同一常量，两条生产死信可用 `ingest-handover-superseded`（沿 superseded_by 链交接）+ 既有 `ingest-requeue-dead` 落地。balances 流不可迁移（快照行数硬校验 consumption.go:759-761 + 检查点绑定自身快照 :1111-1118），按结构性不可重投处置，作为需负责人签字的决策项单列。

### 机制

**A. 交接（runtime，CommitSourceBatch 内）**
1. supersedeStaleActiveScanCycleTx（source_sync.go:528-580）改为返回被 supersede 的 cycle id（无则 ""），行为其余不变。
2. CommitSourceBatch 在事件循环（:411-446）**之后**、UPDATE source_ingest_state（:448）之前调用新函数 handoverSupersededScanCycleEventsTx(ctx, tx, source, stream, from=C, to=in.ScanCycleID, mode=runtime, actor)。放在循环之后是为了：若代理在这个批次里本身重送了 E（reconcile 从 baseline 重扫时常见），代理自己的 (C',E,B') 行先落，交接的 INSERT 在 PK (source,stream,C',E) 上 ON CONFLICT DO NOTHING 让位——"代理最新陈述优先"与 CLAIM-BINDING 的原则一致（source_sync.go:601-603）。C' 的周期行此时已 INSERT（:365-372），FK 立即可解析，不需要 0022 那种 DEFERRABLE。
3. 交接 SQL（原批次、原哈希、原观测时间，只换周期 id）：
```sql
INSERT INTO source_economic_scan_cycle_events(
  source_instance_id,stream_id,scan_cycle_id,event_id,batch_id,payload_hash,observed_at,migrated_from_scan_cycle_id)
SELECT m.source_instance_id,m.stream_id,$to::uuid,m.event_id,m.batch_id,m.payload_hash,
       COALESCE(m.observed_at,sie.observed_at),m.scan_cycle_id
FROM source_economic_scan_cycle_events m
JOIN source_ingest_events sie ON sie.source_instance_id=m.source_instance_id
  AND sie.stream_id=m.stream_id AND sie.event_id=m.event_id
WHERE m.source_instance_id=$1 AND m.stream_id=$2 AND m.scan_cycle_id=$from::uuid
  AND ( $2='payments' OR sie.processing_status NOT IN ('processed', <runtime: 'dead'>) )
ON CONFLICT(source_instance_id,stream_id,scan_cycle_id,event_id) DO NOTHING
```
   流策略（表驱动、每流一条测试）：usage/credits 迁未完成行（queued/failed/processing/waiting_dependency/parked_identity；runtime 模式**不迁 dead**，repair 模式迁）；payments 迁全部行——已处理的支付事件其额度的结转证明要求映射到 published 周期（consumption.go:3480-3506 `cycle.cycle_status='published'`），留在 blocked 周期上会永久 errBalanceCarryForwardProofInvalid；balances **一行不迁**（见 D）。不迁 dead 的原因：dead 且无冻结的事件会永久吊住 C'（consumption.go:731-732），C' 37 分钟后又被 supersede，若再继承就形成逐周期级联；排除后级联止于一跳，dead 行留在 C 上等操作员用 repair 模式显式交接。
4. 写审计 `source.scan_cycle.events_handed_over`（from,to,rows_migrated,rows_skipped_processed,stream_policy），与 :562-575 的 supersede 审计同事务。
5. 并发：CommitSourceBatch 是默认隔离（source_sync.go:296 `pool.Begin`），持有 (source,stream) advisory xact lock（:301）并对 C 行 FOR UPDATE（:533-536）。投影 worker 的 Serializable 事务对 C 行 FOR SHARE（consumption.go:1017）/tryPublish FOR UPDATE（:691）→ 行锁串行化：worker 若已持 FOR SHARE，我们的 UPDATE C 等它提交，其事实在"C 仍 processing"时落地，之后我们交接时它还是 'processing' 状态（MarkProcessed 是另一事务）→ 多一条无害映射。worker 侧若因读写依赖收到 40001 → 已由 SER-BUSY 接住（source_processor.go:221-237）。我们自身不会 40001。幂等：PK 冲突 DO NOTHING；批次重放走 Duplicate 分支（:325-333）不再进入。
6. 认领：交接行满足臂 1 全部条件（schema 3.0、payload_hash 相等、C' 状态可接受、且新增的 5 分钟判据用原批次 ceiling 与原观测时间必然成立），claim 得到 (E, B, C')，ScanCeilingAt=T；ObserveUsageEvent→verifyFactBatchContextTx 查到该行、b.scan_ceiling_at==T、C' 状态 OK；validateFactMetadata 同首送；事实写入 stream_watermark_at=T、source_sequence=B.sequence。verifyFactTrustTx 不拿水位与已发布流水位比较（consumption.go:981-992），无倒流问题；若 event_time ≤ finalized_through 走既有 late-fact reproject 路径（:1826 起）。

**B. 真实重送也要能落地（observed_at 随绑定走）**
- CommitSourceBatch 的映射 INSERT（:437-442）增写 observed_at=event.ObservedAt（代理每次重扫 Record.ObservedAt 都是当次 now()，batch.go:263-268；validateSourceBatch 已要求非零 :268-270）。历史行为 NULL。
- claimBindingSelect（:626-662）从 const 改为 var（像 transientRequeueMarkerSQL :1180-1195 那样由 Go 常量渲染）：臂 1 SELECT 增 `COALESCE(m.observed_at,sie.observed_at) AS observed_at`，WHERE 增 `AND b.scan_ceiling_at <= COALESCE(m.observed_at,sie.observed_at) + <factClockSkewToleranceSQL>`；臂 2 选 sie.observed_at；认领主查询（:678-684）改读 sib.observed_at。ORDER BY b.sequence DESC 不变：有持久化观测时间的新重送（有效）仍优先；历史重送行（observed_at NULL→回退首送观测→ceiling 超 5 分钟）被判据排除，落到交接行。
- 常量：consumption.go 新增 `const factClockSkewTolerance = 5*time.Minute`，validateFactMetadata（:322-323）改用它；渲染 `factClockSkewToleranceSQL`。三张事实表的 CHECK（0009:327/361/442）是 DDL 无法引用 Go 常量，用测试 T5 钉住。processPaymentAdjustment 的 5 分钟（source_processor.go:1155）与 economicRescanActivityWithinWindow 的 5 分钟（source_sync.go:1084）是不同语义，不合并。

**C. 修复工具**
- ingestRequeueDeadReplayBindingTx（ingest_requeue_dead_repair.go:479-538）嵌入的是同一个 claimBindingSelect（自动继承判据）；额外 Scan sib.scan_ceiling_at/sib.observed_at，switch 加第 4 例 `scanCeilingAt.After(observedAt.Add(factClockSkewTolerance))` → ReplayBlocked，reason 点名 validateFactMetadata；再加字段 ReplaySupersededBy（当 ReplayCycleStatus='blocked' 且该周期 superseded_by 非空时填后继 id）提示操作员先跑交接。这一例只可能在臂 2（首批次）出现。
- 新 kind `ingest-handover-superseded`（新文件 ingest_handover_superseded_repair.go + main.go 分发）：枚举 cycle_status='blocked' AND superseded_by_scan_cycle_id IS NOT NULL 且仍有映射行的周期（可 --stream/--cycle/--event 过滤），沿 superseded_by 走到第一个非 blocked 周期（有界、去环；链尾仍 blocked 且无 superseded_by → 报"无活后继"，不写）；dry-run 默认；apply 按周期各开一事务，调用**同一个** handoverSupersededScanCycleEventsTx（repair 模式含 dead），操作员 actor 审计。交接进 published 后继：验证器接受 published（:1026），tryPublish 不再评估它，事实照常落地。之后操作员跑既有 ingest-requeue-dead，ReplayBlocked 变 false。生产两条 usage 死信即走此路：b1de0e2b→…→活周期，(活周期, E, 28883c9a) 用 07:02:28 水位落地，Dead 归零。
- ingest-acknowledge-unreplayable 不改代码，其护栏（ingest_unreplayable_acknowledge.go:126-129）自动随新判定：balances 死信仍可写掉；有交接绑定的事件被拒并指向 requeue。

**D. balances 流（需负责人拍板的决策项）**
被 supersede 的 balances 周期是一份未完成快照：检查点绑定自身 snapshot_id（consumption.go:1111-1118），event id 折入 snapshot（ingest_unreplayable_acknowledge.go:29-33），C' 的快照行数硬校验（:759-761）不允许多一行——结构上不可迁移、不可重投；后继快照对每个账号都有更新的检查点。建议在同一交接函数里对 balances 做"退役"而非迁移：UPDATE source_ingest_events SET processing_status='processed',processed_at=now(),processing_error=CASE WHEN EXISTS(事实已按 payload_hash 落库) THEN NULL ELSE 'SUPERSEDED_SNAPSHOT' END,lease_token=NULL,lease_expires_at=NULL,dependency_kind=NULL,dependency_key_hmac=NULL WHERE 映射到 C 且 processing_status IN ('queued','failed','processing','waiting_dependency','parked_identity')，每条一行审计 `source_ingest_event.superseded_snapshot`（列形状同 acknowledge 工具的 UNREPLAYABLE_BINDING）。含 'processing' 是为了覆盖 supersede 瞬间正被租约的检查点；其 worker 的 MarkSourceEvent{Processed,Failed} 带 `processing_status='processing' AND lease_token=$` 守卫（:744-748）→ 0 行→ErrConflict→recordIsolated，无害。若负责人不接受自动退役，则退回现状：这类检查点走 8 次判死→acknowledge 写掉（生产第三条死信的形状）。

### 代码改动

- `K:/发票/wt-XM-INV-RC104/backend/migrations/0032_scan_cycle_event_handover.sql`：ALTER TABLE source_economic_scan_cycle_events ADD COLUMN observed_at TIMESTAMPTZ, ADD COLUMN migrated_from_scan_cycle_id UUID, ADD CONSTRAINT ..._migrated_from_fk FOREIGN KEY(source_instance_id,stream_id,migrated_from_scan_cycle_id) REFERENCES source_economic_scan_cycles(...) ON DELETE RESTRICT, ADD CONSTRAINT ..._migrated_not_self CHECK (migrated_from_scan_cycle_id IS NULL OR migrated_from_scan_cycle_id<>scan_cycle_id), ADD CONSTRAINT ..._balances_never_migrated CHECK (migrated_from_scan_cycle_id IS NULL OR stream_id<>'balances')。两列可空、无默认、不回填（元数据级变更，零停机）。
  - 为什么：observed_at 让'第二次观测时间'有处可落（今天只有 m.created_at 近似，且 sie.observed_at 首写冻结 source_sync.go:427-436）；migrated_from 让交接行在表内自证、区别于代理投递（对齐 ELIGIBILITY-OPERATIONS.md:622-648 对'手写行'的反对——交接行不是手写，但必须可辨识）；balances CHECK 是快照行数硬校验（consumption.go:759-761）的物理护栏。权限：deploy/postgres/harden-runtime-role.sql:113 对该表是表级 GRANT SELECT,INSERT，新增列无需重放；无 UPDATE 权限也正好，本设计只 INSERT。
- `K:/发票/wt-XM-INV-RC104/backend/internal/postgresstore/consumption.go`：新增 `const factClockSkewTolerance = 5 * time.Minute` 与 `var factClockSkewToleranceSQL = renderFactClockSkewToleranceSQL(factClockSkewTolerance)`（渲染为 `interval '300 seconds'`）；validateFactMetadata（:322-323）改用常量。verifyFactBatchContextTx 不改。
  - 为什么：同一判据今天在 validateFactMetadata 是 Go 字面量、在三张事实表是 DDL 字面量；本切片要把它第三次用在 claimBindingSelect 与修复工具的预测里，必须物理上只剩一处 Go 定义（记忆：被信任的过期闸最危险/同一个哈希钉在两处）。
- `K:/发票/wt-XM-INV-RC104/backend/internal/postgresstore/source_sync.go`：(1) supersedeStaleActiveScanCycleTx（:528-580）签名改为返回 (supersededCycleID string, err)。(2) CommitSourceBatch：事件循环内的映射 INSERT（:437-442）增列 observed_at=event.ObservedAt；循环结束后、UPDATE source_ingest_state（:448）之前，若 supersededCycleID!="" 调用 handoverSupersededScanCycleEventsTx(ctx,tx,in.SourceInstanceID,in.StreamID,supersededCycleID,in.ScanCycleID,handoverModeRuntime,in.Actor)。(3) 新函数 handoverSupersededScanCycleEventsTx(...) (handoverResult, error)：按流策略表 {payments: all, usage/credits: unfinished(+dead only in repair mode), balances: retire} 执行机制 A.3 的 INSERT…SELECT ON CONFLICT DO NOTHING（或 D 的退役 UPDATE），写审计 source.scan_cycle.events_handed_over；模式为显式枚举参数，不是 bool。(4) claimBindingSelect（:626-662）const→var，臂 1 SELECT/WHERE 与臂 2 按机制 B 修改；ClaimUnprocessedSourceEvents 主查询（:678-684）`sie.observed_at`→`sib.observed_at`。
  - 为什么：交接必须与 supersede 同事务、同 advisory 锁（:301）、在 C' 周期行 INSERT 之后（FK）且在代理自己的事件行之后（DO NOTHING 让代理最新陈述优先）；认领判据与运行时校验一致后，'当前有效绑定'才名副其实（今天 CLAIM-BINDING 选到的 published 绑定过不了 5 分钟规则，RELEASE-RC104 之后的新发现）。
- `K:/发票/wt-XM-INV-RC104/backend/internal/postgresstore/ingest_requeue_dead_repair.go`：IngestRequeueDeadRepairEvent 增字段 ReplayScanCeilingAt、ReplayObservedAt、ReplaySupersededBy；ingestRequeueDeadReplayBindingTx（:479-538）的 SELECT 增 sib.scan_ceiling_at、sib.observed_at、c.superseded_by_scan_cycle_id，switch 增第 4 例（ceiling > observed+factClockSkewTolerance → ReplayBlocked，reason 点名 validateFactMetadata），blocked 且 superseded_by 非空时填 ReplaySupersededBy 并在 reason 里提示 `--kind=ingest-handover-superseded`。ingestReplayableCycleStatuses 不动。
  - 为什么：工具的职责是'在烧 8 次之前预测运行时判决'（:114-120 注释）；今天它只复刻验证器三条不复刻元数据校验，导致重投工具放行后再死、写掉工具拒绝（两边互相推诿）。嵌入同一 claimBindingSelect + 同一常量，预测与运行时不可能漂开。
- `K:/发票/wt-XM-INV-RC104/backend/internal/postgresstore/ingest_handover_superseded_repair.go（新）`：RepairIngestHandoverSuperseded(ctx, in{Apply,OperatorID,StreamID,ScanCycleID,EventID}) (result, error)：枚举 blocked 且 superseded_by 非空、仍有映射行且这些事件在任何非 blocked 周期都没有映射的周期；resolveHandoverSuccessorTx 沿 superseded_by 有界（≤64 跳、去环）走到第一个非 blocked 周期，链尾无活后继则报告不写；apply 模式每周期一事务（同 QueueNarrow 的按单元隔离），调用同一个 handoverSupersededScanCycleEventsTx(handoverModeRepair)，balances 周期只报告'不可交接，用 acknowledge'。
  - 为什么：生产两条 usage 死信是在本代码之前被 supersede 的，没有交接行；且 runtime 模式刻意不迁 dead。修复路径必须与运行时穿过同一函数（规则与调用点两段代码要有测试穿过两段）。
- `K:/发票/wt-XM-INV-RC104/backend/cmd/eligibility-repair/main.go`：新增 kindIngestHandoverSuperseded="ingest-handover-superseded"，接受 --stream/--cycle/--event 过滤，dry-run 默认，--apply 需 --operator-id；runIngestHandoverSuperseded 打印 from→to、每流策略、迁移/跳过计数、无活后继的周期清单。
  - 为什么：操作员入口；与 ingest-requeue-dead 分两步是为了让'交接'与'重投'各自有独立审计与 dry-run。
- `K:/发票/wt-XM-INV-RC104/docs/ELIGIBILITY-OPERATIONS.md 与 docs/PRODUCTION-RUNBOOK.md`：改写 ELIGIBILITY-OPERATIONS.md:622-648（'映射表只有一个写者/blocked 无恢复路径'）为：两个入口一个函数、交接行以 migrated_from 自证、balances 例外；RUNBOOK 2150-2170 的'blocked 不是稍后再试'补上交接流程与三条生产死信的处置顺序（handover→requeue-dead→acknowledge balances）。
  - 为什么：手册今天把放大当作'设计如此'；不改文档，下一次值班还会按'承认丢失'处理可救的事实。

### 防住什么
- 被 supersede 的 usage/credits/payments 周期上的未完成事件（queued/failed/processing/waiting_dependency/parked_identity）在 supersede 同一事务里获得后继周期的有效绑定，下一次认领即拿到 (E, 原批次, C')，验证器通过、5 分钟规则按原值成立——'周期 blocked→ErrConflict→泛型分支→8 次判死→闩死'这条链对它们不再启动（例外见 does_not_prevent 第 3 条）。
- 2222 这类'绑定后唤醒 6842 条 parked 事件'与例行周期重叠的场景：parked_identity 事件被迁到后继周期，唤醒后不再撞 blocked 绑定。
- 代理真实重送（reconcile 从 baseline 重扫）产生的新绑定携带自己的 observed_at，能通过 validateFactMetadata；历史上 observed_at 为 NULL 的重送绑定被认领判据排除而不是被选中后必败。
- 后继周期不再在继承事件落地前发布水位（tryPublish 按映射行计数），流水位不再越过缺失事实——这是既有诚实性缺口的收窄，不是新闩：卡住它的事件要么落地、要么被冻结/写掉后放行，路径都存在。
- 支付事件在被 supersede 周期上已处理的额度重新拥有 published 映射，结转证明不再 errBalanceCarryForwardProofInvalid。
- 生产现存两条 usage 死信可被修复（handover→requeue-dead），Dead 计数归零的前提之一；配合 balances 死信 acknowledge，readyz 的 source_ingest_dead_events 闩可释放。
- 修复工具与运行时对'可重投'的定义一致：不会再出现'重投工具说可以、写掉工具也说可以/都说不行'的互相推诿。
- （若采纳 D）被 supersede 快照上的余额检查点不再走 8 次判死，而是带审计地退役，后继快照的检查点接管。

### 防不住什么
- supersede 本身：processing 周期因投影争用拖过 37 分钟就被下一个批次标 blocked（source_sync.go:373-389 的 upsert 只刷新 receiving 周期的 updated_at；consumption.go 无任何路径在 processing 期间刷新它）。本切片让 supersede 变得无害，不让它变少；'刷新 updated_at / 拉开窗口'是另一个角度。
- 一条事件→一条流→五流 AND→整个来源实例所有客户开不了票的放大几何（service.go:611-625、source_sync.go:1122-1131、:1455-1456）。任何其他确定性失败判死后仍会闩死。这是账号级隔离角度的事。
- supersede 瞬间恰好被租约（processing）的那一条事件：worker 手里的 claim 仍是旧三元组，本次尝试撞 blocked→PROJECTION_FAILED 烧 1 次；下一次认领才拿到交接绑定。烧的这 1 次仍走泛型分支（错误分级角度）。
- dead 且无冻结（无落库事实、无账号 hint，source_sync.go:846-878）的事件：runtime 模式不迁它，它留在 C 上；若它在 failed 阶段被迁到 C'，会吊住 C' 直到第 8 次判死并冻结（≤35 分钟），期间 C' 可能被 supersede 一次；级联止于一跳，但那 35 分钟流水位不前进。
- acknowledge 工具对'绑定有效但因非绑定原因（如损坏载荷）反复判死'的事件仍拒绝写掉（既有缺口，ingest_unreplayable_acknowledge.go:126-129），本切片不改它。
- 40001/40P01 风暴（SER-BUSY 已接住）与 ErrConflict 的错误分级——verifyFactBatchContextTx 的 blocked/哈希/水位三种拒绝仍是同一个裸 domain.ErrConflict（consumption.go:1025-1027）。
- 代理侧 shouldAbandonLegacyReconcileCycle 与 ReconcileWindowBounded 的判据（economics_db.go:174-193）不动；读者一的疑问（09-07 是否真有 legacy 放弃）本切片不需要答案，因为交接对 supersede 的成因不敏感。
- 若负责人否决 D：balances 周期被 supersede 时其未完成检查点仍会 8 次判死后需 acknowledge 写掉。
- repair 模式交接进 **published** 后继：事实能落地，但该后继的水位早已发布在缺失事实之上——既有诚实性缺口，本切片只在 runtime 路径（后继尚未发布）收窄它。
- 交接 INSERT…SELECT 的规模：payments 全量、usage/credits 未完成行，最坏一个 6 小时 rolling reconcile 窗口的未完成行数，在持有流级 advisory 锁的批次提交事务里执行；未实测，见 data_integrity_risks。

### 数据完整性风险
- 交接行断言了代理未签名的'周期成员关系'。缓解：batch_id/payload_hash/observed_at 全是代理原值，验证器信任的部分（签名批次、哈希链、水位）没有任何伪造；migrated_from 列 + source.scan_cycle.events_handed_over 审计使其可辨识；ELIGIBILITY-OPERATIONS.md:622-648 的'只有一个写者'需改写为'两个入口、一个函数'。这是本切片最需要负责人知情的语义变更。
- balances 流一行都不能迁：多一条 balance_checkpoint 映射 → C' 快照行数不符 → C' blocked + 全源 SOURCE_GAP 冻结（consumption.go:759-773）。三重护栏：流策略表、迁移里的 CHECK (migrated_from IS NULL OR stream_id<>'balances')、测试 T3 的变异验证。
- D 的自动退役是无人工的写掉。缓解：仅对结构上不可重投的对象（快照绑定检查点）、每条一行审计、事实已落库者不打 SUPERSEDED_SNAPSHOT 标记；仍需负责人签字。若否决，改为 repair 模式的显式命令。
- 事实的 stream_watermark_at 语义：交接落地 = 原批次 T（与首送完全一致，早可见）；真实重送落地 = 重送批次 T''、observed_at=重送观测（晚可见，结转证明可能要等更晚的 balances 周期覆盖，consumption.go:3519-3535 两指针合并逻辑处理，不会错只会晚）。两种都是真实观测，不发明时间。
- late 事实：event_time ≤ finalized_through 的交接事实走既有 late-fact reproject（consumption.go:1826 起），账号会重投影；金额路径全部 NUMERIC(78,0)/big.Int（:1797-1810），无 float。
- 并发：交接与 worker 的 FOR SHARE/FOR UPDATE 在 C 行上串行化（Postgres 行锁，非本代码保证）；worker 侧 SSI 冲突走 SER-BUSY。我方事务是默认隔离，不会因交接自我回滚；批次级重放由 Duplicate 分支（:325-333）挡住，交接 INSERT 由 PK DO NOTHING 幂等。因守卫来自 Postgres 而非本代码，我没有设计并发测试（记忆：并发测试要确定性——没有本代码的注入点可挂栅栏）。
- 规模：交接 INSERT…SELECT 在批次提交事务内、持有 (source,stream) advisory 锁，最坏几万行（一个 6 小时 usage 窗口的未完成行）≈ 秒级；代理对 5xx/超时会重放批次（幂等）。若实测过慢，可行的收缩是 runtime 只迁 unfinished 且加上限并把余量交给 repair，但必须审计 rows_deferred，不能静默截断。
- 回滚：0032 只加可空列，旧二进制的 INSERT 显式列名不受影响；交接行对旧 claimBindingSelect 也是'有效绑定'（周期状态 OK、哈希相等），不会导致旧代码拒绝。但 migrate.Verify 拒绝库里存在未知迁移（migrate.go:74-75），见 rollback。

### 迁移
需要一个迁移 0032_scan_cycle_event_handover.sql（只加可空列与约束，无回填，无新表，无权限重放）：
ALTER TABLE source_economic_scan_cycle_events
  ADD COLUMN observed_at TIMESTAMPTZ,
  ADD COLUMN migrated_from_scan_cycle_id UUID,
  ADD CONSTRAINT source_economic_scan_cycle_events_migrated_from_fk FOREIGN KEY (source_instance_id,stream_id,migrated_from_scan_cycle_id) REFERENCES source_economic_scan_cycles(source_instance_id,stream_id,scan_cycle_id) ON DELETE RESTRICT,
  ADD CONSTRAINT source_economic_scan_cycle_events_migrated_not_self CHECK (migrated_from_scan_cycle_id IS NULL OR migrated_from_scan_cycle_id<>scan_cycle_id),
  ADD CONSTRAINT source_economic_scan_cycle_events_balances_never_migrated CHECK (migrated_from_scan_cycle_id IS NULL OR stream_id<>'balances');
索引：不需要新索引——交接 SELECT 走 PK 前缀 (source,stream,scan_cycle_id)，认领臂 1 仍走 0031 的 (source,stream,event_id)，新增的 observed_at 判据是对已 JOIN 行的过滤。权限：harden-runtime-role.sql:113 是表级 GRANT SELECT,INSERT（无 UPDATE/DELETE），新增列自动覆盖，本设计只 INSERT，不需改 dbroles（记忆：迁移新建表要重放 permissions——本切片没有新表）。D 的退役 UPDATE 作用于 source_ingest_events，invoice_app 已有 UPDATE（:14 的 ALL TABLES 未被 revoke）。部署顺序：先应用 0032 再上二进制（旧代码对新列无感）。三张事实表的 CHECK（0009:327/361/442）不动。

### 测试计划

- **postgresstore/scan_cycle_handover_integration_test.go: TestSupersedeHandsUnfinishedEventsToTheSuccessorWithTheirOriginalBatch —— 用 deadIngestFixture（ingest_requeue_dead_repair_integration_test.go:39-106）在 usage 流提交周期 C：E1 queued、E2 markProcessed、E3 reviveForClaim(failed)；commitSupersedingCycle 提交 C'（带自己的事件 E9）。断言映射表存在 (C',E1,B_C) 与 (C',E3,B_C)，migrated_from=C，observed_at=sie.observed_at；(C',E2) 不存在；claimFor(E1) 的 ScanCycleID==C'、BatchID==B_C、ScanCeilingAt==C 的 ceiling、ObservedAt==首送观测；store.ValidateEconomicFactContext 接受该三元组；validateFactMetadata(用 claim.ScanCeilingAt, claim.ObservedAt) 返回 nil。**：证明 交接行在 supersede 同事务产生、指向原批次、能被认领并通过验证器与 5 分钟规则；已处理行不迁。
  - 变异必红：(a) 删除 CommitSourceBatch 里对 handoverSupersededScanCycleEventsTx 的调用 → 无 (C',E1) 行，认领回退首批次 (C blocked)，ValidateEconomicFactContext 返回 ErrConflict → 红。(b) 把 INSERT…SELECT 的 batch_id 改成 in.BatchID（新批次）→ claim.BatchID≠B_C 且 ScanCeilingAt 变成 C' 的 ceiling → 红。(c) 去掉 `processing_status NOT IN ('processed')` → (C',E2) 出现 → 红。旧实现下 (a) 的断言必红，不是恒真。
- **同文件: TestSuccessorCycleWaitsForHandedOverEventsBeforePublishing —— T1 场景后，把 C' 自己的 E9 markProcessed 并让 C' 完成（ScanComplete 批次），断言 C' cycleStatus 仍 'processing' 且 source_economic_stream_watermarks 无 C' 的行；再把 E1、E3 markProcessed → C' 'published'，水位=C' 的 ceiling。**：证明 后继周期的完整性包含继承事件（tryPublish 按映射行计数），且继承事件落地后周期能正常发布，不是新闩。
  - 变异必红：删除交接调用 → C' 在 E9 处理完就发布，第一段'仍 processing'断言红（这就是缺席型断言的变异验证：没有交接时它不会恒真）。第二段的变异：把 tryPublish 的 NOT IN ('processed','parked_identity') 改成只认 'processed' 不影响本例；改为把交接行的 payload_hash 写错 → 认领臂 1 不选它、事件仍 failed → C' 不发布 → 第二段红。
- **同文件: TestSupersedeNeverHandsOverBalanceCheckpoints —— balances 流：C 含两条 balance_checkpoint（ScanSnapshotRowCount=2）未处理；commitSupersedingCycle 提交 C'（自己一条检查点，row count=1，快照 id 不同）。断言 (C',E_old) 不存在；C' 的映射行数==1；把 C' 的检查点 markProcessed 后 C' 'published'（快照计数通过）；eligibility_freezes 无 SOURCE_GAP 新行。若采纳 D：断言 E_old 两条 processing_status='processed'、processing_error='SUPERSEDED_SNAPSHOT'、dependency_kind NULL，且各有一行 source_ingest_event.superseded_snapshot 审计。**：证明 balances 例外是真的被执行到，而不是碰巧没触发；以及退役路径的列形状满足 0009 的 dependency_pair 与 processed_at CHECK。
  - 变异必红：删除流策略表里 balances 的例外（让 INSERT…SELECT 对 balances 也跑）→ 若 DDL CHECK 存在则 CommitSourceBatch 直接报 23514 → 红；再把 DDL CHECK 也删掉 → C' 映射行数变 3≠1 → C' 'blocked' + SOURCE_GAP 冻结 → 红。两层护栏各自被一个变异证明有效。
- **postgresstore/claim_binding_integration_test.go: TestClaimBindingSkipsARedeliveryWhoseCeilingOutrunsItsRecordedObservation —— 复现生产形状：事件 ObservedAt=now-30min 首送到 C；C 被 C' supersede（产生交接行）；再由 commitSupersedingCycle 用 ceiling=now+1min 提交 C'' 且重送同一 E；测试专用 UPDATE 把 (C'',E) 的 observed_at 置 NULL 模拟 0032 之前的历史行；C'' published。断言 claimFor(E).BatchID==B_C（交接行），不是 B_C''；ValidateEconomicFactContext 接受；validateFactMetadata(claim.ScanCeilingAt, claim.ObservedAt) nil。**：证明 认领判据把'过不了 5 分钟规则'的绑定排除在'当前有效'之外，且优先级回落到交接行；这正是 RC104 后两条死信'两条路都堵死'的形状，现在有一条路通。
  - 变异必红：从 claimBindingSelect 臂 1 的 WHERE 删除 `b.scan_ceiling_at <= COALESCE(m.observed_at,sie.observed_at)+interval` → ORDER BY b.sequence DESC 选中 B_C''（ceiling now+1min > observed(now-30min)+5min）→ validateFactMetadata 返回 'source fact event time/watermark is invalid' → 红。
- **同文件: TestClaimBindingCarriesTheRedeliveryObservationTime —— E 首送 ObservedAt=now-30min 到 C；C 被 C' supersede；C'' 重送 E 且 event.ObservedAt=now（新鲜），C'' published。断言 claim.BatchID==B_C''（最新有效）、claim.ObservedAt==now（不是 now-30min）、claim.ScanCeilingAt==C'' 的 ceiling、validateFactMetadata nil。**：证明 重送观测时间随绑定持久化并被认领带出；CLAIM-BINDING'代理最新陈述优先'的原则在两条绑定都有效时仍成立。
  - 变异必红：(a) CommitSourceBatch 的映射 INSERT 不写 observed_at → NULL→回退首送观测→判据排除 B_C''→claim.BatchID==B_C → 红。(b) 认领主查询把 sib.observed_at 改回 sie.observed_at → claim.ObservedAt==now-30min → 断言红，且 validateFactMetadata 红。
- **postgresstore/consumption_test.go: TestFactClockSkewToleranceMatchesTheSchemaChecks（集成）—— 对 source_usage_events/source_credit_events/balance_reconciliation_checkpoints 各读 pg_get_constraintdef 中含 'observed_at' 的 CHECK，断言其文本与 factClockSkewTolerance 渲染出的 interval 等价（把 DDL 的 '5 minutes' 规范化为秒后比较）；同时断言 claimBindingSelect 文本包含 factClockSkewToleranceSQL。**：证明 Go 常量、SQL 判据、DDL CHECK 三处钉的是同一个值（记忆：同一个哈希钉在两处只改一处会在最后一段才炸）。
  - 变异必红：把 factClockSkewTolerance 改成 4*time.Minute → 与 DDL 的 300 秒不等 → 红；把 claimBindingSelect 里的 interval 手写成 '5 minutes' 字面量而不用渲染变量 → 包含断言红。
- **postgresstore/ingest_handover_superseded_repair_integration_test.go: TestHandoverRepairFollowsTheSupersedeChainAndUnblocksRequeue —— 构造历史形状：C→C'（supersede）→C''（supersede），C'' published；用测试专用 DELETE 删掉 migrated_from IS NOT NULL 的行（模拟 0032 之前的库）；E 在 C 上 killEvent(dead)。dry-run：结果列出 from=C、to=C''（跳过 blocked 的 C'）、rows=1、Applied=false，库中无新行。apply：出现 (C'',E,B_C)，migrated_from=C；然后 RepairIngestRequeueDead dry-run 对 E 报 ReplayBlocked=false、ReplayScanCycleID==C''。再 apply 一次交接：行数不变、审计只多一条 rows_migrated=0 的记录。**：证明 修复路径穿过与运行时同一个函数，沿链找到活后继，幂等；并让既有重投工具的判定翻转。
  - 变异必红：(a) 让 resolveHandoverSuccessorTx 只走一跳（取 superseded_by 不检查其状态）→ to=C'（blocked）→ 重投工具仍 ReplayBlocked → 红。(b) repair 模式也排除 dead → rows=0，无新行 → 红。(c) 去掉 ON CONFLICT DO NOTHING → 第二次 apply 报 23505 → 红。
- **postgresstore/ingest_requeue_dead_repair_integration_test.go: TestIngestRequeueDeadPredictsTheClockSkewRefusal —— 事件唯一绑定是首批次，构造 ceiling=now、event.ObservedAt=now-30min（validateSourceBatch 不拦，批次 CHECK 用 captured_at=ceiling 通过），killEvent。断言 dry-run 报 ReplayBlocked=true、reason 含 'validateFactMetadata'，Requeued=false；AcknowledgeUnreplayableIngestEvent dry-run 不再拒绝（Acknowledged=true）。**：证明 工具对元数据校验的预测生效，写掉工具的护栏据此放行——终止'重投说可以、落地必死、写掉不许'的死循环。
  - 变异必红：删除 ingestRequeueDeadReplayBindingTx 的第 4 例 → ReplayBlocked=false → 第一断言红，且 acknowledge 返回 'has a usable replay binding' 错误 → 第二断言红。
- **postgresstore/scan_cycle_handover_integration_test.go: TestHandoverYieldsToTheAgentsOwnRedeliveryInTheSupersedingBatch —— C 上 E1 queued；commitSupersedingCycle 的 C' 批次本身携带 E1（同 event_id/同 hash，ObservedAt=now）。断言 (C',E1) 恰一行，batch_id==B_C'（代理自己的），migrated_from IS NULL，observed_at==now；另一条未重送的 E3 则有 migrated_from=C 的交接行。**：证明 交接在事件循环之后执行且 DO NOTHING 让位，'代理最新陈述优先'在同批次重送时成立；同时证明交接与重送并存不产生重复映射。
  - 变异必红：把交接调用挪到事件循环之前 → (C',E1) 的 batch_id 变成 B_C、migrated_from=C → 红。
- **postgresstore/scan_cycle_handover_integration_test.go: TestHandedOverUsageFactLandsThroughTheRealProjectionPath —— T1 场景后，用真实 Service.ProcessSourceEvent（或 store.ObserveUsageEvent 以 claim 的字段构造 UsageObservation）处理 claimFor(E1)。断言 source_usage_events 出现一行：external_event_id=E1、stream_watermark_at==C 的 ceiling、source_sequence==B_C.sequence、observed_at==首送观测；MarkSourceEventProcessed 后 E1 'processed'。**：证明 不止验证器通过，事实真的按原水位落地——这是本角度的终点断言，穿过 claim→verify→validateFactMetadata→INSERT 四段。
  - 变异必红：把交接行的 batch_id 改为 B_C' → verifyFactBatchContextTx 的 storedWatermark(C' ceiling)≠传入(claim.ScanCeilingAt 也变成 C' ceiling，通过) 但 validateFactMetadata 的 C' ceiling > 首送观测+5min → ObserveUsageEvent 返回 'source fact event time/watermark is invalid'，无事实行 → 红。

**规模** M。Go 约 300-400 行（交接函数 ~80、认领 SQL 改动 ~30、修复工具新文件 ~200、CLI ~60、常量渲染 ~20）+ 1 个迁移 + 约 500 行集成测试 + 两处文档改写。实现 2 天、测试与影子评估 1 天；若采纳 D 再加半天。生产落地：迁移 0032 → 二进制 → `ingest-handover-superseded` dry-run/apply → `ingest-requeue-dead` → balances 死信 acknowledge → 观察 Dead 归零与 readyz。 · **依赖** 部署顺序硬依赖：0032 必须先于新二进制应用（migrate.Verify 要求库与二进制迁移集完全一致，migrate.go:70-91）；新增列可空，旧二进制对其无感。, 不依赖代理改动、不依赖上游 Sub2API/NewAPI；不依赖 SER-BUSY 之外的 RC104 改动，但与 CLAIM-BINDING 的 claimBindingSelect 同一段 SQL，需在其基础上改。, 与其他角度的接口：(1) 错误分级角度若给 verifyFactBatchContextTx 的三种拒绝分别命名，本切片的'supersede 瞬间被租约的那一条'可从'烧 1 次'变成'不烧'；(2) supersede 窗口/updated_at 刷新角度决定交接触发的频率，本切片对频率不敏感；(3) 账号级隔离角度决定 dead 事件的放大面，本切片只减少 dead 的产生。, D（balances 退役）需要产品负责人对'自动写掉结构上不可重投的检查点'签字；否决则 D 改为 repair 模式的显式命令，其余不受影响。, 生产事实待核：任务中的 07:02:28/09:28:14 两个数是否是 source_ingest_batches.scan_ceiling_at（validateFactMetadata 用的是它，source_processor.go:669，不是 stream_watermark_at）；b1de0e2b 的 superseded_by 链是否终止于非 blocked 周期（决定修复工具能否交接，我无法查生产）。 · **回滚** 二进制回滚到 RC104：交接行对旧 claimBindingSelect 仍是'有效绑定'（周期状态可接受、payload_hash 相等），旧验证器同样接受，已落地事实不受影响；旧 CommitSourceBatch 的显式列名 INSERT 不触碰新列。唯一障碍是 migrate.Verify 对库中未知迁移报错（migrate.go:74-75），两条路二选一：(a) 推荐——回滚目标构建为'RC104 代码 + 捆绑 0032 文件'（与 0031 手法相同：迁移先于代码、可留在库里），不动库；(b) 真要降级到不含 0032 的二进制：先执行 `ALTER TABLE source_economic_scan_cycle_events DROP CONSTRAINT ..._balances_never_migrated, DROP CONSTRAINT ..._migrated_not_self, DROP CONSTRAINT ..._migrated_from_fk, DROP COLUMN migrated_from_scan_cycle_id, DROP COLUMN observed_at;` 再 `DELETE FROM schema_migrations WHERE name='0032_scan_cycle_event_handover.sql'`，交接行留在表里（丢失 migrated_from 标记，但 audit_events 的 source.scan_cycle.events_handed_over 保留全部来龙去脉）。D 的退役写入不可逆（processed + SUPERSEDED_SNAPSHOT），回滚代码不恢复它们——这正是它需要签字的原因；回滚前先看审计计数。修复工具产生的行与运行时同形，回滚处理相同。


### 审稿：数据完整性与财务正确性（重复计数/漏计/时间倒流/跨周期串账/验证器信任语义/S — refuted=True would_ship=False

- [major] **按 code_changes 逐字实现后，交接行 (C',E,B) 在运行时是死行：worker 认领 E 仍拿到 (E,B,C)，verifyFactBatchContextTx 查 m.scan_cycle_id=C → blocked → ErrConflict → 泛型分支 → 8 次判死 → 闩死。目标事故链原样复发。**
  - claimBindingSelect 臂 1 的 SELECT 返回的是 **b.scan_cycle_id（批次的周期）**，不是 m.scan_cycle_id（映射行的周期）。今天两者恒等（CommitSourceBatch 用同一个 in.ScanCycleID 写批次与映射），交接行是第一次让它们分叉——交接行 (C',E,B) 里 B.scan_cycle_id 仍是 C。方案 A.6 断言「claim 得到 (E, B, C')」，code_changes 对 claimBindingSelect 只列了 observed_at 与 5 分钟判据两处改动，没有把 b.scan_cycle_id 换成 m.scan_cycle_id。同样的原因，修复工具 ingestRequeueDeadReplayBindingTx 用 m.scan_cycle_id=sib.scan_cycle_id 回查映射，会回到原行 (C,E,B) 报 ReplayBlocked=true，T7 的「ReplayBlocked=false、ReplayScanCycleID==C''」断言必红。T1 能抓到这个缺口，所以不是不可修，但它是方案中心机制的一处未追到底的假设；修正后 claim.ScanCycleID 与 claim.BatchID 所属周期从此可以不同，所有消费 claim.ScanCycleID 的调用点（cutover manifest :640、usage/credit :674/:706、balance :756、payments :1149/:1191）都需要在这一前提下重新核一遍，ingestRequeueDeadScanCyclesTx 按 batch_id 标 ReplayBinding 的逻辑也会把 (C,B) 与 (C',B) 两行同时标成「重投绑定」。
  - 依据：backend/internal/postgresstore/source_sync.go:636-638（臂 1 SELECT `b.scan_cycle_id`）、:642-646（JOIN c ON c.scan_cycle_id=m.scan_cycle_id 仅用于过滤）；:437-442（今天 batch 与 mapping 写同一个 in.ScanCycleID）；consumption.go:1008-1028（验证器按传入 scan_cycle_id 查映射行）；ingest_requeue_dead_repair.go:490-496（LEFT JOIN m … AND m.scan_cycle_id=sib.scan_cycle_id）、:551-556（ReplayBinding 只比 BatchID）
- [major] **payments 流「迁全部行」把一条 dead 且无冻结的支付事件（例如 RC104 前 40001 风暴或损坏载荷判死的支付）迁进活周期 C'：C' 永远算 incomplete、发不了水位；37 分钟后 C' 被 supersede，ALL 策略再迁到 C''……payments 流水位从此停摆；finalizeSourceAccountsTx 取四流 min，整个来源实例的 finalized_through 不再前进。操作员想写掉：acknowledge 因「有可用绑定」拒绝；想重投：确定性失败再死 8 次。方案自己承认「dead 不迁以免级联」，却对 payments 例外。**
  - tryPublish 的 incomplete 计数只放行「failed/dead 且有 open 冻结」的行；支付事件判死时既没有 payload_hash 关联（关联查询只查 checkpoint/usage/credit 三张表，不查 funding_lots），也没有账号 hint（processPaymentOrder/processPaymentAdjustment 都没有 wrapWithAccountHint，ValidateEconomicFactContext 在账号解析之前就返回），所以 dead 支付事件必然无冻结、必然吊住它映射到的每个周期。今天它只吊住已 blocked 的 C，无害；方案让它自动获得活周期的有效绑定，把「readyz 闩」升级成「finalization 停摆 + 写掉工具拒绝」的死局。does_not_prevent 第 5 条已知 acknowledge 的这个缺口，但方案是主动把事件送进这个缺口。
  - 依据：consumption.go:729-741（incomplete 过滤条件）；source_sync.go:846-878（dead 冻结关联只查三张事实表 + hint）；source_processor.go:1149-1172（payments 校验早于账号解析，返回裸 err）、:159（wrapWithAccountHint 只在 :679/:711/:759 使用）；consumption.go:539-547（min(watermark_at) 且 count 须为 4）；ingest_unreplayable_acknowledge.go:126-129（护栏）
- [minor] **方案宣称交接后事实「原批次原水位、零时间倒流」，这对 usage/credits 成立，对 payments 不成立：结转证明与影子报告对支付额度的可见时间取的是**映射周期**的 scan_ceiling_at，不是批次的。交接到 C' 的支付额度可见时间变成 C'.ceiling；repair 模式交接进几天后的 C'' 则变成 C''.ceiling，账号的 requested_through 会被拉回到「存在 ceiling ≥ C''.ceiling 的 balances 周期」之前。**
  - 我核对了方向：更晚的可见时间只会要求更晚的余额检查点来覆盖，检查点余额必然包含该额度，所以是「只晚不错」，方案在 data_integrity_risks 里对此的描述基本准确；但 one_paragraph 的「事实落地时 stream_watermark_at=T」这句对 payments 这条流是错的表述，文档与测试（T10 只测 usage）没有覆盖 payments 的可见时间语义。
  - 依据：consumption.go:3496-3510（NOT EXISTS … cycle.cycle_status='published'）、:3527-3543（SELECT cycle.scan_ceiling_at … AS visibility）；eligibility_shadow_report.go:501-514（pc.scan_ceiling_at>c.scan_ceiling_at 拉回 requested_through）
- [minor] **「代理最新陈述优先」只在代理恰好在 superseding 批次里重送 E 时成立。C' 的后续批次再送 E 时，代理行撞上已存在的交接行 (C',E,B)，PK (source,stream,cycle,event) 上 DO NOTHING，代理的新 observed_at 被静默丢弃，事实按旧 ceiling T 落地。**
  - 两种都是真实观测，不造成数值错误；但 T9 只证明同批次情形，方案文字把它说成通则，且 0032 的 observed_at 列在这种最常见的重扫形状下不会被写入。
  - 依据：0009_consumption_eligibility_ledger.sql:68-77（PK 定义）；source_sync.go:437-442（ON CONFLICT DO NOTHING）；方案机制 A.2
- [minor] **0032 被描述为「元数据级变更、零停机」，但 ADD CONSTRAINT FOREIGN KEY / CHECK 会在 ACCESS EXCLUSIVE 锁下全表扫描校验存量行；映射表是全源全流全周期的累积表，规模不小。**
  - 锁持有期间 CommitSourceBatch（同表 INSERT）与所有投影 worker（verifyFactBatchContextTx 读该表）全部阻塞；应写成 NOT VALID + VALIDATE CONSTRAINT 或给出实测锁时长。
  - 依据：方案 schema_or_migration；harden-runtime-role.sql:113（表级授权无需重放属实）
- [minor] **T2 断言「C' 'published'，水位=C' 的 ceiling」——发布的流水位写的是周期的 stream_watermark_at（低水位），不是 scan_ceiling_at；这条断言在正确实现下也会红。**
  - 测试断言指向错误的列，实现者可能为了让测试变绿而改错发布逻辑。
  - 依据：consumption.go:686-687（SELECT stream_watermark_at）、:806-812（watermark_at=item.watermark）
- [minor] **superseding 批次自带 ProjectionStatus='blocked' 时，C' 以 blocked 状态 INSERT，运行时交接把 C 的未完成事件迁进一个永远不会被 supersede（非 active）、superseded_by 永远为 NULL 的死周期；修复工具枚举「blocked 且 superseded_by 非空」找不到 C'，沿 C 的链走到 C' 也报「无活后继」。**
  - 与今天等价（都卡死），但方案多写了一批无用行；应在 C' 为 blocked 时跳过交接，并让链走法对「链尾 blocked 且无 superseded_by」给出可操作的下一步。
  - 依据：source_sync.go:365-372（CASE WHEN $12='blocked' THEN 'blocked'）；:531-534（supersede 只找 receiving/processing）

**签字条件**：
- claimBindingSelect 臂 1 改为返回 m.scan_cycle_id（并让 ingestRequeueDeadReplayBindingTx 的回查、ingestRequeueDeadScanCyclesTx 的 ReplayBinding 标记按 (batch_id, scan_cycle_id) 二元组判定）；补一条测试：当映射行周期 ≠ 批次周期时 claim.ScanCycleID 必须等于映射行周期，且 ValidateEconomicFactContext 用该三元组通过。方案文本需明确写出这一改动及 claim.ScanCycleID 语义变化，并逐个复核 :640/:674/:706/:756/:1149/:1191 六个消费点。
- payments 流策略改为与 usage/credits 相同的「runtime 不迁 dead」，已处理行是否迁移单独论证（它只服务结转证明的 published 映射要求）；或者先给支付判死路径补账号 hint 使 EVENT_DEAD 冻结能落，再谈 ALL。必须有一条测试：dead 且无冻结的支付事件被 supersede 后，后继周期仍能发布。
- T2 的发布断言改为比对 source_economic_stream_watermarks.watermark_at == C'.stream_watermark_at。
- 0032 的 FK/CHECK 用 NOT VALID + VALIDATE CONSTRAINT 两步，或给出生产规模下的锁时长实测。
- superseding 批次 ProjectionStatus='blocked' 时跳过运行时交接；链走法在链尾 blocked 且无 superseded_by 时报告清晰的人工路径。
- 方案文档把「原批次原水位」限定为 usage/credits，payments 的可见时间按映射周期 ceiling 单独写明；repair 手册加一步：交接前核查目标事件所属账号在 finalized_through 之后是否已开票。
- D（balances 自动退役）保持为需负责人签字的独立决策项，默认不随本切片上线。

**残余风险**：
- repair 模式交接进 published 后继（生产两条 usage 死信的必经路）会让事实落在已发布水位之下：event_time ≤ finalized_through 走 late-fact reproject（consumption.go:1826-1846），若期间已开票则触发 lot 级红字回退冻结——方向正确但会制造人工处置；方案没有给出这两条事件所属账号是否已开票的核查步骤。
- 持续投影积压下每 37 分钟一次 supersede 会把**整个**未完成积压（含 parked_identity，6842 行那种形状）整体再复制一份到下一周期：映射表线性增长，tryPublish 每周期一次 JOIN sie JOIN eligibility_freezes 的代价随之增长，且这一切发生在持有 (source,stream) advisory 锁的批次提交事务里；未实测。
- 并发上我没能找到反例：CommitSourceBatch 对 C 行 FOR UPDATE（:533-536）与 worker 的 Serializable FOR SHARE（consumption.go:1017）串行化，worker 快照早于我们提交时得到 40001 → SER-BUSY（source_processor.go:221-237），晚于则撞 blocked 烧 1 次；Busy/Processed/Failed 三个标记都带 processing_status='processing' AND lease_token 守卫（source_sync.go:744-748、:970-975），D 的退役不会被迟到的标记翻回。但这些守卫全部来自 Postgres 行锁语义，方案自己也承认没有可挂栅栏的注入点，等于没有并发测试。
- 5 分钟判据现在有三处（Go 常量、claimBindingSelect 的 SQL 渲染、三张事实表的 DDL 字面量），T6 只在测试时比对；DDL 一旦有人改成 NOT VALID 或新表漏加 CHECK，认领判据与落地校验会静默漂开——记忆里「被信任的过期闸」正是这个形状。
- D 自动退役依赖「后继快照对每个账号都有更新的检查点」：我核实了检查点 id 每次采集必然不同（AsOf=transaction_timestamp()，snapshot_id 哈希含 AsOf，event id 折入 payload hash：economics_db.go:1026、cutover.go:571-578、batch.go:263），所以不会被同 id 重送「假满足」；但被退役快照里一个账号的瞬时负余额若在后继快照已被充平，UNKNOWN_NEGATIVE_BALANCE 那条线就看不到它——与现状（判死后 acknowledge）一样丢，只是从此无人过目。
- ingestRequeueDeadScanCyclesTx 按 batch_id 标 ReplayBinding：交接后 (C,B) 与 (C',B) 都被标成重投绑定，操作员在 dry-run 输出里会看到一个 blocked 的「重投绑定」，正是 RC104 前那种「报表与现实不符」的形状。
- usage 事件 event_time ≤ B.ceiling 是否成立（validateFactMetadata 的另一半）在认领时不可预测（载荷加密），工具第 4 例只覆盖 ceiling/observed 半边；方案已说明，但生产那两条死信若栽在这一半，handover→requeue 之后仍会再死一轮。

### 审稿：复发与运维：按方案改完后重放 2222 绑定 + 例行对账重叠 + 40001  — refuted=False would_ship=False

- [major] **重放事故的 balances 流：07:47 balances 周期 e58b9430 被 supersede。方案把 D（自动退役快照检查点）列为「需负责人拍板、可否决」的决策项。若否决，该周期上未完成的 balance_checkpoint 被认领 → verifyFactBatchContextTx 因周期 blocked 返回裸 ErrConflict → 泛型分支 → 8 次 × 5 分钟 → dead → Dead>0 → readyz 永久 503 且五流门禁把整个来源实例的额度标 source_unavailable。这正是生产第三条死信的形状，事故原样复发。**
  - 方案 prevents 第 1 条只覆盖 usage/credits/payments；balances 结构上不可迁移（快照行数硬校验），所以「不做 D」= 事故链在 balances 流上一字不改。D 不是可选项，是复发防线本身；方案把它包装成决策项会让负责人以为否决 D 只是少一个便利。
  - 依据：backend/internal/postgresstore/consumption.go:1025-1027（blocked 周期 → ErrConflict，无区分）；backend/internal/application/source_processor.go:262（ErrConflict 走 PROJECTION_FAILED，+5min）；backend/internal/postgresstore/source_sync.go:71-80（sourceEventDeadThreshold=8）；backend/cmd/api/runtime.go:799-800（Dead>0 → errSourceIngestDeadEvents）；backend/internal/application/service.go:611-625（五流 Ready 门禁 → source_unavailable）；consumption.go:759-761（balances 快照行数校验，方案自己承认不可迁）
- [major] **修复路径 ingest-handover-superseded 对生产两条 usage 死信：沿 b1de0e2b 的 superseded_by 链走到第一个非 blocked 周期。若该周期恰是 9ca5afcb（RC104 后发现的、已带 (E, 90adb1e5) 无效重送映射的 published 周期），交接 INSERT 撞 PK (source,stream,scan_cycle_id,event_id) → ON CONFLICT DO NOTHING → rows_migrated=0，无任何报错。随后 ingest-requeue-dead 仍报 ReplayBlocked（臂 1 被新 5 分钟判据排除，回落臂 2 = 首批次 + blocked 周期），ReplaySupersededBy 又把操作员指回 handover——两个工具互相推诿，而方案新增的第 4 例让 acknowledge 的护栏放行，唯一出口变成把一条本可救回的事实写掉。修复工具以 API 同一凭据连库（invoice_app），对映射表只有 SELECT,INSERT，没有任何手段替换那条冲突行。**
  - 方案对「代理已在目标周期留下无效重送行」的形状没有任何处理规则，却把「生产两条 usage 死信即走此路」写成结论。它自己列的依赖项「b1de0e2b 的链是否终止于非 blocked 周期」问的还不够：还要问终点是否已经带着 E 的行。同时测试 T4 就是这个形状：C'' 既 supersede C' 又重送 E，事件循环先落 (C'',E,B_C'')，交接对 E 让位，E 在任何非 blocked 周期都没有原批次行；claimFor(E).BatchID==B_C 会因臂 2 回退「恰好为真」，紧接着的 ValidateEconomicFactContext 断言在方案自身下就是红的。T4 证明的不是「有一条路通」，而是这个缺口。
  - 依据：backend/migrations/0009_consumption_eligibility_ledger.sql:68-83（PK 与 FK 定义）；backend/internal/postgresstore/source_sync.go:626-662（claimBindingSelect 臂 1/臂 2；臂 2 取 fb.scan_cycle_id 即首批次所在周期）；backend/internal/postgresstore/ingest_unreplayable_acknowledge.go:126-129（ReplayBlocked=true 即放行写掉）；backend/cmd/eligibility-repair/main.go:169-177（readOneLineSecret(databaseURLFile) → pgxpool.New，与 API 同凭据）；deploy/postgres/harden-runtime-role.sql:112-113（REVOKE UPDATE,DELETE；GRANT SELECT,INSERT）；docs/PRODUCTION-RUNBOOK.md:2132（工具在 API 容器内以 /app/bin 运行）
- [major] **后继周期继承未完成事件后，tryPublish 在它们落地前不发布，C' 停在 processing。此时代理下一轮新周期被 supersedeStaleActiveScanCycleTx 以 ErrScanCycleBusy 拒绝（C' 仍 fresh），代理按 Retry-After 30s 反复重试同一批次，流的 last_accepted_at 冻结；5 分钟后 STREAM_STALE——它是 fatal 且没有任何 grace（ECONOMIC_RESCAN_ACTIVE 只降级水位陈旧，不降级心跳陈旧）→ readyz 503 + 五流门禁 source_unavailable，直到 37 分钟后 C'' 再 supersede C'。C'' 又继承同一批未完成事件 → 再来一轮。只要有一条「骑手」事件（waiting_dependency 每 12 小时才回认领；被 ACCOUNT_LOCK_BUSY/SERIALIZATION_BUSY 无限退回的事件；attempt 7 的 failed）跟着周期链走，整个来源实例就以每 37 分钟中 ~32 分钟不可用的占空比持续跳变，且流水位在整段时间内一步不前进（全源 FinalizedThrough 卡死）。今天的行为是后继周期只算自己的事件、37 分钟后即发布并恢复，孤儿留在 blocked 周期上（之后判死闩死——同样糟，但方案声称「不引入新闩」「只是既有诚实性缺口的收窄」，低估了它把一次性 37 分钟停摆变成随骑手寿命滚动的停摆）。**
  - 方案把 supersede 频率与就绪心跳当作「别的角度」，但交接机制本身改变了后继周期的完整性语义，也就改变了它停在 processing 的时长；心跳闩对这个时长是硬 5 分钟。事故的 happy path 里骑手很快落地（≤5 分钟），代价只是几分钟 503；但方案没有任何一跳上限、没有把「继承 N 条未决事件已 M 跳」暴露成命名的就绪理由或指标，运维只会看到 STREAM_STALE / ECONOMIC_RESCAN_ACTIVE 交替，无法定位是哪条事件在骑。
  - 依据：backend/internal/postgresstore/source_sync.go:1096-1097（STREAM_STALE，heartbeat 5m）与 :1071-1072（nonFatalStreamHealthReasons 仅含 ECONOMIC_RESCAN_ACTIVE）；:547-556（fresh 的活周期 → ErrScanCycleBusy）；docs/PRODUCTION-RUNBOOK.md:1904-1918（代理对 503 BUSY 每 30s 重试同一批次、不退出）；agents/sourceagent/runner.go:133-160；backend/cmd/api/runtime.go:218（SOURCE_ECONOMIC_HEARTBEAT_MAX_STALENESS 默认 5m）、:153-156,244（supersede 窗口 37m）；backend/internal/application/source_processor.go:128（sourceDependencyFallbackRetry=12h）；consumption.go:729-741（incomplete 计数含 waiting_dependency/failed 无冻结）；service.go:611-625（ListFundingLots 读 stream.Ready）
- [minor] **supersede 瞬间在途的不是「那一条」而是最多一整批：source-projection worker 每 2 秒 Claim 100 条，串行处理；批次内尚未处理的 claim 都带旧三元组 (E,B,C)，C 被标 blocked 后逐条撞 ErrConflict → PROJECTION_FAILED，各烧 1 次并把 next_attempt_at 推到 +5 分钟。后果一：即便在事故 happy path，C' 至少要等 5 分钟才能发布 → 恰好踩到 5 分钟心跳闩，readyz 短暂 503。后果二：长积压跨多次 supersede 时同一事件可能反复被烧，8 次 supersede（≈5 小时）足以单独判死一条事件。**
  - 方案把它写成「那一条」并归到错误分级角度；数量级和与心跳闩的耦合都没算。
  - 依据：backend/cmd/api/runtime.go:461（Interval 2s, BatchSize 100）；backend/internal/postgresstore/source_sync.go:678-720（Claim 一次性 FOR UPDATE SKIP LOCKED LIMIT $2 并逐条 lease）；source_processor.go:200-280（串行处理，ErrConflict 走 :262 的 +5min）
- [minor] **代理送来的新周期批次本身带 ProjectionStatus='blocked' 时（代理侧投影健康检查失败，ReconcileBlocked），CommitSourceBatch 先执行 supersede、再把 C' 直接以 cycle_status='blocked' 插入。方案的交接无条件以 in.ScanCycleID 为目标 → 未完成事件被迁到一个天生 blocked 的 C'。blocked 周期永远不会再被 supersede（只看 receiving/processing），superseded_by 永远为空；修复工具沿链 C→C' 走到尽头报「无活后继」，运行时也不会再为 C 触发第二次交接——这批事件永久孤儿且两条工具路径都不可达。**
  - 该场景下全源已被 SOURCE_GAP 冻结，影响面不额外扩大，但方案需要一条明确规则：后继为 blocked 时不交接（把行留在 C 上让后续链能找到），否则是无恢复路径的静默孤儿。
  - 依据：backend/internal/postgresstore/source_sync.go:364-372（先 supersede，再按 $12='blocked' 插入 blocked 周期）、:395-399（SOURCE_GAP 冻结）、:531-536（supersede 只扫 receiving/processing）
- [minor] **「代理最新陈述优先」只对 C' 的第一个批次成立：交接在首批次事件循环后执行；C' 后续批次再重送同一事件时，代理的 (C',E,B'_later) 反而撞交接行的 PK 被 DO NOTHING 丢弃，事实以旧批次的 ceiling/observed_at 落地。credits 流每周期从零重扫、必然重送全部事件，因此几乎所有 credits 交接事件都会以旧水位落地。落地本身合法，但方案的原则陈述与 T9 只覆盖首批次，后续批次的优先级实际相反且无测试钉住。**
  - 不是正确性缺陷，但方案对自己的语义描述不准确，且 T9 的变异（把交接挪到循环前）只能证明首批次情形。
  - 依据：backend/internal/postgresstore/source_sync.go:411-446（事件循环）与方案 A.2 的调用位置；agents/sourceagent/economics_db.go:186-189、:292-297（credits 每周期从零）
- [minor] **payments 流「迁全部行」在每次 supersede 都把 C 上的全部支付映射（含早已在别的 published 周期有映射的事件）整体复制到 C'；链越长、supersede 越频繁，映射表按周期数线性膨胀。结转证明用 EXISTS 不受重复影响，但没有必要的写放大，并使 tryPublish 对 C' 的完整性查询扫更多行。**
  - 应只迁「在任何非 blocked 周期都没有映射」的支付事件，与修复工具的枚举条件保持一致。
  - 依据：backend/internal/postgresstore/consumption.go:3480-3511、:3527-3545（结转证明只要求存在一条 published 映射）
- [minor] **可见性：方案只写 audit_events（source.scan_cycle.events_handed_over / superseded_snapshot）。没有就绪理由、SourceHealth 字段、指标或后台页面显示「当前活周期继承了 N 条未决事件、已 M 跳、最老一条来自哪个周期」。运维在滚动停摆期间只能看到 STREAM_STALE 与 ECONOMIC_RESCAN_ACTIVE 交替，runbook 也没有从这两个理由指向「查骑手事件」的路径。**
  - 方案的机制让单条事件可以持续影响全源就绪与水位，却没有把它变成可命名、可定位的信号；「会不会有事件永远等待而无人知晓」的答案是：等待不会无人知晓（心跳闩会响），但知晓的人不知道该看哪里。
  - 依据：backend/internal/postgresstore/source_sync.go:1088-1130（evaluateSourceStreamHealth 的理由集合无相关项）；backend/internal/httpapi 下无任何读取 superseded_by/scan_cycle 的管理端点（grep 空）；docs/PRODUCTION-RUNBOOK.md:1948-1958 只覆盖 BUSY 与 supersede 自愈

**签字条件**：
- 把 D 从「决策项」改为方案硬性组成部分（或给出等价机制），并在 T3 中同时覆盖 D 的退役路径；否决 D 即视为方案未防止事故复发。
- 修复工具必须处理目标周期已带该事件另一批次映射的 PK 冲突：至少显式报告「collision: 目标周期 X 已把 E 绑定到批次 Y（ceiling 超观测 5 分钟）」并计入 rows_collided；且 acknowledge 的护栏不得仅因第 4 例（5 分钟规则）放行——当事件在任何 blocked 周期上存在时间合法的原批次绑定时必须拒绝写掉。重写 T4 以对应方案实际行为，并新增「后继周期已含无效重送」的修复工具测试。
- 为继承设置可见的边界：至少新增一个命名的流健康理由/字段（如 SCAN_CYCLE_INHERITED_PENDING，携带条数与跳数）并写入 runbook；并由负责人明确签字接受「骑手事件存在期间全源以 STREAM_STALE 滚动不可用、水位不前进」这一后果，或给继承加跳数上限并把超限事件的处置写成规则。
- 后继周期以 blocked 插入（in.ProjectionStatus='blocked'）时不做运行时交接，行留在 C 上供链式修复找到；补一条测试。
- payments 只迁「在任何非 blocked 周期无映射」的事件，与修复工具枚举条件同源。
- 补测试钉住 C' 后续批次重送让位于交接行的实际优先级，并在方案文本里改正「代理最新陈述优先」的适用范围。
- 上线前在影子库用生产形状实测：6842 行交接的事务耗时、随之而来的 40001 波次、以及 C' 从 supersede 到发布的时长是否落在 5 分钟心跳预算内。

**残余风险**：
- 生产链拓扑未核：b1de0e2b 的 superseded_by 链终点是哪个周期、它是否已带 (E, 90adb1e5) 行；以及 07:02:28/09:28:14 是否为 source_ingest_batches.scan_ceiling_at（方案自己也列了后者）。这两点直接决定修复工具对现存两条死信是否有效。
- 交接 INSERT…SELECT 在代理 HTTP 请求的批次事务内、持有 (source,stream) advisory 锁执行，规模未实测；同时 Serializable 的 worker（consumption.go:352/474/1081）读同一索引区间会在大批插入时收到一波 40001，SER-BUSY 以 15s 退回——这会推迟 C' 发布，让前 5 分钟撞心跳闩的概率上升。
- D 的自动退役不可逆；我没有核对 balances 快照的完整性契约——若后继快照缺少某账号（上游行消失），该账号在被退役快照上的最后检查点会静默丢失。
- 新 5 分钟判据依赖 m.observed_at 由 CommitSourceBatch 写入；映射表今后若出现第三个写者（或迁移回填）而不写 observed_at，会回退到首送观测并把有效绑定误判为无效。
- T8 依赖「批次 CHECK 用 captured_at=ceiling 通过」，source_ingest_batches 的 DDL 在 0004 里，我未逐字核对该 CHECK。
- 部署顺序：migrate.Verify 只在启动时跑（runtime.go:189），先应用 0032 再上二进制成立的前提是这中间旧二进制不重启；回滚方案 (a) 正确但要写进 runbook。
- 并发守卫来自 Postgres 行锁而非本代码（方案也承认），交接与 worker FOR SHARE 的交错只有推理没有测试；ClaimUnprocessedSourceEvents 的 FOR UPDATE SKIP LOCKED 与 D 的退役 UPDATE 在 sie 行上的等待顺序未验证。
- credits 流每周期从零重扫意味着交接行几乎总会与代理重送并存；T9 之外没有测试钉住「后续批次重送让位于交接行」这一实际优先级。

---

## 提案：XM-INV-CYCLE-WAIT：「绑定周期已 blocked」分级为「等待代理重投递」——不耗预算、不判死、只冻本账号

**角度**：失败分级——verifyFactBatchContextTx 因周期 blocked 拒绝时，不该消耗尝试预算、不该判死；引入第三态（waiting_dependency + dependency_kind='source_scan_cycle'）并回答谁唤醒、唤醒条件、是否无限等待、就绪判定怎么看它

「周期已 blocked」这个拒绝（consumption.go:1024-1027 的 cycle_status 分支）对*周期*是永久的——全仓没有任何路径把 blocked 改回去，运维手册明说不该有（docs/ELIGIBILITY-OPERATIONS.md:640-644）——但对*事件*不是终态：代理的 event_id 是确定性的，下一次重扫会在健康周期下为同一 event_id 追加一条映射（source_sync.go:437-446），手册把这条路定义为唯一正规恢复（ELIGIBILITY-OPERATIONS.md:645-648），09-08 生产的两条死 usage 事件也确实在 09:28 拿到了 published 绑定。所以它既不是 SER-BUSY 那种「15 秒后再试」的瞬时错误（那样会每 15 秒空转、10 分钟宽限后照样 503），也不该判死（判死既错——恢复是预期且自动的——又通过 Dead>0 闩死 readyz、经五流 AND 让整个来源实例所有用户开不了票）。它是「等待外部供货方」，正是 waiting_dependency 已经承诺的契约（docs/SOURCE-SYNC-PROTOCOL.md:438-441：不消耗死信预算、不阻塞无关用户与全局就绪、精确唤醒 + 12 小时兜底）。本角度：verifier 对 blocked 周期改返回带周期信息的专用错误类型（Unwrap 仍是 domain.ErrConflict，旧断言不破）；RunOnce 在 SER-BUSY 分支之后、泛型 PROJECTION_FAILED 之前加一个分支，把事件置为 waiting_dependency(dependency_kind='source_scan_cycle')、退回 attempt_count、next_attempt_at=+12h，并在同一事务用应用层已解析出的 hintAccountID 对该账号开一条 SOURCE_GAP 冻结（trigger_object_type='source_ingest_event'）——账本对这一个账号保持诚实，其余账号、五流闸、readyz 完全不受影响；CommitSourceBatch 为同一 event_id 写入非 blocked 周期的映射时按主键唤醒；标记等待前复核一次「是否已有更新的有效绑定」封掉丢失唤醒。不改 tryPublish 完整性判据、不改 readiness 查询、不新增就绪原因。它是必要而非充分的：唤醒后的重投递绑定仍会撞 validateFactMetadata 的 5 分钟规则（consumption.go:322-326），那是另一角度的事，必须一起落地。

### 机制

一、错误定性（回答「瞬时吗？判死对吗？」）
- 出处：consumption.go:995-1030 verifyFactBatchContextTx，:1024-1027 把 hash 不等、水位不等、周期状态不在 {receiving,processing,published} 三种情况都折成裸 domain.ErrConflict。它在 observeEligibilityFact 里于账号解析之后被调用（:1711；账号在 source_processor.go:657 verifiedExternalAccount 已解析，:679 wrapWithAccountHint 已把账号 id 附在错误上）。
- 对周期：终态。cycle_status 写入点只有 published/blocked（consumption.go:763/791/820、source_sync.go:557），无回退；superseded_by 无代码读取。
- 对事件：可恢复且恢复是自动的、由外部驱动的。event_id=deterministicUUID(source,entity,external_id,op,payload_hash)（agents/sourceagent/batch.go:255-271）；CommitSourceBatch 对已存在的 ingest 行只比对 hash、不动任何列，然后无条件追加新周期映射（source_sync.go:414-446）。CLAIM-BINDING（source_sync.go:581-662）已经会优先取「当前有效且 sequence 最大」的绑定。所以「等下一次重扫」在代码层面就是恢复路径，并且 09-08 生产已实证。
- 结论：三档分级里它属于第三档。SER-BUSY 档（MarkSourceEventBusy，queued+15s，source_sync.go:957-984）不对：blocked 不会在秒级变回来，事件会每 15 秒空转，10 分钟 busy 宽限（:1147）到期后计入 pending → readyz 503、五流闸 EVENTS_PENDING → 用户全挂，只是换了个名字。dead 档不对：判死本身就是 readyz 的 fail-closed 闩（runtime.go:799-800、:765-766），而且 8×5 分钟等于 40 分钟内必死，而重投递间隔是 6 小时对账（agents/sourceagent/runner.go:711-722）——「先死再等」在所有现实时序下都发生。waiting_dependency 档对：它已经有「精确唤醒 + 12h 兜底 + 不计 Pending/Dead + attempt_count 退回」的完整机制（source_sync.go:897-955、:986-1035；RunOnce :240-247；认领谓词 :686-690 会在 next_attempt_at 到期时把 waiting 行再认领一次）。

二、第三态的写入（新方法 MarkSourceEventAwaitingScanCycleRedelivery，source_sync.go）
1. BEGIN（READ COMMITTED，与同族方法一致）；SELECT ... FROM source_ingest_events WHERE pk AND processing_status='processing' AND lease_token=$ FOR UPDATE，不命中返回 ErrConflict（与 MarkSourceEventBusy :972-983 同形）。
2. 复核绑定：逐字嵌入 claimBindingSelect（与 ingest_requeue_dead_repair.go:479-499 相同做法），取「下一次认领会交出的绑定」。若它是有效的（schema 3.0、映射 hash 等于事件 hash、周期状态在 {receiving,processing,published}）且 batch_id 不等于本次被拒的 claim.BatchID，说明在本次认领之后已有新映射落库——直接 UPDATE 为 queued、attempt_count=greatest(attempt_count-1,0)、processing_error='SCAN_CYCLE_REDELIVERED'、next_attempt_at=now()-1s，返回 outcome='requeued'。
3. 否则 UPDATE 为 waiting_dependency、dependency_kind='source_scan_cycle'、dependency_key_hmac=$key、attempt_count=greatest(attempt_count-1,0)、processing_error='WAITING_DEPENDENCY'、next_attempt_at=$next(+12h)、lease 清空。
4. 账号级诚实：hintAccountID 为空则本方法拒绝（返回 error，RunOnce 只在 hint 非空时进入本分支），保证「等待中的孤儿一定有其账号冻结」；hint 非空时先查 eligibility_freezes 是否已有 status='open' AND freeze_reason='SOURCE_GAP' AND trigger_object_type='source_ingest_event' AND trigger_object_id=event_id 的行，没有才调 freezeEligibilityTx(account,'',SOURCE_GAP,'source_ingest_event',eventID,payloadHash)（consumption.go:2372-2412）。先查再冻是因为 freezeEligibilityTx 在 ON CONFLICT 无操作时仍会无条件写审计（:2405）并作废预留（:2399），12h 兜底重入时不该每次重复。
5. 写审计 source_ingest_event.awaiting_redelivery（source、stream、entity_type、被拒的 batch_id/scan_cycle_id/cycle_status、attempt、外部账号 id；不含载荷），与 source_ingest_event.dead（source_sync.go:822-832）同形。
6. COMMIT，返回 outcome。不调 tryPublishEconomicScanCyclesTx：孤儿的唯一映射在 blocked 周期，tryPublish 只评估 processing 周期（consumption.go:686-691），无事可做。

三、谁唤醒、条件是什么
- 主唤醒：CommitSourceBatch 在为该事件写入映射（source_sync.go:437-446）之后，当 in.SchemaVersion=='3.0' 且 in.ProjectionStatus<>'blocked'（代理自报 blocked 时周期以 blocked 插入，:366-372，重投进 blocked 周期不是有效绑定，不唤醒），按主键 UPDATE：WHERE pk AND processing_status='waiting_dependency' AND dependency_kind='source_scan_cycle' → queued、processing_error=NULL、dependency 列清空、next_attempt_at=now()-1s；RowsAffected=1 时写审计 source_ingest_event.redelivery_woken（新 scan_cycle_id、batch_id、sequence）。不经 HMAC 匹配：CommitSourceBatch 在 postgresstore 层没有 keyring，而主键就是精确的；dependency_key_hmac 仍按 application 层 dependencyKey('source_scan_cycle', sourceID, streamID+'\n'+eventID)（source_processor.go:288-290）计算写入，满足 CHECK 并保留 RequeueSourceDependency(kind,key) 这条通用唤醒。
- 唤醒后：认领用 CLAIM-BINDING 选到新周期的绑定（source_sync.go:626-662 preference 1），verifier 通过周期检查。
- 兜底：既有的 12h（sourceDependencyFallbackRetry，source_processor.go:128）；认领谓词 :688 `processing_status='waiting_dependency' AND next_attempt_at<=$1` 已覆盖，认领时 :718-720 清 dependency 列。兜底重入若仍无有效绑定 → 同一分支再等 12h，attempt_count 仍为 0，不重复冻结、不重复审计冻结。

四、丢失唤醒的封堵（并发形状）
- 竞态：事件已被认领（processing，绑定=C1 blocked）时 C2 映射到达，主唤醒的 WHERE 不匹配（status=processing）→ 随后 RunOnce 失败把事件置为等待 → 若无复核，要等 12h。
- 封堵：Mark 先对 sie 行 FOR UPDATE，再读绑定（READ COMMITTED，只见已提交映射），再 UPDATE。三种交错：(a) Mark 先提交 → CommitSourceBatch 的唤醒 UPDATE 排在行锁后，EvalPlanQual 重读到 waiting → 唤醒；(b) CommitSourceBatch 先提交 → Mark 复核看见 C2 有效绑定 → 直接 requeued；(c) CommitSourceBatch 已插映射、唤醒 UPDATE 在 Mark 的行锁上等待 → Mark 看不见未提交映射、写 waiting、提交 → 唤醒 UPDATE 继续 → 唤醒。无死锁：Mark 不取流级 advisory 锁（source_sync.go:298），CommitSourceBatch 只在 sie 行锁上等 Mark。
- 可测性：Store 上加一个包内测试钩子字段（nil 即无操作）挂在「取行锁之后、读绑定之前」，让测试确定性地把 CommitSourceBatch 卡在 (c) 的位置（判据取 pg_stat_activity 里两个后端同时 wait_event_type='Lock' 的峰值），而不是 sleep。

五、会不会无限等待，出口是什么
- 会：balance_checkpoint 的 event id 折入快照哈希，后续扫描产生不同 event id（ingest_unreplayable_acknowledge.go:29-33）；上游行被删也不会再来。这是有意为之：无限等待的代价是「一个账号冻结 + 一行 waiting」，而不是「全实例停摆」。
- 出口：(1) 代理重投递（自动）；(2) 12h 兜底（自动，每次留一条 Warn 日志与审计）；(3) ingest-acknowledge-unreplayable 扩到 waiting_dependency AND dependency_kind='source_scan_cycle'（ingest_unreplayable_acknowledge.go:97-108 的 dead-only SELECT 放宽；护栏 ReplayBlocked 不变 :126）；(4) 冻结走既有 admin MFA 解冻（eligibility_operations.go:218）。可见面：账号冻结队列（admin 已有）、审计行、日志、SourceHealth 新增 awaiting_redelivery 计数。

六、就绪判定怎么看它
- readyz：SourceIngestHealth 的 Pending 只数 queued/failed/processing，Dead 只数 dead，Waiting 单独计且 validateSourceIngestRuntimeReadiness 不看它（source_sync.go:1037-1049；runtime.go:795-811）；sourceReadinessHealthQuery 的 active_event_health 只扫 queued/failed/processing/dead（:1225-1236），由 0013 的部分索引覆盖，waiting 行不进索引 → 保持 index-bound。
- 流级 Ready / 五流闸：evaluateSourceStreamHealth 只把 PendingEvents/DeadEvents 变成原因（:1119-1124），WaitingDependencies 不产生原因；ListFundingLots/assertSourceFreshTx 读的是同一判定（:1409-1461）→ 其他账号照常开票。不新增任何 Reason，所以 runtime.go:677-680 的两个白名单不用动（避免「规则改了、调用点没改」导致 ready 流被判 inconsistent 的陷阱）。
- 唯一被挡的是被冻结的那个账号：Submit 的 SQL 要求 eligibility_status='active'（postgresstore/store.go:503-509、requests.go:352-357）。

七、为什么冻结而不是 not_invoiceable_pending_reconciliation
- 不冻结不行：等待期间该账号少一条用量事实 → 可开票额度被高估 → 真钱风险；今天的设计用「整条流不就绪」兜这个窗口，我把范围缩到账号，就必须在同一事务给账号上锁。
- 自清态（enterPendingReconciliationTx，consumption.go:2448-2479）全仓只以 'UNKNOWN_NEGATIVE_BALANCE' 从余额评估路径进入（:1228/:1393/:4187/:4199、queue_narrow_repair.go:371），是验收线 2026-09-03 对设计 3(A) 的裁定范围；从 ingest 层新开入口需要重新裁定。冻结则有同层精确先例：MarkSourceEventFailed 判死时用 hintAccountID 开 EVENT_DEAD（source_sync.go:846-882）。先按先例做，自清态作为后续提案。

八、为什么复用 SOURCE_GAP 而不新增 freeze_reason
- 语义吻合：前端标签「来源事实或日志出现完整性缺口」（web/src/App.tsx:2975）；这确实是「已知存在的来源事实不在账本里」。
- 新 reason 要改六处枚举（0016:172-185 CHECK、0020:37-41 CHECK、httpapi/accounts_ledger.go:72-90、web/src/types.ts:162、web/src/lib/http-api.ts:541、App.tsx:2977）——正是「枚举散落多处」的病。
- 用独一无二的 trigger_object_type='source_ingest_event' 与既有修复工具隔离：eligibility_repair.go:123 只扫 ('usage','credit')，balance_anchor_repair.go:94/127 只扫 balance 系 trigger，都不会碰到。
- 附带 source_revision_hash=payload_hash：若事件以后因别的原因 failed/dead，tryPublish 的 ef 关联（consumption.go:729-732）会视其为不吊周期，是既有语义的正向副作用。

### 代码改动

- `K:/发票/wt-XM-INV-RC104/backend/internal/postgresstore/consumption.go`：verifyFactBatchContextTx（:995-1030）：新增导出类型 ScanCycleBindingBlockedError{ScanCycleID, CycleStatus string}，Error() 说明「binding scan cycle <id> is '<status>'」，Unwrap() 返回 domain.ErrConflict；:1024-1027 拆成两段：storedHash!=revision 或 !storedWatermark.Equal(watermarkAt) 仍返回裸 domain.ErrConflict；cycleStatus 不在 {receiving,processing,published} 时返回 &ScanCycleBindingBlockedError{scanCycleID, cycleStatus}。
  - 为什么：RunOnce 今天无法把「周期 blocked」与 UNIT_MISMATCH(:1691)/verifyFactTrustTx(:984)/SOURCE_GAP(:1759)/EVENT_PAYLOAD_DRIFT(:1783) 的同一个 ErrConflict 哨兵区分开；Unwrap 到 ErrConflict 让 claim_binding_integration_test.go 与 ValidateEconomicFactContext 的既有 errors.Is 断言继续成立。
- `K:/发票/wt-XM-INV-RC104/backend/internal/postgresstore/source_sync.go`：新增 const SourceDependencyScanCycle = "source_scan_cycle" 与单一切片 sourceDependencyKinds（含既有五种 + 新种），isSourceDependencyKind()；MarkSourceEventWaitingDependency（:898）与 RequeueSourceDependency（:987）两处手写白名单改读该切片。
  - 为什么：同一集合现在钉在两段 Go 代码 + 一条 SQL CHECK 里（0009:99-106），加一种就是三处；仿 transientRequeueMarkers（:1157-1170）做成物理上一处，并配 T5 的发现型测试对照数据库约束。
- `K:/发票/wt-XM-INV-RC104/backend/internal/postgresstore/source_sync.go`：新增 (s *Store) MarkSourceEventAwaitingScanCycleRedelivery(ctx, claim SourceEventClaim, keyHMAC string, next time.Time, hintAccountID string, blocked ScanCycleBindingBlockedError) (outcome string, err error)：校验 key/next/hint 非空；BEGIN；对 sie 行 FOR UPDATE（lease 校验）；调用包内钩子 s.testHooks.afterAwaitingScanCycleLock（nil 则跳过）；用逐字嵌入的 claimBindingSelect 复核下一次认领的绑定，若有效且 batch_id != claim.BatchID → UPDATE 为 queued/attempt-1/'SCAN_CYCLE_REDELIVERED'/next_attempt_at=now()-1s，outcome='requeued'；否则 UPDATE 为 waiting_dependency/dependency_kind='source_scan_cycle'/key/attempt-1/'WAITING_DEPENDENCY'/next；查无同 trigger 的 open SOURCE_GAP 冻结则 freezeEligibilityTx(hintAccountID,'','SOURCE_GAP','source_ingest_event',claim.EventID,claim.PayloadHash)；写审计 source_ingest_event.awaiting_redelivery；COMMIT；outcome='waiting'。Store 结构体加 testHooks struct{afterAwaitingScanCycleLock func()}（仅测试设置）。
  - 为什么：这是第三态的唯一写入点，把「退预算、等待、账号级诚实、丢失唤醒复核」放在一个事务里；hint 为空即拒绝，是「等待中的孤儿一定被冻结」这条不变量的物理保证。
- `K:/发票/wt-XM-INV-RC104/backend/internal/postgresstore/source_sync.go`：CommitSourceBatch 事件循环（:411-446）：在 :437-446 的映射 INSERT 之后，若 in.SchemaVersion=='3.0' && in.ProjectionStatus!='blocked'，执行 UPDATE source_ingest_events SET processing_status='queued',processing_error=NULL,dependency_kind=NULL,dependency_key_hmac=NULL,next_attempt_at=now()-interval '1 second',updated_at=now() WHERE (pk) AND processing_status='waiting_dependency' AND dependency_kind='source_scan_cycle'；RowsAffected=1 时 writeAudit('source_ingest_event.redelivery_woken', {scan_cycle_id,batch_id,sequence})。
  - 为什么：这是精确唤醒。放在映射 INSERT 之后，使第四节分析的三种交错都以行锁顺序收敛；不动 catchup_key_hmac；ProjectionStatus='blocked' 的周期不是有效绑定，唤醒只会空转。
- `K:/发票/wt-XM-INV-RC104/backend/internal/application/source_processor.go`：RunOnce（:166-283）：把 :250-255 的 hint 提取上移到 dependency 分支之前；在 *sourceDependencyWait 分支（:240-247）之后、泛型分支之前新增：var blocked *postgresstore.ScanCycleBindingBlockedError; if errors.As(processErr,&blocked) && hintAccountID != "" { key := p.Service.dependencyKey(postgresstore.SourceDependencyScanCycle, claim.SourceInstanceID, claim.StreamID+"\n"+claim.EventID); outcome, err := store.MarkSourceEventAwaitingScanCycleRedelivery(ctx, claim, key, now().Add(sourceDependencyFallbackRetry), hintAccountID, *blocked); 失败 recordIsolated；成功 logScanCycleRedeliveryWait(logger, claim, *blocked, outcome)（Warn，仅 id/周期/状态/attempt/outcome，不含载荷）; processed++; continue }。新增 logScanCycleRedeliveryWait，与 logTransientRequeue（:74-84）同形。
  - 为什么：规则与调用点分离在两段代码里，这里是调用点；顺序放在 40001 之后是因为 40001 可能包裹在任何位置，放在泛型之前是本角度的全部意义。hint 为空时刻意落回今天的泛型路径，宁可保留旧行为也不制造未冻结的孤儿。
- `K:/发票/wt-XM-INV-RC104/backend/internal/application/source_processor.go`：processPaymentAdjustment（:1132-1195）：:1148-1151 的 ValidateEconomicFactContext 出错时，若 errors.As 命中 ScanCycleBindingBlockedError，则先调 s.verifiedExternalAccount(ctx, claim, payload.ExternalUserID) 懒解析账号；解析成功返回 wrapWithAccountHint(account.ID, err)，解析失败（含 waitForDependency）返回原 err。
  - 为什么：payments 流的 v3 退款调整在 verifier 之前没有解析账号（账号解析在 :1165），是四类经济事实里唯一 hint 缺失的路径；缺失的退款会让额度被高估，方向危险，所以要补 hint 而不是无冻结地等待。
- `K:/发票/wt-XM-INV-RC104/backend/internal/postgresstore/ingest_unreplayable_acknowledge.go`：AcknowledgeUnreplayableIngestEvent：:97-108 的 SELECT 谓词改为 processing_status='dead' OR (processing_status='waiting_dependency' AND dependency_kind='source_scan_cycle')；ErrNoRows 文案改为「no dead or scan-cycle-waiting row」；后续 UPDATE 已清 dependency 列（:138 注释处），无需改；ReplayBlocked 护栏（:126）不动。类型注释与 cmd/eligibility-repair/main.go:87-106 的帮助文本同步。
  - 为什么：balance_checkpoint 孤儿永远等不到重投递，写掉是它唯一出口；护栏不变保证仍只写掉「无有效绑定」的行。
- `K:/发票/wt-XM-INV-RC104/backend/internal/postgresstore/source_sync.go`：SourceStreamHealth 增加 AwaitingRedelivery int64 `json:"awaiting_redelivery"`；SourceHealth（:1334-1347）的聚合增加 count(sie.event_id) FILTER (WHERE sie.processing_status='waiting_dependency' AND sie.dependency_kind='source_scan_cycle')。不改 sourceReadinessHealthQuery、不改 evaluateSourceStreamHealth。
  - 为什么：给运维一个不靠翻审计的计数；SourceReadinessHealth 的注释（:158-160）明确 waiting 计数不进 readyz 查询以保持 index-bound，遵守之。
- `K:/发票/wt-XM-INV-RC104/backend/migrations/0032_scan_cycle_redelivery_wait.sql`：ALTER TABLE source_ingest_events DROP CONSTRAINT source_ingest_events_dependency_pair, ADD CONSTRAINT source_ingest_events_dependency_pair CHECK (...) —— 与 0009:99-106 逐字相同，只在 dependency_kind IN (...) 列表末尾加 'source_scan_cycle'。头注释写明：无新表、无新列、无数据变更、只放宽约束。
  - 为什么：0009:100-102 的 CHECK 会拒绝新 kind；部分索引 source_ingest_events_dependency_wait_idx（0009:109-111）已按 dependency_kind 前缀覆盖，无需新索引。
- `K:/发票/wt-XM-INV-RC104/docs/ELIGIBILITY-OPERATIONS.md、docs/PRODUCTION-RUNBOOK.md、docs/SOURCE-SYNC-PROTOCOL.md`：ELIGIBILITY-OPERATIONS.md:622-648「When the replay binding is blocked」改为：事件现在停在 waiting_dependency/source_scan_cycle 并冻结其账号，出口是重投递自动唤醒或 ingest-acknowledge-unreplayable；PRODUCTION-RUNBOOK.md:2163-2172「其未完成事件永远无法再投影」改写；SOURCE-SYNC-PROTOCOL.md:438-441 增加第六种 dependency kind 及其唤醒方（CommitSourceBatch 主键唤醒）。httpapi/accounts_ledger.go:76 的 SOURCE_GAP 描述可改为「上游数据流缺口（等待自愈或人工处置）」（可选）。
  - 为什么：手册今天把 blocked 周期上的孤儿当作「设计如此」的永久死信；不改文档，下一个值班的人会按旧手册手工判死。

### 防住什么
- 周期被 supersede 后，其未完成事件被认领 → verifier 拒绝 → 泛型 PROJECTION_FAILED → 8×5 分钟判死 → SourceIngestHealth.Dead>0 闩死 readyz（runtime.go:799-800）→ 流级 EVENTS_DEAD → 五流 AND → 该来源实例全部用户 source_unavailable（生产 09-07 08:32 那一步）。本角度后：事件进入 waiting，Dead 与 Pending 都为 0，readyz 200，其他用户照常。
- 确定性拒绝白白烧掉 8 次预算并每 5 分钟对 blocked 周期行 FOR SHARE 争锁（consumption.go:1013-1018）：现在 attempt_count 退回、12 小时才重认领一次。
- 重投递到达后仍需人工 --kind=ingest-requeue-dead：CommitSourceBatch 的主键唤醒使其自动进入队列，CLAIM-BINDING 随即选到新绑定。
- 丢失唤醒（事件在租约中时新映射到达）：标记等待前的绑定复核 + 行锁顺序保证三种交错都收敛为 queued。
- 孤儿事件所属账号在等待期间以高估额度开票：同一事务开 SOURCE_GAP 冻结，Submit 的 eligibility_status='active' 门（postgresstore/store.go:503-509）把它挡住。
- 「blocked 被拒」在库里与其他失败不可区分（processing_error 恒为 PROJECTION_FAILED，SER-BUSY follow-up 2）：现在有 dependency_kind='source_scan_cycle'、审计行 source_ingest_event.awaiting_redelivery、SourceHealth.awaiting_redelivery 计数与 Warn 日志。
- 把 dependency kind 集合从三处手写（source_sync.go:898、:987、0009:99-106）收成一处切片 + 发现型测试，加第七种时不会再漏一处。

### 防不住什么
- 重投递绑定的 scan_ceiling_at 晚于冻结的 observed_at+5min（validateFactMetadata，consumption.go:322-326，在 verifier 之前就拒绝）：唤醒后事件以新绑定被认领 → 元数据校验失败（字符串错误，不是本类型）→ 泛型 → 8 次 → dead → 闩。生产 09-08 那两条 usage 事件正是这个形状，本角度对它们无效；整条链只是从「blocked 判死」后移到「陈旧观测判死」，时间上多活一个重投递间隔。必须与 observed_at/ceiling 角度一起落地，否则 09-07 的事故照样复发，只是晚 1.5 小时。
- 周期被 supersede 这件事本身（37 分钟 processing 停滞判据，source_sync.go:528-579；readyz 宽限与生存上限共用一个值）。本角度只处理后果，不减少孤儿的产生。
- 其他确定性失败仍走泛型 8×5 分钟：UNIT_MISMATCH（consumption.go:1691）、verifyFactTrustTx 清单不符（:984）、EVENT_PAYLOAD_DRIFT（:1783）、无映射行的 ErrForbidden（:1019-1021）、解密/JSON 失败——它们判死后仍会闩 readyz。
- 没有账号提示的事件：cutover_manifest 事件、未绑定用户的 payment adjustment（verifiedExternalAccount 返回 waitForDependency）落回今天的泛型路径；这是刻意的（宁可旧行为也不制造未冻结孤儿），但对这些事件没有改善。
- balance_checkpoint 孤儿永远等不到重投递（event id 折入快照哈希，ingest_unreplayable_acknowledge.go:29-33）：本角度不短路它，只把它从「40 分钟判死闩全站」变成「一个账号冻结 + 每 12 小时一条 Warn，直到人工写掉」。
- 被冻结账号在事件落地后不会自动解冻，仍要 admin MFA 解冻（ResolveEligibilityFreeze，eligibility_operations.go:218），与 EVENT_DEAD 今天一致；自动解冻或改用自清态需要验收线对设计 3(A) 入口范围重新裁定。
- 唤醒后事件 created_at 仍是首送时间：它在 queued 到 processed 之间的几秒到几十秒内会让 validateSourceIngestRuntimeReadiness 的 15 分钟 oldest-pending（runtime.go:807-809）与流级 EVENTS_PENDING 短暂判不就绪——与今天任何 RequeueSourceDependency 唤醒同一属性（09-04 CATCHUP-BURST 就是它），不在本角度范围。
- 若事件因唤醒后的元数据失败进入 failed 并被再次映射到活动周期 C2，它会吊住 C2 直到 EVENT_DEAD 冻结出现（8 次后），期间 C2 可能被 supersede，形成新一轮孤儿——这些孤儿会进入等待而非判死，但根因仍是上一条。
- readyz 对等待中的孤儿完全不可见——告警面要从「readyz 503」改成「冻结队列 / 审计 / Warn 日志 / awaiting_redelivery 计数」，否则一个永远等不到重投递的事件可能没人注意。
- 代理侧任何东西：不改 shouldAbandonLegacyReconcileCycle、不改 ReconcileWindowBounded 粘性、不改重扫窗口；重投递是否发生仍取决于代理的对账窗口是否覆盖该行。

### 数据完整性风险
- 冻结开在错误账号：hintAccountID 来自 verifiedExternalAccount（source_processor.go:657/679）在 verifier 之前解析，与 EVENT_DEAD 的 hint 路径（source_sync.go:861-876）同源，可靠；payment adjustment 为懒解析。错账号的后果是误冻一个可解冻的账号，不会造成超开票。
- 复用 SOURCE_GAP：必须验证既有修复工具不会批量解冻它——eligibility_repair.go:123 只扫 trigger_object_type IN ('usage','credit')，balance_anchor_repair.go:94/127 只扫 balance 系 trigger；本设计用 'source_ingest_event'，T6 里加变异断言（把 trigger 改成 'usage' 就要红）。
- freezeEligibilityTx 无条件作废该账号预留（consumption.go:2399）：等待期间该账号已有的开票预留会被作废，这是既有冻结语义，对账本是保守方向；先查 open 冻结再冻的保护只避免 12h 重入时重复作废。
- tryPublish 完整性判据（consumption.go:716-733）不变：waiting 仍算不完整。孤儿只映射在 blocked 周期，不吊活动周期；丢失唤醒由复核封堵；若仍有 waiting 事件因未知原因挂在活动周期上，37 分钟后被 supersede——与今天同，不更糟。
- 隔离级别：Mark 与 CommitSourceBatch 都是 READ COMMITTED + 行锁（与 :957-984、:289-298 同族）；投影事务是 Serializable（consumption.go:1683）但不读写 sie 行；CommitSourceBatch 的唤醒 UPDATE 与投影的 FOR SHARE OF c 不在同一张表上，不新增 40001 面。
- 幂等：唤醒 UPDATE 带状态谓词，重放同一批次（Duplicate 分支 :320-329 提前返回）不会二次唤醒；freezeEligibilityTx 靠 eligibility_freezes_one_open_trigger 部分唯一索引（0009:530-532）幂等；Mark 的 lease_token 谓词防止过期租约写回。
- 唤醒把事件置为 queued 且 next_attempt_at=now()-1s：若部署到 CLAIM-BINDING 之前的版本（RC104 之前），认领仍用 first_batch_id → 再次 blocked → 再等 12h，无限但无害；生产已是 RC104。
- 冻结的 source_revision_hash=payload_hash 使 tryPublish 的 ef 关联把这个事件将来若 failed/dead 时视为不吊周期（consumption.go:729-732）：这让 C2 在极端情况下仍能发布，但意味着 C2 的水位声明「完整」时该账号的事实缺失——由该账号的冻结兜住，与 EVENT_DEAD+冻结的既有语义完全一致。
- 不涉及金额；所有时间用库内 now()/UTC（next_attempt_at 由 Go 侧 now().Add(12h) 传入，与既有 waiting 路径同法）。
- 回滚窗口：见 rollback——旧代码会在 12h 兜底时把等待行判死并闩死，回滚前必须先清空等待行。

### 迁移
新增 backend/migrations/0032_scan_cycle_redelivery_wait.sql，只做一件事：DROP CONSTRAINT source_ingest_events_dependency_pair 再 ADD 同名 CHECK，dependency_kind 白名单在 0009:101 的五种基础上追加 'source_scan_cycle'，其余逐字不变（waiting_dependency/parked_identity 必须带 kind+HMAC，其他状态必须为 NULL）。不新增表、列、索引、数据（部分索引 source_ingest_events_dependency_wait_idx 0009:109-111 已按 dependency_kind 前缀覆盖；readyz 的 0013 部分索引不含 waiting，保持 index-bound）。无新表 → 不需要重放 compose permissions 作业（RC101 教训只适用于新表）；无新 compose 变量 → verify.ps1 不动。仅前向：先于代码应用；旧代码在新约束下行为不变（从不写该 kind）。同时 Go 侧 sourceDependencyKinds 切片是同一集合的代码副本，T5 用 pg_get_constraintdef 发现式对照，防止两处漂移。不新增 freeze_reason（复用 SOURCE_GAP），所以 0016/0020 的两条 CHECK 与前端三处枚举都不动。

### 测试计划

- **T1 backend/internal/application/scan_cycle_wait_integration_test.go: TestSourceProjectionGradesBlockedCycleBindingAsRedeliveryWait —— 按 source_serialization_busy_integration_test.go:1-230 的夹具建 v3 usage 事件 E 落在周期 C1（CommitSourceBatch，ScanComplete=true）；再提交一个不同 scan_cycle_id、无事件、Now=+1h、StaleActiveScanCycleMaxAge=1m 的批次触发真实 supersede（同 ingest_requeue_dead_repair_integration_test.go:107-145），断言 C1 cycle_status='blocked'；RunOnce 一次。断言：processing_status='waiting_dependency'、dependency_kind='source_scan_cycle'、dependency_key_hmac 匹配 ^h1:[0-9a-f]{64}$、attempt_count=0、processing_error='WAITING_DEPENDENCY'、next_attempt_at=now+12h；eligibility_freezes 恰有一行 status='open' AND freeze_reason='SOURCE_GAP' AND trigger_object_type='source_ingest_event' AND trigger_object_id=E AND source_revision_hash=payload_hash；source_account_eligibility_state.eligibility_status='frozen'；审计 source_ingest_event.awaiting_redelivery 一行且载荷含 scan_cycle_id=C1、cycle_status='blocked'。控制臂（同一表驱动）：a) C1 不 supersede、用 P0001 触发器注入 → 'failed'/PROJECTION_FAILED/attempts=1；b) C1 不 supersede、把账号的 cutover_manifest_hash 改成别的值让 verifyFactTrustTx 返回 ErrConflict（consumption.go:984）→ 仍 'failed'/PROJECTION_FAILED/attempts=1 且开的是 UNIT_MISMATCH 冻结。**：证明 规则（verifier 的新类型）与调用点（RunOnce 新分支）被同一条路径穿过；分级只对「周期状态」这一种 ErrConflict 生效，其他 ErrConflict 仍走泛型；账号级诚实在同一事务里成立。
  - 变异必红：(1) 删掉 RunOnce 新分支 → 主臂 status='failed'、attempts=1 → 红；(2) 让 verifyFactBatchContextTx 的周期分支回退为裸 domain.ErrConflict → 红；(3) 把 RunOnce 分支改成 errors.Is(processErr, domain.ErrConflict) → 控制臂 b 变成 waiting → 红；(4) 删掉 Mark 里的 freezeEligibilityTx 调用 → 冻结/frozen 断言红；(5) 把 attempt_count=greatest(attempt_count-1,0) 改成不退回 → attempts=1 → 红。
- **T2 backend/internal/postgresstore/scan_cycle_wait_readiness_integration_test.go: TestScanCycleWaitLeavesReadinessAndOtherAccountsUntouched —— 用 deadIngestFixture 建两个账号 A/B 同一来源；A 的事件按 T1 进入等待（直接调 MarkSourceEventAwaitingScanCycleRedelivery，用 claimFor 取到的 claim）；断言 SourceIngestHealth().Pending=0 且 Dead=0；SourceReadinessHealth(policy) 后对 usage 流 item.Ready=true、Reasons 不含 EVENTS_PENDING/EVENTS_DEAD；validateSourceIngestRuntimeReadiness 与 validateSourceRuntimeReadiness（cmd/api 包的纯函数，在 cmd/api 测试里以同样夹具数据构造 report）返回 nil；在一个事务里调 assertSourceFreshTx(tx, sourceID, policy) 返回 nil；A 的 eligibility_status='frozen'，B 仍 'active'。**：证明 第三态对 readyz 的两道 dead 闩、流级 Ready、五流闸都不可见；只有 A 被挡。
  - 变异必红：(1) 把 Mark 写入的状态改成 'failed' 或 'dead' → Pending/Dead 非 0、item.Ready=false、assertSourceFreshTx 返回 ErrSourceUnavailable → 红；(2) 让 Mark 对所有账号调 freezeSourceAccountsTx（模拟错误放大）→ B 变 frozen → 红；(3) 删掉冻结 → A 仍 'active' → 红。
- **T3 postgresstore: TestCommitSourceBatchWakesScanCycleWaitOnRedelivery —— 从 T2 的等待态出发，CommitSourceBatch 提交新周期 C2、含同一 event_id/payload_hash（deadIngestFixture.commitEvents）；断言：processing_status='queued'、dependency 列 NULL、processing_error NULL、next_attempt_at<=now()、C2 映射行存在、审计 source_ingest_event.redelivery_woken 一行且 scan_cycle_id=C2；随后 ClaimUnprocessedSourceEvents 认领到 E 且 claim.ScanCycleID=C2、claim.BatchID=C2 批次。反向控制：同样的重投递但 ProjectionStatus='blocked'（周期以 blocked 插入）→ E 仍 waiting、无唤醒审计。再控制：重投递一个*不同* event_id → E 仍 waiting（唤醒是按主键的，不是按流）。**：证明 精确唤醒存在、只在有效周期下触发、唤醒后认领选到新绑定（与 CLAIM-BINDING 接上）。
  - 变异必红：(1) 删掉 CommitSourceBatch 的唤醒 UPDATE → E 仍 waiting → 红；(2) 删掉 ProjectionStatus!='blocked' 守卫 → 反向控制里 E 被唤醒 → 红；(3) 把唤醒 WHERE 去掉 event_id（改成按流）→ 第二个控制里 E 被唤醒 → 红。
- **T4a postgresstore: TestScanCycleWaitRequeuesWhenNewerValidBindingAlreadyCommitted（顺序）—— 先 claimFor 取到 E 的 claim（绑定 C1 blocked），然后提交 C2 含 E（此时 E 在 processing，唤醒 UPDATE 不命中），再调 MarkSourceEventAwaitingScanCycleRedelivery(claim…)；断言 outcome='requeued'、status='queued'、processing_error='SCAN_CYCLE_REDELIVERED'、attempt_count=0、无新冻结、无 awaiting 审计。**：证明 标记等待前的复核规则本身。
  - 变异必红：删掉复核分支 → outcome='waiting'、status='waiting_dependency' → 红；把复核条件里的 batch_id != claim.BatchID 去掉 → 用「C1 仍是唯一绑定」的对照子用例（无 C2）会得到 requeued → 红。
- **T4b postgresstore: TestScanCycleWaitAndRedeliveryInterleaveConverge（并发、确定性）—— 设置 store.testHooks.afterAwaitingScanCycleLock：钩子内启动 goroutine 跑 CommitSourceBatch(C2 含 E)，然后用 until-loop 轮询 pg_stat_activity 直到出现一个后端 state='active' AND wait_event_type='Lock' 且 query 含 'source_ingest_events'（判据是「同时在场峰值」：Mark 持锁 + Commit 在等锁），再让钩子返回；主 goroutine 调 Mark；join 后断言 Mark 的 outcome='waiting'（证明它确实没看到未提交的 C2），最终 status='queued'（证明唤醒在 Mark 提交后接住了），redelivery_woken 与 awaiting_redelivery 审计各一行。**：证明 第四节的交错 (c) 真实发生且收敛；不是靠 sleep 碰运气。
  - 变异必红：(1) 删掉唤醒 UPDATE → 最终 'waiting_dependency' → 红；(2) 把 Mark 的行锁 FOR UPDATE 改成无锁 SELECT → 钩子处轮询永远等不到 Lock 等待（超时失败）→ 红；(3) 把钩子调用移到 UPDATE 之后 → Commit 不再等锁，轮询超时 → 红。
- **T5 postgresstore: TestSourceDependencyKindsMatchDatabaseConstraint —— 读 pg_get_constraintdef(oid) WHERE conname='source_ingest_events_dependency_pair'，用正则抽出 dependency_kind IN (...) 的字面量集合，与 sourceDependencyKinds 做集合相等断言；另断言 MarkSourceEventWaitingDependency 与 RequeueSourceDependency 对切片里每一种 kind 都不返回 'invalid source dependency ...'（用一个不存在的 HMAC 触发到 kind 校验之后的错误即可）。**：证明 三处集合物理上只剩一处 + 一个数据库真相源；范围是发现出来的，不是手列。
  - 变异必红：只在 Go 切片加 'x' → 集合不等 → 红；只在迁移里删掉 'source_scan_cycle' → 红；把 RequeueSourceDependency 的校验改回手写旧列表 → 新 kind 被拒 → 红。
- **T6 postgresstore: TestAcknowledgeUnreplayableAcceptsScanCycleWaitingRow —— 等待态的 balance_checkpoint 孤儿（只有 blocked 绑定）：dry-run 返回 Acknowledged=true 且不改库；apply 后 status='processed'、processing_error='UNREPLAYABLE_BINDING'、dependency 列 NULL、SOURCE_GAP 冻结仍 open；护栏控制：给该事件先按 T3 造出有效绑定（此时它已被唤醒为 queued，不再匹配谓词）→ 返回「no dead or scan-cycle-waiting row」；另一控制：dead 行行为与 ingest_unreplayable_acknowledge_integration_test.go 既有断言完全不变。附带：RepairEligibilityAfterPreAnchorUsage 与 RepairBalanceAnchorEligibility 跑一遍后，该 SOURCE_GAP 冻结仍 open。**：证明 人工出口对第三态可用、护栏未放松、复用 SOURCE_GAP 不会被既有批量修复误解冻。
  - 变异必红：(1) SELECT 谓词改回 dead-only → 第一段报无行 → 红；(2) 把冻结的 trigger_object_type 改成 'usage' → 修复工具把它解冻 → 最后一段红；(3) 删掉护栏 !row.ReplayBlocked → 给一个有有效绑定的 dead 行也能写掉 → 既有测试红。
- **T7 application: TestScanCycleWaitFallbackRetryStaysBudgetNeutral —— T1 之后把 processor.Now 推进 12h+1m 再 RunOnce：事件被再次认领（既有谓词）、verifier 再拒 → 回到 waiting；断言 attempt_count 仍 0、eligibility_freezes 仍恰一行 open、审计 eligibility.frozen.source_gap 计数不变（=1）、awaiting_redelivery 审计计数=2、next_attempt_at 再次 =now+12h。**：证明 无限等待是有界频率、零预算、不重复作废预留/不刷审计；12h 兜底与新分支接得上。
  - 变异必红：删掉「先查 open 冻结再冻」→ freezeEligibilityTx 的无条件审计（consumption.go:2405）让 eligibility.frozen.source_gap 变 2 → 红；把 Mark 的 attempt_count 退回删掉 → 第二轮 attempts=1 → 红。
- **T8 application: TestPaymentAdjustmentBlockedBindingResolvesAccountHintLazily —— v3 payments 流的 refund adjustment 落在被 supersede 的周期；RunOnce 后断言 waiting + 该账号 SOURCE_GAP 冻结；控制：ExternalUserID 未绑定 → 'failed'/PROJECTION_FAILED（落回泛型，明确记录这是刻意的）。**：证明 唯一在 verifier 之前不解析账号的路径也拿到 hint；hint 缺失时不会产生未冻结孤儿。
  - 变异必红：删掉懒解析 → 主臂变 'failed' → 红；把 RunOnce 的 hintAccountID != "" 条件去掉并让 Mark 接受空 hint → 控制臂变 waiting 且无冻结 → 红。

**规模** M。后端约 300 行（verifier 类型 ~25、source_sync 新方法 ~120 含 SQL/注释、CommitSourceBatch 唤醒 ~25、kinds 集中 ~20、SourceHealth 计数 ~10、RunOnce 分支与日志 ~45、payment 懒解析 ~15、acknowledge 谓词 ~10）；迁移约 15 行；测试约 700 行（8 个用例，其中 1 个并发用例需要 Store 测试钩子）；文档 3 处 + CLI 帮助文本。无前端改动。 · **依赖** XM-INV-CLAIM-BINDING（RC104，已在生产）：唤醒后认领必须选到新周期的有效绑定（source_sync.go:626-662），否则永远回到 first_batch_id 的 blocked 绑定，等待变成无限空转。, observed_at / scan_ceiling 角度（validateFactMetadata 的 5 分钟规则对重投递绑定的处理，consumption.go:322-326）：没有它，唤醒后的事件在 verifier 之前就被元数据校验拒绝并走泛型判死，09-07 的链条只是后移。两者必须同一发布或它先于本片。, 迁移 0032 先于代码应用（新 kind 受 0009:99-106 CHECK 约束）。, SER-BUSY 的分级框架与日志约定（source_processor.go:74-84 logTransientRequeue、source_sync.go:1157-1214 markers）：本片的 Warn 日志与「不耗预算」语义沿用其形状，不依赖其代码。, XM-INV-DEAD-REQUEUE 的 ingestRequeueDeadReplayBindingTx（ingest_requeue_dead_repair.go:479-538）与 claimBindingSelect 的逐字嵌入约定：Mark 的复核用同一段 SQL，避免预测与运行时漂移。, 运维告警面调整：告警要从 readyz 503 扩到「冻结队列里 trigger_object_type='source_ingest_event' 的 SOURCE_GAP」与 SourceHealth.awaiting_redelivery，否则永不重投递的孤儿只有 12h 一条 Warn。, 验收线知情（非阻塞）：复用 SOURCE_GAP 与不走自清态是本片的取舍；若负责人要求自清态（enterPendingReconciliationTx 新入口）需对设计 3(A) 范围重新裁定。 · **回滚** 代码级回滚（git revert 本片提交并 roll-forward 旧镜像），迁移 0032 保留——它只放宽 CHECK，旧代码从不写 'source_scan_cycle'，不需要反向迁移。回滚前必须先排空等待行，因为旧的 ClaimUnprocessedSourceEvents 谓词（source_sync.go:688）会在 next_attempt_at（≤12h）到期时认领 waiting_dependency 行，旧 RunOnce 会把它们走泛型判死并闩死 readyz：(1) SELECT event_id FROM source_ingest_events WHERE processing_status='waiting_dependency' AND dependency_kind='source_scan_cycle' 列出；(2) 对有有效绑定的行（ingest-requeue-dead 的 dry-run 报 ReplayBlocked=false）把 next_attempt_at 置为 now() 让新代码先处理完；(3) 对无有效绑定的行用 --kind=ingest-acknowledge-unreplayable 写掉；(4) 确认计数为 0 再回滚。已开的 SOURCE_GAP 冻结与审计行原样保留，由 admin 按既有 MFA 流程解冻，回滚不涉及冻结表。若回滚后又出现 blocked 周期孤儿，行为回到 RC104 现状（判死 + readyz 503 + 人工 requeue/acknowledge）。


---

## 提案：XM-INV-CYCLE-DRAIN-HOLD：后继周期「等排空再接手」（代理侧契约 + 后端只读信号）

**角度**：代理侧——不要在有在途事件时放弃周期

先纠正一个前提：代理侧几乎不存在「放弃周期」这个独立动作。唯一显式的放弃 `shouldAbandonLegacyReconcileCycle`（agents/sourceagent/economics_db.go:191-193）是一次性的——`ReconcileWindowBounded` 只在 :306 置 true、全仓无处置回 false、由 `next := cursor`（:121）粘性携带、`validateStoredFileCursorForStream` 只禁止非 usage 流携带它（state_store_file.go:504-506），所以 09-03 之后它按代码不会再触发（除非 state.json 被重建）。真正常规发生的「放弃」是**每个完成的周期都会换新 id**：`newCycle := Version==0 || Completed`（:264）+ 每个完成周期派生新 `ScanCycleID`（:345），而 Sub2API usage 每分钟一次增量、`Completed=!hasMore`（:122）几乎每轮都完成——于是每一轮轮询都在向后端提交一个「前任仍在投影」的后继周期。后端对这个后继要么 503 忙（前任 `updated_at` 在 37 分钟内），要么 supersede（source_sync.go:362、528-579）；而前任一旦进入 processing，其 `updated_at` 就冻结（同 id 续批的 ON CONFLICT 要求 `final_sequence IS NULL`，:384），投影拖过 37 分钟必被杀。07:08→07:46 正是这条链，与「例行对账」无关。代理无法单方面「确认前任没有在途事件」：协议只有 POST（nginx `limit_except POST`，deploy/nginx/ingest-mtls.conf:39；代理硬性 `INGESTION_ALLOWED_METHODS=POST`，main.go:822-823），请求体与 ACK 体两个方向都是 `DisallowUnknownFields`（receiver.go:302；api_connector.go:266-267 经 signing.go:357），所以唯一向后兼容的只读通道是 HTTP 头，而且代理只有在**发出后继周期的第一页**时才能得到答案。因此本角度的落地形式是一份契约：**后继周期的第一页就是探针；后端在前任「仍在排空」（有未完成事件、且事件在 37 分钟内有进展、且距终页 ≤ 排空上限）时只答 503 忙并用只读头说明原因，绝不 supersede；代理照常等待（runner.go:141-158 已不计入熔断），并把等待变成有名字、有心跳的状态**。上界在后端（两只时钟：进展窗 37 分钟；排空上限默认 3h，低于 6h 对账周期），到点按今天的方式 supersede，但 `supersede_reason` 写明是哪只时钟、审计 before 里附上未完成事件 id，让 RC104 的两个修复工具有明确目标。对账不会无限延后：`modeAt`（runner.go:245-256）在下一次完成前持续返回 ScanReconcile，只是被同一个 hold 推迟 ≤ 排空上限；被扣住的首页在 spool 里每次重发都重新签名（signing.go:320），过期的 ceiling 合法（receiver 只拒未来 ceiling，receiver.go:181；批次 CHECK `scan_ceiling_at<=captured+5m`，0009:26-27；`validateFactMetadata` 要求 ceiling≤observed+5m 而 ceiling 先于 observed 被采集，consumption.go:315-326）。代理侧**不新增任何持久化字段**——state 文件解码是 `DisallowUnknownFields`（state_store_file.go:318），加字段会让回滚后的旧二进制拒绝启动；hold 期间只 `os.Chtimes` 触碰 state.json 的 mtime 让容器 healthcheck（main.go:674-677 只看 mtime）区分「活着在等」与「挂了」。

### 机制

探针 = 后继周期首页（今天已如此）。后端 `supersedeStaleActiveScanCycleTx` 在现有 `stillFresh→busy` 之后新增一段：用从 `tryPublishEconomicScanCyclesTx` 抽出的同一个「未完成」判据（consumption.go:716-731，含 POLICY-ANCHOR 2.5 的 open 冻结逃生口）算出活动周期的 `incomplete`、`last_progress_at=max(sie.updated_at)`（claim/processed/failed/waiting/busy 五条 UPDATE 都写 updated_at：source_sync.go:718-720、744-745、799-800、926-929、970-973）；若 `incomplete>0 && now-last_progress_at<=StaleActiveScanCycleMaxAge && now-active.updated_at<=ScanCycleDrainMaxAge` → 返回带明细的 `*ScanCycleBusyError`（`Unwrap()` 为 `domain.ErrScanCycleBusy`，所有既有 `errors.Is` 调用与测试不变）；否则按今天 supersede，但 reason 改为 `IDLE`（incomplete=0，即 09-03 那种真孤儿行，无事件可孤儿化）/`DRAIN_STALLED`（37 分钟无进展：未冻结的 dead、worker 停摆）/`DRAIN_EXCEEDED`（终页后超过排空上限，典型是 40001 每 15 秒「有进展」的活锁），审计 before 附 `incomplete_events`、`last_progress_at`、前 50 个 event id。receiver 在 503 busy 路径 `errors.As` 取明细写入 `X-Invoice-Active-Scan-Cycle-{Id,Status,Incomplete-Events,Last-Progress-At,Hold-Reason}`，排空 hold 时 `Retry-After: 60`（减少探针对活动周期行 FOR UPDATE 的争用，consumption.go:691 与 source_sync.go:533-536 同一把行锁），普通忙仍为 30。代理 `HTTPIngestClient.Send` 在非 200 时把这些头解析进 `IngestHTTPError.Hold`（uuid/整数/RFC3339 全部有界校验，任一畸形 → Hold=nil 退化为普通忙）；`SyncRunner` 对 `Hold!=nil` 的失败：不计熔断（已有）、`SyncFailure.Hold` 带给 `OnFailure` 打一条 `holding successor cycle … predecessor=… incomplete=… last_progress=… held=…` 日志、调用可选 `Heartbeat` 触碰 state.json mtime；对普通 503/网络失败**不**触碰心跳（那种失败仍应让 healthcheck 变红）。hold 的「since」取后端头里的 `last_progress_at`/活动周期 updated_at 而非代理本地时钟，重启后日志语义不丢且无需持久化。`shouldAbandonLegacyReconcileCycle` 判据不改：在此契约下放弃是安全的——被放弃的 receiving 周期在排空前不会被 supersede，而校验器接受 `receiving`（consumption.go:1027-1029）；且 `SyncPage` 先重放 spool 里前任周期的未 ACK 页再 Scan（coordinator.go:68-86），放弃不会丢页。

### 代码改动

- `backend/internal/postgresstore/consumption.go`：把 tryPublishEconomicScanCyclesTx 内联的「未完成计数」SQL（:716-731）抽成 `scanCycleDrainStateTx(ctx, tx, source, stream, cycleID) (scanCycleDrainState, error)`，返回 Incomplete、LastProgressAt(=max(sie.updated_at) FILTER 未完成)、ManifestRecords、CheckpointRecords、以及最多 50 个未完成 event id；tryPublish 改为调用它（行为不变）。
  - 为什么：supersede 与 publish 必须共用物理上同一个判据，否则两处漂开时 supersede 会把 publish 认为「完整」的周期当成在排空（或反之）而不报错——记忆里「被信任的过期闸」「同一个哈希钉在两处」的教训。
- `backend/internal/postgresstore/source_sync.go`：(1) 新增 `type ScanCycleBusyError struct{ActiveCycleID, ActiveStatus, Reason string; Incomplete int64; LastProgressAt, ActiveUpdatedAt time.Time}`，`Error()` 与 `Unwrap()`（返回 domain.ErrScanCycleBusy）。(2) `SourceBatchInput` 加 `ScanCycleDrainMaxAge time.Duration`（零值=禁用=今天行为，沿用 :181-195 的「零禁用」约定）。(3) `supersedeStaleActiveScanCycleTx`（:528-579）：`stillFresh` 判定之后、UPDATE 之前插入排空判定；supersede 时用三种 reason 常量替换单一 `scanCycleSupersedeReason`（:490，其文案「abandoned by an agent restart」已不成立），每条 ≤256 字符（0022 CHECK）；审计 before payload 追加 incomplete_events/last_progress_at/incomplete_event_ids。(4) :394-399 的 unique_violation 分支保持裸哨兵不变。
  - 为什么：这是契约的服务端半边：只对「已排空(IDLE)/停滞/超上限」的周期腾位，对正在被投影的周期只说忙。有名字的 reason 与事件 id 是上界到达后的恢复路径入口（ingest-requeue-dead / ingest-acknowledge-unreplayable 的 --event 选择）。
- `backend/internal/application/service.go`：`CommitVerifiedSourceBatch` 在 :506 旁把 `ScanCycleDrainMaxAge: s.sourceScanCycleDrainMaxAge` 接进 SourceBatchInput；`Options` 加同名字段。
  - 为什么：与 StaleActiveScanCycleMaxAge 同一接线点，两只时钟在同一处可见。
- `backend/cmd/api/runtime.go`：新增可选环境变量 `SOURCE_SCAN_CYCLE_DRAIN_MAX_AGE`（boundedDurationEnv，默认 3h，范围 0 或 [economicRescanActivityMaxAge+1s, 24h]，0=禁用），在 :244 计算出 economicRescanActivityMaxAge 之后校验并传入 application.Options。不进 compose 必填变量，不改 verify.ps1 的 $productionEnv。
  - 为什么：排空上限必须严格大于进展窗，否则「有进展」分支永远被上限先截断；默认 3h 由 09-07 实测（6842 条释放事件 45 分钟内排空）留 3 倍余量，且低于 6h 对账周期，真卡死当班就能发现。
- `backend/internal/sourceingest/receiver.go`：busy 分支（:203-214）`errors.As(err, &busy)`：命中则写 `X-Invoice-Active-Scan-Cycle-Id/-Status/-Incomplete-Events/-Last-Progress-At/-Hold-Reason` 五个响应头（RFC3339Nano UTC），`Retry-After` 60；未命中（裸哨兵）保持今天的 30 与无头。日志行加 active_cycle/incomplete/reason 字段，仍不记 payload。
  - 为什么：响应头是两个方向都 DisallowUnknownFields 之下唯一向后兼容的只读通道；旧代理忽略未知头，行为与今天完全一致。
- `agents/sourceagent/signing.go`：`IngestHTTPError` 加 `Hold *ScanCycleHold`（ActiveScanCycleID、Status、Incomplete int64、LastProgressAt time.Time、Reason string）；`Send`（:349-355）在非 200 时调用新函数 `parseScanCycleHold(header)`：id 必须匹配 uuidPattern、Incomplete 为 0..10_000_000 的整数、时间 RFC3339 且不晚于 now+5m、Reason ∈ {DRAINING}；任一不合法 → Hold=nil。`Error()` 在 Hold!=nil 时追加 `holding for scan cycle <id> (<n> incomplete)`。
  - 为什么：代理只能通过探针响应得知前任状态；解析必须有界并在畸形时退化为「普通忙」，不让一个坏头改变退避语义。
- `agents/sourceagent/runner.go`：(1) `SyncFailure` 加 `Hold *ScanCycleHold`。(2) `SyncRunner` 加可选 `Heartbeat func(context.Context) error`（nil=no-op）。(3) Run 的失败分支（:126-174）：`busy` 判定不变；若 `ingestErr.Hold!=nil` 则 `failure.Hold=ingestErr.Hold`、调用 Heartbeat（错误只通过 OnScheduleError 风格的回调上报，不影响循环）；等待仍是 max(backoff, RetryAfter) 再抖动。
  - 为什么：把「前任在排空」从匿名 503 变成有名字的状态；心跳只在 hold 时触发，普通失败仍会让 healthcheck 变红，保留原有告警语义。
- `agents/sourceagent/state_store_file.go`：`FileStateStore` 加 `TouchHeartbeat(ctx) error`：在 withProcessLock 内对 s.Path 执行 `os.Chtimes(now, now)`，不读不写内容。**不**给 fileStateEnvelope 加任何字段。
  - 为什么：healthcheck 只看 state 文件 mtime（main.go:674-677）；而 envelope 解码是 DisallowUnknownFields（:318），加字段会让回滚后的旧二进制在启动时「decode agent state file failed」而 fail-closed——这是本设计刻意零持久化的原因。
- `agents/cmd/source-agent-prod/main.go`：runner 接线处（:764-787）加 `Heartbeat: state.TouchHeartbeat`；`OnFailure` 在 `failure.Hold!=nil` 时改打 `holding successor cycle source=%q stream=%q mode=%q predecessor=%q status=%q incomplete=%d last_progress=%q reason=%q retry_in=%q`，否则维持原两条日志。
  - 为什么：运维在事故里读代理日志时需要一眼看出「在等谁、还差多少」而不是一串 transient sync failure。
- `agents/sourceagent/economics_db.go`：仅改 `shouldAbandonLegacyReconcileCycle` 的文档注释（:174-190）：说明它是一次性的、放弃之所以安全依赖接收端的 drain-hold 契约（被放弃的 receiving 周期在排空前不会被 supersede、校验器接受 receiving）、以及 SyncPage 先重放 spool 页的顺序保证不丢页。判据不改。
  - 为什么：判据本身没有可改之处（它无法得知后端状态），但下一位读者必须知道它的安全性靠什么成立——「条件恰好为真」的债要写在它旁边。
- `docs/SOURCE-SYNC-PROTOCOL.md §7 与 docs/PRODUCTION-RUNBOOK.md「blocked 不是稍后再试」段`：协议表加一行：503 SOURCE_SCAN_CYCLE_BUSY 携带 X-Invoice-Active-Scan-Cycle-* 时=前任在排空，代理按 Retry-After 等待、不开熔断、触碰心跳；运行手册改写 2163-2172：supersede 不再终止正在投影的周期，只终止 IDLE/DRAIN_STALLED/DRAIN_EXCEEDED，三种 reason 的处置分别指向哪个工具。
  - 为什么：手册当前把「supersede 杀掉带未完成事件的周期」写成设计如此（2167-2170），必须同步改口，否则下一次值班按旧手册操作。

### 防住什么
- 「新客户绑定释放 6842 条事件 → 前任周期投影拖过 37 分钟 → 代理的下一轮增量/对账后继周期把它 supersede 成 blocked → 事件孤儿化 → 8×5min 判死 → readyz 闩死」：只要前任在 37 分钟窗内还有任何一条未完成事件被 worker 触碰过（claim/failed/busy 都算），后继就只得到 503 忙，前任不会被置 blocked，其事件继续在 receiving/processing 周期下按原绑定投影、最终 published。
- 09-07 的两条 usage（b1de0e2b）与一条 balance_checkpoint（e58b9430）这类「被 supersede 的周期上正在重试的事件」不再产生；supersede 只在 incomplete=0 时是「无痛」的（今天 09-03 那种真孤儿行仍能被腾位，不回退 SUPERSEDE 修复）。
- 代理放弃 legacy 在途对账（或 state 重建后开新周期）时，被放弃的 receiving 周期上已送达、未投影完的事件不再因 supersede 而失去合法绑定。
- 对账被在途周期无限推迟：不可能——hold 由后端上限（默认 3h）封顶，modeAt 在完成前持续选择 ScanReconcile，只是延后不跳过。
- 代理在长时间 hold 中被容器 healthcheck 误判为「挂了」（state.json mtime 5 分钟规则）：hold 迭代触碰心跳；普通失败不触碰，原告警语义保留。
- 任一方向的混版本部署事故：旧代理对新后端=今天行为（忽略头）；新代理对旧后端=今天行为（Hold=nil）；代理 state 文件与 spool 格式零变化，旧二进制可直接回滚。

### 防不住什么
- 排空期间的可用性损失：探针不落库，`last_accepted_at` 只在成功提交时更新（source_sync.go:448），5 分钟后 STREAM_STALE；前任 processing 行的 updated_at 冻结于终页，37 分钟后 ECONOMIC_RESCAN_ACTIVE 宽限失效变成 ECONOMIC_WATERMARK_STALE（source_sync.go:1112-1118）——流不就绪，五流 AND 闸（service.go:611-625）让整个来源实例开不了票，直到排空完成（≤3h）。本设计把「永久闩死」换成「有上界的停摆」，没有缩短停摆；缩短属于就绪/放大角度。
- 停滞或活锁的排空到达上界后仍会被 supersede 并孤儿化：未冻结的 dead 事件（无落库事实也无账号 hint 的判死，source_sync.go:877 条件冻结）与 worker 停摆 >37 分钟走 DRAIN_STALLED；40001 每 15 秒无限重试这类「有进展但永不完成」走 DRAIN_EXCEEDED（3h）。之后仍是泛型 PROJECTION_FAILED → 8 次判死 → EVENTS_DEAD 闩。本设计只让这些残留有名字、有 event id，不让它们自动消失。
- 孤儿事件一旦形成，重扫重投仍救不回：`validateFactMetadata` 的 observed_at 首写冻结 + 新绑定 ceiling 超 5 分钟（consumption.go:315-327；source_sync.go:411-437）不在本角度。
- 一个账号的问题让其他所有客户开不了票（目标第二句）：EVENTS_DEAD 的流级→实例级放大与 assertSourceFreshTx 不在本角度，完全未触及。
- 同一流被两个代理实例同时驱动（误配置）：两个不同 id 互相探针，其中一个会 hold 到上限后被 supersede，与今天的忙碌战争同形。
- 探针本身的争用：每 60 秒一次 CommitSourceBatch 事务在活动周期行上 FOR UPDATE，与 tryPublish 的 FOR UPDATE（consumption.go:691）同锁；可能给 Mark* 事务多添 40001（RC104 已把它当忙碌处理），未做负载测量。
- balances 流被扣住的对账快照会以数小时前的 as_of 落地（BalanceDBConnector 在 prepareReconciliationCycle 时已替换 Current，economics_db.go:745）；不是错误但 as_of 滞后可见。
- state.json 被运维手动重建（Version=0）时，代理对前任毫无记忆：仍会开新周期并探针——契约让它等待，但代理日志里不会有「我放弃了 X」的记录，只有后端审计有。

### 数据完整性风险
- 排空判定在 CommitSourceBatch 的默认隔离级别事务里（source_sync.go:297 `s.pool.Begin` 无选项），计数与决策之间 Mark* 事务可能改变事件状态：向「多等一轮 60 秒」方向出错是安全的；向「IDLE supersede 一个刚刚完成的周期」方向出错不可能——刚 published 的行不再满足 `cycle_status IN ('receiving','processing')` 的 FOR UPDATE 选择，且该行锁与 tryPublish 的 FOR UPDATE（consumption.go:691）互斥串行。
- 探针是同一 batch_id/sequence/body 的重发：成功后若有迟到重复，CommitSourceBatch 的重复检测（source_sync.go:325-334）返回 Duplicate；spool `SaveIfAbsent` 保证只有一个待发页。无重复插入风险。
- 被扣住的首页：`observed_at` 冻结在扫描时刻 T0，ceiling 采集早于 T0，因此 ceiling ≤ observed+5m 恒成立；receiver 对 observed_at/captured_at/scan_ceiling_at 只拒「未来」（receiver.go:155、169、181）；每次重发重新签名（signing.go:320），X-Sent-At 偏差检查不受 hold 时长影响。三张事实表 CHECK `stream_watermark_at <= observed_at + 5min` 同理成立。
- 审计 before payload 新增字段只含 UUID、计数与时间戳，不含 payload/PII；event id 上限 50 条防止审计行膨胀。
- `supersede_reason` 三个常量长度受 0022 CHECK（≤256）约束，用单测钉住；`supersede_pair`/`supersede_requires_blocked` CHECK 与 DEFERRABLE FK 路径不变。
- 时间：`in.Now` 来自 `s.now()`（UTC），列为 timestamptz，响应头 RFC3339Nano UTC，代理解析后仅用于日志与「不晚于 now+5m」的有界校验，不参与任何代理侧决策。金额：未触及任何金额字段。
- 代理侧 `os.Chtimes` 只改 mtime、不改内容；但 `readUnlocked` 若有基于 mtime 的新鲜度断言需核对（我在 :294-321 看到的是 Lstat + 解码，未见 mtime 语义断言，标为拿不准，实现时读完整函数再定）。
- ScanCycleDrainMaxAge=0 或未接线（旧调用方）→ 分支整体跳过，行为逐字节等于今天——与 StaleActiveScanCycleMaxAge 的零禁用约定一致，不会因漏接线而静默改变 supersede 语义。

### 迁移
无迁移、无 DDL。后端新增一个可选环境变量 `SOURCE_SCAN_CYCLE_DRAIN_MAX_AGE`（默认 3h；0 禁用；须 > 派生的 economicRescanActivityMaxAge 且 ≤ 24h），不进 docker-compose.prod.yml 必填集合，因此不需要同步 verify.ps1 的 $productionEnv。`ScanCycleBusyError` 为 Go 内部类型，`errors.Is(err, domain.ErrScanCycleBusy)` 语义保持。代理 state.json / pending.enc 格式零变化（刻意：envelope 解码 DisallowUnknownFields，state_store_file.go:318）。协议层新增 5 个响应头，均为可选、只读。

### 测试计划

- **backend/internal/postgresstore/source_sync_integration_test.go: TestCommitSourceBatchHoldsSuccessorWhileActiveCycleDrains —— 用 provisionV3SupersedeSource/v3TestChain.commit 建一个已终页(ScanComplete=true→processing)的前任周期，含 1 条 queued 事件（updated_at 新鲜）；newV3SupersedeBatchInput 构造后继，Now = 前任 updated_at + 2×staleMaxAge，ScanCycleDrainMaxAge=3h；断言 errors.Is(err, domain.ErrScanCycleBusy) 且 errors.As 出 *ScanCycleBusyError{Incomplete:1, Reason:"DRAINING", ActiveCycleID=前任}，前任行仍 processing、无 source.scan_cycle.superseded 审计行、后继行不存在。**：证明 超过 37 分钟宽限但仍在排空的前任不被 supersede；旧实现下此测试必红（旧实现会 supersede 并接受后继）。
  - 变异必红：删除 supersedeStaleActiveScanCycleTx 里新增的排空分支（或把 `drain.Incomplete > 0` 改成 `>= 0` 之外的任何让它不成立的改法）→ 走 supersede → 前任变 blocked → 红。
- **同文件: TestCommitSourceBatchSupersedesStalledDrainWithNamedReason —— 与上同布，但测试内 UPDATE 该事件 updated_at = Now − staleMaxAge − 1s；断言接受后继、前任 blocked、supersede_reason 含 "DRAIN_STALLED"、审计 before payload 的 incomplete_events=1 且 incomplete_event_ids 含该 event id。**：证明 无进展的排空按上界腾位，且残留有名字、有 id（上界到了怎么办的恢复入口）。
  - 变异必红：去掉 `now−LastProgressAt <= StaleActiveScanCycleMaxAge` 条件（只看 incomplete>0）→ 永远忙 → 断言「接受后继」失败 → 红；或把审计 payload 里的 event id 列表删掉 → 红。
- **同文件: TestCommitSourceBatchSupersedesDrainExceedingMaxAge —— 事件 updated_at 新鲜（有进展），但 Now = 前任 updated_at + DrainMaxAge + 1s；断言 supersede 且 reason 含 "DRAIN_EXCEEDED"。**：证明 「有进展但永不完成」的活锁被排空上限封顶，hold 不是无限的。
  - 变异必红：删除 `now−active.updated_at <= ScanCycleDrainMaxAge` 条件 → 一直忙 → 红。
- **同文件: TestCommitSourceBatchIdleActiveCycleStillSupersedesAsBefore —— 前任为 receiving（未终页）且其唯一事件已 processed（incomplete=0，tryPublish 不评估 receiving 行所以不会发布）；Now 超过 37 分钟；断言 supersede 且 reason 含 "IDLE"，后继 receiving。**：证明 不回退 XM-INV-SCAN-CYCLE-SUPERSEDE 对 09-03 真孤儿行的修复；hold 只保护有未完成事件的周期。
  - 变异必红：把排空分支的 `Incomplete > 0` 改成无条件（只要在 DrainMaxAge 内就忙）→ 返回忙 → 红。
- **同文件: TestSupersedeAndPublishShareOneIncompletePredicate —— 前任 processing，唯一事件 failed，但已存在 status='open' 且 source_revision_hash=该事件 payload_hash 的 eligibility_freezes 行（POLICY-ANCHOR 2.5 逃生口）；先直接调 tryPublishEconomicScanCyclesTx 断言周期 published；再在另一相同布局的源上让后继探针经过 supersede 路径，断言它得到的 drain.Incomplete==0（通过 IDLE reason 观察）。**：证明 规则（未完成判据）与两个调用点是同一段代码：规则与调用点之间必须有测试穿过两段。
  - 变异必红：在 supersedeStaleActiveScanCycleTx 里重新内联一份不 LEFT JOIN eligibility_freezes 的计数 SQL → 它数出 incomplete=1 → 返回 DRAINING 忙而非 IDLE → 红。
- **backend/internal/sourceingest/receiver_test.go: TestReceiverExposesDrainHoldHeadersOnBusy —— acceptor.err = &postgresstore.ScanCycleBusyError{...} → 503、body code SOURCE_SCAN_CYCLE_BUSY、Retry-After=60、五个 X-Invoice-Active-Scan-Cycle-* 头值逐字相等；子用例 acceptor.err = domain.ErrScanCycleBusy（裸哨兵）→ 503、Retry-After=30、五个头全部缺席。**：证明 只读信号只在有明细时出现，裸哨兵路径与今天逐字节一致（旧代理/旧路径兼容）。
  - 变异必红：改错任一头名或在裸哨兵路径也写头 → 红；缺席断言先做变异：把「写头」无条件化后再跑，确认子用例确实红过。
- **agents/sourceagent/stream_protocol_test.go（或新建 signing_hold_test.go）: TestHTTPIngestClientParsesDrainHoldHeaders —— httptest 服务器返回 503 + 合法头 → IngestHTTPError.Hold 各字段相等、RetryAfter=60s；三个畸形子用例（非 uuid 的 id、Incomplete="99999999999"、Last-Progress-At=now+1h）→ Hold==nil 但 StatusCode/RetryAfter 仍正确。**：证明 代理只信有界、合法的信号；畸形头退化为普通忙而不是改变退避。
  - 变异必红：删掉解析 → 合法用例 Hold==nil → 红；删掉任一校验 → 对应畸形用例 Hold!=nil → 红。
- **agents/sourceagent/runner_test.go: TestRunnerHoldsWithHeartbeatWithoutOpeningCircuit —— scriptedPageSyncer 先返回 4 次 &IngestHTTPError{503, RetryAfter:60s, Hold:&ScanCycleHold{...}}，再 2 次普通 &IngestHTTPError{503, RetryAfter:30s}（无 Hold），再成功；MaxConsecutiveFailures=2；记录 Heartbeat 调用次数与 OnFailure 收到的 Hold；断言：Heartbeat 恰好 4 次（只在 hold 迭代）、每次 hold 的 Sleep ≥ 60s、无 Hold 的两次 OnFailure.Hold==nil、Run 不返回错误、熔断未开。**：证明 hold 有名字、有心跳、不计熔断；心跳不泄漏到普通失败。
  - 变异必红：把 Heartbeat 改为每次失败都调 → 计数 6 → 红；把 hold 计入 consecutiveFailures → 第 2 次 hold 熔断 → Run 返回错误 → 红；不把 Hold 塞进 SyncFailure → 红。
- **agents/sourceagent/state_store_file_test.go: TestTouchHeartbeatBumpsMtimeWithoutRewritingEnvelope —— 初始化 state 文件，读出字节与 mtime；把 mtime 设到 10 分钟前；TouchHeartbeat；断言 mtime 已刷新到现在附近、文件字节逐位不变、随后 FileCursorStore.Load 成功；再用一份包含未知字段的 envelope 证明 readUnlocked 会拒绝（DisallowUnknownFields 仍在，即「加字段=回滚炸」这个前提本身也被钉住）。**：证明 心跳只动 mtime，零格式变化，回滚安全的依据可复验。
  - 变异必红：TouchHeartbeat 改为重写 envelope（哪怕内容相同也会因 JSON 键序/换行变化）→ 字节比较红；或改为触碰别的路径 → mtime 未刷新 → 红。
- **agents/sourceagent/（coordinator 测试）: TestAbandonNeverDropsPendingPageOfInFlightCycle —— 游标为在途 usage ScanReconcile 周期 A（Completed=false、ReconcileWindowBounded=false），spool 预置 A 的未 ACK 页；记录 IngestClient 收到的批次顺序；SyncPage(ScanReconcile) 第一次发送的批次 scan_cycle_id 必须是 A，之后的 Scan 才带 legacy_reconcile_cycle_abandoned 且 cycle id ≠ A。**注：旧实现下此测试为绿，是回归护栏而非新行为证明。****：证明 放弃永远发生在前任的最后一页已交付之后（契约成立的代理侧前提）。
  - 变异必红：把 SyncPage 里 ResumePendingPage 移到 Connector.Scan 之后 → 第一次发送的是新周期 → 红。
- **backend/cmd/api（runtime 配置测试）: TestScanCycleDrainMaxAgeMustExceedRescanActivityWindow —— SOURCE_SCAN_CYCLE_DRAIN_MAX_AGE=30m（小于派生的 37m）→ 启动错误；=0 → 通过且禁用；缺省 → 3h。**：证明 两只时钟的顺序被启动门禁钉住，不会出现上限先于进展窗截断的静默配置。
  - 变异必红：删除校验 → 30m 用例通过 → 红。

**规模** M。后端约 200 行（抽判据、ScanCycleBusyError、supersede 分支与三种 reason、receiver 头、runtime 环境变量）+ 5 个集成测试 + 1 个 receiver 单测；代理约 150 行（头解析、SyncFailure.Hold、Heartbeat、TouchHeartbeat、main 日志）+ 4 个单测；文档 2 处。无迁移，1 个可选环境变量。部署顺序建议后端先于代理（任一顺序都安全）。 · **依赖** RC104 已在生产（SER-BUSY）：排空期间的 40001 不再消耗尝试预算，否则 hold 保护的周期里事件仍会在 hold 期间判死；CLAIM-BINDING 与本设计无交互。, 本设计的后端半边（supersedeStaleActiveScanCycleTx 排空分支 + 只读头）——代理半边没有它完全惰性；若「后端 supersede 只腾位已排空周期」由另一角度单独交付，须与本设计合流为同一判据函数与同一 reason 词表。, 可用性角度：hold 超过 5/37 分钟后的 STREAM_STALE / ECONOMIC_WATERMARK_STALE（探针不落库、processing 行 updated_at 冻结）需要就绪角度处理（例如活动周期有进展时刷新宽限依据），否则 hold 期间整个来源实例仍开不了票。, 放大角度：EVENTS_DEAD 的流级→实例级放大不在本角度，DRAIN_STALLED/DRAIN_EXCEEDED 残留判死后的影响面仍是整个实例。, 运维：手册 2163-2172 与 ELIGIBILITY-OPERATIONS 的 blocked 段改口；DRAIN_* 残留的处置以审计 before 里的 event id 直接喂给 ingest-requeue-dead / ingest-acknowledge-unreplayable（后者对无有效绑定的事件放行）。 · **回滚** 后端：把 `SOURCE_SCAN_CYCLE_DRAIN_MAX_AGE=0` 重启 api 即回到今天的 supersede 语义（排空分支整体跳过，reason 回落为 IDLE 文案与今天等价）；或直接回滚镜像——无迁移、无 DDL、`ScanCycleBusyError` 只是内部类型，旧镜像对同一库无任何不兼容。代理：直接回滚镜像——state.json/pending.enc 零格式变化（刻意不加 envelope 字段，因为 :318 是 DisallowUnknownFields），旧二进制可原地加载；混版本任一方向都退化为今天行为（旧代理忽略新头；新代理遇旧后端 Hold=nil）。回滚后残留的 supersede 审计行新增字段只是多出的 JSON 键，无读取方依赖。


### 审稿：复发与运维：按方案改完后重放 09-07（2222 绑定 + 后继周期 + 40 — refuted=False would_ship=False

- [major] **hold 期间（≤3h）对账到期：runner 每轮循环重新算 mode（runner.go:122-123，失败分支 :174 continue 后同样重算）。到期后 SyncPage(ScanReconcile) 先重放 spool 里被扣住的那页——它是一个 ScanIncremental 的终页（coordinator.go:68-84）；resumedScanPage 用 `HasMore = !cursor.Completed`（coordinator.go:126）→ runCycle 判 Complete（runner.go:280-283）→ Run 里 `result.Complete && mode == ScanReconcile` 把 lastReconcile 记成现在并持久化（runner.go:178-181）。一次根本没跑的对账被记作已完成，下一次要再等 6h（docker-compose.sources.yml:24）。**
  - 方案明确写「对账不会无限延后……只是延后不跳过」，代码说的是相反：只要 hold 跨过到期点就整整跳过一轮。这个 bug 今天在「37 分钟 busy/重启恰好跨到期点」时也存在，但方案把窗口从 ≤37min 扩到 ≤3h（6h 周期下约一半概率），且对账正是让 CLAIM-BINDING 有新绑定可选的唯一重投路径——跳过它就是把残留孤儿的自愈再推迟 6h。方案对此的说法是「条件恰好为真」类的静默债。
  - 依据：agents/sourceagent/runner.go:122-123, 174, 178-181, 245-256; agents/sourceagent/coordinator.go:68-84, 123-127; agents/sourceagent/batch.go:438-481（先 spool 后发送，重放的是原 body）
- [major] **前任周期里有一条事件进入 waiting_dependency（source_funding_lot / source_cutover_manifest 两种不 park 的等待，source_sync.go:874-876 之外的 kind）：MarkSourceEventWaitingDependency 写一次 updated_at 后不再动（source_sync.go:926-931），next_attempt_at = now+12h（source_processor.go:128 `sourceDependencyFallbackRetry = 12 * time.Hour`）；tryPublish 的未完成判据把它算作 incomplete（consumption.go:716-720 `NOT IN ('processed','parked_identity')`）。方案的 stall 时钟是 `now - max(sie.updated_at) ≤ 37m`，37 分钟后判 DRAIN_STALLED → supersede → 周期 blocked。依赖到达时 RequeueSourceDependency 把它放回 queued（source_sync.go:1019-1026）→ 认领 → verifyFactBatchContextTx 因 blocked 拒绝（consumption.go:1025-1029）→ 走泛型 PROJECTION_FAILED（source_processor.go:262）→ 8 次判死 → EVENTS_DEAD 闩死（runtime.go:765-766）。**
  - 这正是方案声称「完全没变、本方案切断」的那条链，只是触发条件从 40001 换成了合法的依赖等待。方案「prevents」第一条说「任何一条未完成事件被 worker 触碰过（claim/failed/busy 都算）」就受保护——等待中的事件恰好不会再被触碰。does_not_prevent 只列了 dead-unfrozen 与 worker 停摆，没有列这个更常规的情况。方案并未让这种复发变得比今天更糟，但也没有堵上它。
  - 依据：backend/internal/application/source_processor.go:128, 237-246, 262; backend/internal/postgresstore/source_sync.go:874-876, 911-931, 1019-1026; backend/internal/postgresstore/consumption.go:716-731, 1025-1029
- [major] **值班按运行手册 §7.1 处置：手册 1948-1958 写明「SOURCE_SCAN_CYCLE_BUSY 超过宽限窗仍卡住 = 真实事故，检查 updated_at/superseded_by 后再考虑手动修复」，而方案下「超过 37 分钟仍 BUSY」恰恰是正常的 drain-hold；方案只改写 2163-2172 那段，没有改 §7.1。手动修复在手册里指的就是 `UPDATE source_economic_scan_cycles SET cycle_status='blocked'`（1930-1936 描述的旧办法）——对一个正在排空的周期执行它，会把 hold 正在保护的那几条事件当场孤儿化，复现 09-07 的整条链。**
  - 方案的安全性有一半靠「人不要在 hold 期间去动那一行」，但给人的手册仍是旧指示；且前 37 分钟代理日志和今天一模一样（见下一条），运维手里没有任何能区分「hold」与「wedge」的证据。
  - 依据：docs/PRODUCTION-RUNBOOK.md:1904-1920, 1930-1936, 1948-1958；方案 code_changes 第 11 项只覆盖 2163-2172
- [minor] **方案把排空判定放在现有 `stillFresh → busy` 之后（source_sync.go:542-548）：前任终页后的前 37 分钟返回裸哨兵，receiver 不带任何 X-Invoice-Active-Scan-Cycle-* 头、Retry-After 仍是 30、代理 Hold=nil、不打「holding」日志、不触碰心跳。代理健康检查只看 state.json mtime、上限 5 分钟（main.go:670-677；docker-compose.sources.yml:30 `SOURCE_LOCAL_HEALTH_MAX_AGE: 5m`，:37-40 每 30s 一次），于是第 5–37 分钟容器 unhealthy，第 37 分钟起反而因心跳变回 healthy。**
  - 「有名字、有心跳」的属性在最常见的 hold 长度（几分钟到 37 分钟，也是 RC104 后 40001 场景实际会停留的区间）里完全不存在；健康状态先红后绿与直觉相反，会误导值班。修法是在 stillFresh 分支也算一次 drain 明细并带头返回（成本与 tryPublish 每次 Mark 都在跑的同一条 SQL 相同）。
  - 依据：backend/internal/postgresstore/source_sync.go:542-548; backend/internal/sourceingest/receiver.go:203-214; agents/cmd/source-agent-prod/main.go:670-677; deploy/docker-compose.sources.yml:27, 30, 37-40
- [minor] **方案称「supersede 只在 incomplete=0 时是无痛的（IDLE）」。被放弃的 receiving 周期里已处理完的事件，其事实仍只映射到一个最终变 blocked、永远不会 published 的周期。资金额度与余额检查点的资格计算只认 `cycle.cycle_status='published'`（consumption.go:3510-3516 `unmappedFunding → errBalanceCarryForwardProofInvalid`，:3543，:3611，:3670），该错误从 processEligibilityProjectionJob 的两处 ensureBalanceCarryForwardProofTx 调用冒出（:3307，:3394）。要等下一次对账把同一 event id 重投、CommitSourceBatch 为已处理事件补一行新映射（source_sync.go:420-428）后才自愈——结合上一条对账被跳过，可能是 6–12h。**
  - 不是回归（今天的 supersede 同样如此），但方案要改写手册并把 IDLE 写成「无痛」，下一位读者会据此判断 IDLE 残留无需处置。usage/credits 事实不受此门控，payments/balances 受。
  - 依据：backend/internal/postgresstore/consumption.go:3307, 3394, 3464, 3510-3516, 3543, 3611, 3670; backend/internal/postgresstore/source_sync.go:420-428
- [minor] **测试计划第 5 条「TestSupersedeAndPublishShareOneIncompletePredicate」后半段要求在「相同布局」上让后继探针观察到 IDLE。但 tryPublish 在每次 CommitSourceBatch 与 Mark* 事务末尾都会跑（source_sync.go:470-473, 744-748, 934-938），一个 processing 且按共享判据 incomplete=0 的周期在任何探针到达前就已 published，探针看到的是「无活动周期 → 直接接受」，根本不会经过 supersede 分支、也没有 IDLE 可观察。要观察 IDLE 必须换成 receiving 行（tryPublish 只评估 processing+final_sequence，consumption.go:690-694）或故意缺 cutover manifest（:748-752）——那就不是「相同布局」，「同一判据」的证明力随之减弱。另：第 4 条在旧实现下变红只是因为 reason 字符串含 IDLE，不是行为差异；作为「不回退 09-03 修复」的回归护栏，它应当在旧实现下为绿。**
  - 任务要求挑至少两条质疑「旧实现下会不会照样绿」：第 5 条按字面写不出来（无法到达断言点），第 4 条的红绿由新标签决定而非行为。第 10 条方案自己已承认旧实现为绿。
  - 依据：backend/internal/postgresstore/source_sync.go:470-473, 744-748, 934-938; backend/internal/postgresstore/consumption.go:690-694, 748-752
- [minor] **回滚段说 `SOURCE_SCAN_CYCLE_DRAIN_MAX_AGE=0` 时「reason 回落为 IDLE 文案」。禁用时 incomplete 根本没算，被 supersede 的周期完全可能带着未完成事件，却被写成 IDLE（=无事件可孤儿化）；恢复工具与手册按 reason 选路径就会选错。**
  - 这是记忆里「被信任的过期闸」的形状：一个有名字的标签在关掉判据后仍然写出去。禁用态应保留独立的 legacy/UNCHECKED 文案（沿用今天 :490 的文本即可）。
  - 依据：backend/internal/postgresstore/source_sync.go:490, 549-553（今天单一 reason 的写入点）；方案 rollback 段

**签字条件**：
- 修 runner：被重放的非对账页不得把 lastReconcile 记成完成（在 spool/receipt 里带上页的 mode，或在 runner 里比对 cursor 的周期模式），并加一条「hold 跨越对账到期点后对账仍会真正执行」的测试；同时把方案里「延后不跳过」的说法改成与代码一致。
- 把 waiting_dependency 从 stall 时钟里摘出来（按 next_attempt_at 或 dependency 唤醒视为「仍在等待」而非「无进展」），或在 does_not_prevent 里明写「等待中的事件仍会在 37 分钟后被 supersede 并孤儿化，链条与 09-07 相同」，二选一，不能沉默。
- 排空明细在 stillFresh 分支也返回（同一条 SQL），让头、日志、心跳、Retry-After 60 从第一次探针起就生效；否则健康检查会出现 5→37 分钟红、之后绿的倒置。
- 改写运行手册 §7.1（1904-1958）而不只是 2163-2172：明确「超过宽限窗仍 BUSY 且头里 Incomplete>0 且 Last-Progress-At 新鲜 = 正常 hold，禁止手动把该行置 blocked」，并把 IDLE 对 payments/balances 的自愈依赖（要等下一次对账重投映射）写进去，不要写「无痛」。
- 禁用态（DRAIN_MAX_AGE=0 或未接线）不得写 IDLE，保留独立的 legacy/UNCHECKED 文案。
- 测试计划第 5 条改用 receiving 行或缺 manifest 的布局并如实标注「布局不同」；第 4 条改成在旧实现下为绿、在 hold 过宽时为红的行为护栏。
- 建议（非阻断）：给 SourceStreamHealth 加一个非致命 reason（如 SCAN_CYCLE_DRAINING 携带周期 id/incomplete/last_progress），让 readyz 与后台健康报告在 hold 期间能说出「在等谁」。

**残余风险**：
- 09-07 原样重放在 RC104 之后很可能根本走不到本方案的分支：b1de0e2b 的两条 usage 与 e58b9430 的一条 checkpoint 会以 15s 间隔重试（source_processor.go:69, 221-236），45 分钟内只观测到 12 条 40001，逐次失败概率远低于 100%，周期大概率在 37 分钟内 published。方案真正的价值区间是「busy 循环持续 >37 分钟」：例如 2222 这种 6842 条的资格追赶作业长时间持有账号 advisory 锁（source_processor.go:203-218 ACCOUNT_LOCK_BUSY 15s）或持续的 SSI 中止（Observe* 为 Serializable：consumption.go:352, 474, 1081, 1682；FOR SHARE OF c :1017）。方案的「prevents #1」描述的是这个窄场景，不是 09-07 的字面重放。
- hold 期间整个来源实例仍开不了票：探针回滚不更新 last_accepted_at（source_sync.go:300, 448）→ 5 分钟 STREAM_STALE（:1096-1098）；processing 行 updated_at 冻结 → 37 分钟后 ECONOMIC_WATERMARK_STALE（:1112-1118）；五流 AND 闸（service.go:611-625）。「一个账号的问题让所有人开不了票」这句目标完全没动，只是把「永久」换成「≤3h」。
- DRAIN_STALLED / DRAIN_EXCEEDED 残留仍走泛型 PROJECTION_FAILED → 8 次 → dead → EVENTS_DEAD 闩死（runtime.go:765-766, 797-800），且比今天最多晚 3h 才发出这个唯一会让人介入的响亮信号；这 3h 里 readyz 只显示 STREAM_STALE，没有任何 reason 说「在等 X 周期排空、还差 N 条」。方案没有给就绪/后台健康报告加 reason，只有 receiver 日志可见。
- 代理上报 projection_status='blocked' 的批次同样要过 supersede（source_sync.go:362），会被 hold 最多 3h（今天 ≤37m）才落库并触发 SOURCE_GAP 冻结（:403-407）；期间就绪在 5 分钟内已因 STREAM_STALE 关门，所以实际暴露 ≤5 分钟，但持久冻结记录被推迟。
- 探针每 ~60-72s（SOURCE_MAX_BACKOFF=1m，docker-compose.sources.yml:27；main.go:897）在活动周期行上 FOR UPDATE（source_sync.go:533-536），与 Serializable 观察者的 FOR SHARE（consumption.go:1017）和 tryPublish 的 FOR UPDATE（:690-694）串行；探针不写行所以不会自己制造 40001，但会让观察者等锁；未做负载测量。
- hold 期间的 balances 快照会以数小时前的 as_of 落地（economics_db.go:745 已 Replace Current）；资格查询按 scan_ceiling_at 排序（consumption.go:3611-3612, 3670-3672），hold 内无更新周期可插队，逻辑上无错，但 as_of 滞后可见。
- TouchHeartbeat 走 withProcessLock 的 .lock 文件（state_store_file.go:378-408），readUnlocked 无 mtime 语义断言（:294-321，已核对）；os.Chtimes 与并发的 healthcheck 读取只在锁内串行，风险低。

### 审稿：数据完整性与财务正确性（重复/漏计/时间倒流/跨周期串账/Serializabl — refuted=True would_ship=False

- [fatal] **设计的头号目标场景「新客户绑定 + 例行对账重叠 → 不再闩死 readyz」在已部署的 RC104 代码下仍然复发，且本设计让它更确定地复发。时间线：客户 X 未绑定；07:00 增量周期 C1 首次投递用量事件 E（source_ingest_events.observed_at=07:00，C1 之后 published）→ E 因身份未绑定进入 parked_identity；10:00 例行对账 C2（滚动窗口从上次对账基线重扫）再次投递 E → CommitSourceBatch 跳过 sie 插入但新增映射 (C2,E)；**本设计的 hold 保证 C2 顺利 published**（这正是设计目的）；两天后 X 绑定 → RequeueSourceDependency 把 E 置 queued → ClaimUnprocessedSourceEvents 用 claimBindingSelect 取「最新的有效绑定」= C2 的批次（sequence 更大）→ 处理器把 claim.ScanCeilingAt（≈09:55）作为 watermarkAt 传给 validateFactMetadata，与 claim.ObservedAt（=sie.observed_at=07:00，首写冻结）比较 → 09:55 > 07:05 → 「source fact event time/watermark is invalid」→ PROJECTION_FAILED×8 → dead → hint 冻结 → EVENTS_DEAD 闩死整个实例。credits 流更糟：每个周期都把 position 归零重扫全部 credit 行，所以每分钟都给每个 parked credit 事件加一条更新的映射；绑定唤醒时「最新有效绑定」的 ceiling ≈ now−5m，必死。**
  - 这不是本设计引入的，但本设计明确写「CLAIM-BINDING 与本设计无交互」且 prevents[0] 断言不会闩死——两者都不成立。更关键的是因果反转：今天的无条件 supersede 恰好把重投的对账周期打成 blocked，使其映射无效，CLAIM-BINDING 回落到原始 first_batch_id（published，ceiling≈observed）而能过 5 分钟规则；本设计让对账周期可靠 published 后，「最新有效绑定」恒为晚数小时的批次，5 分钟规则必然拒绝。09-08 生产「那 2 条 usage 重投后立刻又失败」正是这个机制的第一个样本（绑定到 90adb1e5、水位 09:28 vs observed 07:08），它不是那两条事件的特例，而是对所有「先 parked、后被重投、再被唤醒」事件的通则。财务后果：事件死→账号 EVENT_DEAD 冻结→余额承接证明出现负差额→账号停在 not_invoiceable_pending_reconciliation（fail-closed，不会开错金额），但实例级 readyz 永久 503，与本设计要消灭的事故同形。测试计划里没有任何一条会抓到它。
  - 依据：backend/internal/postgresstore/source_sync.go:646-647（claimBindingSelect 首选臂：cycle_status IN (receiving,processing,published) ORDER BY b.sequence DESC LIMIT 1）；:679（claim 取 sie.observed_at）；:419-441（已存在事件只比哈希不改 observed_at，但仍插入新映射 ON CONFLICT DO NOTHING）；:986-1024（RequeueSourceDependency 唤醒为 queued）；backend/internal/application/source_processor.go:669（StreamWatermarkAt: claim.ScanCeilingAt）；backend/internal/postgresstore/consumption.go:315-323（watermarkAt.After(observedAt+5m) → 拒绝）；agents/sourceagent/economics_db.go:264（每个完成周期都是 newCycle）、:291（credits 每周期 position 归零）、reconcileWindowStart（对账从上次对账基线重扫，覆盖其间所有增量投递）；设计文 depends_on「CLAIM-BINDING 与本设计无交互」。
- [major] **DRAIN_STALLED / DRAIN_EXCEEDED 两条自动 supersede 分支是机器替人做的「核销」决定。场景 A：排空进行到一半，api/worker 因部署或故障停摆 40 分钟；前任 P 的未完成事件 sie.updated_at 不再推进 → 37 分钟 → DRAIN_STALLED → P 置 blocked → worker 恢复后这些事件被认领，绑定 P 已 blocked → verifyFactBatchContextTx ErrConflict → 8 次判死；由于 observed_at 首写冻结 + 任何后来绑定的 ceiling 都 > observed+5m，这些事件从此不可重投，只能 ingest-acknowledge-unreplayable 写掉——本来 worker 一恢复就能正常落地的用量被永久丢失。场景 B：一个重度客户绑定释放 3 万条用量，按 09-07 实测 6842 条/45 分钟（≈2.5 条/秒，单账号 advisory lock 串行）需要 3.3 小时 → 3h 到点 DRAIN_EXCEEDED → 余下数千条被孤儿化 → 同上。**
  - supersede 一个有未完成事件的周期 = 把「可恢复的延迟」变成「不可恢复的数据缺口」，而两个判据都无法区分「事件坏了」和「worker/处理慢」。财务方向：丢失 usage → consumed_cash_minor 少算 → available 少算（accounts_ledger.go:115 available=GREATEST(consumed−reserved−issued,0)）；余额承接证明会因负差额把账号停在 pending_reconciliation，所以最终是 fail-closed 而非开错金额，但需要人工 re-anchor 才能恢复，且 EVENTS_DEAD 让全实例停摆。设计把 3h 的依据写成「6842 条留 3 倍余量」，样本量 n=1，且没把上限和 incomplete 规模挂钩。真正需要自动腾位的只有 IDLE（incomplete=0，09-03 那种孤儿行）；STALLED/EXCEEDED 期间 readyz 早已因 STREAM_STALE/ECONOMIC_WATERMARK_STALE 变红，告警已存在，自动 supersede 没有换来任何可用性，只换来数据丢失。
  - 依据：source_sync.go:528-579（现有 supersede 路径，设计要在 stillFresh 之后插入的分支）；consumption.go:315-323（5 分钟规则）+ source_sync.go:419（observed_at 首写不更新）→ 孤儿化后任何重投都过不了；source_sync.go:1026（verifier 只收 receiving/processing/published）；backend/internal/postgresstore/ingest_unreplayable_acknowledge.go:144（写掉=processed、不落事实）；accounts_ledger.go:115；consumption.go:2060-2080（负差额→pending_reconciliation 的生产案例注释）；backend/internal/application/source_processor.go:128（sourceDependencyFallbackRetry=12h：非 parked 的 waiting_dependency 事件 37 分钟内不会有 updated_at 进展，同样被判 STALLED）。
- [minor] **IDLE 分支对 cycle_status='processing' 且 final_sequence 非空、incomplete=0 的前任执行 supersede（置 blocked），而正确动作是 publish。这种状态可达：(1) 唯一未完成事件的 open freeze 在别的路径上晚些创建（tryPublish 只由 Mark* 触发，freeze 不触发）；(2) 修复工具把周期上最后一条 dead 事件写为 processed 时不调用 tryPublishEconomicScanCyclesTx；(3) source_cutover_manifests 缺失时 tryPublish `continue`。**
  - 把一个已完整的周期打成 blocked 会丢掉它的水位推进与 finalizeSourceAccountsTx 触发；事实本身已落地所以不会错账，但 supersede_reason='IDLE' 会让值班人以为「无事可做」。今天的无条件 supersede 也这么做，不是回归，但设计给它起了名字就该把语义做对。
  - 依据：consumption.go:684-731（tryPublish 只选 processing+final_sequence，incomplete≠0 continue，无 manifest continue）；ingest_unreplayable_acknowledge.go:144-150（UPDATE processed 后无 tryPublish 调用，grep 全文件无 tryPublish）；source_sync.go:718-760（MarkSourceEventProcessed/Failed 才调 tryPublish；MarkSourceEventBusy 不调）。
- [minor] **「未完成」判据把 parked_identity 排除在外，所以一个被代理中途放弃的 receiving 周期若只剩 parked 事件，会被判 IDLE 并 supersede；这些 parked 事件在身份绑定唤醒后绑定已 blocked。另有一个极窄竞态：RequeueSourceDependency 的唤醒 UPDATE 不锁周期行，探针在提交快照里看到 incomplete=0 → IDLE supersede 提交 → 唤醒随后提交 → 事件 queued 但绑定 blocked。**
  - 设计 prevents[2]「被放弃的 receiving 周期上已送达、未投影完的事件不再失去合法绑定」对 parked 事件不成立；与今天等价，非回归，但措辞过强。竞态窗口毫秒级、只影响 receiving 周期，标为拿不准是否值得处理。
  - 依据：consumption.go:716-722（NOT IN ('processed','parked_identity')）；source_sync.go:1019-1025（唤醒只 UPDATE sie）；探针在 :533-536 FOR UPDATE 周期行但不锁 sie。
- [minor] **设计文对并发影响的描述有两处不准：(a)「可能给 Mark* 事务多添 40001」——Mark* 用 s.pool.Begin 默认 READ COMMITTED，不会收到 40001；探针在 hold 路径上不写周期行（回滚），Serializable 的 Observe* 只会在 FOR SHARE OF c 上等锁，不会 40001；(b) stillFresh（<37 分钟）分支仍返回裸哨兵、无响应头、Retry-After 30，所以代理在前 37 分钟打「transient sync failure」、之后才打「holding」，同一件事两种日志。**
  - 不是完整性缺陷；但 (b) 会让值班人在事故最初 37 分钟得不到设计承诺的「在等谁、还差多少」，且两种 Retry-After 让探针频率在 37 分钟处突变。
  - 依据：source_sync.go:297（s.pool.Begin 无隔离选项）、:718/:744/:799/:926/:970（Mark* 同样默认隔离）；consumption.go:1010-1016（verifier FOR SHARE OF c）；设计 mechanism 段「stillFresh→busy 之后新增」与 receiver 改动「未命中（裸哨兵）保持今天的 30 与无头」。

**签字条件**：
- 把 CLAIM-BINDING 列为硬依赖并先修：claimBindingSelect 首选臂只考虑 `b.scan_ceiling_at <= sie.observed_at + interval '5 minutes'` 的有效绑定（这是 validateFactMetadata 会接受的全部集合），其余回落 first_batch_id；新增集成测试「parked 事件 → 被后续 published 周期重投 → 身份绑定唤醒 → 必须 processed」，并对 credits 流（每周期重投）单独跑一遍。上线前对生产跑只读普查，数出带多条 published 映射的 parked 事件。
- 只保留 IDLE（incomplete=0）为自动 supersede；DRAIN_STALLED / DRAIN_EXCEEDED 改为「继续 hold + 带明细的告警/审计」，supersede 非空闲周期必须经运维工具显式执行（工具输出 incomplete 事件 id 列表并要求 --apply）。若坚持保留自动上限，上限必须随 incomplete 数量与近期吞吐推算，且判定前要证明 worker 仍在别处推进。
- IDLE 分支遇到 processing+final_sequence 非空 的前任时先调用 tryPublishEconomicScanCyclesTx，只有仍未发布才 supersede；两个修复工具改事件状态后也调用 tryPublish。
- stillFresh（<37 分钟）分支同样返回带明细的 ScanCycleBusyError（Reason=FRESH），使响应头、日志与 Retry-After 从第一次探针起一致。
- 从设计文与手册中删除「CLAIM-BINDING 与本设计无交互」的表述，改为记录 finding 1 的机制与修复。

**残余风险**：
- hold 机制本身经核对未发现重复计数、漏计、时间倒流或串账：被扣住首页的 observed_at 冻结于扫描时刻、ceiling 先于 observed 采集（economics_db.go:308-343 → :150 ObservedAt=now()；balances :560/:581 CeilingAt=snapshot.AsOf 先于 :640 now()），validateFactMetadata、receiver 的仅拒未来检查、批次/事实表 CHECK 在任意 hold 时长下恒成立；后继从前任 ceiling 位置续扫（prepareCursor 对 ScanIncremental 不改 PositionCursor）无缝无重；水位回退由 tryPublish regressed 兜底；探针事务在 hold 时整体回滚不留痕（批次 INSERT 在 :350 之后、supersede 在 :373，回滚一起消失）。
- 无法在本地核实的关键事实：生产库中当前有多少 parked_identity 事件已经带有 ≥2 条 cycle_status='published' 的映射（即 finding 1 的存量弹药）。建议负责人在下一位客户绑定前跑一条只读 SQL 数出来；若 >0，下一次绑定会直接复现 09-08 的死信签名。
- worker 停摆与「事件坏了」在 sie.updated_at 上不可区分（finding 2 场景 A）；若坚持保留自动 supersede，至少要在判定前确认同一来源其它流/其它周期最近有 Mark* 进展，否则停摆期间的 supersede 是纯粹的数据破坏。
- sourceDependencyFallbackRetry=12h（source_processor.go:128）：source_cutover_manifest 一类非 parked 的 waiting_dependency 事件在等待期间 updated_at 不动，会被 DRAIN_STALLED 误判；目前只在 cutover 周期出现，影响面小但存在。
- 多小时 hold 期间若运维批准了新的 source runtime version（source_ingest_state 校验 :318-320），被扣住的旧批次会得到 409 → 代理 Permanent 停机；fail-closed 但需要人工，窗口从分钟级扩到小时级。同理 signing key 轮换、manifest/configuration hash 变化都在 hold 结束时才暴露。
- 带 projection_status='blocked' 的后继会被 hold 至多 3h，SOURCE_GAP 冻结相应延后；期间前任会把一段来源现在报告有缺口的范围发布成水位。STREAM_STALE 已经在 5 分钟后关闭开票闸，所以不会开出发票，但发布的水位与来源健康状态存在最长 3h 的语义错位。
- 探针每 ~60s 在活动周期行上 FOR UPDATE，Serializable 观察者的 FOR SHARE OF c 会短暂排队；不会产生 40001，但排空吞吐会因此略降，未测量。
- credits 流每周期归零重扫且每次都给已存在事件加映射行（source_sync.go:436-441 ON CONFLICT DO NOTHING 只按 (cycle,event) 去重），source_economic_scan_cycle_events 以「每分钟 × 全部 credit 事件」的速度增长；与本设计无关但会放大 finding 1 并影响 tryPublish/探针计数成本。
- 余额检查点 as_of 滞后（设计已列）：核对了 ensureBalanceCarryForwardProofTx 只用 event_time/stream_watermark_at 等来源侧时间，落地的挂钟时刻不参与证明，因此只是延迟不是错误。
- 审计 before payload 里的 incomplete_event_ids（≤50）若不加 ORDER BY 则不确定，测试会抖。
- 设计测试 #1 把 in.Now 设为 updated_at+2×37m，而事件 updated_at 由 DB now() 写入；不显式把该事件 updated_at 抬到 in.Now 附近，drain 判定会走 STALLED 而非 DRAINING，测试本身会红——实现时要注意 in.Now（应用时钟）与列值（DB 时钟）的混用，这一点在现有 stillFresh 里已经存在。
- 容器 restart: unless-stopped 且无 autoheal（deploy/docker-compose.sources.yml:6,37-41），unhealthy 不会触发重启；心跳的价值仅限监控口径，不影响完整性。