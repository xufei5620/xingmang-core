# XM-INV-READYZ-DETAIL：让 readyz 说出是哪一道挂了，让判死能被发现

- **status:** implemented，自测通过（后端 `go build` / `go vet ./...` /
  `go test -race -p 1 -count=1 ./...` 全绿，29 个有测试的包，接真实
  PostgreSQL）。**无迁移，无前端改动，无 contracts 改动。** 未部署、未推送、
  未打 tag。
- **branch:** `ai/claude/XM-INV-READYZ-DETAIL`，基线 `331777a`（RC103，生产
  正在跑的那条线），worktree `K:/发票/wt-XM-INV-READYZ-DETAIL`。
- **commits:** 见文末「Commits」。

## 一、先说复核结果：三条前提都成立，但道数对不上

brief 给的每一处结论我都自己看过源码。**三条关键前提全部成立**，另有一处
数字对不上，不影响设计方向，所以没有停下来等确认（已在开工时同步给
team-lead）。

### 成立的

1. **短路。** `backend/cmd/api/runtime.go` 原第 436 行起的 `Readiness` 闭包，
   每一步都是 `if ... != nil { return err }`，确实第一道失败就返回。
2. **判死没有独立记录。** `source_processor.go` 的 `RunOnce` 无条件调
   `logProjectionFailure`（WARN），紧接着调 `MarkSourceEventFailed`。关键在
   `postgresstore/source_sync.go`：那条 UPDATE 写的是
   `processing_status=CASE WHEN attempt_count>=8 THEN 'dead' ELSE 'failed' END`
   并且 `RETURNING processing_status` **扫进了本地变量 `newStatus`**——判死这
   件事存储层早就算出来了，只是方法签名只返回 `error`，调用方拿不到。这就是
   这一片要接的那个缝。
3. **未鉴权。** `httpapi/server.go` 里是裸的
   `s.mux.HandleFunc("GET /readyz", ...)`，没有 `s.require(...)` 包裹；
   `deploy/nginx/invoice.solov.cc.conf.template` 的 `location = /readyz`
   也没有 `allow`/`deny`，server 块里同样没有全局网段限制（只有第 86 行
   一条 `limit_except POST { deny all; }`，是别的路由的）。
   **`https://invoice.solov.cc/readyz` 是完全公开的。**

### 对不上的

- **不是七道，是十一个不同的失败返回点。** brief 漏了三个：
  `SourceReadinessHealth` 查询本身、`EligibilityProjectionHealth` 查询本身、
  以及最后一道 `validateSourceRuntimeReadiness`。顺序也和 brief 列的不同
  （ingest 那道在 eligibility 之前）。
- **`health.Dead > 0` 有两处，不是一处。**
  `validateSourceIngestRuntimeReadiness` 看的是 `source_ingest_events`，
  `eligibilityProjectionReady` 看的是 `eligibility_projection_jobs`。两者
  报的话完全不同、修法完全不同，但对外都只是同一句 `NOT_READY`。加上
  `validateSourceRuntimeReadiness` 里的 `DeadEvents != 0`，一共三处「dead」。

  **这一条要说准确**（team-lead 2026-09-08 更正，我第一版写岔了）：事故当时
  **是分清了**的——两张表都查了，`source_ingest_events` 的 dead 是 3、
  `eligibility_projection_jobs` 的 dead 是 0，所以定位到前者。问题不在于
  「没分清」，而在于**分清的代价**：只能靠逐道读源码、再挨个查库反推。
  **系统本身从没向外说过任何能区分这三处的话**——端点没说，日志也没说（当时
  连日志都没有，见下一条）。一个没读过这段代码的运维根本无从下手，而这正是
  readyz 本该替人回答的问题。**准确的问题陈述比夸张的更有说服力**，所以这一
  片的依据是「外部观察者无法区分」，不是「谁没分清」。
