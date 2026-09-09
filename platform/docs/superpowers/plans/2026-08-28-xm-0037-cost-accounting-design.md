# XM-0037 成本核算与毛利设计文档

> **状态:设计稿,待产品负责人拍板。不含实现,不建连接器,不派生代码。**
> 蓝本:admin.solov.cc 线上 = **SoloAI v2.5.130 增强版(commit 14e6f617)**。
> **产品口径共同权威**:`K:/星芒统一控制平台/星芒统一控制平台_UI原型交接文档_Claude实施版_v1.0.md`
> 的 **§10「财务和成本计算语义」+ §13「数据合同」**(产品负责人认可的口径)。本设计
> 是 SoloAI 蓝本(实现细节标准答案)与 UI 交接 §10/§13(产品口径与前端消费形状)的**合并**。
> 依据:`docs/evidence/EV-2026-08-27-soloai-cost-accounting.md`、
> `docs/evidence/EV-2026-08-27-upstream-cost-recon.md`、
> SoloAI 源码 `K:/soloai/soloai-v2-src`(已 checkout 14e6f617,只读参考)、
> 宪法 2/9/10/12/13/14 条、ADR-003/013/014/018。
> 引用 SoloAI 处标注 `文件:行号`(标准答案);引用 UI 交接处标注 `§10.x/§13`;引用平台处为接入点。
>
> **更新记录**:
> - v1(2026-08-28):照 SoloAI v2.5.130 蓝本起草。
> - **v2(2026-08-28):合并 UI 交接 §10/§13。新增:订阅账号成本摊销模型(§10.3,SoloAI 无)、
>   贡献利润分层(§10.5)、汇率元数据与金额携币种/字符串大整数(§10.1/§13)、共享上游可用天数
>   (§10.4)、ChannelSummary/UpstreamSummary/Money/Observed 供数映射(§13)、术语统一
>   充值成本率↔recharge_ratio(§10.2)。影子对比范围收窄到「计量型渠道」。**
> - **v2.1(2026-08-28):增补 §12——订阅摊销的舍入与余数分配严谨口径(账目自洽)、
>   ChannelSummary 供数粒度核对(successRate 来自监控线、后置)、§10 落章节核对表;
>   §11 增第 14 项(摊销舍入/余数策略)。既有章节未重排。**
> - **v2.4(2026-08-28):增补 §15——XM-0049 收尾包(分组倍率补上存储、
>   可用天数接进告警规则、阈值可配且两个进程共用一份解析)。
>   §14.3 第 2 项与 §14.6 的三条 follow-up 因此关闭。既有章节未重排。**
> - **v2.3(2026-08-28):增补 §14——切分 d 落地时的裁定与偏差登记
>   (余额游程编码、可用天数的窗口与五种「给不出」、两个摘要同粒度的理由、
>   **余额读取的覆盖率边界:两个真实驱动都还没接通**、演示种子的身份取舍)。
>   完整理由见 docs/modules/finance/README.md。既有章节未重排。**
> - **v2.2(2026-08-28):增补 §13——切分 c 落地时的裁定与偏差登记
>   (摊销行 ratio_snapshot=1、账号级聚合行、platform_id 归属列、§11 第 14 项选定
>   「账号轴半进 + 天轴末日吸收」、三处相对本稿的实施偏差、对 §5.1 的唯一偏离)。
>   完整理由见 docs/modules/finance/README.md。既有章节未重排。**

---

## 0. 一句话与验收标准

平台的成本 / 毛利,口径与两个权威一致:**计量型渠道**(Key/上游中转)照 SoloAI v2.5.130
逐笔对齐,**订阅型渠道与贡献利润**照 UI 交接 §10.3/§10.5(SoloAI 无此口径,为平台原创)。

- **计量型渠道验收**:平台与 SoloAI 并行 14 天,逐(账号,业务日)对比
  `usageRevenue / supplyCost / grossProfit`,**在「分」(scale-2)粒度 0 差异**。为什么是「分」
  而不是原始位:平台宪法禁 float,金额按整数最小货币单位算,SoloAI 用 float64——两条路在「分」
  以下的尾数本就不可能逐比特相等(§2.4),都会舍入到同一个「分」。平台这条整数路**比 SoloAI 更准**。
- **订阅型渠道验收**:SoloAI 把订阅上游成本当固定月费边际≈0(EV-soloai §边界),**无可影子对比的对象**;
  订阅成本按 §10.3 摊销公式自洽,验收靠单元测试 + 与实际付款票据核对,不进 SoloAI 影子对比。

---

## 1. 目标与对齐承诺

1. **成本 = 真实上游现金成本,不是估算、不是人工拍脑袋**。计量型照搬 SoloAI 取数/折算/切日/归属
   四步(§3.1–§3.3);订阅型按 §10.3 分摊真实付款(§3.5)。
2. **渠道毛利 = 使用计费收入 − 供给成本;毛利率 = 毛利 / 收入**(收入 ≤ 0 时为 nil 不显示,
   对齐 `relay_profit.go:331`)。**贡献利润**在毛利之下再减支付手续费与可归属基础设施/代理成本(§3.6/§10.5)。
3. **平台新建即无历史包袱**:利润台账第一天就把 `ratio_snapshot` 与 `platform_id` 写全,
   不复刻 SoloAI 演进期(0176 之前 / 0182 之前)的 NULL 兼容与写入方漏写缺陷。
4. **金额纪律(§10.1 + 宪法 13)**:货币金额用整数最小货币单位(int64,DTO 以字符串传大整数)
   **或** Decimal 字符串;汇率/倍率/单价用定点 Decimal;禁 float/double;**每个金额必带 Currency**;
   **每条汇率必带 `rate_at`/`source`/`base_currency`/`quote_currency`**。这是平台比 SoloAI(float64 USD)
   更严处,§2.4 证明「换算不丢精度、分粒度对齐不掉队」。
5. **对齐承诺的边界**:计量型承诺「分」粒度 0 差异,**不承诺**与 SoloAI 原始 float64 逐比特相等
   (既不可能、也等于把 float 的不精确抄进来);订阅型 / 贡献利润 **不与 SoloAI 对比**(它没有)。

---

## 2. 数据模型

平台是全新库,无 SoloAI 迁移演进包袱。下一条平台迁移编号从 **`000008`** 起
(现有最高 `db/migrations/000007_alerts.up.sql`),放独立 schema(建议 `finance`),沿用平台迁移规范:
`.up.sql` 只前进不可改(规格 §5.7),`environment` 外键 `core.environment`,金额不建 NUMERIC 列(见 §2.4)。

### 2.0 渠道接入方式(access_method)——三套成本口径的分叉点

UI 交接 §13 `ChannelSummary.accessMethod` 枚举 `upstream_key | official_api | subscription_account`,
**供给成本按接入方式分三套口径**:

| access_method | 含义 | 供给成本口径 | 对齐对象 |
|---|---|---|---|
| `upstream_key` | 上游中转 reseller(sub2api/newapi)的 Key | 计量:实扣 ÷ recharge_ratio(§3.1) | ★SoloAI 逐笔 |
| `official_api` | 官方 API 直连(原厂计费) | 计量:原厂账单 / token×单价(**v1 口径待定,见 §11**) | 无(SoloAI 无) |
| `subscription_account` | 订阅账号(固定月费 + 代理) | 摊销:§10.3 每日订阅+代理成本(§3.5) | 无(SoloAI 当≈0) |

`upstream_key` 是本次与 SoloAI 影子对比的**唯一**范围。`subscription_account` 为平台原创,
`official_api` v1 先占位。

### 2.1 成本登记簿(每渠道 / 每上游账号)

对应 SoloAI `relay_stations` 的成本相关列(0084 `recharge_ratio`、0090 `sub2api_token_map`),
平台收拢成登记簿。**这不是模型成本单价表**——SoloAI 无按模型成本单价(0180 明确无可测来源),
计量型唯一成本配置是每上游账号一个倍率。

