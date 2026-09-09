# platformusers（被管平台终端用户清单）只读接入契约 v1 — **DRAFT**

- 状态:**DRAFT**。端点普查已确认上游入口存在,**响应形状尚未对着实现核对**。
- 实现:`connectors/platformusers`(契约与 fake)、`internal/platform/platformusers`(平台侧查询入口)。
- 消费方:管理端「平台 → 用户管理」页签(ADMIN-IA v3 §2.1)。
- 依据:交接文档 §9.3 用户管理、规格 §9.1 新鲜度、宪法 7 条(凭据)、13 条(金额)。

## 0. 为什么单独一个连接器

`connectors/sub2api` 与 `connectors/newapi` 读的是**自营实例的运营面板汇总**
(用户总数、总余额、日收入),结果进指标表。本包读的是**逐用户明细**——
两者的敏感度、分页形态与端点都不同,而且逐用户明细**不进指标表、不落平台库**。

与 `connectors/metering` 是同一条理由:同名不同义的两个口径放进同一个包,
迟早有人拿错。

## 1. 数据源事实

| 平台 | 上游入口 | 状态 |
|---|---|---|
| Sub2API | `GET /admin/users` | 端点已确认存在,响应形状未核对 |
| NewAPI | `GET /api/user` | 端点已确认存在,响应形状未核对 |

**CPA 与服务器不在本契约内**:CPA 是渠道代理,它的「用户」是代理商(语义不同,
原型自己留了这个提问);服务器根本没有终端用户。给它们挂一个永远空的用户页签,
等于说「这个平台没有用户」,而事实是我们压根没接。

### ⚠️ 原型自己声明的 v1 边界(逐字)

> 当前只读契约 v1 仅提供用户总数与总余额;逐用户今日充值、今日消费和消费明细
> 是目标界面,接真实数据前需扩展 read contract v2。

因此本契约把**逐用户充值/消费**建模成 `Amount{Known:false}`,而不是缺省 0——
0 与「上游没给」在界面上长得一样,而它们在运营上完全相反。

## 2. 能力清单

```
platformusers.users.read
```

全部必须能被 `registry.ParseCapability` 解析且 `IsWrite()` 为 false;
`contracttest` 断言这一点,让「只读」成为可验证属性而非口头承诺(ADR-018 闸 4)。

## 3. 数据形状

### 3.1 `User`

| 字段 | 类型 | 说明 |
|---|---|---|
| `ID` | string | 上游用户标识,平台一律当不透明字符串 |
| `Username` | string | 展示名 |
| `EmailMasked` | string | **已打码**;空串=上游没记;`invalid-contact`=记了但形状不认识 |
| `Status` | enum | `active` / `limited` / `disabled` / `unknown` |
| `Balance` | Amount | 可用余额 |
| `PeriodRecharge` | Amount | **所选区间**的充值;v1 上游多半 `Known=false` |
| `PeriodConsumed` | Amount | **所选区间**的消费(计费口径);同上 |
| `Last30dConsumed` | Amount | 近 30 天消费,**滚动窗口、不随区间变**;同上 |
| `LastActiveAt` | time | 零值=从未活跃(≠ 很久以前活跃过) |
| `TokenPrefix` | string | 令牌前缀,**至多 8 字符**,永远不是完整 Key |

**不含**明文邮箱、手机号、完整 API Key。前两样在契约层打码,第三样根本不进
这个结构——令牌前缀足以把一条请求对上一个用户,完整 Key 属于凭据(宪法 7 条)。

### 3.2 `Amount` / `CountValue` — 可能缺席的值

```go
type Amount struct {
    MinorUnits int64   // 最小货币单位，**整数**
    Currency   string
    Known      bool    // false = 上游没给；此时另两个字段必须是零值
}
```

金额一律 int64 最小货币单位 + 币种,**全程零 float**(宪法 13 条)。
对外 JSON 里 `minor_units` 是**十进制字符串**:JSON number 在 JavaScript 里是
float64,超过 2^53 会静默丢精度。

`contracttest` 断言:`Known=false` 时必须同时是零值(一个「未知」却带着数字的
Amount 迟早会被某个调用方直接读出来用);`Known=true` 时必须有币种。

### 3.3 邮箱脱敏口径(唯一实现:`platformusers.MaskEmail`)

| 输入 | 输出 |
|---|---|
| `zhangwei@example.com` | `zh***@example.com` |
| `abc@example.com` | `a***@example.com` |
| `a@example.com` | `***@example.com` |
| `""` | `""` |
| `姓名 <a@b.com>` / `13800001111` / `user@` | `invalid-contact` |