- **`readyz` 把 err 整个丢掉，连日志都没有**——`if err := s.readiness(ctx);
  err != nil` 之后 err 再没被用过。这比 brief 预设的更糟（原以为至少有记录）。
  **按 team-lead 的排序，这一条排在最前面**：哪怕公网响应体一个字不改，只要
  服务端日志记下是哪一道挂了，这次事故的定位时间就从几小时变成一分钟。
- **eligibility 那条判死已经有记录了，source_ingest 那条没有。**
  XM-INV-PROJECTION-FAILURE-GRADING 在 `consumption.go` 写了
  `eligibility.projection.dead` 审计事件；`source_ingest_events` 这条没有对应
  的。**这是本片第二件事最有力的依据：不是提议加一个新机制，而是同一件事在
  仓库里早有做法，只是漏了一条路径。**

## 二、第一件：readyz 说出是哪一道

### 做法

检查序列从 `buildProductionRuntime` 里的内联闭包搬到
`backend/cmd/api/readiness.go` 的 `readinessProbe.evaluate`，**顺序一字不
改、短路行为一字不改**，依赖全部改成注入的函数值。搬家有两个理由：一是每一
道现在能被单独打红（原来要测「PDF sidecar 那道」，得真让一个 sidecar socket
拒连，所以十一道一道都没有单独覆盖）；二是每一道现在能挂自己的名字。

失败用 `httpapi.NotReady(check, summary, err)` 包一层，三个字段的分工就是
安全边界：

| 字段 | 内容 | 去向 |
| --- | --- | --- |
| `Check` | 闭集常量里的稳定标识符 | **公网响应体** |
| `Summary` | 一句固定的面向运维的话 | **公网响应体** |
| `Err` | 真实底层错误（含 DSN／主机／端口／账号 id） | **只进日志，绝不出响应体** |

响应体从：

```json
{"error":{"code":"NOT_READY","message":"required dependencies are unavailable"}}
```

变成：

```json
{"error":{"code":"NOT_READY","message":"source ingestion has dead events requiring operator repair","check":"source_ingest_dead_events"}}
```

日志同时多一条：

```
level=ERROR msg="readiness check failed" check=source_ingest_dead_events error="source ingestion contains dead events"
```

**原来这里连日志都没有**——`if err := s.readiness(ctx); err != nil` 把 err
整个丢掉了。

### 我选了哪种，为什么（brief 要求必须说明）

**选了「公网回稳定检查名」，不是「公网照旧、只进日志」。** 理由按份量排：

1. **公网体是唯一能拿到判据的消费方。** `deploy/roll-forward.sh` 第 121 行
   轮询的是 `$public_origin/readyz`，`deploy/check-pending-spools.sh` 同样从
   外面看。它们今天只能看状态码，失败时只会说「readyz not 200 within 600s
   (source freshness or dependency gate)」——括号里那句含糊的猜测，正是因为
   没有别的信息可说。有了 `check` 字段，发布脚本能直接说出卡在哪一道。
2. **仓库已有先例。** 同样未鉴权的 `/healthz` 已经在回
   `{"status":"ok","source_mode":...,"auth_mode":...}`，本来就暴露了内部形态。
   反例 `location = /metrics { return 404; }` 挡的是**指标**（数量、分布、
   时序），那是另一个量级的信息，不是一个子系统名。
3. **风险是可界定的，且被结构性地限住了**（下一节）。

team-lead 2026-09-08 给了同向的判据，一并记在这里：**稳定检查名本身不携带
任何数据**——没有账号 id、没有连接串、没有计数；泄漏的只是「这套系统有这么
一道检查」，而攻击者从代码里本来就能知道。**具体的数字和 id 一律不进响应体。**
这一条在实现里是**结构性成立**的，不是靠自觉：`readinessSummaryPattern` 的
字符集里根本没有数字，所以「dead 有 3 条」这种计数**拼都拼不出来**（M8 变异
证明了塞一个 `db1` 进去就会被打红）。