```
finance.upstream_account          -- 一个「上游账号/渠道」一行(对应 §13 UpstreamSummary/部分 ChannelSummary)
  id             uuid PK
  system_type    text NOT NULL     -- 'sub2api' | 'newapi' | 'official' ...(§13 systemType)
  access_method  text NOT NULL     -- 'upstream_key' | 'official_api' | 'subscription_account'(§2.0)
  base_url       text              -- 上游站网址(https,不含凭证);official 为原厂 endpoint
  credential_ref text NOT NULL     -- secret://<scope>/<name>,永不存明文(ADR-014)
  recharge_ratio NUMERIC           -- 除数,定点 Decimal(计量型必填;订阅型 NULL)。见 §3.4
  currency       text NOT NULL DEFAULT 'USD'   -- 该账号计价币种(§10.1:金额必带币种)
  business_day_tz text NOT NULL DEFAULT '+08:00'  -- 业务日切日时区,显式声明(宪法 14)
  status         text NOT NULL     -- active | disabled
  environment    text NOT NULL REFERENCES core.environment(id)
  created_at / updated_at timestamptz
```

- **`recharge_ratio` 是除数(对齐 SoloAI 0084)**;§13 暴露的 `rechargeCostRate`(充值成本率)
  = `1 / recharge_ratio`,是展示投影(§3.4),**不另存**以免两值漂移。
- **分组倍率 `groupRate`**(§13 `ChannelSummary.groupRate`)独立存储/展示,**不并入 `recharge_ratio`,
  前端不重复乘算**(§10.2)。

令牌 ↔ 自营账号映射(SoloAI `sub2api_token_map` JSON),平台单独成表:

```
finance.token_map
  upstream_account_id uuid NOT NULL REFERENCES finance.upstream_account(id) ON DELETE CASCADE
  upstream_token_id   text NOT NULL   -- 成本侧键
  own_account_id      text NOT NULL   -- 收入侧键:sub2api=账号id;newapi=channel_id
  PRIMARY KEY (upstream_account_id, upstream_token_id)
```

**上游令牌明文 sk-**:sub2api 成本侧要用每令牌明文 `sk-` 打 `/v1/usage`(§3.1)。SoloAI 把它
AES-GCM 加密落 `relay_token_keys`(0084C)以规避 newapi 取 key 限流。平台不落明文表,由
`SecretProvider` 解析,`Reveal()` 只在拼 HTTP 头一瞬出现,绝不入日志/响应(ADR-014、宪法 7)。
**令牌映射与 sk- 维护方式见 §11。**

### 2.2 利润台账(对齐 relay_profit_daily,供 §13 grossProfit)

SoloAI `relay_profit_daily` 现列(0091 六列 + 0176 `ratio_snapshot` + 0179 `platform_id`)。平台等价:

```
finance.profit_daily
  upstream_account_id uuid NOT NULL   -- 对应 SoloAI station 维度
  business_day  date NOT NULL         -- CST 业务日(库内 date;切日口径 §4)
  token_id      text NOT NULL         -- 上游令牌id(成本侧键)
  account_id    text NOT NULL         -- 自营账号id / channel_id(收入侧键)
  platform_id   text                  -- 自营平台归属快照;NULL=写入当时未配对(§5.2)
  revenue_minor bigint                -- 使用计费收入,整数最小单位(§2.4);NULL=未知(不是 0)
  cost_minor    bigint                -- 供给成本,整数最小单位;NULL=未知(不是 0)
  currency      text NOT NULL DEFAULT 'USD'
  ratio_snapshot NUMERIC NOT NULL     -- 本行 cost 折算实际用的倍率,逐行冻结(§6.3)
  updated_at    timestamptz NOT NULL DEFAULT NOW()
  PRIMARY KEY (upstream_account_id, business_day, token_id)
```

`revenue_minor - cost_minor = grossProfit`(§13 `ChannelSummary.grossProfit` / `UpstreamSummary.grossProfit`)。
与 SoloAI 差异(全是「更严 / 更全」,不改口径):金额 `bigint` 整数最小单位(vs SoloAI `NUMERIC` 写 float64)、
`ratio_snapshot` NOT NULL 第一天写全、`platform_id` 第一天写全(仍无外键,四桶见 §5.2)、未知值用 NULL 不写 0。

### 2.3 余额历史(runway 用,§10.4)

对应 SoloAI `relay_balance_history`(0080A,仅余额变化落点)。仅用于可用天数预警,**不参与成本**。

```
finance.balance_history
  id            bigserial PK
  upstream_account_id uuid NOT NULL REFERENCES finance.upstream_account(id) ON DELETE CASCADE
  balance_minor bigint NOT NULL       -- 余额,整数最小单位
  currency      text NOT NULL         -- §10.4:余额与消耗单位须一致
  captured_at   timestamptz NOT NULL DEFAULT NOW()   -- 库内 UTC(宪法 14);展示为 observedAt
```

### 2.4 金额存储:整数最小单位 + 币种 + 汇率元数据(★核心决策,合并 §10.1)

**§10.1 + 宪法 13 硬性要求**:货币金额 = 整数最小货币单位 **或** Decimal 字符串;汇率/倍率/单价 =
定点 Decimal;禁 float/double;**每金额带 Currency**;**每汇率带 `rate_at`/`source`/`base_currency`/`quote_currency`**。

**存储标度 scale = 6(微美元,int64)**,理由不变:
- newapi 的 `quota/500000` 在 scale-6 下**精确无舍入**(`micro = quota × 2`);scale-2 做不到。故 scale ≥ 6。
- 比展示所需的「分」低 4 位,分级展示零舍入;int64@scale-6 上限 ±9.2×10¹² USD,无溢出。
- (备选 scale-8:对齐 sub2api 用户余额 `decimal(20,8)` 原生精度。见 §11。)

**字符串→最小单位**复用 `connectors/sub2api/amount.go`:`rawAmount`(:37,原样留字面不过 float64)、
`decimalToMinorUnits(raw, scale)`(:82,ASCII 逐位 + 半进 `digits[point]>='5'`,溢出报错)、
`rescaleMinorUnits`(:188,高精度累加末尾一次性半进降标)、`currencyScale`(`upstream.go:131`,未知币种硬报错不猜)。

**折算(除以 recharge_ratio)整数定点**:`ratio` 表示为 `ratio_num / 10^ratioScale`,
`cost_micro = round_halfup(usage_micro × 10^ratioScale / ratio_num)`,唯一舍入 ≤ 0.5 微美元/行,
与 SoloAI float64 除法(~1e-16)舍入到分必然相同。

**DTO 传输(§13 `Money`)**:`{ amountMinor: string, currency }`——**大整数按字符串传**(超 2^53 不丢精度,
与 connector/invoice 契约同源)。读回不过 float:`ops/store.go:88` `UseNumber()` + `json.Number.Int64()`。

**多币种与汇率**:上游余额可能是 CNY(EV-upstream-cost-recon:Kimi/DeepSeek payg CNY)。方案:
`balance_minor` 存**原生币种**(带 `currency`);需归一 USD 时另存一条汇率记录
`{ rate(定点 Decimal), rate_at, source, base_currency, quote_currency }`,展示层用它换算并显示汇率时点/来源。
**成本/收入台账全程 USD**(sub2api/newapi 计量口径本就是 USD),汇率只在余额归一/跨币展示时出现。

**worked example**(sub2api,actual_cost=`"5.813729"`,ratio=`1.5`):平台 `5813729` 微美元
→ `5813729×10/15 = 3875819.33…` 半进 `3875819` = `$3.875819` → 分 `$3.88`;
SoloAI `float64(5.813729)/1.5 = 3.8758193…` → 分 `$3.88`。**一致**。

**结论(给拍板)**:整数最小单位与 SoloAI float 的对齐在「分」粒度 0 差异,分以下不承诺逐位。
newapi 侧因输入是整数 credits、scale-6 下折算前都精确;sub2api 侧输入是浮点字面量、平台解析比 SoloAI 更准。

### 2.5 订阅成本批次 + 代理资产(§10.3,SoloAI 无,平台新增)

订阅型渠道成本不是「计量实扣」,而是「固定付款按天/按账号摊销」。需两类新实体:

```
finance.subscription_cost_batch  -- 一笔订阅付款 = 一个成本批次(续费=新批次,不覆盖历史,§10.3)
  id             uuid PK
  upstream_account_id uuid NOT NULL REFERENCES finance.upstream_account(id) ON DELETE CASCADE
  paid_minor     bigint NOT NULL    -- 实际支付(整数最小单位)
  surcharge_minor bigint NOT NULL DEFAULT 0   -- 附加费用
  refunded_minor bigint NOT NULL DEFAULT 0    -- 已退款金额(部分退款冲减成本基础,§10.3)
  currency       text NOT NULL
  starts_on      date NOT NULL      -- 订阅开始
  expires_on     date NOT NULL      -- 到期(有效天数 = expires_on - starts_on)
  account_count  int  NOT NULL      -- 账号数量(摊到每账号)
  terminated_on  date               -- 提前失效日;NULL=正常。剩余未摊销转损失(§3.5)
  proxy_batch_id uuid               -- 关联代理批次(NULL=无代理,代理成本为 0)
  created_at / updated_at timestamptz

finance.proxy_asset               -- 代理资产(§10.3:IP 开通/到期、购买平台、地址、账号、凭证)
  id             uuid PK
  paid_minor     bigint NOT NULL    -- 代理实际支付
  surcharge_minor bigint NOT NULL DEFAULT 0
  refunded_minor bigint NOT NULL DEFAULT 0
  currency       text NOT NULL
  opened_on      date NOT NULL      -- 开通
  expires_on     date NOT NULL      -- 到期
  shared_account_count int NOT NULL -- 分摊账号数
  buy_platform   text               -- 购买平台
  buy_address    text               -- 购买地址
  credential_ref text               -- secret://(不存明文)
  mounted        boolean NOT NULL DEFAULT false  -- 未挂载时成本明确为 0(§10.3)
  created_at / updated_at timestamptz
```

**成本快照按天生成**:订阅型也写 `finance.profit_daily`(同表,`cost_minor` 来自当日摊销值),
使毛利/贡献利润三套口径在台账层统一,前端一视同仁读 `grossProfit`。

---

## 3. 取数、折算与三套成本口径

### 3.1 计量型成本取数(upstream_key,标准答案)

成本 = 上游今日实扣 ÷ `recharge_ratio`;`ratio ≤ 0` 视为 1(`relay_profit.go:97`)。

**Sub2API**(`relaymon/sub2api.go:263` `FetchSub2APIUsageCost`):`GET {base}/v1/usage`,
`Authorization: Bearer <令牌明文 sk->`(apikey 自鉴权,非 admin key;须已分组否则 403),
取 `usage.today.actual_cost`(USD)。

**NewAPI**(`relaymon/newapi.go:221` `FetchNewAPITokenCost`):
`GET {base}/api/log/self/stat?type=2&start_timestamp=<今日CST起>&end_timestamp=<now>&token_name=<名>`
(`type=2` = 仅 consume 不含充值,关键),`New-Api-User: <UserID>` + `Cookie: session=<Session>`,
取 `data.quota`,`成本(USD) = quota / 500000`(`NewAPIQuotaPerUSD=500000`,`newapi.go:21`)。

**注意**:平台现有 `connectors/sub2api` 的 `sub2api.cost.daily` 读的是 admin 面板 `trend[].cost`
(刻意避开 `actual_cost`),是自营实例口径,**不是本核算成本**。XM-0037 成本侧必须新走
「每令牌 `/v1/usage` actual_cost」,**不得复用** `sub2api.cost.daily`。走 ADR-018 平台独立只读通道。

### 3.2 收入取数(usageRevenue,标准答案)

**Sub2API**(`admin/sub2api_admin.go:79` `AccountRevenue`):
`GET {base}/api/v1/admin/accounts/{id}/stats?days=30`,`x-api-key: <admin_key>`,
取 `data.summary.today.user_cost`(USD);`today==null` = **今日零流量 = 已知 0**(不是未知)。

**NewAPI**(`platformdb/channel_revenue.go` `FetchNewAPIChannelRevenueToday`):只读直连自营 new-api 库
`SELECT COALESCE(SUM(quota),0) FROM quota_data WHERE created_at >= <CST今日00:00 unix> AND channel_id::text = <own_account_id>`,
`收入(USD) = SUM(quota)/500000`。`channel_id` 即 `token_map` 的值。生产实测按账号按日与库逐位相等(0180 注释)。

**§10.5 铁律:收入 = 使用计费,用户充值/余额/赠送额度不得混入使用收入**。SoloAI 用 `user_cost`
(使用量计费)本就满足;平台照此,`quota_data.quota` 是消费计费不是充值。

### 3.3 渠道毛利

`grossProfit = usageRevenue − supplyCost`;`margin = grossProfit / usageRevenue`,`revenue ≤ 0` 时 nil
(`relay_profit.go:331`)。平台在整数最小单位相减,margin 展示层算比率。

### 3.4 术语统一:充值成本率 ↔ recharge_ratio(§10.2)

UI §10.2:`上游现金成本 = 上游额度消耗 × 充值成本率`;SoloAI:`成本 = 实扣 ÷ recharge_ratio`。
**同一件事的乘/除两表述**,`充值成本率 = 1 / recharge_ratio`。设计统一如下,避免两处实现各用一种:

- **规范存储 = `recharge_ratio`(除数,定点 Decimal,对齐 SoloAI 0084)**——影子对比要 0 差异,
  必须用与 SoloAI **同一个数**、**同一种运算(÷)**。
- **`rechargeCostRate = 1 / recharge_ratio`** 是 §13 暴露的展示投影,定点 Decimal 字符串。
- **成本计算恒用 `usage ÷ recharge_ratio`(整数除法)**,**绝不**先把 `充值成本率` 舍入成有限小数再乘——
  那会二次舍入、破坏影子对齐。
- **分组倍率 groupRate 独立**:除非后端契约明确「倍率已含在『上游额度消耗』中」,**前端不重复乘算**(§10.2)。
- **待拍板**:若产品希望以 `充值成本率`(实付现金/额度,§10.2 的乘数)为规范可编辑量,则需定义
  与 SoloAI `recharge_ratio` 的精确互为倒数关系(见 §11)。

### 3.5 订阅账号成本摊销(§10.3,SoloAI 无)

按 UI §10.3 公式,全整数最小单位运算:

```
有效天数        = expires_on − starts_on
每日订阅成本    = (paid + surcharge − refunded) / 有效天数 / account_count
每日代理成本    = (proxy.paid + proxy.surcharge − proxy.refunded) / 有效天数(代理) / shared_account_count
                  代理未挂载(mounted=false)→ 每日代理成本 = 0
今日账号供给成本 = 每日订阅成本 + 每日代理成本      → 写入 profit_daily.cost_minor
渠道毛利        = usageRevenue − 今日账号供给成本
```

批次规则(§10.3):
- **续费 = 新增成本批次,不覆盖历史**:同 `upstream_account` 多条 `subscription_cost_batch`,
  当日成本取「`starts_on ≤ business_day ≤ expires_on` 且未 `terminated_on` 的批次」之和。
- **提前失效**:`terminated_on` 置值后,`terminated_on..expires_on` 的剩余未摊销成本**转为损失**
  (一次性计入 `terminated_on` 当日,或单列损失科目——记账口径见 §11)。
- **部分退款**:`refunded_minor` 增加 → **冲减成本基础**,后续每日摊销自动降低(不追溯改历史已摊)。
- 摊销含两次除法(/有效天数、/账号数),整数最小单位下除不尽必留余数——**N 天日额之和须精确等于批次总额**(账目自洽)。
  严谨口径(累计整数法 / 最大余数法 / 末日吸收)与两次除法顺序、网格余数分配见 **§12.1**;这是 §11 第 14 项待拍板。

### 3.6 平台贡献利润分层(§10.5,SoloAI 无)

```
使用收入 usageRevenue
− 供给现金成本 supplyCost
= 渠道毛利 grossProfit                    ← §2.2 台账直接供出(计量+订阅统一)
− 支付手续费 paymentFees
− 可归属基础设施/代理成本(按批准口径)
= 贡献利润 contributionProfit
```

