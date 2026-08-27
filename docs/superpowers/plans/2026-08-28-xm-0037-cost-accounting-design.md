# XM-0037 成本核算与毛利设计文档

> **状态:设计稿,待产品负责人拍板。不含实现,不建连接器,不派生代码。**
> 蓝本:admin.solov.cc 线上 = **SoloAI v2.5.130 增强版(commit 14e6f617)**。
> 依据:`docs/evidence/EV-2026-08-27-soloai-cost-accounting.md`(现行核算全貌+版本确认)、
> `docs/evidence/EV-2026-08-27-upstream-cost-recon.md`(上游余额可得性)、
> SoloAI 源码 `K:/soloai/soloai-v2-src`(已 checkout 14e6f617,只读参考)、
> 宪法 2/9/10/12/13/14 条、ADR-003(Action 唯一写入口)、ADR-013(River 只作后台任务)、
> ADR-014(SecretProvider 与 CredentialRef)、ADR-018(通道隔离)。
> 引用 SoloAI 处标注 `文件:行号`,均为「标准答案」;引用平台处为接入点。

---

## 0. 一句话与验收标准

平台的成本 / 毛利,口径与 admin.solov.cc v2.5.130 **逐笔一致**。验收标准:
平台与 SoloAI 并行跑 14 天,逐(账号,业务日)对比 `revenue / cost / profit`,
**在「分」(scale-2)粒度上 0 差异**。为什么是「分」而不是原始位:平台宪法禁 float,
成本按整数最小货币单位计算,而 SoloAI 用 float64 —— 两条路在「分」以下的尾数
本就不可能逐比特相等(见 §2.4),但都会舍入到同一个「分」。平台这条整数路
**比 SoloAI 更准**,是被对齐方(SoloAI)去逼近平台,而不是反过来。

---

## 1. 目标与对齐承诺

1. **成本 = 真实上游实扣,不是估算、不是人工填**。取数、折算、切日、归属四步全部
   照搬 SoloAI 增强版口径(§3、§4)。
2. **毛利 = 收入 − 成本;毛利率 = 毛利 / 收入**(收入 ≤ 0 时毛利率为 nil 不显示,
   对齐 `relay_profit.go:331`)。
3. **平台新建即无历史包袱**:第一天就把 `ratio_snapshot` 与 `platform_id` 写全,
   不复刻 SoloAI 演进期(0176 之前 / 0182 之前)的 NULL 兼容与写入方漏写缺陷。
4. **平台比 SoloAI 更严的地方**只有一处:金额存储用整数最小单位(int64),不用 float。
   这一处必须证明「换算不丢精度、对齐不掉队」——即 §2.4 的核心设计决策。
5. **对齐承诺的边界**:平台承诺在「分」粒度 0 差异,**不承诺**与 SoloAI 原始 float64
   逐比特相等(那既不可能、也等于把 float 的不精确抄进来)。若产品要求更细粒度对齐,
   见 §11 待拍板。

---

## 2. 数据模型

平台是全新库,无 SoloAI 的迁移演进包袱。三张表:成本登记簿、利润台账、余额历史。
下一条平台迁移编号从 **`000008`** 起(现有最高 `db/migrations/000007_alerts.up.sql`),
放独立 schema(建议 `finance`),沿用平台迁移规范:`.up.sql` 只前进不可改(宪法/规格 §5.7),
`environment` 外键 `core.environment`,金额不建 NUMERIC 列(见 §2.4)。

### 2.1 成本登记簿(每上游账号一行)

对应 SoloAI 的 `relay_stations` 里与成本相关的那几列(0084 `recharge_ratio`、
0090 `sub2api_token_map`),平台把它们收拢成一张「登记簿」。**这不是模型成本单价表**
——SoloAI 没有按模型的成本单价(0180 明确无可测来源),唯一成本配置是每上游账号一个
`recharge_ratio`,登记簿照此建模。

```
finance.upstream_account          -- 每个「上游 reseller 账号」一行
  id            uuid PK
  upstream_kind text NOT NULL      -- 'sub2api' | 'newapi'
  base_url      text NOT NULL      -- 上游站网址(https,不含凭证)
  credential_ref text NOT NULL     -- secret://<scope>/<name>,永不存明文(ADR-014)
  recharge_ratio NUMERIC NOT NULL DEFAULT 1   -- 除数,Decimal(宪法 13:比例用 Decimal)
  currency      text NOT NULL DEFAULT 'USD'
  business_day_tz text NOT NULL DEFAULT '+08:00'  -- 业务日切日时区,显式声明(宪法 14)
  status        text NOT NULL      -- active | disabled
  environment   text NOT NULL REFERENCES core.environment(id)
  created_at / updated_at timestamptz
```