**残余风险，明说：** 攻击者轮询 `/readyz` 能知道当前哪个子系统坏了，也能枚举
出这套系统里有 AV 扫描器和 PDF sidecar。我判断可接受，因为：暴露的是**子系统
类别**而非实例（没有主机名、端口、版本、数量）；上传路径是 fail-closed 的
（`document.ScannerChain` 里任一扫描器不可用，上传就被拒，不存在「趁 AV 挂了
传个马」的窗口）；而且返回 503 这件事本身已经告诉了外界「我现在不健康」。
**如果你认为这条不该接受，回退成本极小**：把
`httpapi.writeNotReady` 里 `publishCheck == ""` 的分支改成无条件走，公网就
立刻回到原来那句，日志一行不少——两件事本来就是分开实现的。

### 安全上做了什么硬保证

不靠「每个调用点小心点」，靠**响应边界上的结构性闸门**
（`httpapi/readiness.go`）：

```go
readinessCheckNamePattern = regexp.MustCompile(`^[a-z][a-z_]{2,63}$`)
readinessSummaryPattern   = regexp.MustCompile(`^[a-z][a-z -]{9,159}$`)
```

**两个字符集里都没有数字，也没有 `.` `:` `/` `@` `%`。** 主机名、IP、端口、
DSN、文件路径、UUID、账号 id ——没有一个能用这些字符拼出来。任何不匹配的
东西一律降级成原来那句通用文案（并在日志里记 `check=rejected_check_name`，
好让这种 bug 被找出来）。

这条闸门防的是**将来某次随手的改动**，比如有人图省事写
`NotReady(check, err.Error(), err)`。M3′ 变异证明了它确实拦得住（见下）。

### 短路保留，理由

**保留短路，不改成「全跑完报告所有失败项」。**

1. **预算不够。** handler 给整个探针 3 秒，而 `pdfScanner` 自己带 20 秒
   timeout，加上两次 TCP/socket ping 和三条 DB 查询。第一道已经失败还继续跑
   完，很可能在报告任何东西之前先把预算花光——运维拿到的会是一堆
   context deadline，比「第一个真因，带名字」差得多。
2. **后面那些多半是前面的果。** 检查是按依赖顺序排的（settings 依赖库，
   ingest 判定依赖 health 查询）。库挂了还去跑 source health 查询，报出来的
   是连带失败，会把人往错的方向带。
3. **本来就够了。** 那六个小时丢在「一个名字都没有」，不是丢在「只显示了一个
   名字」。

`TestReadinessProbeShortCircuitsOnTheFirstFailure` 把这个决定钉住了：同时让
clamav 和 pdf-scanner 坏掉，必须报 clamav，且断言 pdf-scanner **没有被 ping
到**。

### 日志限流

容器 healthcheck 每 10 秒探一次（`deploy/docker-compose.prod.yml`）。不限流的
话那六个小时会写进约 2000 行一模一样的日志，把别的都埋了。沿用仓库已有的
`eligibilityProofPendingWarnInterval` 同一个 5 分钟节奏，并且做成一个小状态
机（`readinessOutcomeLog`）：

- 同一道持续失败：5 分钟一行。
- **换了一道：立刻记**（失败项变了本身就是新闻）。
- 恢复：记一行 `readiness recovered` 然后清空，所以一段故障在日志里**有头有尾**。
- 一直健康：**一行不写**。

### 检查名词表（14 个）

粒度是**故意不均匀**的：生产里真会发生的条件各给一个名字（尤其三处 dead），
而结构一致性断言（健康报告本身畸形、流重复）共用一个粗桶——那些意味着报告
坏了而不是依赖挂了，且日志里带着原文。**公网粗而稳，日志细而准。**

`database`、`admin_settings`、`invoice_issuer`、`clamav_daemon`、
`clamav_signatures`、`pdf_scanner`、`source_health_query`、
**`source_ingest_dead_events`**、`source_ingest`、`eligibility_health_query`、
**`eligibility_projection_dead_jobs`**、`eligibility_projection_stuck`、
`eligibility_projection`、**`source_stream_dead_events`**、`source_streams`。

运维用的对照表已写进 `docs/PRODUCTION-RUNBOOK.md`（「Reading a 503 from
`/readyz`」一节，含每个名字该去哪儿看）。