- `grossProfit` 是台账核心(逐渠道,§13 供出)。`contributionProfit` 是**平台级/批准口径的上层汇总**,
  在毛利之上再减两项:`paymentFees`(支付手续费,数据源=支付系统)、`可归属基础设施/代理成本`
  (代理成本已在 §2.5;基础设施成本需「批准口径」的归因规则)。
- **这两项的数据源与归因口径需产品拍板(§11)**;v1 台账先把 `grossProfit` 供准,`contributionProfit`
  作为可关闭的上层卡片,数据未就位时诚实占位(宪法 12)。

---

## 4. 口径常量(逐条标注「必须与权威一致,否则对不上」)

| 常量 | 值 | 一致性要求 |
|---|---|---|
| 业务日时区 | **CST = 固定 +08:00,无夏令时** | ★与 SoloAI 一致(`cstNow` `relay_profit.go:60`)。库内 UTC + 业务日显式声明 +08:00(宪法 14) |
| 收入/成本切日 | **共用同一时间权威** | ★一致。各切各的会「收入记 D、成本记 D+1」且不报错(`channel_revenue.go` 注释) |
| 币种 | 计量台账全程 USD;金额均带 Currency(§10.1) | ★一致;跨币仅在余额/展示,带汇率元数据 |
| NewAPI 倍率 | `NewAPIQuotaPerUSD=500000` | ★一致。抄错不报错,金额差 50 万倍(`newapi.go:16`) |
| newapi 成本 type | `type=2`(仅 consume) | ★一致 |
| sub2api 成本源 | `/v1/usage` `today.actual_cost`(非 `trend[].cost`) | ★一致(§3.1 警告) |
| 累计口径 | 自上次重置起每日快照之和(非上游 30 天窗口) | ★一致 |
| 今日覆盖 | 今日行每轮 upsert 覆盖,过去日冻结 | ★一致 |
| 可用天数日均窗口 | **近 7 日**(§10.4);无消耗/过期不显示伪精确天数 | ★与 SoloAI runway 一致;诚实(宪法 12) |
| 收入定义 | 使用计费,**不含用户充值/余额/赠送**(§10.5) | ★一致(SoloAI 用 user_cost) |

---

## 5. 三条不静默纪律(照搬 SoloAI)

### 5.1 取数失败存 NULL / 跳过,绝不写 0
`revToday==nil && costToday==nil` → 跳过整对(`relay_profit.go:129`);库层两侧 nil 返回
`ErrProfitNothingKnown`(`store/postgres/relay_profit.go:32/46`);只有一侧已知则**纯 UPDATE 不建行**
(缺一侧没有可断言的利润,宁可这对不出现,也不把未知写 0——那会让利润凭空等于收入)。
**已知 0(sub2api `today==null`)与未知 nil 必须区分**。平台 `revenue_minor/cost_minor` 用可空 `bigint`。
UI §13 亦要求「未知字段 Fail Closed,不猜测补值」——同一条纪律。

### 5.2 平台归属四桶,不 COALESCE、不 INNER JOIN 静默丢金额
`SumRelayProfitByPlatform`(`store/postgres/relay_profit.go:266`)四桶:**有效平台 / 指向已移除平台
(单独成桶)/ NULL 未归属(单独成桶)**,`LEFT JOIN`、`platform_id` 不 COALESCE;恒等式
`四桶行数之和 == 独立 COUNT 窗口总行数`(同事务快照)。平台照搬。

### 5.3 今日覆盖、过去冻结
今日行 `ON CONFLICT DO UPDATE` 反复刷新;过去日不再触碰。`platform_id` 的 COALESCE 方向
「空缺可补、已有不动」(`relay_profit.go:77`)——绑定变更只影响新行,不追溯改写历史行。

---

## 6. 三诚实边界(照搬)+ 订阅诚实

### 6.1 无模型级成本 —— 禁摊派
`migrations/0180`:成本只有「每令牌每日一个标量」,上游端点无模型维度。**模型级成本无可测来源**。
用量可拆模型×小时×渠道(0180 `relay_model_usage_hourly`,有 requests/tokens/revenue)但**刻意无 cost 列**。
平台若做模型维度,**只放用量不放成本**,渠道级成本仍权威(台账里 join)。

### 6.2 自营库 `account_stats_cost` 是售价不是成本
2026-08-01 实测:账号 258 `sum(account_stats_cost)=45.02` vs 上游实扣 `5.81`(差 7.75 倍,逐账号不同)。
那是我方挂牌价口径,**与上游实扣无可用换算**。平台成本一律走 §3.1 上游 actual_cost。

### 6.3 `recharge_ratio` 需 `ratio_snapshot` 逐行冻结
倍率可随时改写且无历史(SoloAI 0084);不冻结则「上游涨价」与「倍率被调整」分不开。SoloAI 0176 加
`ratio_snapshot` 逐行冻结。平台第一天起 `NOT NULL`,倍率修改走 Action + 审计(§8.3)。

### 6.4 订阅诚实(§10.3)
**代理未挂载成本明确为 0**(不猜);**提前失效剩余转损失不藏**;**部分退款冲减不追溯篡改已摊历史**。
订阅型无 SoloAI 影子对象,更要靠票据核对,不得用「看着差不多」的估算冒充实付摊销。

---

## 7. 上游余额与可用天数(§10.4,对齐 SoloAI runway)

- **采集**:周期扫描(SoloAI 5min,`relay_scheduler.go:24`,按 `scan_interval_minutes` 自适应最低 1min),
  base_url + 解密凭证拉上游**已存**余额,**仅变化时**落 `balance_history`(`relay_scheduler.go:461`)。
- **余额来源**(读上游已存余额,不平台直连原厂):Sub2API 管理员 token 读 `/admin/accounts` 的 `extra`
  快照(纯只读);NewAPI 读 `channel.balance`(需上游先开 `CHANNEL_UPDATE_FREQUENCY`,否则死水;覆盖率低)。
  平台**不**自己调 `update_balance`(写权限+会禁渠道)。
- **可用天数(§10.4)**:`预计可用天数 = 上游余额 / 该上游全部映射渠道的统一口径近7日日均消耗`。
  一个上游被多渠道/Key 共享,**日均消耗必须聚合该上游名下全部映射渠道**(去重:sub2api 按 account.id、
  newapi 按 type+base_url 归并,EV-upstream-cost-recon)。要求:**余额与消耗单位一致**;**必显示观测时间
  observedAt**;**无消耗或数据过期不显示伪精确天数**(显示「—」或「数据过期」,宪法 12);低于阈值进告警+待处理队列。
- **档位阈值**(SoloAI 0177,严格递增)`critical=5 < warning=10 < serious=20`,可在设置调。**只做预警,不参与毛利**。
- **边界**:余额差分会被充值污染(正跳变判充值不计负成本),SoloAI 正因此不用它算成本;订阅制上游无边际成本
  (固定月费),可用天数对其无意义(它走 §3.5 摊销)。

---

## 8. 与既有平台设施的接法

### 8.1 成本采集 = River 周期任务(照 `sub2api_sync`)
新增 `internal/platform/jobs/cost_sync.go`,照 `sub2api_sync.go` 四件套(job kind 常量、`Args` 实现
`river.JobArgs`、`Worker` 嵌 `river.WorkerDefaults`、间隔常量),在 `jobs/client.go` 的 `NewClient`
`AddWorker`+`append(periodic,…)`,env 走 `cmd/platform-worker/config.go` 的 `XM_COST_SYNC_*`。遵 ADR-013。
每轮:遍历登记簿 → 计量型取数(§3.1/3.2)/订阅型摊销(§3.5)→ 折算(§2.4)→ upsert 台账(§5 纪律)。

### 8.2 指标键(§13 供数 + 宪法 12 新鲜度)
按 `<system>.<subject>.<grain>`,建议 `finance.cost.daily` / `finance.revenue.daily` / `finance.profit.daily`
/ `finance.margin.daily`(**勿用连接器已占的 `sub2api.cost.daily`**,不同口径,§3.1)。定 Go 常量、
注册进 `ops/freshness.go` 白名单(`RegisterMetricKey`)、与 `ops/metrickeys_test.go` 对齐(漂移即 CI 失败)。
金额以 `*_minor_units`(int64)承载,**DTO 转 §13 `Money{amountMinor:string}`**。每个展示值带
`Observed`(observedAt/source/watermark/freshness,§13),新鲜度状态机(失败>未初始化>延迟>部分>新鲜)复用现有。

