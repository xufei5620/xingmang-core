# finance —— 成本登记簿 + 利润台账（XM-0037a / b）

成本核算的**配置底座**与**入账事实**两层：

- **登记簿（a）**：每个上游账号一行（接入方式、凭据引用、充值倍率、
  业务日时区），加上「上游令牌 ↔ 自营账号」的映射。
- **利润台账（b）**：每（上游账号，业务日，上游令牌）一行的收入 / 成本 /
  毛利，由 River 周期任务写入，只读 Query 供出。

规格权威是 `docs/superpowers/plans/2026-08-28-xm-0037-cost-accounting-design.md`
（下称「设计稿」，§ 号均指它）。本文只记**实现层的取舍与偏差**，
不复述口径——口径以设计稿全文为准。

**范围到切分 b 为止。** 余额历史（§2.3）、订阅成本批次与代理资产（§2.5）、
看板供数（§8.5）、影子对比（§9）分别属于 XM-0037c / d / e，
各自新增迁移与代码。

---

## 这个模块的三条铁律

1. **凭据只经 CredentialRef**（ADR-014、宪法 7 条）。本包从不解析、从不持有、
   从不返回明文；查询接口回显的永远只是 `secret://<scope>/<name>`。
   连**被拒绝时的错误**也不回显被拒的值——见下面「拒绝时不回显」。
2. **倍率是除数，且是规范存储量**（§3.4）。「充值成本率」= 1/倍率 只是展示
   投影，不入库；成本计算恒用整数除法（`money.Divide`），绝不先取倒数再乘。
3. **金额不在本包出现**。登记簿里一个金额列都没有，成本金额是 037b 的事。
   宪法 13 条在这里体现为「这张表里没有一个金额列」。

---

## 相对设计稿的两处偏差（都记在这里）

设计稿 §2.1 给出了两张表的列清单。实现有两处与那份清单不同，
都不是口径改动，但值得单独说清。

### 1. `finance.token_map` 多了一列 `credential_ref`

§2.1 的 `token_map` 列清单里没有它，但**同一节的正文**要求：

> sub2api 成本侧要用每令牌明文 `sk-` 打 `/v1/usage`（§3.1）……
> 平台不落明文表，由 `SecretProvider` 解析

§11.11 与 §12 的拍板同样是「sk- 走 SecretProvider」。而 `SecretProvider` 按
`CredentialRef` 寻址——那个 ref 必须存在某处，唯一的「每令牌」维度就是这张表。

所以这一列是那两条要求的**强制推论**，不是新增口径：它存的仍然只是引用，
明文一如既往地不进库。newapi 侧留空（成本走账号级 New-Api-User + Cookie）。

`finance.token_map.set` 对 **sub2api 计量型渠道**强制要求这一列：
没有它，采集任务每轮都会报一次「凭据缺失」，不如在登记那一刻就拒绝。

### 2. `upstream_account` 多了一条唯一索引

§2.1 只给了 uuid 主键，没有自然键。实现加了

```sql
CREATE UNIQUE INDEX upstream_account_env_system_base_url_key
    ON finance.upstream_account (environment, system_type, base_url)
    WHERE base_url IS NOT NULL;
```

**这是有意收紧。** 没有它时，重复登记同一个上游站不会报错，只会让同一笔上游
实扣被两行各算一遍——成本翻倍，而两行看起来都很正常。`base_url` 为空的行
（订阅型可能没有可读端点）不进唯一集合；换一套 `system_type` 不算重复
（同一个域名后面可能真的挂着两套系统）。

---

## 三套成本口径的分叉点：`access_method`

`access_method` 不是一个分类标签，三种取值对应三条完全不同的成本算法（§2.0）：

| 取值 | 成本口径 | 倍率 | 影子对比 |
|---|---|---|---|
| `upstream_key` | 实扣 ÷ `recharge_ratio`（§3.1） | **必填** | ★进（§9 的唯一范围） |
| `official_api` | 原厂账单（v1 占位后置，§12 拍板） | 不约束 | 不进 |
| `subscription_account` | 固定付款按天 / 按账号摊销（§3.5，037c） | **必须为空** | 不进（SoloAI 无此对象） |

