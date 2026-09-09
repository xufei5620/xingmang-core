# XM-INV-SHADOW-BINDING —— 代为绑定（`operator_attested`）实现交接

分支 `ai/claude/XM-INV-SHADOW-BINDING-IMPL`，基线 `b3ded69`（RC107，含 L1 死信
兜住）。设计稿在 `ai/claude/XM-INV-SHADOW-BINDING` 分支的
`docs/handoffs/XM-INV-SHADOW-BINDING-DESIGN.md`；负责人 2026-09-09 拍板方案一。
运行手册见 `docs/PRODUCTION-RUNBOOK.md` 第 9c 节「代为绑定（`operator_attested`）」。

**没有推送、没有碰生产、没有改上游源码。**

## 1. 拍板结论落到了哪里

| 决定 | 落点 |
| --- | --- |
| 方案一（影子绑定 CLI） | `backend/cmd/account-bind/main.go` |
| 接受不可撤回 | 运行手册 9c「先读这三条」第 1 条 |
| 撤回档位 (a)「保留，日后自己认领」 | 同上 + `cmd/api` 的认领路径测试 |
| 一次一个、低峰、盯 `/readyz` | 时机门写进代码（`operatorBindTimingGate`），不再只靠纪律 |
| 新 `binding_method = operator_attested` | `postgresstore.BindingMethodOperatorAttested` |
| `binding_status` 直接 `verified` | `OperatorBindExternalAccount` 硬编码 |
| 影子用户预填 platform / platform_user_id | 同上，走 `claimPlatformIdentityTx` |
| (issuer, subject) 取平台登录会铸的同一对 | `platformLoginOrigin` 读与 `cmd/api` 相同的环境变量 |
| 冻结解除（限「一次一个」代绑定） | 运行手册 3.1 节原文下方追加，原文未删 |

## 2. 改了什么

**新增**

- `backend/cmd/account-bind/main.go` —— CLI。默认 dry-run，`--apply` 必带
  `--operator-id`（UUID），`migrate.Verify` 之后才动数据。
- `backend/internal/postgresstore/operator_bind.go` —— 单事务四步：解析该平台唯一
  enabled 的 `source_instance` → `ensureUserTx` → `claimPlatformIdentityTx` →
  `bindExternalAccountTx` → `requeueSourceDependencyTx`。
- `backend/internal/application/binding_keys.go` —— 两个 HMAC 命名空间的**唯一**
  定义处，导出给 CLI 用。
- 三个测试文件（见第 4 节）。

**改了（都是抽取，不改行为）**

- `postgresstore/identity.go`：`EnsureUser` / `BindExternalAccount` /
  `ClaimPlatformIdentity` / `GetEnabledSourceInstanceID` 的函数体抽成 `*Tx` 版，
  公开方法只剩事务边界。
- `postgresstore/source_sync.go`：`RequeueSourceDependency` 同样抽取，并多返回一个
  `PRE_POLICY_SKIPPED` 条数；`SourceIngestHealth` 加 `UncontainedDead()` 方法。
- `cmd/api/runtime.go`：`validateSourceIngestRuntimeReadiness` 改成调用
  `UncontainedDead()`，不再自己做减法。
- `application/service.go`、`application/source_processor.go`：改成调用
  `binding_keys.go` 里的导出函数。
- `backend/Dockerfile`：tools 镜像加 `invoice-account-bind`。

## 3. 三个设计上的判断，以及为什么

**(a) 为什么不直接调 `Service.BindExternalAccount`。** 设计稿 §B 已经写了：派
HMAC 与唤醒都只对 `platform_password_login` 生效。新 method 必须自己做这两步。
漏 HMAC 会撞 `UNIQUE NULLS NOT DISTINCT`（RC55 踩过的 23505），漏唤醒则停放事实
永远不被扫。

**(b) HMAC 命名空间为什么要挪到 `application/binding_keys.go`。** 原来
`"external-platform/"+id` 和 `"source-dependency/"+kind` 这两个串各只出现一次，
在 `service.go` 和 `source_processor.go` 里。CLI 需要同样的值，最省事的写法是在
CLI 里再写一遍字面量——而那正是「被信任的过期闸」那类问题：命名空间漂开之后不会
报错，只会算出一个匹配不到任何行的 key，然后老老实实报告「released: 0」。所以
抽成导出函数，物理上只剩一处，两边都调它。

**(c) 时机门为什么复用 `UncontainedDead()` 而不是自己写 `Dead > 0`。** 同样的
理由，而且这条判据 2026-09-07 刚把 `/readyz` 钉在 503 三十个小时（三条死信其实
都有冻结兜住）。门禁的判据要是别处逻辑的副本，漂开时它不报错，只是继续回答旧
问题。现在两边调同一个方法。

门在 pending 这一维上**故意比 `/readyz` 严**：`/readyz` 宽容十五分钟内的积压
（api 必须在 worker 补数期间保持可用），代绑定不宽容。这不是抄漏了，是「一次
一个」的机械化——注释里写清楚了。

## 4. 测试与变异

专用库 `invoice_test_shadowbind`。

`backend/internal/postgresstore/operator_bind_integration_test.go`