### 8.3 写操作走 Action(§14.3 + ADR-003)
`recharge_ratio`、`token_map`、登记簿、订阅批次/代理资产增删改都是平台配置写,走 Action(宪法 2)。
建议 `finance.upstream_account.set` / `finance.recharge_ratio.set` / `finance.subscription_batch.set` 等,
**RiskLevel = L1**(Foundation-A 内核对 L2+ fail-closed,L1 才可执行)。照 `registry/actions.go` 的
`registry.service.create`(L1)模板 + 契约 JSON(`contracts/actions/*.json`)。写操作返回 `action_run_id`,
异步返回 `job_id`,支持 Idempotency-Key/X-Request-ID(§14.3)。审计经 ctx 注入,`credential_ref` 只记 ref 不记明文。
**L3/L4 AI 不作第二审批人**(宪法 10、§14.3);Foundation-B 前写能力只显门禁说明(§14.3)。

### 8.4 凭证与只读通道(ADR-014 / ADR-018 / §14)
上游凭证经 `CredentialRef`(`secret://`),`SecretProvider.Resolve` 按需解密,`Reveal()` 只在拼头一瞬。
前端只见 CredentialRef/状态/最近轮换/最后使用,**密码/Token/Cookie/SSH/Root 不回显**(§14.2);
fixture 禁真秘密。采集走平台独立只读账号(ADR-018 四道只读闸,不复用开票线 Bridge V4)。

### 8.5 数据合同供数映射(§13)
台账须能供出 §13 形状(API 以 OpenAPI 为准,时间 RFC3339 UTC 前端按用户时区展示):

| §13 字段 | 台账来源 |
|---|---|
| `Money{amountMinor,currency}` | `*_minor`(int64)→ 字符串;`currency` 随行 |
| `Observed<T>{observedAt,source,watermark,freshness}` | ops 观测元数据(宪法 12) |
| `ChannelSummary.usageRevenue/supplyCost/grossProfit` | `profit_daily.revenue_minor/cost_minor/(差)` 按渠道聚合 |
| `ChannelSummary.accessMethod/groupRate/credentialRef` | `upstream_account.access_method/groupRate/credential_ref` |
| `ChannelSummary.successRate/assuranceStatus` | **非成本台账**——来自监控/探测线,此处只声明供数边界 |
| `UpstreamSummary.rechargeCostRate` | `1 / recharge_ratio`(§3.4 投影) |
| `UpstreamSummary.balance` | `balance_history` 最新点(带 observedAt) |
| `UpstreamSummary.usageRevenue/supplyCost/grossProfit` | 该上游名下全部渠道聚合 |

---

## 9. 影子对比方案(仅计量型渠道)

**范围**:仅 `access_method='upstream_key'`(sub2api/newapi)。订阅型 / official_api / 贡献利润
**不进影子对比**(SoloAI 无对象)。目标:并行 14 天,逐(账号,业务日)0 差异(分粒度)。

**取数**:SoloAI 侧读 `relay_profit_daily`(NUMERIC);平台侧读 `finance.profit_daily`
(`*_minor`)折到分。**对比 SQL**:
```sql
-- SoloAI:每(账号,日)分粒度
SELECT account_id, day, round(SUM(revenue)::numeric,2) rev_c, round(SUM(cost)::numeric,2) cost_c
FROM relay_profit_daily WHERE day >= :since GROUP BY account_id, day;
-- 平台:整数最小单位→分(半进)
SELECT account_id, business_day day, round(SUM(revenue_minor)::numeric/1e6,2) rev_c,
       round(SUM(cost_minor)::numeric/1e6,2) cost_c
FROM finance.profit_daily WHERE business_day >= :since GROUP BY account_id, business_day;
```
对齐工具 full-outer-join,标出:仅一侧有行、rev/cost/profit 分差 ≠ 0。

**口径检查清单**(对不上逐条查,全来自 §4):
- [ ] 业务日都 CST +08:00?收入成本共用同一时间权威?
- [ ] newapi `type=2`?`500000`?sub2api 用 `/v1/usage actual_cost` 非 `trend[].cost`?
- [ ] `recharge_ratio` 取同一时点值?平台 `ratio_snapshot` = SoloAI 当时 `recharge_ratio`?(§3.4 用÷不用预舍入率)
- [ ] 未知两侧都跳过(不写 0)?已知 0(today==null)两侧都写 0?
- [ ] 累计都是「自重置起每日之和」?
- [ ] 平台四桶行数恒等式成立?

**可接受误差**:**目标 0(分粒度精确整数)**。建议保留容差旋钮 `≤ 1 分($0.01)/(账号,日)` 仅兜
「真值恰在半分边界 ±float64 epsilon」的极小概率个例;出现 >0 差异先按清单排口径,不先放宽容差。见 §11。

---

## 10. 实施切分(拍板后,每项独立 Task;对齐 UI 交接阶段 5)

| Task | 范围 | 依赖 |
|---|---|---|
| **XM-0037a** | 成本登记簿(`upstream_account`/`token_map`,含 access_method)+ 计量型成本/收入取数连接器(§3.1/3.2,ADR-018 只读 + §2.4 整数折算) | ADR-014/018 |
| **XM-0037b** | 利润台账(`profit_daily`)+ River 周期任务(`cost_sync`,§5 三纪律 + 四桶) | 037a |
| **XM-0037c** | 订阅成本批次 + 代理资产(`subscription_cost_batch`/`proxy_asset`,§2.5)+ 摊销(§3.5)+ 写 Action | 037a |
| **XM-0037d** | 看板:ChannelSummary/UpstreamSummary 供数(§8.5)、毛利卡、可用天数卡(§7)、贡献利润上层卡(§3.6) | 037b/c |
| **XM-0037e** | 影子对比工具(§9,仅计量型) | 037b |

（UI 交接阶段 5「冻结 Channel/Upstream/Cost Batch/Proxy Asset 契约,先只读,影子对账后再考虑写」与此一致。）

---

## 11. 待拍板问题(供产品负责人拍板;含建议)

1. **金额存储标度**:scale-6(微美元,newapi 精确)vs scale-8(对齐 decimal(20,8))?(建议 scale-6 + 分粒度 0 差异)
2. **充值成本率规范表示(§3.4)**:规范存 `recharge_ratio`(除数,对齐 SoloAI)还是 `rechargeCostRate`(乘数,§10.2)?
   若后者为可编辑规范量,如何保证与 SoloAI 除数互为精确倒数以过影子对比?(建议:除数为规范存储,乘率为展示投影;成本恒用÷)
3. **影子对比可接受误差**:严格 0(分粒度)还是加 ≤$0.01/(账号,日) 容差旋钮?(建议保留旋钮、默认严格 0)
4. **订阅「有效天数」口径(§10.3/§3.5)**:按自然日还是计费日?时区?按天摊销的边界(起止含端)?(建议自然日 +08:00,起含止含)
5. **提前失效剩余未摊销「转损失」记账口径**:一次性计入失效当日 `cost`,还是单列损失科目不进渠道毛利?(建议单列损失,不污染渠道毛利)
6. **贡献利润两项数据源与归因(§3.6/§10.5)**:`支付手续费` 从支付系统怎么取?`可归属基础设施/代理成本` 的「批准口径」归因规则谁定?(建议 v1 只供 grossProfit,contributionProfit 上层卡待数据源就位)
7. **official_api 成本口径(§2.0)**:原厂账单直取还是 token×单价估算?v1 是否纳入?(建议 v1 占位,后置)
8. **代理成本「分摊账号数」定义**:代理挂给几个账号就摊几份,动态变化如何按天取?(建议按当日挂载关系快照)
9. **多币种汇率源(§2.4/§10.1)**:CNY 上游余额归一 USD 的汇率 `source` 用哪家、刷新频率?(建议登记簿配置汇率源 + rate_at 存证)
10. **runway/可用天数是否第一版就要**:newapi 主力盲区、订阅制无意义。(建议 037d 做但标注覆盖率边界)
11. **令牌映射维护**:`token_map` + 每令牌 sk-,手工 vs 自动发现?sk- 走 SecretProvider vs 加密缓存?(建议映射 Action 手工 + 自动发现候选;sk- 走 SecretProvider)
12. **多自营平台是否第一版分桶**:`platform_id` 四桶第一版上还是先单平台留列?(建议列第一天写全,分桶随 037d 启用)
13. **成本采集频率**:对齐 SoloAI 5min 还是登记簿可配?(建议可配,默认 5min)
14. **订阅摊销舍入/余数策略(§12.1)**:日摊销额舍入到 scale-6 后,Σ 日额须 == 批次总额(账目自洽)。用累计整数法
    (第 d 日 = round(总额×d/N) − round(总额×(d−1)/N),Σ 精确、无末日特判)、最大余数法、还是末日吸收余数?
    两次除法(/天、/账号)先后顺序与网格余数分配口径?(建议累计整数法:批次账目自洽、每日误差 ≤ 1 微美元)