选错的后果不是报错，是**一个看起来完全正常的错数字**。所以：

- 它 `NOT NULL` 且**无默认值**：登记时必须显式选；
- 登记后**不可改**（`finance.upstream_account.set` 当场拒绝）——改它会让同一个
  账号的历史成本前后用两套算法算出来，而台账里没有任何痕迹。要变只能新登记
  一条并停用旧的，这样「哪段时间按哪套口径算」才有据可查；
- 三条倍率约束（计量型必填 / 订阅型必空 / official 不约束）在**领域层与库层
  各写一遍**：领域层能说人话，库层保证任何写入路径都绕不过去。

---

## 倍率：为什么这么较真

`recharge_ratio` 是**唯一**会改变成本口径的字段，且它可随时改写、
SoloAI 侧无历史（迁移 0084）。四处设计都是围着它转的：

### 存储：NUMERIC，读回不经 float

比例用 Decimal（宪法 13 条），所以是 `NUMERIC` 列而不是 `bigint`。
读回走 `pgtype.Numeric` 的 `Int` + `Exp`（精确整数对），
**绝不**走 `Float64Value()`——那是把一个精确的十进制数塞进 53 位尾数，
`1.15` 读回来会变成 `1.1499999999999999`。倍率是除数，尾数错一点点，
逐日折算下来就是影子对比里对不上的那一分钱。

`store_integration_test.go` 的 `TestRechargeRatioSurvivesNumericRoundTrip`
拿 `1.15` / `0.1` / `3.14159` 这类 float64 表示不了的值逐条往返，
并比对折算结果是否一致。

### 参数：定点十进制**字符串**，不是数字

Action Schema 只有 int / string / bool / string_slice 四种类型，没有 Decimal；
而 JSON 数字一路解成 float64，`1.15` 进来就已经不是 `1.15` 了。
所以倍率参数是字符串，由 `money.ParseRatio` 在整数域里解析。
HTTP 响应同理：`"recharge_ratio":"1.15"` 而不是 `"recharge_ratio":1.15`。

### 「未配置」≠「配置成 0」

`money.Ratio` 有一个 `set` 标记。没有它的话 `ParseRatio("0")` 与从未赋值会给出
同一个结构体，于是「订阅型渠道本就不该有倍率」（正常状态）与
「有人把倍率填成 0」（错误配置）在校验里长得一模一样，只有一条能报出来。

库里未配置落 **SQL NULL**——落 0 的话读回来会变成「配置成 0」，
而那是个非法值；一张自洽的表不该在读回时产出自己拒绝写入的东西。

### 非正倍率：算术层兜底，库层拒绝

SoloAI 对 `ratio <= 0` 的处理是**按 1 折算**（`relay_profit.go:97`，§3.1 逐字要求），
`money.Divide` 照样实现了那条兜底。但**登记簿不生产这种行**（库层 CHECK +
领域校验 + Action 参数校验三道）：一个被静默当成 1 的 0 倍率，会让「上游涨价」
与「有人把倍率填成 0」在台账上长得一模一样（宪法 12 条）。

两条纪律分工明确——**算术层与标准答案对齐，库层不让垃圾进来**。

### 改倍率有独立的 Action 与独立的权限

`finance.recharge_ratio.set` 与 `finance.upstream_account.set` 分开（§6.3）：
倍率改动的审计事件必须能被单独检索出来（「这个月倍率被谁改过几次」），
混在一条通用更新里，那个问题就只能靠比对 before/after 才答得出。
`reason` 必填并进事件的 reason 列——改倍率直接改变毛利报表。

**不追溯历史**：已入账的 `profit_daily` 行各自冻结 `ratio_snapshot`
（§6.3，037b 实现），改倍率只影响此后新算的行。

---

## 业务日时区只收固定偏移