| 测试 | 钉住什么 |
| --- | --- |
| `TestOperatorBindWakesParkedFactsInTheSameTransaction` | 唤醒的两个分支：策略起点后的放行、起点前的 `PRE_POLICY_SKIPPED` |
| `TestOperatorBindRollsBackEverythingOnDryRun` | 四步真的共用一个事务（dry-run 后库里什么都没留下） |
| `TestOperatorBindRequiresBlindIndexBecauseNullsCollide` | 必须自派 HMAC；并且用两个 NULL 索引真撞出 23505，证明这个要求不是装饰 |
| `TestOperatorBindRefusesAnExternalAccountOwnedBySomebodyElse` | 绑定行属于别人 → `ErrForbidden`，且已创建的影子用户随事务回滚 |
| `TestOperatorBindRefusesAMismatchedPlatformIdentity` | (issuer, subject) 已存在但 platform 对不符 → 拒绝 |
| `TestOperatorBindIsIdempotent` | 重跑不改任何东西 |
| `TestOperatorBindTimingGate` | 未兜住的死信关门、兜住的死信开门（证明读的是 `UncontainedDead`） |
| `TestOperatorBindAuditTrailNamesTheOperator` | 三条审计：`user.created`、`external_account.bound`、`external_account.operator_bound`（后者 actor 是 operator） |
| `TestOperatorBindRefusesApplyWithoutAnOperator` | `--apply` 必须有审批人 |

`backend/cmd/api/shadow_binding_claim_integration_test.go` —— **设计稿 §E 前提 5
（原标「推断」）现在是实测的**：用生产的 `provisionPlatformOrOIDCUser` 打真库，
影子用户日后登录落到影子行、`Claimed=true`、`ResolveOrCreate` 一次都没被调、
`invoice_users` 仍然只有一行。带一个常驻反例（没绑过的账号走另一条分支、铸新
身份、`Claimed=false`），否则「落到 X」这个断言在「绑定啥也没干、登录自己建了
X」的世界里也成立。

另外两条也在这里：错 issuer 仍然能认领但库里的 issuer 永远是错的；影子用户被
置非 active 之后客户登录被拒（撤回档位 (b) 的代价）。

`backend/cmd/account-bind/main_test.go` —— CLI 接线、参数拒绝、
`platformLoginOrigin` 与 `cmd/api` 的默认值逐字一致、`userEmailAAD` 与
`application/crypto.go` 逐字一致、dry-run 不写库、apply 端到端。

**变异验证（每条都真跑过，红→改回→绿）**

| # | 变异 | 结果 |
| --- | --- | --- |
| 1 | 删掉 `ExternalSubjectHMAC` 的形状校验 | 红：`accepted a binding with no external subject blind index`，并当场撞出 23505 |
| 2 | 删掉 `requeueSourceDependencyTx` 调用 | 红：`released=0 want 2`；停放事实仍是 `parked_identity` |
| 3 | 关掉 platform 不符的拒绝 | 红：`returned <nil>, want domain.ErrForbidden` |
| 4 | 删掉 UPSERT 的 `WHERE ...invoice_user_id=EXCLUDED.invoice_user_id` | 红：绑到别人账号上不再报错 |
| 5 | 删掉时机门的 apply 拒绝 | 红：`apply was allowed while an uncontained dead event was present` |
| 6 | 让 dry-run 也提交 | 红：`dry run reported applied` |
| 7 | 关掉 `runtime.go` 的认领分支 | 红：`login did not report taking the claim path` |

变异 7 顺带查出一件对运行手册有用的事：影子用户是按「真登录会铸的同一对
(issuer, subject)」建的，所以**即使认领分支不存在**，`ResolveOrCreate` 也会找到
同一行。也就是说「不会铸第二个用户」有两道独立保障，不止认领路径一道。

## 5. 偏离与未证实

**偏离**

- 时机门在 dry-run 下**不报错退出**，只标 `NO-GO` 并照常打计划。派工写的是
  「dry-run 与 apply 都查……不满足拒绝 apply」，我按字面实现：两种模式都查，
  拒绝的是 apply。理由是预演的用途就是回答「现在能不能做」，门关着时直接退出
  反而让人问不到。
- 派工说「沿 `identity-migrate` 骨架，复用 `EnsureUser` / `BindExternalAccount` /
  `WakeSourceAccountFacts`」，同时要求**单事务**。这两条直接冲突：那三个都是
  `Store` 上自带事务的方法。我的取法是把它们的函数体抽成 `*Tx` 版，公开方法保持
  原样只留事务边界——复用的是同一份 SQL，不是复制一份。代价是动了三个热点函数，
  收益是没有第二份会漂的副本。
- `Service.WakeSourceAccountFacts` 本身没有被调用（它自带事务）；调用的是它底下
  同一个 `requeueSourceDependencyTx`。
- 新增了 `backend/Dockerfile` 的一行——不加的话工具进不了 tools 镜像，运行手册里
  的 `docker run` 就是空话。派工没提，但属于「让交付真的能用」的范围。

**未证实**

- 生产上补数实际要多久（设计稿估「几十分钟到一小时」）。测试里的规模是三条事实，
  不构成任何时间证据。第一次做的时候必须实测并记回运行手册。
- `POLICY_ANCHOR` 首次评估是否按构造 matched（设计稿 §E 前提 6）。本片没碰。
- 候选客户在上游是否真的有起点后的充值 + 消耗（§E 前提 4）——只能人工核对，工具
  读不到上游库。
- 备份副本方案（设计稿方案二）的密钥前提（§E 前提 8）。没做。

## 6. 下一个人接手要注意

- `binding_method` 至今**不进任何 wire**，管理端账本也看不到它。影子清单只能人工
  另存——这一点设计稿 §B 风险 7 已经说了，本片没有改变它。
- 加新的 `binding_method` 值时，注意 `Service.BindExternalAccount` 里那两个
  `== "platform_password_login"` 判断：它们是白名单式的，新值默认既不派 HMAC 也
  不唤醒。这次是自己在 CLI 侧补的，不是改那两个判断——改它们会影响真实登录路径。
- 时机门里 `Pending=0` 这一条会让「连着绑第二个」在上一个排干之前直接失败。这是
  设计意图，`TestOperatorBindIsIdempotent` 里那段 drain 的注释解释了。