- **本地名至多保留 2 个字符**,域名整体保留。保留域名的理由与 `MaskIP` 保留 /24
  相同:运营要回答「是不是同一家公司在批量注册」,抹掉之后这个问题只能靠登上游
  控制台解决——而那条路绕开了平台的权限与审计。
- 保留的是**前缀**不是后缀:处理工单的人对着 `zh***@` 还认得出是谁。
- **解析失败从不回退到透传**(宪法 7 条同一条思路:拦不住就拒绝)。
  本地名与域名都要过 `plausibleEmailPart`——少了这一步,`姓名 <a@b.com>`
  会被打码成 `姓名***@b.com>`,真实姓名原封不动地留在输出里(有测试钉住)。
- **打码发生在连接器**,不在 HTTP 层:否则明文会先进平台进程内存、日志缓冲、
  错误包装,再在最后一步被抹掉。

### 3.3b 统计区间 `Period`(XM-0053)

原型顶部的「统计区间」控件:**一个锚点业务日 + 一个粒度**,不是一对起止日。
存成起止对的话,「周」这个语义在往返之间会丢掉——回显时只能说
「2026-08-24 到 2026-08-30」,而人选的是「这一周」。

```go
type Period struct {
    Day         string      // 锚点业务日 YYYY-MM-DD;空 = 今天
    Granularity Granularity // day / week / month;空 = day
    From, To    string      // 解析出的**闭区间**业务日,由 Normalize 填,回显用
}
```

- **周 = 周一至周日**(ISO 口径)。Go 的 `time.Weekday` 里 `Sunday==0`,
  直接拿它当偏移会把周日划进上一周;
- **月 = 自然月**;
- **业务日按 CST 固定 +08:00 切**(`DefaultBusinessDayLocation`,与 metering 同源)。
  不用 `LoadLocation("Asia/Shanghai")`:那依赖容器里有 tzdata,而且会随 tzdata 更新而变。

「今天」由**服务端**解释。前端拿浏览器本地日期去填,在 UTC-5 的机器上会填成
账面上的昨天,而那一天的数字同样合理,没人会怀疑(宪法 14 条)。

拼错的业务日(`2026-8-1`、`2026-02-30`)与拼错的粒度(`weekly`)**当场报错**,
不静默回落:回落之后人会拿着一天的数字当一周的看。

### 3.3c 区间合计 `Totals` 带**覆盖率**

```go
type Totals struct {
    Recharge, Consumed Amount
    CoveredUsers       int64 // 流水已知的用户数
    TotalUsers         int64 // 符合筛选条件的用户数
}
```

合计只能把**上游给得出流水的那些用户**加起来,而 v1 契约对一部分用户给不出
(见 §0 的原型 warnbar)。两个数不等时这个合计是一个**下界**,界面必须说出来——
一个盖住这件事的合计会被读成全量,那正是宪法 12 条要防的「裸数字冒充完整数据」。

`UserPage` 另有 `ActiveToday CountValue`(今日活跃,按业务日数)与
`Period`(回显服务端实际用的区间,让「这一周是哪七天」只有一份实现)。

### 3.4 搜索**不匹配邮箱**

`ListFilter.Query` 匹配用户名 / 用户 ID / 令牌前缀。邮箱已经打码,拿明文去搜
一份打过码的数据只会永远搜不到;而为了搜索保留一份明文,等于把打码做了个寂寞。

### 3.5 分页与上限

- 游标分页,`NextCursor` 为空表示到底;
- `DefaultLimit=50`,`MaxLimit=200`。上限不是性能考虑,是**披露面**考虑:
  一次能拉走多少条用户明细应该是一个有上限的数,而不是由调用方决定。

## 4. 权限

```
platform.users.read
```

**不复用 `ops.read`**:后者看到的是聚合数字(平台有多少用户、总余额多少),
本 scope 是**逐用户**的资金明细。即便邮箱已打码,一份逐用户清单也足以还原
一家客户的经营规模,与 `request.read` 同一档
(一个回答「他用了什么」,一个回答「他花了多少」)。

`DefaultRoleScopeMap`:给 `admin`,**不给 `staff`**——
`resolver_test.go` 的 `TestDefaultRoleScopeMapIsConservative` 钉住了这个决定。

**列表读取不进审计**,与 `request.read` 一致:逐条**正文**才进审计,元数据列表
不进。否则每打开一次用户页签就往审计链里塞一条,那份「谁看过什么」的清单
会被噪音淹掉。

## 5. 只读闸(ADR-018)

| 闸 | 落点 |
|---|---|
| 1 https | `NewRealClient` 构造期拒绝非 https 端点 |
| 2 目标白名单 | 留空=一个请求都发不出去(fail closed) |
| 3 凭据只经引用 | 只接受 `secret://<scope>/<name>`,明文由 SecretProvider 在构造请求头那一瞬解析 |
| 4 只读接口 | `ReadClient` 只有 `ListUsers` 一个方法 |