`business_day_tz` 只接受 `±HH:MM`，**不接受 IANA 时区名**。

写 `Asia/Shanghai` 看起来更规范，但那是一个**会随 tzdata 更新而变**的定义；
核算口径里的 CST 必须是固定 +08:00 无夏令时（§4 标 ★，与 SoloAI `cstNow`
一致）。让领域层与库层都只认偏移量，口径就不会随某次基础镜像升级悄悄漂走。

`BusinessDayLocation()` 用 `time.FixedZone` 构造，也不依赖容器里有没有
tzdata——scratch 镜像里 `LoadLocation` 会失败，而业务日切日不该因为基础镜像
瘦身就换个口径。

---

## 拒绝时不回显

领域错误会一路走到 Action 的 `Message`、进 HTTP 响应、进日志
（`httpapi.safeMessage` 把 `action.Error` 的 Message 原样回给调用方）。
而两条校验拦下的恰恰是「有人把明文粘进了这个字段」：

- `credential_ref` 不是 `secret://` 形态；
- `base_url` 含 `user:pass@` 段。

回显被拒的值等于把那次手滑变成一次真正的泄漏（宪法 7 条）。
所以这两条的错误只说清形状要求，不带输入。
`account_test.go` 的 `TestRejectionsNeverEchoSecrets` 钉住它。

---

## 权限：四个 scope，刻意不合并

| Scope | 用途 |
|---|---|
| `finance.read` | 读登记簿 |
| `finance.upstream_account.manage` | 登记 / 修改账号（L1） |
| `finance.recharge_ratio.manage` | 改倍率（L1） |
| `finance.token_map.manage` | 维护映射（L1） |

**不复用 `ops.read`**（对照 `alerts.ScopeRead` 复用它的理由）：告警内容就是
运营指标的判读结果，泄漏面完全相同；登记簿不是——倍率是商业条款
（我们从上游拿到几折），映射是成本归属的对账键。能看指标的人不该自动能看到
我们跟每个上游谈的价。

写权限拆成三个，是因为三件事的爆炸半径不同：改倍率直接改毛利报表；
映射写错会把成本记到别的渠道上（两条渠道一个虚高一个虚低而**合计完全正确**，
最难从总数上看出来）；改 base_url 只是让采集打不通，很快就会有人发现。

四个 Action 都是 **L1 + 仅人类身份**：
- L1 是 §8.3 的硬性要求——Foundation-A 内核对 L2+ 是 fail-closed，
  声明成 L2 等于让这个动作在当前阶段根本执行不了；
- 仅人类是 ADR-009 红线：登记簿决定「用哪套凭据、按什么倍率算成本」，
  让采集任务自己能改这两样，等于给了一条「算不出来就把倍率改到能算」的路径。

环境**不由参数传**：取自调用者身份（宪法 15 条）。每个 Handler 在读到资源
之后再复核一次资源所属环境——内核只校验「这个 Action 允许在你的环境执行」，
它不认识资源，一个 staging 身份完全可能拿着生产账号的 UUID 打过来。

---

## 数据模型

见 `db/migrations/000008_finance_registry.up.sql`（每条约束都带了理由）。
查询在 `db/queries/finance.sql`，Go 侧仓储在 `store.go`。

`token_map` 的反向索引 `token_map_own_account_idx` **刻意不是唯一索引**：
一个自营账号由多把上游 key 供给是真实存在的形态（同一渠道挂多把 key 做冗余），
约束成一对一会让运营在扩容那天被库拦住，然后想出一个绕开登记簿的办法。

---

## 利润台账（XM-0037b）

台账是**事实**，登记簿是**配置**：配置随时可改，台账过去冻结。两者分成两个
仓储类型（`Store` / `ProfitStore`）就是为了让「哪些方法受 §5.3 约束」
不必靠记忆。

### 三条不静默纪律各自落在哪一层

设计稿 §5 的三条纪律没有一条能只靠一层保证，所以每条都写了明确的落点：