---

## 12. v2.1 增补:订阅摊销舍入 · 供数粒度 · §10 落章节核对

> 本节 v2.1 追加,**不重排既有章节**。UI §10 各条口径在 v2 已分别落入正文(见 §12.3 核对表,
> 勿在本节重复);本节只补两处需更严谨的地方:订阅摊销的舍入与余数分配(§12.1)、
> ChannelSummary 供数粒度(§12.2)。

### 12.1 订阅摊销的舍入与余数分配(承接 §3.5,用 §2.4 精度框架)

§10.3 的每日订阅成本含**两次除法**:`(实付+附加−退款) / 有效天数 / 账号数`,代理成本再 `/ 有效天数 / 分摊账号数`。
整数最小单位下除不尽必留余数;若每日各自四舍五入,**N 天日额之和会偏离批次总额**(账目对不上)。方案(定点整数,零 float):

- **推荐:累计整数法(cumulative-integer,批次账目自洽)**。沿「天」轴,第 d 日应计
  `cost_d = round_halfup(总额_micro × d / N) − round_halfup(总额_micro × (d−1) / N)`。
  性质:`Σ_{d=1..N} cost_d ≡ 总额_micro`(精确、无漂移),每日误差 ≤ 1 微美元,**无末日特判**。
- 备选:**最大余数法**(N 份 floor,把 `总额 − Σfloor` 个最小单位按小数余数从大到小各 +1);
  **末日吸收**(前 N−1 天 floor,末日 = 总额 − 前面之和;最简单,但末日略高,报表须能解释)。
- **两次除法的顺序**:先按账号把总额用累计整数法分到每账号(Σ 账号 ≡ 总额),再对每账号沿天轴累计整数法
  (Σ 天 ≡ 该账号额)。两轴顺序需固定并写进契约,否则同一批次不同实现会得到不同的逐格值(虽总额一致)。
- **退款/续费/提前失效对余数的影响**:`refunded` 变化 → 只重算**剩余未摊天**的总额基础,不追溯改已摊日额(§10.3);
  续费=独立新批次(独立跑一遍累计整数法);提前失效=把 `terminated_on..expires_on` 的未摊余额一次性计入损失(§11 第 5 项)。
- 展示仍在「分」粒度;scale-6 中间精度保证「分」以下不因摊销累积出现可见误差。**采用哪种见 §11 第 14 项待拍板。**

### 12.2 ChannelSummary 供数粒度核对(§13,回应「逐渠道 vs 台账粒度」)

UI §13 `ChannelSummary` 要**逐渠道**的 `usageRevenue / supplyCost / grossProfit / successRate`(均 `Money`=amountMinor 字符串)。
台账 `finance.profit_daily` 粒度 `(upstream_account_id, business_day, token_id)` 且带 `account_id`——**比渠道更细**,
逐渠道是它的向上聚合,不是缺口:

| ChannelSummary 字段 | 第一版可供 | 粒度 / 口径 |
|---|---|---|
| `usageRevenue` | ✅ | `SUM(revenue_minor)` 按渠道键聚合 → Money 字符串 |
| `supplyCost` | ✅ | `SUM(cost_minor)`(计量型 §3.1 / 订阅型 §3.5)按渠道键聚合 |
| `grossProfit` | ✅ | `usageRevenue − supplyCost`(整数相减) |
| `successRate` | ⚠️ **后置** | **不在成本台账**——来自监控 / 探测线(relaymon),展示层 join,非本设计供出 |

- **「渠道键」需产品定义**:`upstream_account`(一上游账号=一渠道)/ `(upstream_account, token)`(令牌粒度)/
  自营侧 `account_id`(用户命中的自营渠道)——三者台账都能聚合,但 UI「渠道」指哪层要定,否则毛利归属错位。
  **默认建议:渠道 = `upstream_account`,令牌为其下钻。**
- **`platform_id` 四桶(§5.2)是「归属自营平台」的正交轴**,不是渠道粒度;渠道聚合与平台归集是两个不同 GROUP BY,别混。
- 结论:**第一版逐渠道能供 usageRevenue/supplyCost/grossProfit 三金额;successRate 由监控线在展示层补,标注来源与新鲜度**(宪法 12)。

### 12.3 UI §10/§13 各条 → 已落章节核对表

(供拍板者快速核对:每条已在 v2 正文有对应,不在本增补重复。)

| UI 出处 | 要求 | 已落章节 |
|---|---|---|
| §10.1 | 金额整数最小单位/Decimal 字符串、带币种、汇率带 rate_at/source/base/quote | §2.4、§1(4) |
| §10.2 | 上游现金成本=消耗×充值成本率;充值成本率=1/recharge_ratio;分组倍率独立 | §3.4、§2.1 |
| §10.3 | 订阅每日摊销、续费新批次、提前失效转损失、部分退款冲减、代理未挂载=0 | §2.5、§3.5、§6.4、(舍入)§12.1 |
| §10.4 | 可用天数=余额/全部映射渠道近7日日均消耗;无消耗/过期不显示伪精确 | §7 |
| §10.5 | 使用收入−供给成本=毛利−支付手续费−可归属基础设施/代理=贡献利润;充值不入收入 | §3.6、§3.2 |
| §13 | Money/Observed/ChannelSummary/UpstreamSummary 供数 | §8.5、§12.2 |

---

## 附:关键 SoloAI 引用速查(标准答案,只读)

| 主题 | 文件:行 |
|---|---|
| 利润引擎 | `internal/admin/relay_profit.go`(:60 cstNow、:129 两侧 nil 跳过、:258 cost=raw/ratio、:331 margin nil) |
| 落库/四桶 | `internal/store/postgres/relay_profit.go`(:32 ErrProfitNothingKnown、:46 Upsert、:266 四桶) |
| sub2api 成本 | `internal/relaymon/sub2api.go:263`(`/v1/usage` today.actual_cost) |
| newapi 成本 | `internal/relaymon/newapi.go:221`(`/api/log/self/stat?type=2`,:21 500000) |
| sub2api 收入 | `internal/admin/sub2api_admin.go:79`(`/admin/accounts/{id}/stats` today.user_cost) |
| newapi 收入 | `internal/platformdb/channel_revenue.go`(quota_data,/500000) |
| 5min 余额扫描 | `internal/admin/relay_scheduler.go:24`/:461 |
| 迁移 | 0084/0090/0091/0176/0179/0182/0180/0080/0085/0177 |

## 附:UI 交接 & 平台接入点速查

