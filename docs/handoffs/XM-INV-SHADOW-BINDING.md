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
- `backend/internal/application/binding_keys.go` —— 两个 HMAC 命名空间与
  `UserEmailAAD` 的**唯一**定义处，导出给 CLI 用。
- 三个测试文件（见第 4 节）。

**改了（都是抽取，不改行为）**

- `postgresstore/identity.go`：`EnsureUser` / `BindExternalAccount` /
  `ClaimPlatformIdentity` / `GetEnabledSourceInstanceID` 的函数体抽成 `*Tx` 版，
  公开方法只剩事务边界。
- `postgresstore/source_sync.go`：`RequeueSourceDependency` 同样抽取，并多返回一个
  `PRE_POLICY_SKIPPED` 条数；`SourceIngestHealth` 加 `UncontainedDead()` 方法。
- `cmd/api/runtime.go`：`validateSourceIngestRuntimeReadiness` 改成调用
  `UncontainedDead()`，不再自己做减法。
- `application/service.go`、`application/source_processor.go`、
  `application/crypto.go`：改成调用 `binding_keys.go` 里的导出函数。
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
`platformLoginOrigin` 缺变量时拒绝运行且归一化方式与 `cmd/api` 一致、
AAD 直接调 `application.UserEmailAAD`、issuer 与库矛盾时拒绝、退出码分级、
dry-run 不写库、apply 端到端。

复审后新增的（见第 4b 节）：

| 测试 | 钉住什么 |
| --- | --- |
| `TestOperatorBindTimingGateRefusesAPendingBacklog` | `Pending > 0` 分支（原来零覆盖）；用刚建的 queued 事件证明比 `/readyz` 严，排干后门重开 |
| `TestOperatorBindRefusesAnIssuerTheDatabaseContradicts` | issuer 与库里该平台已有身份矛盾 → 拒绝；第一个身份空过也断言了 |
| `TestOperatorBindCountsFactsEverSeen` | `facts ever seen` 要含已唤醒过的（否则正确的重跑会误报告警） |
| `TestPlatformLoginOriginRefusesAnUnsetVariable` | 缺环境变量不再静默用默认值 |
| `TestRunRefusesAnIssuerThatDisagreesWithTheDatabase` | 同上，端到端走一遍 |
| `TestExitCodeSeparatesTheTimingGateFromRealFailures` | 时机门拒绝退 3，其它失败退 1 |

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
| 8 | 删掉时机门的 `Pending > 0` 分支（复审第 4 条） | 红：`gate reported satisfied with a pending backlog` |
| 9 | 恢复 `platformLoginOrigin` 的静默默认值（复审第 1 条） | 红：`silently used a default origin "https://api.solov.cc" instead of refusing` |
| 10 | 关掉 issuer 与库矛盾的拒绝 | 红：store 层与 CLI 层各红一条 |
| 11 | `FactsEverSeen` 只数 `dependency_key_hmac`、不数 `catchup_key_hmac` | 红：`a released fact stopped being counted after the wake: 0` |
| 12 | 关掉「已有非 operator_attested 绑定就拒绝」（复审 A） | 红：dry-run 与 apply 都返回 nil；且 `binding_method became "operator_attested"`，正是复审员探针看到的那一幕 |
| 13 | 关掉未处理 identity_binding 事件的拒绝（复审 B） | 红：`apply returned <nil>, want ErrOperatorBindIdentityProjectionOpen` |
| 14 | 删掉 `invoice_oidc_user` 唤醒（复审 C） | 红：`released=0 want 1` |
| 15 | 关掉 `--external-user-id` 数字校验（复审 E） | 红：四个反例全部落到后面的环境变量检查上，说明校验没在参数阶段拦住 |
| 17 | 把唤醒 WHERE 里的 `dependency_key_hmac=$2` 换成恒真条件（复审 G 的诱饵） | 红：`released=3 want 2` 与 `released=2 want 1`；加诱饵前这一改是看不出来的 |
| 18 | issuer 检查删掉 binding_method 过滤（退回上一轮的比对集） | 红：`2 different oidc_issuer values ... refusing to add another until that is explained`，与复审员预测的生产失败一字不差 |
| 19 | 删掉按账号的 advisory lock | 红：`the bind committed while another transaction held this account's advisory lock` |
| 20 | `lock_timeout` 由 5s 改 90s | 红两处：`SHOW` 断言直接红；竞争测试也红，但失败方式是 `context deadline exceeded (elapsed 1m30s)` 而非 55P03——两种回归可区分 |
| 21 | `exitTimingGateRefused` 常量由 3 改 4 | 红：`timing gate refusal exits 4, want 3`（原来的自证写法在这里是绿的） |
| 22 | 去掉 dry-run 的 `(rolled back; ...)` 后缀 | 红：dry-run 输出缺该后缀（apply 侧另有一条断言它**不**出现） |

（编号 16 未使用，见 4f 末。）

变异 7 顺带查出一件对运行手册有用的事：影子用户是按「真登录会铸的同一对
(issuer, subject)」建的，所以**即使认领分支不存在**，`ResolveOrCreate` 也会找到
同一行。也就是说「不会铸第二个用户」有两道独立保障，不止认领路径一道。

## 4b. 第一轮对抗复审（运维 / CLI 安全视角）修了什么

复审判 FAIL。四条 major 全部修完；四条 minor 修完三条，一条转 follow_up。