令牌 ↔ 自营账号映射(SoloAI 的 `sub2api_token_map` JSON `{"<上游令牌id>":"<自营账号id>"}`)
平台单独成表,便于校验与审计,语义等价:

```
finance.token_map                 -- 上游令牌 ↔ 自营账号 配对
  upstream_account_id uuid NOT NULL REFERENCES finance.upstream_account(id) ON DELETE CASCADE
  upstream_token_id   text NOT NULL   -- 成本侧的键
  own_account_id      text NOT NULL   -- 收入侧的键:sub2api=账号id;newapi=channel_id
  PRIMARY KEY (upstream_account_id, upstream_token_id)
```

**上游令牌明文 sk-**:sub2api 成本侧要用每令牌的明文 `sk-` 打 `/v1/usage`(§3.1)。
SoloAI 把它 AES-GCM 加密落 `relay_token_keys`(0084C)以规避 newapi 取 key 限流。
平台不落明文表,改由 `SecretProvider` 解析(secret 作用域下每令牌一条,或按需取回后
加密缓存),`Reveal()` 只在拼 HTTP 头那一瞬出现,绝不入日志 / 响应(ADR-014、宪法 7)。
**令牌映射与 sk- 的维护方式(手工 vs 自动发现)见 §11 待拍板。**

### 2.2 利润台账(对齐 relay_profit_daily)

SoloAI `relay_profit_daily` 现列(0091 六列 + 0176 `ratio_snapshot` + 0179 `platform_id`):
`(station_id, day, token_id, account_id, revenue, cost, ratio_snapshot, platform_id, updated_at)`,
主键 `(station_id, day, token_id)`。平台等价表:

```
finance.profit_daily
  upstream_account_id uuid NOT NULL   -- 对应 SoloAI 的 station 维度
  business_day  date NOT NULL         -- CST 业务日(库内 date;切日口径见 §4)
  token_id      text NOT NULL         -- 上游令牌id(成本侧键)
  account_id    text NOT NULL         -- 自营账号id / channel_id(收入侧键)
  platform_id   text                  -- 自营平台归属快照;NULL=写入当时未配对(见 §5.2)
  revenue_minor bigint                -- 收入,整数最小单位(见 §2.4);NULL=未知(不是 0)
  cost_minor    bigint                -- 成本,整数最小单位;NULL=未知(不是 0)
  currency      text NOT NULL DEFAULT 'USD'
  ratio_snapshot NUMERIC NOT NULL     -- 本行 cost 折算实际用的倍率,逐行冻结(见 §6.3)
  updated_at    timestamptz NOT NULL DEFAULT NOW()
  PRIMARY KEY (upstream_account_id, business_day, token_id)
```

与 SoloAI 的差异(全部是「更严 / 更全」,不改口径):

| 项 | SoloAI v2.5.130 | 平台 XM-0037 |
|---|---|---|
| 金额类型 | `NUMERIC`,写入值是 Go `float64` | `bigint` 整数最小单位(宪法 13) |
| `ratio_snapshot` | 可空(0176 之前存量行 NULL) | **NOT NULL**,第一天写全 |
| `platform_id` | 可空、无外键,且 0182 前写入方漏写 | 第一天写全;仍无外键(四桶,见 §5.2) |
| 未知值 | `revenue/cost` 侧 nil 时不写(0 是已知 0) | 同(`NULL` = 未知,不是 0) |

`platform_id` **刻意保留无外键**(与 SoloAI 0179 同源取舍):外键会让「移除平台」
被历史永久阻塞,而历史归属必须在平台消失后继续存活。代价由 §5.2 的四桶恒等式兜住。

### 2.3 余额历史(可选,runway 用)

对应 SoloAI `relay_balance_history`(0080A,仅余额变化时落点)。仅用于 runway 燃尽预警,
**不参与成本**(成本用更精确的每令牌 actual_cost,见 §7)。

```
finance.balance_history
  id            bigserial PK
  upstream_account_id uuid NOT NULL REFERENCES finance.upstream_account(id) ON DELETE CASCADE
  balance_minor bigint NOT NULL       -- 余额,整数最小单位
  currency      text NOT NULL
  captured_at   timestamptz NOT NULL DEFAULT NOW()   -- 库内 UTC(宪法 14)
```