四道闸**全部在构造期校验**:一个配置错误的客户端应该让进程在启动时说清楚,
而不是等到有人打开用户页签才报错。

## 6. 错误映射

| 连接器分类 | Action 错误码 | HTTP | 前端表现 |
|---|---|---|---|
| `bad_response` | `INVALID_PARAMS` | 400 | 查询条件不合法 |
| `auth` | `EXECUTION_FAILED` | 502 | 上游拒绝了只读凭据 |
| `rate_limited` | `EXECUTION_FAILED` | 502 | 上游限流 |
| `not_supported` | `ADVANCED_CONTROLS_REQUIRED` | 501 | **功能待上线**,不是「上游故障」 |
| `forbidden_target` / `write_attempt` | `INTERNAL` | 500 | 服务内部错误(平台自己的配置问题) |
| 平台不认识 / 未接入 | `NOT_REGISTERED` | 404 | Not Found(交接文档 §8) |

**上游错误的原始文本一个字都不进 Message**:Message 要回给前端,而这条通道
上游的错误正文里可能带着用户邮箱与令牌。

## 7. 配置(`cmd/platform-api`)

| 环境变量 | 取值 | 默认 |
|---|---|---|
| `XM_PLATFORM_USERS_MODE` | `off` / `fake` / `real` | `fake` |

默认 `fake` 而不是 `off`(与 reqlog 相反),因为两条通道的代价不同:reqlog 的
fake 是「用户与模型的完整对话」,有人会以为自己在看真实问答;用户清单的 fake
是一批一眼可辨的样本客户,且 `data_source` 带 `-fake`,前端据此挂演示横幅。

`real` 今天**显式拒绝启动**并说明原因,而不是悄悄回落到 fake——回落之后
没有人会发现自己配的 real 没生效。

## 8. 等真实响应核对的清单

接入前必须逐条核对,核完才谈得上把 `real` 打开:

1. **分页形态**:offset/limit 还是不透明游标?本契约用游标;若上游只有 offset
   分页,`NextCursor` 的「不重不漏」保证不成立,要在契约里降级说明。
2. **字段名**:用户 id / 用户名 / 邮箱 / 余额 / 状态 分别叫什么。
3. **余额的单位与标度**:分?元?浮点字符串?本契约要 int64 最小货币单位;
   上游若给浮点字符串,转换必须走定点解析而不是 `ParseFloat`(宪法 13 条)。
4. **逐用户充值/消费到底有没有**:原型的 warnbar 说 v1 没有。没有就让
   `PeriodRecharge`/`PeriodConsumed`/`Last30dConsumed` 保持 `Known=false`,
   **不要填 0**。
   ⚠️ **fake 现在供得出这三列(XM-0053),real 仍然给不出**——这是有意的落差:
   前端要能在样本上把「区间切换」「近30天对照」这些交互做完并测到,而真实
   上游那边的边界一个字没松。界面上的提示条明说了这几列来自样本数据源。
   接 real 时若上游确实没有,让它们回到 `Known=false`,界面自动退回「—」。
4b. **区间怎么传给上游**(XM-0053 新增):上游是收「起止时间戳」还是「日期 +
   粒度」?本契约对外是「锚点日 + 粒度」(§3.3b),对上游发什么由 RealClient
   翻译。若上游只按自然日聚合,「周/月」要在连接器里按天累加——那时**必须**
   保证每一天都取到了,取不全就整段 `Known=false`,不能给一个少了两天的合计。
4c. **今日活跃**:上游给不给「今日活跃用户数」?给不出就 `ActiveToday.Known=false`,
   第一格的副行显示「上游没给今日活跃数」,**不要拿本页活跃条数顶上**。
5. **状态枚举**:上游的取值集合,补进 `ParseUserStatus`。
6. **联系方式字段**:除邮箱外是否还有手机号——有就一并打码,明文不得出连接器。
7. **总数与总余额**:上游给不给?给不出就 `Known=false`,界面显示「—」,
   **不要拿本页条数当总数**(那会让分页控件显示一个错的总页数)。
8. **排序是否支持服务端**:本契约把排序键传给上游;若上游不支持,要么在
   连接器里做整页排序(仅限单页可容纳的量),要么在契约里说明排序只在本页内生效。

## 9. 破坏性变更

形状一旦冻结再变就发 v2。当前处于 DRAFT,允许在 v1 内修正形状——
但每次修正都要同步改本文件、`contracttest` 与 fake 样本三处。