**major 1：issuer 会被静默写错。** 手册原来写
`docker run -e SUB2API_LOGIN_BASE_URL`（不带 `=值`）。这两个变量只在
`.env.production` 里，交互 shell 没有 export，`-e VAR` 遇到未设置的变量什么也不
传——工具于是回落到编译进去的默认值。而那个默认值今天**恰好**等于
`.env.production.example` 里的值，所以错误完全不可见；等哪天变量改了就会把错的
issuer 永久写进 `invoice_users.oidc_issuer`，而我自己那条
`TestShadowBindWithAWrongIssuerStillClaimsButLeavesTheIdentityWrong` 已经证明登录
不会修正它。典型的「条件恰好为真」。

修了三处，比复审要求的多一处：

- 工具侧删掉默认值，变量缺失直接拒绝运行——两种模式都拒，dry-run 也不例外：
  预演出来的 issuer 要是错的，它教给运维的就是错的。测试
  `TestPlatformLoginOriginRefusesAnUnsetVariable`。
- 手册改用 `--env-file "$PRODUCTION_ENV_FILE"`，并写清为什么 `-e VAR` 不行。
- **额外加的** `checkPlatformIssuerConsistency`：把 issuer 与库里该平台已有身份的
  issuer 对一遍，不一致直接拒绝。只做前两条的话，issuer 的正确性仍然完全押在
  「运维传对了文件」上；这一条让判据自己为自己负责。它的边界也写明了：该平台
  第一个身份没有可比对象，检查空过，所以摘要把那一行标成
  `<- FIRST identity for this platform` 交人工核对。测试
  `TestOperatorBindRefusesAnIssuerTheDatabaseContradicts`（含空过分支）与
  `TestRunRefusesAnIssuerThatDisagreesWithTheDatabase`。

**major 2：dry-run 清单漏了两条能救命的行。** 补了 `issuer:` 与
`ingest waiting:`，写清怎么比、什么情况必须停手。并按复审建议加了显式告警：
`facts ever seen` 为 0 且是新建绑定时打
`WARNING: no parked facts for this external id`。

要说清它**不是**什么：非 0 不能证明 id 是对的。敲错的 id 落在另一个真实但未绑定
的客户身上时计数很健康，所有权守卫也不拦（它只拦已被别人绑走的 id）。所以告警
文案与手册都明写「两种情况都要回上游后台再核一次」，没有把它包装成一道守卫。
测试 `TestOperatorBindCountsFactsEverSeen`。

**major 3：观察窗口没有可执行命令。** 全部换成能直接粘的 psql 块，沿用手册既有
的 `docker compose ... exec -T postgres psql -X -v ON_ERROR_STOP=1` 写法——不是
复审提到的 `docker exec -i invoice-system-prod-postgres-1 ... -f -`，手册里没有
那种写法，我按实际存在的房子风格来。`dependency_key_hmac` 人手算不出来，所以
摘要现在把它打出来、手册让运维存成变量再粘进查询；它是盲索引、库里本来就以明文
存着，打印不泄露任何东西。另外补了一条用 `invoice_user_id` 查影子身份形状的查询
（`binding_method` 必须是 `operator_attested`）。

**major 4：`Pending > 0` 分支零测试。** 复审删掉那三行全绿，确实如此。补
`TestOperatorBindTimingGateRefusesAPendingBacklog`：用一条**刚建的** queued 事件
（`/readyz` 会宽容的那种）证明这道门确实比 `/readyz` 严，再排干确认门会重新打开
——否则「拒绝」有可能是 fixture 里别的原因造成的。

**minor 5（修了）**：时机门拒绝改用 sentinel `ErrOperatorBindTimingGate`，CLI
退出码 3；手册加退出码表。「现在不是时候」与「出错了」对脚本是两件事。

**minor 6（修了）**：手册加 `--email` 会明文留在 shell 历史与 `ps` 里的提醒。

**minor 7（修了）**：`userEmailAAD` 收进 `application.UserEmailAAD`，
`application/crypto.go` 与 CLI 都调它，CLI 的本地拷贝删除。复审说得对，原来那个
测试是自证循环。`auth/identity_migrate.go` 里第三份**没动**：package auth 是独立
身份边界、不导入 application（`auth/doc.go`），并进来要新开一个叶子包，超出本片
范围——见第 7 节 follow_up。

**minor 8（当轮改了，下一轮按派工改回，最终交给别的片）**：手册 2186/2250 两处
`/app/bin/invoice-eligibility-repair` + `/run/secrets/invoice-db-url` +
`field-keyring.json` 确实是错的（结论与复审给的两个分支都不同），但**本片最终没有
动它们**——见第 4c 节末与第 7 节 follow_up。

## 4c. 第一轮第二、三批复审（数据完整性 + 运维实测）修了什么

两批一起修。数据完整性 2 major + 5 minor，运维补充 1 major + 1 minor + 1 条更正。

### major A：会静默改写客户自证过的绑定（最严重的一条）

复审员在副本里跑了探针，`before=platform_password_login → after=operator_attested`，
`applied=true`、无报错。我复现并确认成立。

成因：`bindExternalAccountTx` 的 UPSERT 只拦「属于**别人**」的行
（`WHERE external_accounts.invoice_user_id=EXCLUDED.invoice_user_id`）。客户自己
用平台密码登录过之后，那一行的属主**正是**代绑定会解析到的同一个 invoice_user
——两条路径用的是同一对 (平台 origin, 上游 id)——于是 UPSERT 走 DO UPDATE 分支，
把 `binding_method` 改写成 `operator_attested`、`verified_at` 重置为 now()，再补一
条 `operator_bound` 审计声称这条绑定是运营建的。`binding_method` 是唯一能区分
「客户自证」与「运营代建」的痕迹，所以这个覆盖不可恢复。

修法：`FOR UPDATE` 那条 SELECT 一并取出 `binding_method`；已存在且不是
`operator_attested` 就拒绝（`ErrOperatorBindWouldOverwriteBinding`）。已存在且是
`operator_attested` 才放行，那是本工具自己的幂等重跑。