### 2.4 金额存储:整数最小单位方案(★核心设计决策)

这是平台唯一比 SoloAI 严的地方,也是团队要拍板的头号问题。完整推理:

**SoloAI 侧(被对齐方)**:
- 成本 `cost = actual_cost(float64) / recharge_ratio(float64)`(`relay_profit.go:258`),
  以 `float64` 写进 `NUMERIC` 列。即 SoloAI 的「真值」本身就是一个 float64 除法结果,
  带 float64 精度(~15–17 位有效数字)。
- 收入:sub2api = `today.user_cost`(float64,admin API);
  newapi = `SUM(quota) / 500000`(float64)。

**平台侧(宪法禁 float)**:全程整数定点,零 float64。方案:

1. **存储标度 scale = 6(微美元,int64)**。理由:
   - newapi 的 `quota` 是整数 credits,`quota/500000` USD 在 scale-6 下**精确无舍入**:
     `micro = quota × 10^6 / 500000 = quota × 2`(整数)。scale-2(分)做不到
     (`quota × 100/500000 = quota × 0.0002` 非整数)。故 scale 必须 ≥ 6。
   - scale-6 比展示所需的「分」低 4 位,分级展示零舍入误差;int64@scale-6 上限 ±9.2×10¹²
     USD,无溢出风险。
   - (备选 scale-8:与 sub2api 用户余额 `decimal(20,8)` 原生精度一致。若实测 sub2api
     的 `actual_cost / user_cost` 常带 >6 位小数,可选 8。见 §11。)

2. **上游金额入库走「字符串 → 最小单位」防线**,复用 `connectors/sub2api/amount.go`:
   - `rawAmount`(`amount.go:37`)把 JSON 数字**原样留字面文本、绝不过 float64**;
   - `decimalToMinorUnits(raw, scale)`(`amount.go:82`)按 ASCII 逐位 + 半进(`digits[point] >= '5'`)
     转 int64,溢出报错不静默;
   - 币种小数位由 `currencyScale`(`upstream.go:131`)显式登记,未知币种硬报错不猜。
   - 关键:sub2api 的 `actual_cost / user_cost` 是上游 JSON 的浮点字面量,平台按字面量
     解析到 scale-6 **精确到 6 位小数**——这一步平台已经比 SoloAI 的 float64 更准。

3. **折算(除以 recharge_ratio)用整数定点除法**。ratio 是 Decimal(如 `1.5`),
   表示为 `ratio_num / 10^ratioScale`(把 `"1.5"` 解析成 `15`,`ratioScale=1`)。则:
   ```
   cost_micro = round_halfup( actual_cost_micro × 10^ratioScale / ratio_num )
   ```
   全整数运算,唯一的舍入在这一步,误差 ≤ 0.5 微美元 / 行(SoloAI 的 float64 除法误差
   ~1e-16 相对值)——两者舍入到「分」必然相同。`rescaleMinorUnits`(`amount.go:188`)
   提供「高精度累加、末尾一次性折到分」的整数半进降标,可直接复用。

4. **读回不被 float 污染**:观测值若经 `ops` 存 jsonb,读回走
   `internal/platform/ops/store.go:88` 的 `dec.UseNumber()`,int64 最小单位不被 float64 腐蚀;
   消费侧取 `json.Number.Int64()`(`alerts/rules.go` `asInt64` 已是此法),绝不 `Float64()`。

5. **展示层还原**:对齐比对与看板都在「分」粒度做半进舍入后比较 / 显示。

**worked example**(sub2api,actual_cost=`"5.813729"`,ratio=`1.5`):
- 平台:`5.813729` →(scale-6)`5813729` 微美元;`5813729 × 10 / 15 = 3875819.33…` → 半进
  `3875819` 微美元 = `$3.875819` → 分:`$3.88`。
- SoloAI:`float64(5.813729) / 1.5 = 3.8758193…` → 分:`$3.88`。**一致**。

**结论(给拍板)**:整数最小单位与 SoloAI float 的对齐,**在「分」粒度可保证 0 差异**,
在「分」以下不承诺逐位相等(不可能且无意义)。newapi 侧因输入是整数 credits,在 scale-6
下连折算前都精确;sub2api 侧输入是浮点字面量,平台解析比 SoloAI 更准,折算后分级对齐。
唯一残余风险是「真值恰好落在半分边界 ±float64 epsilon」的极小概率个例(§9 用容差兜)。