为了让 `errors.Is` 能分辨，把四处内联 `errors.New(...)` 提成了包级 sentinel：
`errEligibilityProjectionDead`、`errEligibilityProjectionStuck`、
`errSourceStreamDeadEvents`、`errSourceIngestDeadEvents`。**文案一个字没改**，
所以按文案断言的旧测试照样绿。

## 三、第二件：判死能被发现

### 做法

`MarkSourceEventFailed` 的签名从 `error` 改成 `(string, error)`，返回它**本来
就已经算出来**的那个 grade。生产调用点只有一个（`source_processor.go`）。
新增 `SourceEventFailed`/`SourceEventDead` 两个常量对齐 SQL 里的字面量。

`RunOnce` 在 mark 之后按 grade 分流。终态那一次做**两件事**：

1. **实时信号**——`logSourceEventDead`，**Error 级**。
2. **持久记录**——`MarkSourceEventFailed` 在**同一个事务里**写一条
   `source_ingest_event.dead` 审计事件。

```
level=WARN  msg="source event projection failed" ...          ← 第 1..7 次，会自愈
level=ERROR msg="source event marked dead" ... attempt=8 ...  ← 第 8 次，终态
```

### 为什么是级别

brief 的判据是「一个只按错误级别做巡检的人应该能发现它」，那就让它是 Error。
这也是 `internal/application` 包里**唯一**一条 Error；整个 api 进程里 Error
只用在启动被拒、进程停止、后台 worker 失败三处，所以这条不会被淹。

顺带排除掉的：**没有指标设施可复用**——`backend/go.mod` 只有 pgx、go-oidc、
go-jose、oauth2，没有 prometheus/otel，nginx 那边
`location = /metrics { return 404; }`。新造一套指标通道还得有人去订阅它，
**在事故当天帮不上任何忙**。

日志字段：`source_instance_id`、`stream_id`、`event_id`、`entity_type`、
`attempt`、`error`。**沿用 `logProjectionFailure` 的规矩：只有 id 和错误，
绝不含 payload**（有测试专门断言 payload 明文不出现）。

### 审计事件：我第一版判断错了，已按 team-lead 的意见改回来

**第一版我没写审计行**，理由是「`source_ingest_events` 那一行本身就是持久
证据，再写一条是冗余；缺的不是记录而是没人吭声」。**这个判断是错的**，
team-lead 2026-09-08 指出了更强的依据，我接受并已实现：

> 「不是我提议加一个新机制，而是同一件事在仓库里已经有做法，只是漏了一条
> 路径……这样评审的人只需要判断『一致』，不需要判断『合不合理』。」

这条比我原来的推理有力。我当时把问题看成「要不要加一层记录」（于是去权衡
冗余），实际的问题是「同一个不可逆状态转换，仓库里已经有一半在写审计行，
另一半没写」——**那是缺口，不是设计选择**。而且两者答的问题本来就不同：
日志是几秒内被巡检抓到的实时信号，审计行是三周后容器日志早已滚掉时还在的
那条记录。

**形状严格照抄，没有另设计。** 参考了两个现成先例：

| 来源 | 抄了什么 |
| --- | --- |
| `source_ingest_event.repair_requeued`（`eligibility_repair.go`，同一张表已有的审计事件） | action 前缀、`object_type="source_ingest_event"`、`object_id=event_id`、payload 里带 `source_instance_id`/`stream_id` 的习惯 |
| `eligibility.projection.dead`（`consumption.go`，同一类判死） | 判级字段 `attempts` / 错误 / `threshold`，以及**写在应用 grade 的同一个事务里** |

**事件名是 `source_ingest_event.dead`，不是我原先设想的 `source.event.dead`**
——仓库里这张表的既有前缀就是 `source_ingest_event.`，照它来。