**两种模式都拒，包括 dry-run**——dry-run 在这里打一份漂亮的计划，等于打印一份
销毁证据的计划。CLI 另外为这个 sentinel 打了一段解释（`printRefusalDetail`），
说明「通常是客户已经自己登录过了，这时候本来就无事可做」。

测试 `TestOperatorBindRefusesToOverwriteACustomerProvedBinding`（两种模式各断言一
次，并回查 `binding_method`/`verified_at` 未变、没有 operator_bound 审计、停放事实
没被消耗）+ `TestOperatorBindAllowsItsOwnIdempotentRerun`（守卫不能误伤幂等重跑）。

顺带修了一处**测试互相遮蔽**：原来
`TestOperatorBindRefusesAnExternalAccountOwnedBySomebodyElse` 用
`platform_password_login` 当既有绑定，加了新守卫之后它会先撞上新守卫，于是
「属于别人要拒绝」那条就再也测不到了（删掉 UPSERT 的 WHERE 会保持绿）。改成用
`operator_attested` 当既有绑定，两道守卫各自独立可证。

### major B：身份投影事件与代绑定不是同一对身份

`identity_binding` 事件用 `FindUserByOIDC(payload.Issuer, payload.ProviderSubject)`
解析属主，那是**中心 OIDC**；代绑定绑的是（平台登录 origin，上游 id）。影子身份
满足不了这类事件 → ErrForbidden → `PROJECTION_FAILED`（既不算依赖等待也不算瞬态）
→ 八次尝试后死信 → `/readyz` 503 且解不掉。

复审员指出这个形状与「客户首次平台密码登录」同型，**不是本次引入**；生产实测
（2026-09-09 10:35Z 只读）也显示没有现实触发面：全库 `identity_binding` 只有 2 条、
都已 processed、最后一条 2026-08-26。按派工「便宜的护栏 + 明写风险」处理：

- 摘要新增 `identity_binding open (whole deployment)` 一行；非 0 时 `--apply` 拒绝
  （`ErrOperatorBindIdentityProjectionOpen`），CLI 打解释。
- 手册 9c 新增「代绑定给客户留下的两个代价」第二条，写清后果与为什么不改投影链路。

**这一行是全库计数，不是按上游 id 的**，摘要与手册都明说了。做不到按 id：停放的
`identity_binding` 事件按中心 OIDC 的盲索引挂依赖，工具手上只有平台 origin 与上游
id，事件载荷又是加密的，从这里无法判断某条属于哪个客户。派工要的是「该上游 id
是否有」，我实现的是能算得出来的那个，并把差别写明——不想让一个全局数字看起来
像是针对这个客户的结论。

### minor C：漏了 EnsureUser 的那条唤醒

`Service.EnsureUser` 铸完身份会发 `invoice_oidc_user` 唤醒（键：空 source instance
+ `issuer+"\n"+subject`）。代绑定复用 `ensureUserTx` 铸了身份却只发了
`source_external_account` 那条。补上，两条唤醒的结果合并计入
`Released`/`PrePolicySkipped`（对运维来说就是一批积压被放开）。
测试 `TestOperatorBindFiresTheInvoiceOIDCUserWake`，带另一身份的诱饵行。

### minor D：认领路径拿不到已验证邮箱 —— 记入文档，不改登录路径

现状：没被代绑的客户首次登录走建号路径，`EnsureUser` 会把平台带回的邮箱记成已验证
收件地址；被代绑过的走认领路径，那条路径**从不调用 `EnsureUser`**，邮箱进不了
`verified_emails`，客户必须自己走一次邮箱挑战才能提交。

派工倾向做到同等。**我判断不做，把代价写进手册**，理由三条：

1. 认领分支不能简单地调 `EnsureUser`。对投影创建的用户，principal 的
   (issuer, subject) 是中心 OIDC 的那一对，与库里那行不同，`EnsureUser` 会**再铸一个
   invoice_user**——正是认领路径存在的意义所要防止的孤儿。要做同等就得新写一条
   「按 principalID 直接登记已验证邮箱」的路径。
2. 那条路径会改变**所有**被投影绑定账号的首次登录行为，不只是影子账号，而
  「已验证收件地址」正是发票真正寄出去的地方；还要同时决定既有 SSO 已验证地址被
   取代时怎么办。这需要它自己的一片和自己的评审。
3. 对一个由运营代建的身份来说，要求客户自己证明收件地址，本来也更稳妥。

手册 9c「两个代价」第一条写明了这件事，并说明它不是本片引入的。

### minor E：`--external-user-id` 没有形状校验

两个平台的 id 都是 `strconv.FormatInt` 出来的十进制整数。粘错字段（尤其是邮箱）会
铸出一个 (issuer, subject) 永远不会被任何登录复现的影子用户——**没人认领得到**，
而不可逆的 `PRE_POLICY_SKIPPED` 已经写掉了。加 `^[0-9]{1,20}$`，四个反例进
`TestRunRefusesBadInputBeforeTouchingTheDatabase`。

### minor F：按账号的 advisory lock

另外两个 `external_accounts` 写入方（`BindExternalAccountFromSource`、
`RevokeExternalAccountFromSource`）都先取
`pg_advisory_xact_lock(hashtextextended($1,4))`，`$1 = sourceInstanceID+"\n"+externalUserID`。
代绑定原来只开 Serializable 事务。补上同一把锁（同键同种子），取在 source instance
解析出来之后、任何判定依据被读取之前。