---

## 3. 取数与折算(照搬公式,端点/鉴权逐条)

成本 = 上游今日实扣 ÷ `recharge_ratio`;收入 = 自营侧实收;毛利 = 收入 − 成本。
`ratio ≤ 0` 视为 1(`relay_profit.go:97`)。

### 3.1 成本取数(标准答案)

**Sub2API**(`relaymon/sub2api.go:263` `FetchSub2APIUsageCost`):
- 端点:`GET {base_url}/v1/usage`
- 鉴权:`Authorization: Bearer <该令牌明文 sk->`(apikey 自鉴权,非 admin key;
  上游 token 须已分组否则 403)
- 取值:`usage.today.actual_cost`(USD)= 该令牌今日在上游的真实扣费
- 折算:`cost = today.actual_cost / recharge_ratio`

**NewAPI**(`relaymon/newapi.go:221` `FetchNewAPITokenCost`):
- 端点:`GET {base_url}/api/log/self/stat?type=2&start_timestamp=<今日CST起>&end_timestamp=<now>&token_name=<令牌名>`
- `type=2` = 仅统计 consume(**不含充值**),这是关键,别丢
- 鉴权:`New-Api-User: <UserID>` + `Cookie: session=<Session>`
- 取值:`data.quota`,`成本(USD) = quota / 500000`(`NewAPIQuotaPerUSD = 500000`,`newapi.go:21`)
- 折算:`cost = (quota / 500000) / recharge_ratio`

**注意**:平台现有 `connectors/sub2api` 的 `sub2api.cost.daily` 指标读的是 admin 面板
`trend[].cost`(`upstream.go`,刻意避开 `actual_cost`),那是**我方自营实例的口径,不是
本核算的成本**。XM-0037 成本侧必须新走「每令牌 `/v1/usage` actual_cost」这条路,
**不得复用** `sub2api.cost.daily`。此接法走 ADR-018 的平台独立只读通道。

### 3.2 收入取数(标准答案)

**Sub2API**(`admin/sub2api_admin.go:79` `AccountRevenue`):
- 端点:`GET {base}/api/v1/admin/accounts/{account_id}/stats?days=30`
- 鉴权:`x-api-key: <admin_key>`
- 取值:`data.summary.today.user_cost`(USD);`today == null` 表示**今日零流量 = 已知 0**
  (不是未知,`sub2api_admin.go:103`、`relay_profit.go:192` 有生产实测佐证)
- 累计窗口值:`data.summary.total_user_cost`(近 31 天,平台自建累计不用它,见 §4)

**NewAPI**(`platformdb/channel_revenue.go` `FetchNewAPIChannelRevenueToday`):
- 直读自营 new-api 库(只读连接,不走 HTTP admin —— 协议对不上):
  ```sql
  SELECT COALESCE(SUM(quota), 0)
    FROM quota_data
   WHERE created_at >= <CST 今日 00:00 的 unix 秒>
     AND channel_id::text = <own_account_id>
  ```
- `收入(USD) = SUM(quota) / 500000`(与成本用同一个 500000 常量)
- `channel_id` 正是 `token_map` 的**值**(own_account_id);切日按 CST(见 `cstDayStartUnix`)
- 生产实测:按账号按日与库逐位相等(`migrations/0180` 注释:账号 29/252/259 精确吻合)

### 3.3 毛利

`profit = revenue − cost`;`margin = profit / revenue`,`revenue ≤ 0` 时 `margin = nil`
(`relay_profit.go:331-338`)。平台在「分」粒度整数相减,`margin` 展示层算比率。

---

## 4. 口径常量(逐条标注「必须与 SoloAI 一致,否则影子对比对不上」)