| 纪律 | 领域层 | 库层 | 采集层 |
|---|---|---|---|
| §5.1 取数失败写 NULL 不写 0 | 金额是 `*int64`；`Validate` 拦两侧全空 | 金额列可空 + `profit_daily_not_entirely_unknown` CHECK | 读不到 → 该侧留 nil，不写 0 |
| §5.2 平台归属四桶 | `PlatformBucket` / 恒等式类型 | `SumProfitDailyByPlatform` 不 COALESCE、不 INNER JOIN | —— |
| §5.3 今日可覆盖、过去冻结 | `assertWritableDay`（唯一关口） | 表达不了（`now()` 不是 IMMUTABLE） | 业务日按账号时区现算 |

第三条**只能**在 Go 层：Postgres 的 CHECK 只接受 IMMUTABLE 表达式，
而判据是「现在是哪天」。所以 `ProfitStore` 里没有第二个 INSERT/UPDATE 入口
——写入路径必须全部经过那一个关口。

### 「只有一侧已知 → 纯 UPDATE 不建行」

这是最容易被顺手优化掉的一条，代价说清楚：缺一侧就没有可断言的利润。
把未知的那侧当 0 建行，报表上会出现一条「毛利 = 收入」的记录，
它不报错、不缺字段、看起来完全正常，而它是假的（宪法 12 条）。

两个「跳过」（`ErrProfitNothingKnown` / `ErrProfitOneSidedNoRow`）是
**语义错误而不是故障**：采集器把它们计入跳过并继续，不当采集失败去重试——
重试也不会让上游多出一个数来。三类计数在观测里分开呈现，合成一个 failed
之后，看板上的红点就再也说不清该不该有人起来处理。

### 相对设计稿 §2.2 的四处列增补

§2.2 给了 `profit_daily` 的列清单。实现多了四列，都不是口径改动：

1. **`business_day_tz`**（逐行冻结的切日偏移）。登记簿的
   `business_day_tz` 是可改的（`finance.upstream_account.set` 允许改它），
   改完之后历史行的 `business_day` 究竟按哪个偏移切出来的就再也说不清了。
   与 `ratio_snapshot` 要逐行冻结（§6.3）是同一个道理；宪法 14 条要求
   「业务日结时区显式声明」，声明在行里才叫显式。
2. **`source`**、3. **`cost_observed_at`**、4. **`revenue_observed_at`**
   （数据来源与新鲜度）。台账是 037d 看板 `ChannelSummary` 的唯一供数
   （§12.2），而 §13 要求每个展示值带 `observedAt` / `source`——
   ops 指标是**环境级聚合**，答不出「这一行的数字是什么时候、谁读来的」。
   两侧各一个观测时刻而不是合并成一个：它们读的是不同上游、在不同时刻、
   可以各自失败（§5.1 要求它们能分别为 NULL）。合成一个的话，一次新鲜的
   收入读取就会替一份陈旧的成本读数背书。

另有一列 **`profit_minor` 是生成列**，不是第三个可写金额列。§2.2 把毛利
定义为 `revenue_minor - cost_minor`（一个减法，不是一列数据），所以让库
去算：三个数不可能漂移，且 NULL 传播天然正确——任一侧未知，毛利就是未知，
而不是「等于另一侧」。

### 四桶用 EXISTS，不用 LEFT JOIN

§5.2 字面写的是 `LEFT JOIN`。实现用 `EXISTS`，因为 `core.service` 的唯一键是
`(service_type, instance_id)`：只按 `instance_id` 左连会在同名不同类型时
**放大行数**，而放大之后恒等式（四桶行数之和 == 独立窗口计数）会以一种
非常难查的方式失败。`EXISTS` 保留了 LEFT JOIN 的意图（不丢行），
且结构上不可能改变行数。

恒等式在 `SumByPlatform` 内部当场校验，不平就报
`ErrPlatformBucketMismatch` 并把差额写进错误文本——返回一个凑合的结果
等于把一个可查的故障变成一份可信的错报表。两条查询跑在同一个
**REPEATABLE READ 只读事务**里：不这样的话，两次查询之间新写进来的一行
会让恒等式失败，而那是采集任务的正常节奏，不是分桶出错。