| 主题 | 出处 |
|---|---|
| 财务口径权威 | UI 交接 §10.1(金额)/§10.2(Key)/§10.3(订阅)/§10.4(可用天数)/§10.5(贡献利润) |
| 前端消费形状 | UI 交接 §13(`Money`/`Observed`/`ChannelSummary`/`UpstreamSummary`),§14(安全权限) |
| 字符串→最小单位 | `connectors/sub2api/amount.go`;`upstream.go` currencyScale |
| 读回不过 float | `internal/platform/ops/store.go:88` UseNumber();`alerts/rules.go` asInt64 |
| 凭证 | `internal/platform/secrets/*`;ADR-014 |
| River 周期任务 | `internal/platform/jobs/sub2api_sync.go`、`client.go`;ADR-013 |
| Action | `internal/platform/action/*`、`registry/actions.go`、`contracts/actions/*.json`;ADR-003 |
| 指标键 | `internal/platform/ops/freshness.go`、`ops/metrickeys_test.go` |
| 只读通道/迁移 | ADR-018;新迁移从 `db/migrations/000008_*` 起 |
| 宪法 | 2(Action)、9/10(审批/AI)、12(新鲜度)、13(金额禁 float、比例 Decimal)、14(UTC、业务日显式) |

---

## 12. 拍板记录(2026-08-28,产品负责人经 AskUserQuestion 确认)

**开工方式:按默认全线开工 a~e。** 业务默认一并生效:

| 拍板项 | 结果 |
|---|---|
| 订阅有效天数口径 | 自然日,CST+08:00,起止**含两端** |
| 提前失效剩余未摊销 | **单列损失科目**,不计入渠道当日成本(毛利不受一次性冲击,损失单独可见可追溯) |
| 多币种汇率源 | v1 手工汇率表,每条必带 rate_at/source/base/quote |
| 贡献利润两数据源(支付手续费/基础设施归因) | 未定 → 贡献利润卡诚实占位,待 M3 支付接入再接 |

技术项(产品负责人授权 AI 拍定):scale-6 微美元整数;影子对比严格 0 差异+容差旋钮默认关、
仅计量型渠道;recharge_ratio 除数为规范量、「充值成本率」仅展示投影;令牌映射手工 Action+
自动发现候选、sk- 走 SecretProvider;platform_id 四桶第一天写全、看板分桶后启;采集频率可配
默认 5min;official_api 成本口径占位后置;代理分摊按当日挂载快照;runway 随 037d 做并标注
覆盖率边界。

**本节之后,§11 的问题全部关闭;实施以本文档全文为规格,切分 a~e 逐片出 PR。**

---

## 13. XM-0037c 实施标注（2026-08-28，切分 c 落地）

> 本节只**登记裁定与偏差**，不重排既有章节。完整理由与反面案例见
> `docs/modules/finance/README.md` 的「订阅成本摊销（XM-0037c）」一节。

### 13.1 产品负责人裁定（本片开工前给出，已实施）

| 裁定项 | 结果 | 落点 |
|---|---|---|
| 摊销行的 `ratio_snapshot` | **恒为 1**——语义「摊销值即成本，未经折算」 | `finance.AmortizationRatio`；`WriteAmortizedRow` 强制 |
| 多令牌同账号的收入归属（§12.2 的「渠道键」） | **账号级聚合行**：一行 `(account, business_day, 哨兵)`，`cost` = 该账号全部令牌之和，`revenue` = 账号级收入；**单令牌账号维持令牌级行**保留下钻 | `Collector.collectAggregatedAccount`；结构化日志 `finance_revenue_attribution_aggregated` |
| `platform_id` 归属（§5.2 四桶的配置面） | 登记簿**新增可选列** `upstream_account.platform_id`，`finance.upstream_account.set` 参数扩展；**可空、登记后可改**（它是归属标注，不是口径分叉点，与 `access_method` 不同）；NULL 仍 = 未配对 | 迁移 000010；`PlatformResolver` 默认读它 |
| 提前失效的剩余未摊销额 | **单列损失科目**，不计入渠道当日成本（§12 拍板第 5 项的实现） | `finance.amortization_loss` |

### 13.2 §11 第 14 项（摊销舍入 / 余数策略）——本片选定

**两个轴用两套规则**，因为它们要保证的东西不同：

- **账号轴**（`/ account_count`、`/ shared_account_count`）：**半进**（§2.4 框架）。
  这一轴没有需要配平的 Σ——一笔批次只落到**一个**账号头上，其余份额由它们
  各自的批次登记，所以要的就是「最接近真值的那个整数」。
- **天轴**（`/ 有效天数`）：**末日吸收**（§12.1 的第三种方案）。
  前 N−1 天取整数除法的商（**向零截断**），末日 = 总额 − 前面之和。
  `Σ 日额 ≡ 份额` 精确成立，且**与商怎么取无关**——末日的定义就是那条差额。

  ⚠️ 前 N−1 天**不用半进**：半进时 `(N−1)×商` 可能超过总额，末日变负
  （`total=5`、`n=7` 时末日 = −1），而一天负成本会让那天的毛利凭空变大。
  截断的商恒 ≤ 真值，末日因而恒 ≥ 其余各期 ≥ 0。

**两次除法的顺序**固定为「先账号、后天」（§12.1 要求顺序写进契约）。

### 13.3 相对本设计稿的实施偏差（三处，均非口径改动）

1. **有效天数含两端**：§3.5 的公式字面是 `expires_on − starts_on`，实施为
   `+ 1`。以 §12 拍板「起止含两端」为准（更晚且经产品负责人确认）。
   少那一天不会让末日免费——末日吸收会把它的钱塞进倒数第二天。

2. **新增 `refunded_on` 列**（批次与代理各一）：§2.5 的列清单里没有它，
   但它是 §12.1「退款只重算剩余未摊天、不追溯改已摊日额」的**强制推论**
   ——「哪一天之前算已摊」只能由一个日期回答。配套约束：退款额与生效日
   同时有或同时无；生效日必须落在有效期内（期外的退款没有剩余未摊天可
   吸收那笔冲减，v1 无「过期后信用」科目，故拒绝而不是静默丢弃）；
   累计退款额只增不减。

3. **账号级聚合行的哨兵是 `account:<own_account_id>` 而不是空串**：
   台账主键第三段是 `token_id`，同一个上游账号下**可以有两个各自挂多把令牌
   的自营账号**，空串会让它们撞主键、后写的覆盖前一个（金额少一块而剩下
   那一行看起来完全正常）。两条库层 CHECK 把哨兵与真令牌的命名空间切开。

### 13.4 对 §5.1 的唯一一处偏离

订阅型入账走 `ProfitStore.WriteAmortizedRow`：**收入未知时照样建行**
（计量型的 `WriteRow` 在同样情形下不建行）。

§5.1 的「只有一侧就不建行」防的是「拿一次失败的读取拼出一行」；而订阅成本
不是读来的——它是平台自己付出去的钱按天摊开的算术，输入全在自己库里，
不存在读不到。一并压住的代价是：一条还没接上收入通道的订阅渠道（v1 常态，
newapi 收入 DSN 单独一片）会一行台账都没有，平台自己承诺的支出在报表上
完全不可见——那比「成本已知、收入 NULL、毛利 NULL」更不诚实。

收入侧仍照 §5.1：未知即 NULL，绝不写 0。

### 13.5 影子对比（§9）的边界不变

订阅型不进影子对比。台账里识别它的判据是
**`ratio_snapshot = 1` 且 `token_id` 以 `account:` 开头**，037e 据此排除。

### 13.6 本片未做（后续切分）

余额历史与可用天数（§2.3/§7）、看板供数（§8.5）、影子对比工具（§9）、
newapi 收入 DSN、`official_api` 的成本口径（§2.0，v1 占位后置）。
贡献利润（§3.6）的两项数据源仍未定，卡片继续诚实占位（§12 拍板）。

---

## 14. XM-0037d 实施标注（2026-08-28,切分 d 落地)

> 本节只**登记裁定与偏差**,不重排既有章节。完整理由与反面案例见
> `docs/modules/finance/README.md` 的「上游余额 · 可用天数 · 看板供数」一节。

### 14.1 §11 第 10 项(runway 是否第一版就要)——本片落地并标注边界

§12 拍板「runway 随 037d 做并标注覆盖率边界」。做了,边界如下,**这是本片
最要紧的一条事实**:

| 驱动 | 余额读取 | 卡在哪 |
|---|---|---|
| sub2api | `not_supported` | `/admin/accounts` 的 `extra` 是随上游版本演进的自由 JSON,本仓没有可核对的样本。余额是除数的分母——字段名猜错一层得到的不是报错,是一个看起来完全正常的可用天数 |
| newapi | `not_supported` | 障碍在上游侧:§7 已记「需上游先开 `CHANNEL_UPDATE_FREQUENCY`,否则死水」。死水余额比没有余额更危险——它算出的天数看起来精确、实际停在上个月 |
| Fake | ✅ 完整实现 | 带一条缓慢下降的序列,让「仅变化时落一条」那条分支在演示里真的被走到 |

**因此:生产里今天的可用天数会全部显示为「未接入」。** 覆盖率
(`runway_coverage: {total, known, reasons}`)是响应的一等字段并显示在卡上——
不显式说出来,看板上就只是一排「—」,看起来像坏了。

接通所需的是**一份真实响应样本**(sub2api)与**一次上游配置变更 + 一个刷新
时间戳**(newapi),不是更多代码。

### 14.2 可用天数的口径（§10.4 五条硬要求的实现选择）

| 选择 | 理由 |
|---|---|
| 窗口 = 近 7 个**完整**业务日,**不含今天** | 今天还在累积,算进去会让日均偏低、天数虚高,且虚高幅度每天早上最大——一个每天规律性说谎的预警值 |
| 日均 = 已知成本 ÷ **有数据的天数**(不是 ÷ 7) | 只采到 3 天时除以 7 会把日均压到四成,天数虚高一倍多——偏乐观正是最危险的方向 |
| 天数**向下取整**;透支落 0 天 | 保守估计;负天数没有意义,而 0 天已是最高档告警 |
| 币种不一致**不换算** | 没有汇率,编一个出来算出的天数是纯粹的错数 |
| 余额过期判据 = **最近一次确认**(30 分钟) | 见 14.3:余额稳定是常态不是故障 |
| 阈值非法时归 `critical` | 算得出天数却没有档位 = 看板上一个没有颜色的数字,而「没有颜色」看起来像「没问题」 |

「给不出」分五种且**各有各的话**:`not_applicable`(订阅型没有余额这个概念)/
`no_balance` / `balance_stale` / `no_consumption` / `currency_mismatch`。
合成一个「暂无数据」,「不需要做什么」与「需要有人去接通」就成了同一件事。

### 14.3 相对本设计稿的实施偏差(两处,均非口径改动)

1. **`balance_history` 是游程编码表,多两列**(`observed_at` / `source`),
   且 `captured_at` 的语义收紧为「这个值**第一次**被看到的时刻」。
   §2.3 的列清单里只有 `captured_at`,而 §7 要求「仅变化时落一条」、
   §10.4 要求「数据过期不显示伪精确天数」——两条碰在一起,只有一个时刻的话,
   **一个健康账号的余额一周没动就会被判成观测过期**,把正常状态显示成故障。
   于是每行是一段游程:新鲜度看 `observed_at`,「什么时候变的」看 `captured_at`。

2. **§13 的 `ChannelSummary.groupRate` 给不出**:登记簿里没有这一列
   (§2.1 只说它「独立存储/展示」,没给存储)。响应因此**不出这个字段**,
   而不是编一个 `"1"`——那个 1 会被前端乘进成本里(§10.2 明确要求
   「分组倍率独立,前端不重复乘算」)。要它得先加一列。

### 14.4 §12.2 的「渠道键」在供数层的落点

两个摘要端点**今天是同一个粒度**(一行一个 `upstream_account`),差别在投影:
渠道看钱、上游看供给。这是当前 schema 的事实而不是偷懒——登记簿的唯一索引
`(environment, system_type, base_url)` 让「一个供应商挂多个账号」在有 base_url
的账号上不可能发生;没有 base_url 的账号各自成一个供应商。
所以「按供应商聚合」的结果**就是**一行一个账号。

`supplier_key` 仍然回报,让这条不变量可被检查(httpapi 有断言钉住),
也让「哪天唯一索引放宽了」变成前端的一次 groupBy 而不是后端重构。

### 14.5 演示数据种子的身份取舍(需评审确认)

`XM_FINANCE_FAKE_SEED=true` 时启动经 **L1 Action** 种一批演示登记簿记录
(生产硬拒、幂等、走内核不绕库)。

登记簿那几个 Action 是 `humanOnly`(ADR-009 红线),而种子要调它们。
三条路里选了「**以 HUMAN 身份执行**」——绕过内核破坏宪法 2 条,
放宽 Action 到收 SERVICE 削弱的是生产护栏,而谎报类型只发生在非生产环境。
用 Issuer `xingmang://finance-demo-seed` 把来路写死,
`AuthenticationLevel` 留空(这里确实没有发生过认证)。

### 14.6 本片未做(留给 e 片与后续)

影子对比工具(§9,仅计量型)是成本线最后一片。
贡献利润(§3.6)的两项数据源仍未定,卡片继续诚实占位(§12 拍板)。
`groupRate` 的存储、可用天数阈值的可配置化(§12 说「可在设置调」,
而平台目前没有设置面,今天是后端常量 + 响应原样回报)同样后置。

---

## 15. XM-0049 收尾包（2026-08-28）

> 关闭 §14.3 第 2 项（groupRate 给不出）与 §14.6 的三条 follow-up 中的三条。
> 完整理由见 `docs/modules/finance/README.md` 的「收尾包」一节与
> `docs/modules/alerts/README.md` 的规则表。

### 15.1 分组倍率补上存储（关闭 §14.3 第 2 项）

迁移 000012 给 `upstream_account` 加可空 `group_rate NUMERIC`（必须为正）。
§13 的 `ChannelSummary.groupRate` 因此有了来源。

**口径没变,只是从「没有存储所以不出」变成「没配所以不出」**:
§10.2 的「分组倍率独立存储/展示,不并入 recharge_ratio,前端不重复乘算」
逐字有效——后端一次都不会乘它,响应用 `omitempty`(没配就不出这个字段,
不给空串),前端类型是可选字段。守这条的是一条**行为**用例
(两个只差一个分组倍率的账号,采集写出来的台账必须逐位相同),不是注释。

### 15.2 可用天数接进告警(关闭 §10.4 的最后一条要求)

新规则 `upstream.runway.low`,是 alerts 包**唯一一条不读 ops 观测**的规则:
可用天数是平台自己算出来的(余额 ÷ 近 7 日日均消耗,两侧原料都在自己的库里),
塞进一条 ops 指标再解 JSON 会多出一处「谁来算」与一处「怎么解」。
本包因此声明瘦接口 `RunwaySource`,由 `*finance.SummaryStore` 满足
(`finance` 不 import `alerts`,无环)。

三条判据:只看计量型上游;**算不出天数的一律不告警**
(「余额还没读到」是 §7 的覆盖率边界、当前是常态,报成告警会让每个环境
一上来就满屏红,然后规则被静默掉);严重度逐条按天数算而不是每档一条规则
(两条规则的话,一个 3 天的上游会同时命中两条)。

### 15.3 阈值可配,一份解析两个进程

`XM_FINANCE_RUNWAY_WARN_DAYS` / `XM_FINANCE_RUNWAY_CRIT_DAYS`(默认 10/5)。
两个消费者跑在两个进程里(api 回报给看板、worker 判告警档),所以:
**解析只有一份**(`finance.ParseRunwayThresholds`,两个 cmd 都调它)、
**部署一致靠 compose**(两个服务取同一个 .env 变量)、
**非法值拒绝启动**(静默回落会让写错的部署以为自己调过了)。

第三档 serious(默认 20)不可配;WARN 若被调到 ≥20 它自动让位到 WARN+1
——不让位会让三档不递增,`levelFor` 的兜底把每一条上游判成 critical,
一次配置手滑变成满屏红。

### 15.4 仍然开着的 follow-up(都要外部输入)

sub2api 余额的 `/admin/accounts` 响应样本;newapi 的上游
`CHANNEL_UPDATE_FREQUENCY` 配置 + 一个刷新时间戳(§7 的覆盖率边界);
阈值的设置面 UI;贡献利润的两项数据源(§3.6,待 M3 支付接入)。