| 常量 | 值 | 一致性要求 |
|---|---|---|
| 业务日时区 | **CST = 固定 +08:00,无夏令时** | ★必须一致。SoloAI `cstNow()`(`relay_profit.go:60`)、`cstDayStartUnix`。库内时刻存 UTC,业务日切日显式声明 +08:00(宪法 14)——两者不冲突 |
| 收入 / 成本切日 | **共用同一时间权威** | ★必须一致。同一笔请求两边各切各的会「收入记 D、成本记 D+1」,利润凭空多一天又少一天且不报错(`channel_revenue.go` 注释) |
| 币种 | 全程 USD | ★必须一致 |
| NewAPI 倍率 | `NewAPIQuotaPerUSD = 500000`(USD = quota/500000) | ★必须一致。抄错不报错,只让某平台金额差 50 万倍(`newapi.go:16`) |
| newapi 成本 type | `type=2`(仅 consume,不含充值) | ★必须一致 |
| sub2api 成本源 | `/v1/usage` 的 `today.actual_cost`(非 admin `trend[].cost`) | ★必须一致(见 §3.1 警告) |
| 累计口径 | **自上次重置起,每日快照之和**(非上游 30 天窗口) | ★必须一致。SoloAI 累计 = `relay_profit_daily` 全行求和,重置即删行(`relay_profit.go` 头注) |
| 今日覆盖 | 今日行每轮 upsert 覆盖,过去日冻结 | ★必须一致 |

---

## 5. 三条不静默纪律(照搬)

### 5.1 取数失败存 NULL / 跳过,绝不写 0

SoloAI:`revToday==nil && costToday==nil` → 跳过整对(`relay_profit.go:129`);
库层 `UpsertRelayProfitDaily`(`store/postgres/relay_profit.go:46`)——两侧都 nil 返回
`ErrProfitNothingKnown`(不静默);只有一侧已知则**纯 UPDATE 不建行**(缺一侧就没有可断言
的利润,宁可这对不出现,也不能把未知成本写成 0 —— 那会让利润凭空等于收入)。
**已知 0 与未知 0 必须区分**:sub2api `today==null` 是已知 0(写 `&0.0`),取数失败是 nil。
平台照搬:`revenue_minor / cost_minor` 用可空 `bigint`,NULL=未知;跳过逻辑等价。

### 5.2 平台归属四桶,不 COALESCE、不 INNER JOIN 静默丢金额

SoloAI `SumRelayProfitByPlatform`(`store/postgres/relay_profit.go:266`)把归属分四桶:
**有效平台 / 指向已移除平台(单独成桶)/ NULL 未归属(单独成桶)**,用
`LEFT JOIN own_platforms`(非 INNER)、`platform_id` 上**不 COALESCE**。恒等式:
`四桶行数之和 == 独立 COUNT 的窗口总行数`(`BucketRowsSum` vs `TotalRows`,同事务快照),
破了就是聚合 SQL 伪造或丢了行。平台照搬这套聚合与恒等式断言。

### 5.3 今日覆盖、过去冻结

今日行 `ON CONFLICT DO UPDATE` 反复刷新;过去日不再触碰。`platform_id` 的 `COALESCE`
方向刻意「空缺可补、已有不动」(`relay_profit.go:77`)——绑定变更只影响新行,不追溯改写
历史行(页面明确承诺)。平台第一天就写全 `platform_id`,但保留同方向 COALESCE 语义。

---

## 6. 三诚实边界(照搬)

### 6.1 无模型级成本 —— 禁摊派

`migrations/0180` 是最该读的诚实文档:成本只有「每令牌每日一个标量」,上游两个端点
(`/v1/usage`、`/api/log/self/stat`)都只回标量、无模型维度。**模型级成本目前无可测来源**。
用量可以拆到 模型×小时×渠道(0180 的 `relay_model_usage_hourly`,有 requests/tokens/revenue),
但**该表刻意没有 cost 列**——能填进去的只可能是按收入/请求数摊派的推算值,与实测值在图表
里无法区分。平台若做模型维度,同样**只放用量不放成本**,渠道级成本仍是权威(利润台账里,
消费方自己 join)。

### 6.2 自营库 `account_stats_cost` 是售价不是成本

2026-08-01 生产只读实测:账号 258 当日 `sum(account_stats_cost)=45.02`,而上游真实实扣
只有 `5.81`(差 7.75 倍,且逐账号不同:0.85/0.67/0.74/0.38)。那几列是我方挂牌价口径,
**与上游实扣没有可用换算**。平台成本一律走 §3.1 的上游 actual_cost,**禁止**用自营库任何
「看着像成本」的列充数。

### 6.3 `recharge_ratio` 需 `ratio_snapshot` 逐行冻结

`recharge_ratio` 可被随时改写且无历史(SoloAI 0084)。不冻结的话,「上游涨价」与「倍率被调整」
分不开(量价归因需要拆开)。SoloAI 0176 加 `ratio_snapshot` 逐行冻结(存量行 NULL、消费方须显式排除)。
平台第一天起 `ratio_snapshot NOT NULL`,`recharge_ratio` 的修改走 Action + 审计(§8.3),留变更轨迹。