### `platform_id` 现在必然为 NULL

登记簿（§2.1）里没有任何一列记录「这个自营账号属于哪个自营平台」，
平台归属的配置面是 037c/d 的事。所以采集器的 `PlatformResolver` 默认返回
空串 → 落库 NULL → 全部进「未归属」那一桶。

§1.3 要求 platform_id「第一天就写全」指的是**写入路径从第一天起就带着
这一列**（不复刻 SoloAI「写入方漏写」的缺陷），不是「第一天就必须有值」
——§2.2 明确 NULL = 写入当时未配对。留下 `PlatformResolver` 这个钩子，
037d 接上归属配置时只需注入一个函数，不必回来改台账的写入路径。

`COALESCE` 方向是「空缺可补、已有不动」（§5.3）：绑定变更只影响新行，
不追溯改写历史归属——否则今天调一次绑定，上个月的平台毛利就变了。

### 收入归属歧义：N 把令牌供给同一个自营账号

`token_map` 的反向索引刻意不是唯一索引（见 000008 迁移），所以「一个自营
账号由多把上游 key 供给」是登记簿允许的形态。但收入端点是**账号级**的
（§3.2），而台账的行是**令牌级**的，于是这种形态下没有可用的归属规则：

- 把同一个收入写进 N 行 → 按渠道 SUM 会算 N 遍；
- 只写进其中一行 → 另外几行变成「成本已知、收入未知」，按 §5.1 不建行，
  那几笔成本会从台账里消失。

两条路都产出一个**看起来完全正常的错数字**，而设计稿没有给这种形态的
归属规则。所以采集器的选择是：**这一组令牌本轮不入账，并把歧义显式报出来**
（结构化日志 `finance_revenue_attribution_ambiguous` + 观测标 partial）。
宁可缺一块并说清楚，也不给一个不知道错在哪的数。**需要产品定义 N:1 的
收入拆分口径**，已记入 PR 的 follow_ups。

### newapi 现在只有成本一侧

newapi 的收入走自营 new-api 库的**只读直连**（§3.2 的 `quota_data`），
不在 `connectors/metering` 这条 HTTP 契约上——它的 `AccountRevenue` 每轮
都返回 `not_supported`。那是**预期内的常态**，不记为采集失败。

后果是：在收入通道接上之前，newapi 账号只有成本一侧，因而按 §5.1
**不建行**。这不是采集坏了，是纪律生效。收入通道属于后续切片。

### 周期任务与环境变量

`internal/platform/jobs/cost_sync.go`，job kind `finance_cost_sync`，
默认 5 分钟（§12 拍板：可配）。

| 环境变量 | 作用 |
|---|---|
| `XM_FINANCE_COLLECT_ENABLED` | 停用开关（宪法 26 条） |
| `XM_FINANCE_COLLECT_INTERVAL` | 采集周期，默认 `5m` |
| `XM_FINANCE_COLLECT_MODE` | `fake`（默认）/ `real` |
| `XM_FINANCE_COLLECT_INSTANCE_ID` | 观测与台账行的 `source` |
| `XM_FINANCE_COLLECT_TARGET_ALLOWLIST` | real 模式的主机精确清单（ADR-004） |
| `XM_FINANCE_COLLECT_REQUEST_TIMEOUT` | 单次上游请求超时 |

设计稿 §8.1 建议的前缀是 `XM_COST_SYNC_*`；实现用 `XM_FINANCE_COLLECT_*`
与文件名 `cost_sync.go` 并存——前缀与模块名（finance）对齐，
运维按模块找变量比按任务名找更顺手。

**endpoint 与凭据引用不进环境变量**：那两样逐账号不同，来自登记簿
（§2.1 的 `base_url` / `credential_ref`）。进程配置里再放一份，两处迟早会漂，
而漂了之后采集会用着 A 的地址、B 的凭据，报出来的错还是「认证失败」。