注释写明了为什么：行不存在时 `FOR UPDATE` 锁不住任何东西，而「行不存在」正是代绑定
的常态；目前的保护实际上只来自 Serializable 在 UPSERT 上抛 40001，**安全性不能只
挂在隔离级别上**——哪天有人为了少踩 40001 把它降成 Read Committed，这道保护就无声
消失了。锁顺序：external_accounts（这把 advisory 键）→ invoice_users →
source_ingest_events；投影写入方不碰 invoice_users，无环。

### minor G：唤醒测试没有诱饵行

原来只停放了目标账号的事实，「只放出本 key 的行」在套件里恒真。两个唤醒测试各加了
一条属于**别人**的停放事实并断言它仍是 `parked_identity`。作用域本身是从基线继承来
的（`requeueSourceDependencyTx` 的 WHERE 逐字未变），加诱饵是为了让它以后被改宽时会
红——变异 17 证明了这一点。

### 运维 major 9：镜像版本下限

9c 的命令在 rc107 tools 镜像上根本跑不起来（`/usr/local/bin` 里没有
`invoice-account-bind`，要 RC108 才有），而 9c 唯一的前置检查「镜像 ID 与发布清单
一致」rc107 也能过——清单一致不代表二进制存在。9c 开头加了醒目的版本下限，命令里
也加了行内注释。

### 运维 minor 10：语句与锁超时

工具连的是 `invoice_owner`，那个角色**没有任何超时**（`invoice_app` 才有
15s/5s/15s，见 `deploy/postgres/010-invoice-roles.sh`）。事务里要 `FOR UPDATE` 两张
表再批量 UPDATE 几千行 `source_ingest_events`，等锁时会攥着自己的锁最长五分钟。
在事务开头加：

- `lock_timeout='5s'` —— 与 `deploy/postgres` 里三处批量脚本的取值逐字一致
  （`balance-history-cleanup.sql`、两个 `apply-*.sh`）。
- `statement_timeout='5min'` —— 取 CLI 自己的 context 上限，让服务端与客户端在同一
  刻放弃，而不是客户端走了服务端还在磨。
- `idle_in_transaction_session_timeout='15s'` —— 与 `invoice_app` 角色同值。

### 运维更正：9c 的角色与挂载保持不动；2186/2250 两处改回原样

- 9c 用 `invoice_owner_database_url` 是对的（app 角色 15s 语句上限兜不住全表聚合 +
  批量 UPDATE），保持不动。
- 2186/2250 那两处既有 eligibility-repair 命令，我上一轮改过，**这一轮按派工改回
  原样**，只记 follow_up。核实结果供接手的人参考，不必重查：`docker-compose.prod.yml`
  里**没有任何 `target:` 键**，所以 compose secret 一律挂成
  `/run/secrets/<secret 名>`，即 `invoice_app_database_url` / `invoice_field_keyring`，
  库里不存在 `invoice-db-url` 或 `field-keyring.json` 这两个名字；api 镜像只有
  `/app/migrations` 与 `/app/qpdf-policy-gate`，没有 `/app/bin`，tools 二进制也不在
  api 镜像里。也就是说那两段在「docker exec 进 api 容器」语境下同样不成立。

  **悬案已由派工方查明（2026-09-09）**：RC104/RC105 生产上实际跑的是
  `docker run --rm --pull=never --network invoice-system-prod_invoice_db`
  + 两个 `-v` 挂载 + `--entrypoint /usr/local/bin/invoice-eligibility-repair`
  + `invoice-system-tools:0.1.0-rc105`，与 9c 的形状**完全一致**。也就是说手册那两段
  `/app/bin` + `invoice-db-url` 的文本**从来没有被执行过**，是写错的。修正由
  XM-INV-PENDING-RECON 的 C6 承担（那片已经在手册里补了正确的 docker run 块，并要求
  补齐 eligibility-repair 的完整命令行），本片不再动。

## 4d. 复审拿生产数据打回：issuer 检查的比对集选错了

我上一轮加的 `checkPlatformIssuerConsistency` 用的是「`invoice_users` 里
`platform=<平台>` 的全部行」。这在我的 fixture 上成立，在真库上不成立——复审员
拿生产只读数据一对就红了。生产 sub2api 侧的分布是：

| platform | oidc_issuer | 行数 | 来源 |
|---|---|---|---|
| sub2api | `https://api.solov.cc` | 9 | 平台密码登录铸的 |
| sub2api | `https://auth.solov.cc/realms/solov` | 1 | 中心 OIDC 铸的，随后认领了 sub2api 平台身份，绑定是 `source_signed_oidc_projection` |
| newapi | `https://xm.solov.cc` | 1 | 平台密码登录铸的 |
| （空） | `https://console.solov.cc` | 1 | 不影响 |

于是 `SELECT DISTINCT oidc_issuer ... WHERE platform='sub2api'` 返回 2 个值，
撞上我那条 `len(inUse) > 1` 的「先解释清楚再说」分支，**每一次 sub2api 代绑定都会
被拒**，第一次生产 dry-run 就会撞上。

这正是我自己在 4b 节里写「让判据为自己负责」时想防的那类错误的另一面：判据换了，
但**取证范围**是我手列的，而手列的范围只反映了 fixture 里有什么。

改法选了派工给的 (b)，并且再收紧一点：比对集是「在**这个 source instance** 上持有
`platform_password_login` 或 `operator_attested` 绑定的身份」。

- 用 binding_method 过滤，是因为它就是「这个身份由平台登录铸出来」的直接证据；
  `source_signed_oidc_projection` 属于中心 OIDC 铸的身份，issuer 本来就该不同，
  是要排除的对象而不是矛盾。
- 用 `external_accounts.source_instance_id` 而不是 `invoice_users.platform` 限定范围，
  是因为后者只记录**首次认领**的那个平台（runtime.go 认领分支 + RC57 canary），
  多平台身份在那一列里只会留下一个平台，范围会取错。