---

## 7. 上游余额与 runway

- **采集**:周期扫描(SoloAI 5 分钟,`relay_scheduler.go:24` `relayScanInterval`,按
  `relay_settings.scan_interval_minutes` 自适应最低 1min),用 base_url + 解密凭证拉上游余额,
  **仅余额变化时**落 `balance_history`(`refreshBalance`,`relay_scheduler.go:461`)。
- **余额来源**(读上游**已存**余额,不做平台直连原厂):
  - Sub2API:管理员 token 读 `/admin/accounts` 的 `extra` 字段,拿全部余额快照,零外呼纯读
    (EV-upstream-cost-recon §关键机制)。
  - NewAPI:读 `channel.balance`(需上游先开 `CHANNEL_UPDATE_FREQUENCY` 定时刷新,否则死水;
    覆盖率低,主力上游多为盲区)。平台**不**自己调 `update_balance`(写权限+会禁用渠道)。
- **runway**:`runway 天数 = 余额 / 近 7 日均成本`;档位阈值(SoloAI 0177,严格递增)
  `critical=5 < warning=10 < serious=20`,可在设置里调。runway **只做预警,不参与毛利**。
- **边界**:余额差分会被充值污染(正跳变判充值不计负成本),SoloAI 正因此不用它算成本,
  只用更精确的每令牌 actual_cost。订阅制上游(Claude/Codex/Gemini 套餐)拿不到边际成本
  (固定月费),runway 对这类账号无意义。**runway 是否第一版就要,见 §11。**

---

## 8. 与既有平台设施的接法

### 8.1 成本采集 = River 周期任务(照 `sub2api_sync` 模式)

新增 `internal/platform/jobs/cost_sync.go`,照 `sub2api_sync.go` 四件套:
job kind 常量、`Args` 结构实现 `river.JobArgs`(`Kind()`/`InsertOpts()`,队列
`QueueMaintenance`、`UniqueOpts.ByPeriod`)、`Worker` 嵌 `river.WorkerDefaults`、
间隔常量(建议对齐 SoloAI 的 5min,或按 `finance.upstream_account` 的扫描间隔配置)。
在 `internal/platform/jobs/client.go` 的 `NewClient` 里 `AddWorker` + `append(periodic, ...)`,
env 走 `cmd/platform-worker/config.go` 的 `XM_COST_SYNC_*`。遵 ADR-013:River 只做
定时同步/快照/报表/告警投递,不做工作流。每轮:遍历登记簿 → 取数(§3)→ 折算(§2.4)
→ upsert 利润台账(§5 纪律)。

### 8.2 指标键(cost.* / profit.* / margin.*)

按平台 `<system>.<subject>.<grain>` 命名,建议 **`finance.cost.daily` /
`finance.profit.daily` / `finance.margin.daily`**(勿用 `sub2api.cost.daily` ——
那已被连接器占用且是不同口径,见 §3.1)。在产出模块定 Go 常量,注册进
`internal/platform/ops/freshness.go` 的白名单(`RegisterMetricKey` / `registeredMetrics`),
并与 `ops/metrickeys_test.go` 的一致性测试对齐(漂移即 CI 失败)。金额值在观测里以
`*_minor_units`(int64)承载,新鲜度状态机(失败>未初始化>延迟>部分>新鲜)复用现有机制
(宪法 12:新鲜度可见,禁裸数字冒充实时完整)。

### 8.3 写操作走 Action(`recharge_ratio` 修改等)

`recharge_ratio`、`token_map`、登记簿增删改都是平台配置写,走 Action(宪法 2、ADR-003)。
建议 `finance.upstream_account.set` / `finance.recharge_ratio.set` 等,**RiskLevel = L1**
(低风险平台配置;注意 Foundation-A 内核对 L2+ 一律 fail-closed,L1 才可执行)。
照 `internal/platform/registry/actions.go` 的 `registry.service.create`(L1)模板:
`action.Definition` + `Handler` + sqlc 查询 + 契约 JSON(`contracts/actions/*.json`)。
审计经 ctx 注入(`RecordResource/RecordBefore/RecordAfter/RecordReason`),Before/After 摘要
调用方**预脱敏**;`credential_ref` 只记 ref 字符串不记明文。`recharge_ratio` 改动的
Before/After 天然给 §6.3 的倍率变更留轨。