```go
writeAudit(ctx, tx, AuditActor{Type: "source_connector", ID: claim.SourceInstanceID,
    Reason: "source event reached the terminal dead grade"},
    "source_ingest_event.dead", "source_ingest_event", claim.EventID, nil,
    map[string]any{"source_instance_id": ..., "stream_id": ..., "entity_type": ...,
        "attempts": attemptCount, "processing_error": errorCode,
        "threshold": sourceEventDeadThreshold})
```

**无条件写**，这是它补的缺口所在：同一函数里那条 `EVENT_DEAD` 冻结是**有条件**
的（只在能把事件关联到账号时才写，关联不上就什么都不留——那段代码自己的注释
就承认这是结构性缺口）。审计行不带这个条件，判死就有记录。

**注意**：`writeAudit` 把 `after` 过 `stateHash` 哈希后存，所以 payload 里
那些字段读不回来，能直接读的是 action / object_id / actor / reason /
`created_at`。`eligibility.projection.dead` 也是同样情况——**保持一致**优先于
让它更好读；要改就两条一起改，那是另一片的事。

**无迁移。** `audit_events` 的 `object_type`/`object_id` 是自由文本、无 CHECK
约束，`invoice_app` 对该表的 INSERT 权限早就有（`writeAudit` 遍布全仓）。
**没有新建表，所以不涉及 compose permissions 作业的重放。**

### 顺带收掉的一个魔数

`8` 原本在两条查询里各写一遍：`MarkSourceEventFailed` 的
`attempt_count>=8`（判级）和 `ClaimUnprocessedSourceEvents` 的
`attempt_count < 8`（认领谓词）。**这两个必须严格相等**——认领谓词小于判级
阈值，事件会在能被判死之前就不再被认领（**readyz 反而对着一个永久卡住的事件
保持绿色**，正好是这一片要修的 bug 的反面）；大于则会反复认领已经死掉的行。
现在两处读同一个 `sourceEventDeadThreshold`，且都以**查询参数**绑定（不是字符
串拼接）。它同时是审计事件的 `threshold` 字段，对应
`projectionFailureDeadThreshold` 在 eligibility 那边的角色，取值也刻意相同。

`TestSourceEventDeadThresholdGovernsBothTheGradeAndTheClaimPredicate` 不去断言
这个常量，而是**真跑满 8 次 RunOnce**、逐次核对状态，再验证第 9 次认领不到
——因为要测的正是这两条查询是否同步。

## 四、我动了 `source_processor.go` 的哪几行

**另一个 agent 在改这个文件里 40001 的错误分类。我只加观测，一行分类逻辑都
没碰。** 三处，改完后的行号：

| 行 | 改了什么 |
| --- | --- |
| 50-54 | 给已有的 `logProjectionFailure` 文档注释加一段（说明它仍然在 mark 之前写、仍然不知道 grade）。**纯注释，函数体没动。** |
| 64-96 | 新增 `logSourceEventDead`（64-87 注释，88-96 函数）。**纯新增，不改任何已有函数。** |
| 220-230 | `RunOnce` 里唯一的实质改动：把 `MarkSourceEventFailed` 的返回接成 `grade, err`，然后 `if grade == postgresstore.SourceEventDead { logSourceEventDead(...) }`。 |

第 221-231 那一段在 `logProjectionFailure(p.logger(), claim, processErr)` 之后
——**在 `errors.Is(processErr, domain.ErrAccountLockBusy)` 和
`errors.As(processErr, &dependency)` 那些分类分支的下游**，和 40001 分类应该
不在同一处。合并时如果冲突，我这边要保留的只有「`grade, err =` 这个接法」和
「那个 `if grade == ...` 三行」。

同时改了 `postgresstore/source_sync.go`（`MarkSourceEventFailed` 的签名与
`return`、审计事件、三个常量、两条查询改用参数绑定阈值），这个文件 brief
没提到有别人在动。

## 五、变异验证（全部「改条件／改常量」，无一处删代码）

每一条都配了**对照组**并确认它没红。跑法：临时改文件 → 跑目标与对照 → 还原。
脚本在 scratchpad，不在仓库里。