没选 (a)（`oidc_subject = platform_user_id`）的理由：它靠的是「中心 OIDC 的 subject
是 Keycloak UUID、不会等于十进制上游 id」这个**格式巧合**。今天成立，但它不是被任何
约束保证的，属于「靠外部事实成立的判断」。(b) 用的是语义证据。

测试 `TestOperatorBindIssuerCheckIgnoresCentrallyMintedIdentities`：fixture 里同时
造两个平台登录身份和一个「中心 OIDC issuer + 已认领 sub2api 平台身份 +
`source_signed_oidc_projection` 绑定」的身份，先断言**朴素查询确实看得到 2 个
issuer**（不然这个测试什么也没证明），再断言检查通过、`PlatformIssuerInUse` 是平台
登录那个；最后仍然断言一个真正矛盾的平台登录 issuer 会被拒。

变异 18（把 binding_method 过滤删掉，退回按 source 取全部绑定）→ 红，报的正是
`2 different oidc_issuer values ... refusing to add another until that is explained`
——与复审员预测的生产失败一字不差。

摘要那一行与手册的措辞同步改成「与该来源上平台登录铸的身份一致」，并写明中心 OIDC
用户为什么不算矛盾。

## 4e. 终审 PASS 后的三条 minor（仅测试与一处等价抽取）

终审判 PASS，只剩 minor。三条都收了，**没有任何行为改动**。

**1. 退出码测试是自证的。** 原来拿 `exitCodeFor(...)` 的返回值与 `exitTimingGateRefused`
比，常量改成 4 照样绿——而手册退出码表写的是字面量 3，运维脚本 key 的也是字面量。
改成直接断言 3 与 1，失败信息里点名手册那张表。变异 21（常量改 4）→ 红。

**2. 三条 `SET LOCAL` 没有测试。** 把它们抽成
`operatorBindSessionLimits` 表 + `applyOperatorBindSessionLimits`（语句与顺序逐字
未变，纯抽取），`TestOperatorBindSetsItsSessionLimits` 在事务里 `SHOW` 回来断言
`5s` / `5min` / `15s`。

测试开头先断言 **`SHOW lock_timeout` 在设置前是 `0`**——invoice_owner 本来就没有这三
个设置，不先证明这一点的话，「读回来是 5s」在一个本来就有 5s 的角色上恒真。用 `SHOW`
而不是回读常量，是因为它返回 PostgreSQL 自己规范化后的拼写，能顺带抓到被服务端悄悄
重新解释的值。变异 20（`lock_timeout` 改 90s）→ 红。

**3. advisory lock 没有测试。** `TestOperatorBindTakesTheAccountAdvisoryLock` 用第二
条连接先持有那把锁（`pg_advisory_xact_lock(hashtextextended($1,4))`，键
`sourceInstanceID+"
"+externalUserID`，与 identity.go 两个写入方逐字一致），再跑
一次真实 `--apply`。

这一条同时钉住两件事，而且缺一不可：

- 绑定**必须失败**——只有它确实取了同一把锁（同键同种子）才可能失败；
- 必须在几秒内以 **SQLSTATE 55P03** 失败而不是阻塞——只有 `lock_timeout` 真的生效
  才可能。

所以变异 19（删掉 advisory lock）→ 红，`the bind committed while another transaction
held this account's advisory lock`；变异 20（`lock_timeout` 改 90s）在这条测试上也红，
但**失败方式不同**：`context deadline exceeded (elapsed 1m30s)` 而不是 55P03，两种
回归因此可区分。最后释放锁再跑一次并断言成功，证明前面的拒绝来自竞争而不是 fixture
里别的什么。

这也补上了行级 `FOR UPDATE` 覆盖不到的那块：影子绑定时 `external_accounts` 那一行还
不存在，`FOR UPDATE` 锁不住任何东西。

## 4f. 终审余段的 minor（4、5、7、8、9）

第 6 条（F/10 零测试）在 baf5319 里已经做完，消息交叉了；其余五条如下。除第 7 条外
都是注释与文档改准，**没有行为改动**。

**4. 退出码 2 的范围被写宽了。** 手册与代码注释都写「命令行本身写错」，实际只有
「多给了位置参数」和「三个路径参数不是绝对路径」退 2；旗标**值**非法
（`--platform=sub3api` / `--external-user-id=alice` / `--operator-id=bob`）都在
`run()` 里退 1。

两个选项里选了**改文档不改行为**：把校验挪到 `main()` 会改变退出码契约，而派工这一轮
明确只接受注释/顺序级别的行为改动。手册那张表与代码注释都改成「2 = 位置参数/相对
路径；旗标值非法退 1」，并加一句「写脚本时不要用『退 2 就是命令写错了』来分流」。

**5. advisory lock 的注释不成立。** 我写的「取在任何判定依据被读取之前」是错的：
`sourceIngestHealthTx` 在锁之前就读了。

但**把健康读挪到锁之后并不能解决那个问题**，所以我没有挪，而是把注释写准并写明真实
边界：时机门的依据是**全库**的（所有 ingest 事件），而这把锁是按 (source, 上游账号)
的；两个运营对**不同**账号 apply 时拿的是不同的锁，互不阻塞，都能读到 Pending=0 都
过门。挪顺序改不了这一点——不同键本来就不互斥。真正兜底的是 Serializable
（复审员实测到 `Canceled on identification as a pivot`），以及运行手册那条「一次一个、
一个人操作」的**流程**规矩，那条规矩不是代码强制的。注释现在把这三件事都说了。

（顺带：挪顺序还会改变错误优先级——平台没有 enabled source instance 时会先报那个而不是
先报时机门。为一个解决不了的问题去改错误优先级不划算。）