### 8.4 凭证与只读通道(ADR-014 / ADR-018)

所有上游凭证经 `CredentialRef`(`secret://<scope>/<name>`),`SecretProvider.Resolve`
按需解密,`SecretValue.Reveal()` 只在拼头一瞬。平台成本/收入采集走**平台自己的独立只读账号**
(ADR-018:不复用开票线 Bridge V4 通道;四道只读闸:拒可写连接配置、强制只读事务、
每次连接复核服务端只读+权限、Connector 包内无写路径)。newapi 收入的自营库直连也在此约束下。

---

## 9. 影子对比方案

**目标**:平台与 SoloAI 并行跑 N=14 天,逐(账号,业务日)对比,验收 0 差异(分粒度)。

**对比维度**:`(upstream_account/station, business_day, token_id/account_id)` 上的
`revenue / cost / profit`。

**取数**:
- SoloAI 侧:直读其 `relay_profit_daily`(`revenue/cost` 为 NUMERIC);毛利 = 差。
- 平台侧:读 `finance.profit_daily`(`revenue_minor/cost_minor`),按 §2.4 折到「分」。

**对比 SQL(示意,两库各取后在对齐工具里 join)**:
```sql
-- SoloAI 侧:每(账号,日)分粒度四舍五入到分
SELECT account_id, day,
       round(SUM(revenue)::numeric, 2) AS rev_cent,
       round(SUM(cost)::numeric,    2) AS cost_cent
FROM   relay_profit_daily
WHERE  day >= :since
GROUP  BY account_id, day;

-- 平台侧:整数最小单位 → 分(半进)
SELECT account_id, business_day AS day,
       round(SUM(revenue_minor)::numeric / 1e6, 2) AS rev_cent,
       round(SUM(cost_minor)::numeric    / 1e6, 2) AS cost_cent
FROM   finance.profit_daily
WHERE  business_day >= :since
GROUP  BY account_id, business_day;
```
对齐工具 full-outer-join 两侧,标出:仅一侧有行(配对/切日错位)、rev/cost/profit 分差 ≠ 0。

**口径检查清单**(对不上时逐条排查,全部来自 §4):
- [ ] 业务日都按 CST +08:00 切?收入成本共用同一时间权威?
- [ ] newapi `type=2`?`500000` 常量?sub2api 用 `/v1/usage actual_cost` 非 `trend[].cost`?
- [ ] `recharge_ratio` 取的是同一时点的值?平台用 `ratio_snapshot`、SoloAI 用当时 `recharge_ratio`?
- [ ] 未知值两侧都跳过(不写 0)?已知 0(today==null)两侧都写 0?
- [ ] 累计都是「自重置起每日之和」,不是上游 30 天窗口?
- [ ] 平台归属四桶行数恒等式成立(没有静默丢行)?

**可接受误差**:**目标 0(分粒度精确整数)**。建议保留一个容差旋钮 `≤ 1 分($0.01)/(账号,日)`
仅用于兜住 §2.4 末尾那种「真值恰在半分边界 ±float64 epsilon」的极小概率个例;
若 14 天出现任何 >0 差异,先按上面清单排口径,而不是先放宽容差。**容差是否第一版就要、
放多大,见 §11。**

---

## 10. 实施切分(拍板后,每项独立 Task)

| Task | 范围 | 依赖 |
|---|---|---|
| **XM-0037a** | 成本登记簿(`finance.upstream_account` / `token_map` 迁移)+ 成本取数连接器(sub2api `/v1/usage`、newapi `/api/log/self/stat?type=2`,走 ADR-018 只读通道 + §2.4 整数折算) | ADR-014/018 |
| **XM-0037b** | 利润台账(`finance.profit_daily` 迁移)+ River 周期任务(`cost_sync`,§5 三纪律 + 四桶聚合) | 037a |
| **XM-0037c** | 看板毛利卡(收入/成本/毛利/毛利率,分粒度展示)+ runway 卡(余额历史 + 燃尽档位) | 037b、§7 |
| **XM-0037d** | 影子对比工具(§9 的取数 + full-outer-join + 口径清单 + 差异报表) | 037b |

（`recharge_ratio` / `token_map` 的写 Action §8.3 随 037a 落;指标键 §8.2 随 037b 注册。）

---