| # | 变异 | 目标（应红） | 对照（应绿） | 结果 |
| --- | --- | --- | --- | --- |
| M1 | ingest 判死分流指向错误的 sentinel（`errSourceIngestDeadEvents` → `errEligibilityProjectionDead`） | `.../source_ingest_dead_events` | `.../source_stream_dead_events`、`.../eligibility_projection_dead_jobs`、`.../database_ping`、短路用例 | ✅ 全对 |
| M2 | clamav 与 pdf-scanner 两段**互换顺序** | `TestReadinessProbeShortCircuitsOnTheFirstFailure` | `.../clamav_daemon`、`.../pdf_scanner`、整张表 | ✅ 全对 |
| M3′ | 响应体 message 改成 `publishSummary + ": " + err.Error()` | `TestReadyzPublishesTheCheckNameAndLogsTheRealCause`（泄漏断言） | 拒收/未分类四例、ready 路径、限流两例 | ✅ 全对 |
| M4′ | `readinessSummaryPattern` 放宽成 `^.{1,500}$` | `.../summary_carrying_a_database_url` | `.../check_name_carrying_a_host`、`.../empty_check_name`、`.../unclassified`、happy path | ✅ 全对 |
| M5 | `readinessFailureLogInterval` 改成 0 | 限流单测 + 限流 e2e | happy path、ready 路径 | ✅ 全对 |
| M6 | `if grade == SourceEventDead` 改成 `== SourceEventFailed` | 判死接线集成测试 | 级别单测、原有 `TestLogProjectionFailure`、兄弟用例 `TestDeadUsageEventWithoutPersistedFact...` | ✅ 全对 |
| M7 | 判死日志从 `logger.Error` 降成 `logger.Warn` | 级别单测 + 判死接线集成测试 | grade 常量用例、原有 `TestLogProjectionFailure` | ✅ 全对 |
| M8 | `readinessSummaryDatabase` 里塞进主机名 `db1` | 无标识符守卫 + `.../database_ping`（逐字文案） | `.../clamav_daemon`、名字唯一性 | ✅ 全对 |
| M10 | `readinessCheckNamePattern` 放宽成 `^.{1,500}$` | `.../check_name_carrying_a_host` | dsn 用例、未分类、happy path | ✅ 目标红、对照绿 |
| M11 | `readinessCheckNamePattern` 放宽成 `^.{0,500}$`（接受空串） | `.../empty_check_name` | —— | ✅ 红 |
| M12 | 判死日志里 `claim.Attempt` 改成 `claim.Attempt+1` | 判死接线集成测试（`attempt=8` 对账断言） | —— | ✅ 红在 `attempt=9` |
| M13 | 审计写入**移出** `if newStatus == SourceEventDead` 分支（变成无条件写） | 「重试不写审计行」+「8 次后恰好 1 行」 | 冻结兄弟用例、级别单测 | ✅ 全对 |
| M14 | 审计 action 改名 `.dead` → `.died` | 同上两条 | 冻结兄弟用例 | ✅ 全对 |
| M15 | 审计 `Reason` 文案改成别的话 | 逐字 reason 断言 | 阈值耦合用例、冻结兄弟用例 | ✅ 全对 |
| M16 | 认领谓词与判级阈值解耦（传 `sourceEventDeadThreshold-1`） | 阈值耦合用例（第 8 次认领不到） | 级别单测、readyz 逐道表 | ✅ 全对 |
| M17 | 审计 `object_type` 改成 `ingest_event`（偏离 repair_requeued 先例） | object_type 断言 | 阈值耦合用例 | ✅ 全对 |

**两次头一版变异给的是假信号，重做了，记在这里免得后人踩：**

- **M3 头一版**把 message 直接换成 `err.Error()`，结果 `publishSummary` 变成
  未使用变量 → **整包编译不过** → 包里所有用例连坐红。这正是 brief 警告的那
  类假信号：目标「红」了，但不是因为行为变了。改成 `publishSummary + ": " +
  err.Error()` 让变量仍被使用，才是有效变异。