**7. dry-run 打出的 id 会被抄去做观察窗口变量。** dry-run 确实 INSERT 过那两行再回滚，
所以打出来的是**真实但已不存在**的 id；而手册要求把 `external_account_id` /
`invoice_user_id` 存成变量去跑观察 SQL，抄 dry-run 的会让每条查询都返回空，看起来就像
绑定失败了。

dry-run 时两行加后缀 `(rolled back; --apply will mint different ids)`；手册观察窗口段
加了醒目警告，要求三个值一律抄 `--apply` 那次的。这是本批**唯一**的行为改动（只是多打
一段字）。测试两侧都断言：dry-run 必须有这个后缀、apply 必须没有（否则会教运维去怀疑
那些真正能用的 id）。变异 22（去掉后缀）→ 红。

**8. `--email` 对已存在的身份是静默空操作。** `ensureUserTx` 的 UPSERT 只在
`EXCLUDED.email_verified` 为真时替换密文，而本工具永远传 FALSE（这本身是对的——运营
敲进去的地址不构成验证）。所以对已存在的身份重跑并加 `--email` 什么也不会发生。手册
`--email` 那段补了一句说明，并指出要补邮箱只能走客户自己的验证流程。

**9. `FactsEverSeen` 的口径比注释窄，而且窄两处。** 复审员点出 PRE_POLICY_SKIPPED 那处
并提示「后面可能还有一处更窄」——对照 `requeueSourceDependencyTx` 核完，确实有两处：

- PRE_POLICY_SKIPPED 分支把 `dependency_key_hmac` 置空且**从不**写 `catchup_key_hmac`；
- 释放分支的 `catchup_key_hmac=CASE WHEN processing_status='parked_identity' THEN $2
  ELSE catchup_key_hmac END`，从 `waiting_dependency` 释放的行保持原值（通常是 NULL）。

两类行最后两个键都空，这个计数看不见它们。所以真实口径是「**现在还挂着本账号两个键
之一**的行」，它只会**少报**、不会多报。

没有把口径改宽（那些行已经没有任何可关联的键，改不了），而是把字段注释、函数注释与
摘要说明都写准。同时说明为什么这对唯一的用途是安全的：告警只在 `BindingCreated` 为真
时打，而一个事实已经被唤醒过的账号必然已经有绑定，`BindingCreated` 就是 false，所以
少报不会造成误报警。

**变异编号 16 的缺口**：15 之后直接跳到 17，中间没有 16 号——那是我编号时跳过的，不是
漏掉一条变异。为避免再被当成缺失，这里记一笔。

## 4g. 生产实跑打回：9c 的 origin 取法在真机上不成立

RC109 上线后，主控者按 9c 对候选账号跑第一次 dry-run，被工具直接拒了：
`SUB2API_LOGIN_BASE_URL is not set: ...`。

根因（已复核）：生产 `.env.production` 里**根本没有**
`SUB2API_LOGIN_BASE_URL` / `NEWAPI_LOGIN_BASE_URL` 这两个键。api 容器里的值来自
`deploy/docker-compose.prod.yml:259-260` 的
`${SUB2API_LOGIN_BASE_URL:-https://api.solov.cc}` / `${NEWAPI_LOGIN_BASE_URL:-https://xm.solov.cc}`
——`:-默认值` 意味着这两个键在 env 文件里是**可选的**。于是 `--env-file` 传进去的是空，
工具（按设计）拒绝运行。

**这是我第二次犯同一类错误。** 上一次是 issuer 比对集拿 fixture 当真相源（4d 节），
这一次是拿 `deploy/.env.production.example` 当真相源——例子文件第 113、114 行确实列了
这两个键，我就据此断定真机上也有。可 compose 里写着 `:-默认值`，那本身就是「这个键
可以不存在」的声明，我没有读到那一层。**模板里有 ≠ 真机上有。**

改法：origin 从 **api 容器实际生效值**取，那才是「必须与之一致」的那个东西：

```
S="$(docker exec invoice-system-prod-api-1 printenv SUB2API_LOGIN_BASE_URL)"
N="$(docker exec invoice-system-prod-api-1 printenv NEWAPI_LOGIN_BASE_URL)"
docker run ... -e "SUB2API_LOGIN_BASE_URL=$S" -e "NEWAPI_LOGIN_BASE_URL=$N" ...
```

`--env-file` 按派工保留，无害：`-e` 优先级高于它，显式值总会赢；万一日后有人把这两个
键写进 env 文件且值不同，赢的仍是从 api 容器取到的实际生效值。命令块里加了一行
`printf` 把取到的两个值打出来，空串就说明容器名不对或没在跑，先解决再往下走。

**工具的错误信息也改了**（本节唯一的代码改动，纯文案）：原来那句让人去用
`--env-file`——正是刚刚失败的那个做法，留着会让下一个运维原样再撞一次。现在它给出
`docker exec ... printenv` 的取法，并明说「不要指望 --env-file，这个键在
.env.production 里是可选的」。对应测试断言从「消息里含 --env-file」改成「含
printenv 且含 Do NOT rely on --env-file」。

工具**拒绝运行**这个行为本身是对的、没有改：它挡住了一次会把错 issuer 永久写进
`invoice_users.oidc_issuer` 的操作，正是 4b 节加它的目的。错的只是它和手册给出的
补救办法。

## 4h. 首次生产代绑定的实测（账号 2823，2026-09-09）

主控者在生产执行，我未碰生产。数据由主控者提供，机制部分我复核过。