## 11. 待拍板问题(供产品负责人拍板)

1. **金额精度 / 存储标度**:采纳 scale-6(微美元,newapi 精确、sub2api 分级对齐)还是
   scale-8(对齐 sub2api `decimal(20,8)` 原生精度)?对齐承诺锁定在「分」粒度 0 差异,
   还是要更细?(建议:scale-6 + 分粒度 0 差异。)
2. **影子对比可接受误差**:目标 0(分粒度)。是否引入 `≤ $0.01/(账号,日)` 的容差旋钮
   兜半分边界个例?还是坚持严格 0、出现即排查?(建议:保留旋钮,默认严格 0。)
3. **runway 是否第一版就要**:runway 只对可查余额且非订阅制的上游有意义(newapi 主力盲区、
   订阅制无边际成本)。第一版做还是留到 037c 之后?(建议:037c 做但标注覆盖率边界。)
4. **令牌映射怎么维护**:`token_map`(上游令牌↔自营账号)+ 每令牌 sk- 明文的获取,
   手工维护还是自动发现?sk- 存 secret 作用域还是按需取回后加密缓存(SoloAI `relay_token_keys` 式)?
   (建议:映射 Action 手工维护 + 自动发现候选;sk- 走 SecretProvider。)
5. **多自营平台是否第一版就分桶**:`platform_id` 四桶归集第一版就上,还是先单平台、
   预留列后续启用?(建议:列第一天就写全,聚合分桶随 037c 看板需要启用。)
6. **成本采集频率**:对齐 SoloAI 5min,还是按登记簿可配?(建议:可配,默认 5min。)

---

## 附:关键 SoloAI 引用速查(标准答案,只读)

| 主题 | 文件:行 |
|---|---|
| 利润引擎(配对/记录/毛利/nil 跳过) | `internal/admin/relay_profit.go`(:60 cstNow、:129 两侧 nil 跳过、:258 cost=raw/ratio、:331 margin nil) |
| 落库(不静默/四桶/今日覆盖) | `internal/store/postgres/relay_profit.go`(:32 ErrProfitNothingKnown、:46 Upsert、:266 SumRelayProfitByPlatform 四桶) |
| sub2api 成本取数 | `internal/relaymon/sub2api.go:263` FetchSub2APIUsageCost(`/v1/usage` today.actual_cost) |
| newapi 成本取数 | `internal/relaymon/newapi.go:221` FetchNewAPITokenCost(`/api/log/self/stat?type=2`,:21 500000) |
| sub2api 收入取数 | `internal/admin/sub2api_admin.go:79` AccountRevenue(`/admin/accounts/{id}/stats` today.user_cost) |
| newapi 收入直查库 | `internal/platformdb/channel_revenue.go` FetchNewAPIChannelRevenueToday(quota_data,/500000) |
| 5min 余额扫描 | `internal/admin/relay_scheduler.go:24`(间隔)、:461(refreshBalance) |
| 迁移 | 0084(recharge_ratio/token key)、0090/0091(利润台账)、0176(ratio_snapshot)、0179/0182(platform_id+回填缺陷)、0180(模型级成本无来源)、0080/0085(余额监控)、0177(runway 阈值) |

## 附:关键平台接入点速查

| 主题 | 路径 |
|---|---|
| 字符串→最小单位(整数金额) | `connectors/sub2api/amount.go`(decimalToMinorUnits/rescaleMinorUnits/rawAmount)、`upstream.go` currencyScale |
| 读回不过 float | `internal/platform/ops/store.go:88` UseNumber();`internal/platform/alerts/rules.go` asInt64 |
| 凭证 | `internal/platform/secrets/{ref,provider,value,router,audit}.go`;ADR-014 |
| River 周期任务模板 | `internal/platform/jobs/sub2api_sync.go`、`client.go` NewClient;ADR-013 |
| Action 模板 | `internal/platform/action/*`、`internal/platform/registry/actions.go`、`contracts/actions/*.json`;ADR-003 |
| 指标键 | `internal/platform/ops/freshness.go`、`ops/metrickeys_test.go`、`connectors/sub2api/contract.go` |
| 只读通道 | ADR-018;新迁移从 `db/migrations/000008_*` 起 |
| 宪法条款 | 2(Action 唯一写)、9/10(审批/AI 红线)、12(新鲜度可见)、13(金额禁 float、比例用 Decimal)、14(库内 UTC、业务日显式声明) |