- **M4 头一版**用了 `^.{1,4096}$`，超过 Go regexp 的 1000 次重复上限，
  `MustCompile` 在包 init 里 panic → 同样是全包连坐。改成 `{1,500}`。

**M10 顺手挖出我自己测试里的一个真问题。** 放宽名字 pattern 后
`.../empty_check_name` 竟然还是绿的——因为空名字被**两层**挡着：pattern，以及
`writeNotReady` 里 `publishCheck == ""` 的兜底。我又单独跑了 M11（pattern 改
成接受空串），它变红了，红在日志断言上（`check=rejected_check_name` 不再出
现），**所以那条断言不是恒真的**。M11 还暴露了一个边角：如果 `logCheck` 能是
空串，`readinessOutcomeLog.record` 会把它当成「恢复」，那条 503 就会**一行日
志都不写**。今天不可能发生（两个兜底名都是非空常量，pattern 也要求至少 3 个
字符），我加了
`TestReadinessFailureFieldsAlwaysNamesSomethingForTheLog` 把它钉死。

**缺席型断言清单**（brief 要求全部变异验证，均已覆盖）：

- 响应体不含底层错误 → M3′ 打红。
- 响应体不含 DSN／主机名 → M4′、M10 打红。
- 重试期不出现 Error 级日志、不出现「marked dead」 → M6、M7 打红。
- 日志不刷屏（30 次探测只 1 行） → M5 打红。
- 后一道检查没被跑到 → M2 打红。
- 重试不写 `source_ingest_event.dead` 审计行 → M13 打红。
- 死掉的事件不再被认领（第 9 次 `processed=0`） → M16 打红。
- 日志不含 payload 明文 → **未做变异验证**，见「风险」第 4 条。

## 六、门禁

在 `K:/发票/wt-XM-INV-READYZ-DETAIL/backend` 下跑的，全部真跑、真绿：

- `go build ./...` —— 通过。
- `go vet ./...` —— 通过，无输出。
- `go test -race -p 1 -count=1 ./...`，`INVOICE_TEST_DATABASE_URL` 指向本机
  已在跑的 `invoice-test-pg`（127.0.0.1:55432）——**29 个有测试的包全部 ok，
  5 个 no test files，0 条 FAIL，退出码 0**。`internal/postgresstore` 248s，
  `internal/application` 23s，整轮约 6 分钟。收尾时又完整跑了第二遍确认。
- `gofmt` —— 新增/改动文件均已 `gofmt -w`；仓库整体 `gofmt -l` 会列出几乎所有
  文件，那是 CRLF 工作区的既有现象（`core.autocrlf=true`，仓库里存的是 LF），
  与本片无关。

**没跑：**

- `scripts/verify.ps1` 全量 —— 跨前端、Docker 发布镜像门禁、Keycloak/Nginx
  校验，本片是纯后端改动，沿用多个前序切片的同一先例。
- 任何需要连生产的东西 —— 按硬约束，没有也不该有生产访问。
- 前端 —— 一行没动。

## 七、Commits

- `fbda234` feat(readyz): name the failing check, and announce dead source events
  （实现 + 测试 + runbook）
- 本文档另起一个 commit。

**未推送**（按约束）。

## 八、文件清单

新增：

- `backend/cmd/api/readiness.go` —— 检查名/文案常量、`readinessProbe`。
- `backend/cmd/api/readiness_test.go` —— 逐道表驱动（14 个子用例）+ 对照组 +
  唯一性 + 无标识符守卫 + 短路 + proofPending 非回归。
- `backend/internal/httpapi/readiness.go` —— `ReadinessCheckError`、
  `NotReady`、两个 pattern 闸门、`readinessOutcomeLog`。
- `backend/internal/httpapi/readiness_test.go` —— 端点行为、拒收降级、限流。
- `backend/internal/application/source_processor_dead_log_test.go` —— 级别可
  区分性（单测）。
- `backend/internal/application/source_processor_dead_log_integration_test.go`
  —— 接线证明（真库，第 1 次 vs 第 8 次）、审计行断言（含重试期的缺席断言）、
  阈值耦合用例（真跑满 8 次 + 第 9 次认领不到）。