| 项 | 值 |
|---|---|
| dry-run | 14:43:28Z，origins 从 api 容器 `printenv` 复制 |
| issuer | `https://api.solov.cc`，标 `matches every platform-login identity on this source` |
| 时机门 | GO；pending 0 / dead 0 / waiting 647,541 / identity_binding open 0 |
| `PRE_POLICY_SKIPPED` | **0** |
| `released to queued` / `facts ever seen` | **552** / 552 |
| apply | 14:54:26Z，与 dry-run 逐字一致（waiting 648,022、released 552） |
| 新建 | invoice_user `c7e5bb12…`、external_account `eb233095…`、`operator_attested`/`verified`（`verified_at` 14:54:29.025Z） |
| T+3s | `source_account_eligibility_state` 已出行（`syncing`，catchup 键在，`finalized_through` = 08-31 16:00Z） |
| T+23s | 41 processed / 59 processing / 452 queued；全库 dead 0 |

`PRE_POLICY_SKIPPED=0` 是对的：2823 是 09-03 注册，两笔订单与四百多条用量全在策略
起点（08-31 16:00Z）之后，没有起点前的事实可写off。dry-run 与 apply 的数字逐字一致，
也印证了「dry-run 的数是实测不是估算」这条设计。

### 补数实测耗时（这条终于从估算变成事实）

观察脚本每 30 秒采样，所以「≤」是采样上界。

| 时刻 | 事件 |
|---|---|
| 14:54:26Z | `--apply` 落地，released 552 |
| 14:54:29Z | 资格状态行出行（`syncing`，T+3s） |
| 14:56:49Z → 14:58:22Z | 未处理 257 → 191 → 125 → 58，约 66 条/30 秒 |
| ≤14:58:53Z | 排空、`/readyz` 回 200、离开 `syncing` 变 `active`（连击 0），三件事同一采样点 |
| 14:59:05.605Z | **首次评估**，T+4 分 39 秒；同一事务里 `pending_reconciliation` entered→exited、`projection.rebuilt` ×14、写下 1 条结转证明 |
| 15:17:21Z | 复查：`active`、连击 0、`finalized_through` 推进到 14:56:27Z、无 overage |

**排空速率约 2.1 条/秒**，四个区间分别是 2.13 / 2.13 / 2.16 条/秒，从 apply 起算的
累计均值 2.06 → 2.09，高度线性。按 2.15 条/秒预测 552 条需 257 秒，实测上界 267 秒
（T+4 分 27 秒），吻合。

由此得出一条能写进手册、按下 `--apply` 前就能算的规则：

> 窗口秒数 ≈ `released to queued` ÷ 2

**设计稿的「几十分钟到一小时」对这个规模高估了一个数量级**（552 条实际 4 分半）。
但拿同一速率外推设计稿提到的那个 6842 条的账号是**约 53 分钟**——所以那个估算并没有
错，错的是把它当成了与账号规模无关的常数。窗口长度由 `released to queued` 决定。
这也是我这边第二次出现「估的时间比实测长得多」（上一次是镜像门禁），记在这里。

速率的适用范围要说清楚：**一个账号的一次实测**，事件类型构成（用量／余额检查点／
订单）与当时库负载都会影响它，当量级用，不当承诺。

补数结果：2 个额度批各 ¥10.00（对应起点后 2 单）、151 张余额检查点、400 条用量入账。
一次健康补数的审计序列（可拿来核对）：

```
user.created → external_account.operator_bound → identity_catchup.completed
→ policy_anchor.bootstrapped → pending_reconciliation entered → exited
→ projection.rebuilt ×14
```

### 首次评估：紧跟排空，不是「再等 15 分钟」——我这句写错了

首次评估落在 **14:59:05.605Z（T+4 分 39 秒）**，排空后约 12 秒，而不是我在手册里写
的「出行之后还要等 15 分钟终局延迟」。

核对了机制，主控者的解释成立：评估覆盖的是 `(finalized_through, requested_through]`，
而 `requested_through` 最多推到「现在减 `defaultEligibilityFinalizationDelay`（15
分钟）」。那条线只挡**最新的一段窗口**；代绑定放出来的是**历史**事实（2823 那批是
09-03 起的），远早于它，所以一排空就立刻可评估。我把「延迟挡的是最新窗口」误读成了
「每次评估前都要等一刻钟」。手册对应句子已按实测改正，并写明早期版本那句是错的。

**所以整件事从 `--apply` 到首次评估不到 5 分钟**，不是原先以为的「补数几十分钟 + 再
等一刻钟」。首次评估同一事务里还完成了 `pending_reconciliation` entered→exited、
`projection.rebuilt` ×14、写下 1 条结转证明。

评估行样本（也记进手册，供以后核对「正常长什么样」）：08-31 16:00 那张策略起点检查点
（balance 50,000,000）判 `positive_classified_non_cash`（expected 0、difference
50,000,000，即起点合成的 0.50 元当量 `UNKNOWN_POSITIVE`）；09-03 的四张判 `matched`
（expected = balance、difference 0）。**起点那张不是 `matched` 是正常的**，不要当异常
去查。15:17:21Z 复查：`active`、连击 0、`finalized_through` 已推进到 14:56:27Z、无
overage。

至此本片**没有未证实项**了。

### 顺带查实的一件事：补数窗口内 `/readyz` 必然是 503，而且与死信无关

主控者报「readyz 503（预期）」。我去核了机制，结论是**确实预期，但我的手册没写，而且
写反了**——原文让运维「同时看 `/readyz`」，把状态码当死信信号用。

原因：`readyz` 的 `source_ingest` 那道闸判的是 `min(created_at)` 距今多久（>15 分钟
即不健康），而**唤醒不会重置 `created_at`**（`requeueSourceDependencyTx` 里没有这一列）。
放出来的 552 条带的还是它们当初入库的时间，一放出来就立刻越线。所以窗口一开就红、
一直红到排空，`check` 是 `source_ingest`；只有 `check` 是 `source_ingest_dead_events`
才是真出事。