**生产环境禁 fake，启动即拒**。与 sub2api / newapi 同一条纪律，
但后果更重：Fake 的读数不只进 ops 指标，还会被**写进利润台账**，
而历史业务日过去冻结（§5.3），没有任何后续采集会去覆盖它——
只能靠人工数据修复（宪法 2 条的 Platform Lifecycle Operation）挖出来。

### 任务超时为什么放宽到 2 分钟

`sub2api_sync` 是**固定三次**上游读取，River 默认 1 分钟够用。本任务是
O(账号数 × 令牌数) 次——成本侧每个令牌一次请求（§3.1 的 apikey 自鉴权
决定了它无法批量），收入侧每个自营账号一次。几十把令牌就是几十次串行往返。
1 分钟会让规模稍大的部署每轮都被掐断在半路，而半路被掐断的那一轮
**已经写进去一部分行了**（逐行 upsert），看板上会是一份每轮都不完整、
且每轮缺的不是同一批的台账。

### 指标：三条，不是两条

`finance.cost.daily` / `finance.revenue.daily`（`connectors/metering` 定义，
观测「上游说了什么」）+ `finance.profit.daily`（本包定义，观测
「台账记下了什么」）。

两者**本就该不同**：三条纪律会让一部分读数不入账。合并成一条会让那个
差异永远看不见。

### Query：`GET /api/v1/finance/profit-daily`

复用 `finance.ScopeRead`，不另立 scope：台账里的毛利就是「倍率 × 用量」的
结果，能看登记簿里那个倍率的人已经能推出毛利的量级，泄漏面完全相同。

响应形状是 UI 交接 §13：`Money{amount_minor: string, currency, scale}`。
**三个金额都可空**，`null` = 未知——把未知渲染成 `"0"` 会让页面显示一个
笃定的 $0.00，而真相是我们那天没读到数。业务日以 `YYYY-MM-DD` 出，
不是 RFC3339 时刻：它是一个日历日，渲染成带时区的时间戳会让前端按浏览器
时区再解释一次，跨零点的用户看到的就是前一天。

**台账没有写路径。** 它只由采集任务写；回填历史是 Platform Lifecycle
Operation（宪法 2 条），不是一个 API。

---

## 相关文件

| 文件 | 作用 |
|---|---|
| `db/migrations/000008_finance_registry.{up,down}.sql` | 登记簿两张表与全部库层约束 |
| `db/migrations/000009_finance_profit_daily.{up,down}.sql` | 利润台账与库层约束（§2.2 + §5.1 的 CHECK） |
| `db/queries/finance.sql` | sqlc 查询 |
| `internal/platform/finance/account.go` | 领域类型与校验 |
| `internal/platform/finance/store.go` | 仓储（含 NUMERIC ↔ Ratio 的精确换算） |
| `internal/platform/finance/actions.go` | 四个 L1 Action |
| `internal/platform/finance/permissions.go` | 四个 scope |
| `internal/platform/money/` | 整数定点金额与倍率算术（本模块与 037b/c/e 共用） |
| `internal/platform/finance/profit.go` | 台账领域类型与三条纪律的类型层表达 |
| `internal/platform/finance/profit_store.go` | 台账仓储（三条分支写入 + 四桶归集 + 恒等式） |
| `internal/platform/finance/collector.go` | 一轮采集：登记簿 → 取数 → 折算 → 入账 |
| `internal/platform/finance/observations.go` | `finance.profit.daily` 观测 |
| `internal/platform/jobs/cost_sync.go` | River 周期任务（默认 5min） |
| `internal/platform/httpapi/finance.go` | `GET /api/v1/finance/upstream-accounts` |
| `internal/platform/httpapi/profit_daily.go` | `GET /api/v1/finance/profit-daily` |
| `contracts/actions/finance.*.json` | Action 契约 |
| `connectors/metering/` | 消费本登记簿的计量取数连接器 |
| `contracts/connectors/metering.read.v1.md` | 取数契约 |