改动：

- `backend/cmd/api/runtime.go` —— 闭包搬走、绑定真实依赖、四个 sentinel。
- `backend/internal/httpapi/server.go` —— handler 改调 `writeNotReady`，
  Server 加 `readinessLog` 字段，新增 `writeNotReady`。
- `backend/internal/postgresstore/source_sync.go` —— `MarkSourceEventFailed`
  返回 grade 并写 `source_ingest_event.dead` 审计事件；新增
  `SourceEventFailed`/`SourceEventDead`/`sourceEventDeadThreshold` 三个常量；
  判级与认领两条查询改为参数绑定该阈值。
- `backend/internal/application/source_processor.go` —— 见第四节。
- `docs/PRODUCTION-RUNBOOK.md` —— 新增「Reading a 503 from `/readyz`」一节
  （对照表 + `docker logs` 与审计表查询命令）。

## 九、风险 / 需要你签字的地方

1. **公网暴露检查名这个权衡**（第二节）。这是本片唯一一个真正的产品决定，
   请你自己过一遍那个残余风险再签。回退成本极小，做法写在那一节里。
2. **`MarkSourceEventFailed` 改了签名。** 生产调用点只有一个，但如果有别的
   worktree 正在加新的调用点，合并时会编译不过（这是好事——不会静默出错）。
3. **判死路径上多了一次 `audit_events` INSERT，且在同一个事务里。** 它失败会
   让整笔回滚，事件停在 `processing` 直到租约（10 分钟）到期后被重新认领——
   与该函数里 `freezeEligibilityTx` 已有的失败语义**一致**，不是新的失败模式。
   量级也不必担心：只在第 8 次、即事件终结那一次写，不是每次重试都写。
4. **「日志不含 payload 明文」这条断言我没做变异验证。** 它沿用的是
   XM-INV-OBS-BUNDLE 已有的写法，而要变异它就得真的往日志里塞明文密文，
   我不愿意在测试代码里写这种东西。断言本身是有效的（`logSourceEventDead`
   压根不接触 `PayloadCiphertext`），但它属于「结构上不可能」而非「被证明会
   变红」，如实标出来。
5. 无浮点金额、无新的密钥/密文落盘或落日志、无 `contracts/` 改动、无 admin
   OIDC 改动、**无迁移**（审计事件复用既有 `audit_events` 表，`object_type`
   无 CHECK 约束、`invoice_app` 的 INSERT 权限早已存在，**因此不涉及 compose
   permissions 作业的重放**）、未碰任何发布身份文件、未打 tag、未碰
   `backend/Dockerfile`、未碰 `K:/星芒统一控制平台/` 与其他 worktree ——逐条
   确认过。

## 十、后续（建议，不阻塞本片）

1. **`deploy/roll-forward.sh` 第 121 行现在把响应体丢掉了**
   （`curl -s -o /dev/null`），失败时只会说「readyz not 200 within 600s
   (source freshness or dependency gate)」——那句括号里的猜测，现在可以换成
   真的检查名了。**我故意没改**：`wt-XM-INV-ROLLFORWARD-HARDEN` 正在动那个
   文件，不想制造冲突。改动很小，谁在那个 worktree 顺手带上即可。
   `deploy/check-pending-spools.sh` 同理。
2. 管理端可以考虑把 `check` 展示在源健康页上，省得运维去 curl。
3. 若将来真的接了指标通道，`readiness check failed` 和
   `source event marked dead` 这两条是天然的计数点；本片没有为此预留任何
   东西，也不建议现在预留。
4. `writeAudit` 把 `after` 哈希后存，所以 `source_ingest_event.dead` 和
   `eligibility.projection.dead` 的 `attempts`/`threshold` 等字段都读不回来。
   这一片**刻意保持了与既有先例一致**而没有单独优化。若哪天要让审计负载可读，
   那是横跨全表的一次改动，两条一起改。
