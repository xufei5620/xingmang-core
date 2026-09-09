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
- `docs/PRODUCTION-RUNBOOK.md` 2186/2250 两处既有的 eligibility-repair 命令：
  路径与 secret 名本来就是错的，一并改正（见第 4b 节 minor 8）。

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

**minor 8（修了，但结论与复审给的两个分支都不同）**：手册 2186/2250 两处
`/app/bin/invoice-eligibility-repair` + `/run/secrets/invoice-db-url` +
`field-keyring.json` **本来就是错的**，不是「docker exec 进 api 容器」的另一种
语境。`/app/bin` 在整个仓库里只出现在这两处；`backend/Dockerfile` 装到
`/usr/local/bin`；compose secret 叫 `invoice_owner_database_url` /
`invoice_field_keyring`；api 镜像里也没有 tools 二进制。所以直接改正，改成与 9c
相同的 docker run 形状并加注说明。

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

## 7. Follow-up（本片没做，记在这里）

- **`userEmailAAD` 还有第三份。** `auth/identity_migrate.go:81` 的
  `userEmailAADForMigration` 与 `application.UserEmailAAD` 是同一个字面量。没并是
  因为 package auth 刻意不导入 application（`auth/doc.go` 写了理由），要并就得把
  这个串挪进一个新的叶子包，两边都导入它。三份里现在有两份是同一处定义；剩下这
  一份漂开的后果与前面一样：加密当场成功，客户第一次登录时解密失败。
- **补数实际耗时仍未实测。** 第一次生产代绑定时把 `--apply` 时刻、
  `source_account_eligibility_state` 出行时刻、首次评估时刻记进发布记录，把设计稿
  那句「几十分钟到一小时」的估算换成事实。手册 9c 观察窗口一节已经写了要记。
- **`checkPlatformIssuerConsistency` 在每个平台的第一个身份上空过。** 这是无法
  消除的：库里没有可比对象。缓解是摘要把那一行标出来交人工核对。等两个平台各有
  一个真实身份之后，这道检查才真正开始生效。
- **管理端仍然看不到 `binding_method`。** 影子清单只能人工另存，设计稿 §B 风险 7
  说过，本片没有改变。真要治，得让账本 wire 带上 `binding_method`，那是另一片。
