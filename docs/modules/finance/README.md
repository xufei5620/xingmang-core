# finance —— 成本登记簿（XM-0037a）

成本核算的**配置底座**：每个上游账号一行（接入方式、凭据引用、充值倍率、
业务日时区），加上「上游令牌 ↔ 自营账号」的映射。

规格权威是 `docs/superpowers/plans/2026-08-28-xm-0037-cost-accounting-design.md`
（下称「设计稿」，§ 号均指它）。本文只记**实现层的取舍与偏差**，
不复述口径——口径以设计稿全文为准。

**范围只有切分 a。** 利润台账 `profit_daily`（§2.2）、余额历史（§2.3）、
订阅成本批次与代理资产（§2.5）、看板供数（§8.5）、影子对比（§9）
分别属于 XM-0037b / c / d / e，各自新增迁移与代码。

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

## 相关文件

| 文件 | 作用 |
|---|---|
| `db/migrations/000008_finance_registry.{up,down}.sql` | 两张表与全部库层约束 |
| `db/queries/finance.sql` | sqlc 查询 |
| `internal/platform/finance/account.go` | 领域类型与校验 |
| `internal/platform/finance/store.go` | 仓储（含 NUMERIC ↔ Ratio 的精确换算） |
| `internal/platform/finance/actions.go` | 四个 L1 Action |
| `internal/platform/finance/permissions.go` | 四个 scope |
| `internal/platform/money/` | 整数定点金额与倍率算术（本模块与 037b/c/e 共用） |
| `internal/platform/httpapi/finance.go` | `GET /api/v1/finance/upstream-accounts` |
| `contracts/actions/finance.*.json` | Action 契约 |
| `connectors/metering/` | 消费本登记簿的计量取数连接器 |
| `contracts/connectors/metering.read.v1.md` | 取数契约 |