连带影响也核了（都不影响服务，但事先不知道会慌）：

- api 容器 healthcheck 打的就是 `/readyz`（`interval: 10s`、`retries: 12`），所以约
  **2 分钟后 `docker ps` 会显示 api 为 `unhealthy`**，直到排空。**不会被重启**——
  compose 是 `restart: unless-stopped`，Docker 不会因 unhealthy 重启容器。
- **用户流量不受影响**：Nginx 只透传 `/readyz`，页面与 API 是另外的 `location`；
  `ingest-proxy` 对 api 的依赖是 `service_started` 而非 `service_healthy`，不级联。

手册观察窗口段据此改了三处：盯死信改用 SQL 并明说别用状态码；新增「readyz 整窗口
恒红 + 怎么用 `check` 字段区分良性与真出事」；新增「api 会显示 unhealthy、不会重启、
流量不受影响」，并提醒**开工前跟订阅了 readyz/容器健康的告警值班人打招呼**，否则每次
代绑定都会稳定误报一次。

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

（下面四条按 2026-09-09 账号 2823 的生产实测更新过，见 4h 节。）

- ~~生产上补数实际要多久~~ —— **已证实**：552 条约 4 分半排空、速率约 2.1 条/秒、
  首次评估 T+4 分 39 秒。已转成可预估规则写进手册。
- ~~候选客户在上游是否真的有起点后的充值 + 消耗（§E 前提 4）~~ —— 对 2823 **已证实**：
  补数落地 2 个额度批各 ¥10.00（对应起点后 2 单）、400 条用量、151 张余额检查点。
  注意这只对**这一个**候选成立，每个新候选仍要人工去上游后台核，工具读不到上游库。
- `POLICY_ANCHOR` 首次评估是否按构造 matched（§E 前提 6）—— **仍不算证实，而且实测
  数据看起来与那句话字面不符**：2823 的首次评估里，08-31 16:00 那张**策略起点**检查点
  判的是 `positive_classified_non_cash`（expected 0、difference 50,000,000，即起点合成
  的 `UNKNOWN_POSITIVE`），只有 09-03 之后的四张判 `matched`。这**可能**只是「前提 6
  说的是锚点之后的检查点」这种读法差异，而不是行为不符——**我没有去核 consumption.go
  那段注释的原意，不下结论**。谁要用到这条前提，请自己对着代码核一遍，别拿这里的观测
  当结论。
- 备份副本方案（设计稿方案二）的密钥前提（§E 前提 8）。没做，本片也用不到。

## 6. 下一个人接手要注意

- `binding_method` 至今**不进任何 wire**，管理端账本也看不到它。影子清单只能人工
  另存——这一点设计稿 §B 风险 7 已经说了，本片没有改变它。
- 加新的 `binding_method` 值时，注意 `Service.BindExternalAccount` 里那两个
  `== "platform_password_login"` 判断：它们是白名单式的，新值默认既不派 HMAC 也
  不唤醒。这次是自己在 CLI 侧补的，不是改那两个判断——改它们会影响真实登录路径。
- 时机门里 `Pending=0` 这一条会让「连着绑第二个」在上一个排干之前直接失败。这是
  设计意图，`TestOperatorBindIsIdempotent` 里那段 drain 的注释解释了。

## 7. Follow-up（本片没做，记在这里）

- **`userEmailAAD` 还有第三份。** `auth/identity_migrate.go:81` 的
  `userEmailAADForMigration` 与 `application.UserEmailAAD` 是同一个字面量。没并是
  因为 package auth 刻意不导入 application（`auth/doc.go` 写了理由），要并就得把
  这个串挪进一个新的叶子包，两边都导入它。三份里现在有两份是同一处定义；剩下这
  一份漂开的后果与前面一样：加密当场成功，客户第一次登录时解密失败。
- ~~补数实际耗时仍未实测~~ —— **已全部实测完毕**（4h 节，2026-09-09 账号 2823）：
  552 条约 4 分半排空，速率约 2.1 条/秒，首次评估在 T+4 分 39 秒。已转成「窗口秒数
  ≈ `released to queued` ÷ 2」写进手册 9c，并改正了手册里「还要等 15 分钟终局延迟」
  那句错话。**本片自此没有未证实项。**
- **`checkPlatformIssuerConsistency` 在每个平台的第一个身份上空过。** 这是无法
  消除的：库里没有可比对象。缓解是摘要把那一行标出来交人工核对。等两个平台各有
  一个真实身份之后，这道检查才真正开始生效。
- **管理端仍然看不到 `binding_method`。** 影子清单只能人工另存，设计稿 §B 风险 7
  说过，本片没有改变。真要治，得让账本 wire 带上 `binding_method`，那是另一片。
- **认领路径拿不到已验证邮箱**（第 4c 节 minor D）。要做到与建号路径同等，需要新写
  一条「按 principalID 直接登记已验证邮箱」的路径，并决定既有 SSO 已验证地址被取代
  时怎么办。影响所有被投影绑定的账号，不只影子账号，值得单独一片。
- ~~手册 2186/2250 两处 eligibility-repair 命令路径与 secret 名有误~~ —— **已由
  XM-INV-PENDING-RECON 的 C6 处理**，本片不动。结论：那两段文本从未被执行过，
  RC104/RC105 实际跑的与 9c 形状一致（证据见第 4c 节末）。
- **`identity_binding` 护栏是全库粒度**，做不到按上游 id（原因见 4c major B）。真要
  按 id，得让工具能从中心 OIDC 那一侧反查，或者让事件带上可关联的非加密标识。
