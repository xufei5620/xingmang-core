# XM-CARD0/1/2/3：Infini 卡服务接入（连接器、领域层、Action、同步作业、管理端）

## status

READY（待验收线审读、复跑并人工合入）。**真实端点验证被密钥与 IP 白名单阻塞**
——签名口径、字段形状、金额单位目前只在文档层面成立。

## branch

`ai/claude/XM-CARD0-infini-connector`（base `release/v0.1-launch` @ `2bd90d3`）

## commit

- `9bd74eb` — feat(connector): 供应商写通道与 Infini 请求签名
- `751e157` — feat(infini): 九个端点、替身、连接器契约
- `a63724e` — feat(cards): 开卡 Action、限额、幂等台账、投影表迁移
- `ddf3e7e` — feat(cards): reveal Action 与审计贡献接线
- `ea2f380` — docs(handoff): 记录实现期发现
- `(本次)` — feat(cards): 四个资金/状态 Action、同步作业、PgStore、装配
- `(本次)` — feat(web): 管理端卡片页与 ADR-021

## summary

平台接入 Infini 的 API 开卡能力：开卡、充值、赎回、冻结、解冻、查看卡面，
外加卡片与流水的投影同步。定位是**内部运营自用**，但架构上不堵死以后
对外开放（见「对外开放的落点」）。

### 为什么要动 internal/platform/connector

`ReadOnlyTransport` 在 `RoundTrip` 层拒绝一切非 GET/HEAD 请求（ADR-018 闸 4），
而 Infini 除查询外全是 POST——平台此前**发不出任何 POST 给任何上游**。

新增并列的 `VendorWriteTransport`，闸门与只读通道一一对应（方法白名单、
主机完全相等匹配、allowlist 为空 fail closed、拒绝重定向、强制超时）。
理由与边界写在 **ADR-021**（本片新增，待验收线批准）：ADR-018 的适用范围是
「平台对 NewAPI/Sub2API 建立独立只读账号」，针对平台不拥有其业务真相的上游；
Infini 是平台作为客户购买服务的供应商。

两条通道唯一实质差别：「包内无写路径」在写通道上**消灭不掉**，替代约束是
走 Action + 幂等 + 金额上限三条上层纪律。

### 上游没有幂等键，这是整个设计的轴心

`/v2/cards/apply` 没有 `request_id` 之类的去重字段（文档已逐字核对）。
于是「请求发出去但没收到回复」既不是成功也不是失败，而是必须被显式表示、
且**禁止自动重试**的第三态 `unknown`。

平台自建的抓手是 `card_alias`：把幂等键的 SHA-256 前 8 字节派生成
`xm-<16 hex>` 写给上游，超时后用 `ListCards(alias)` 精确匹配对账。

对账收敛规则（`ReconcileIssue`）：

| 情形 | 结论 |
|---|---|
| alias 精确匹配到 1 张卡 | 收敛为 succeeded，卡落投影 |
| 匹配到多张 | **资损**，最高优先级报人工，绝不自动挑一张认下 |
| 宽限期内查不到 | 继续等，不打扰人 |
| 超宽限期仍无结论 | 亮红条要人工，**不自己改判成失败**——改判会解锁重试 |

错误分类到台账状态的映射判据只有一条：**这次请求有没有可能已经在上游生效**。
auth / rate_limited / rejected 等确定没生效 → failed（可重试）；
unavailable / bad_response 可能生效了 → unknown（禁止重试）。
默认分支落在 unknown 一侧，不认识的错误按最坏情况处理。

例外：冻结/解冻在上游天然幂等，超时落 failed 而不是 unknown——把天然幂等的
操作也锁进不确定态，只会让卡在出事时冻不上。

### 敏感数据

库里只有上游给的 `mask` 掩码卡号。完整卡号、CVV、有效期只在
`cards.card.reveal` 的一次性返回值里出现：不落库、不进日志、不进审计正文
（审计只记「谁在何时看了哪张卡」）。`RevealedCard` 的 `String()`/`GoString()`
返回打码文本，`%v`/`%+v`/`%#v` 都印不出来；管理端把明文放在组件局部 state，
关闭对话框即清空。

### 金额

卡余额与流水金额经 `money` 包换算成整数最小单位，币种不认识时报错而不猜 2 位。
申请金额与 `card_balance` **保持文本形态不换算**：单位是 token（USDT/USDC），
而 `money.CurrencyScale` 只登记法币，猜错的那 10000 倍不会有任何症状。
台账里两份都存（文本供发送与审计，整数供今日累计求和）。

限额以申请金额本身的单位计，**不做汇率换算**——「1 USDT = 1 USD」这种假设
一旦写进代码就再也没人会去质疑它。上限未配齐时 fail closed。

### 对外开放的落点

现在只做内部运营，但三处已经预留：`infini_card.owner_ref` 列（归属过滤依据）、
四档独立权限串（`card.read` / `card.issue` / `card.manage` / `card.reveal`）、
读端点的 `owner_ref` 过滤参数。以后开放给外部用户只需给读权限 + 把 ownerRef
改成由身份推导（**不能让请求方自己挑**，那是权限边界）。

## files_changed

**平台层**：`connector/vendor_transport.go`（+ 测试）、`connector/types.go`
（新增 `method_not_allowed`、`rejected` 两个错误分类）

**连接器**：`connectors/infini/` 九个文件（签名、请求、信封、卡片解码、
九个端点、能力契约、替身）+ 五个测试文件

**领域层**：`internal/platform/cards/` 十个文件（限额、操作台账与对账、
开卡编排、四个资金/状态操作、Action 声明与 Handler、同步器、读模型、
PgStore）+ 七个测试文件

**作业**：`jobs/card_sync.go`（+ 测试）、`jobs/client.go`（装配与校验）

**端点**：`httpapi/cards.go`（+ 测试）、`httpapi/router.go`（三个只读路由）

**装配**：`cmd/platform-api/cards.go`、`cmd/platform-api/main.go`、
`cmd/platform-worker/cards.go`、`cmd/platform-worker/main.go`

**迁移**：`db/migrations/000026_infini_cards.{up,down}.sql`（三张表）

**契约**：`contracts/connectors/infini.card.v1.md`、
`contracts/actions/cards.card.{issue,reveal}.v1.json`

**架构**：`docs/adr/ADR-021-供应商写通道.md`

**前端**：`api/cards.ts`、`components/CardsPanel.tsx`（+ 测试）、
`pages/CardsPage.tsx`、`router.tsx`、`ui-admin/navigation.ts`（+ 测试）

## tests_run

- `go test ./...` —— 53 个包全绿
- `go vet ./...`、`go build ./...` —— 干净
- `bash scripts/check-governance.sh` —— 退出码 0
- admin-web `vitest run` —— 104 文件 / 1480 用例全绿
- ui-admin `vitest run` —— 17 文件 / 261 用例全绿
- 两个前端包 `tsc --noEmit` —— 干净

新增测试约 90 条。**全部走替身或 httptest，没有一次真实上游调用，没有花钱。**

## not_run

- **真实端点验证**（被密钥与 IP 白名单阻塞）：签名口径、字段形状、金额单位、
  时间格式、`card_alias` 能否当幂等信标，全部只在文档层面成立。
- **PgStore 的集成测试**：需要测试库（`XM_TEST_DATABASE_URL`）。SQL 经
  `go vet` 与编译，但没有对真实 Postgres 跑过。**合入前建议由验收线补跑**。
- Storybook 构建：本片没有新增 ui-admin 组件（卡片页的组件都在 admin-web 内）。
- 前端门禁走的是 node 直调 tsc/vitest（worktree 无 node_modules，按既定配方
  用 junction 镜像主检出），不是 `pnpm -r run`。

## risks

1. **签名口径未经真实验证**。首次调用若 401，排查顺序：签名串结尾换行 →
   `headers` 值 → Date 格式 → 时钟偏差（±300 秒）→ IP 白名单。
2. **`card_alias` 能否在开卡时设置尚未验证**。整个幂等方案建立在这个假设上。
   若上游忽略该字段，超时对账要退回「持卡人 + 时间窗 + 金额」的模糊匹配。
   **这是真实验证的头号项目。**
3. **金额单位未验证**。`available_balance` 若不是美元，换算会静静地错 100 倍
   ——不报错、不告警，只有对账时才看得出。
4. **交易流水去重键是派生的**。上游流水接口不返回交易 id，用
   （卡 + 时刻 + 金额 + 商户 + 类型）的摘要认定同一笔。理论上会把两笔完全
   相同的交易折成一笔，但那比每轮同步造重复行要好——后者会让流水金额翻倍。
5. **写通道是新增的攻击面**。平台从此能向外部发 POST，虽被 allowlist 与方法
   白名单框住，但误用后果是花真钱。
6. **PgStore 未对真实库跑过**，SQL 层的字段名/类型错误要到运行时才暴露。

## follow_ups

**挡住上线的（产品负责人）**

- 申请 API key（keyId + secret）→ 申请 `card.create` 与 `card.reveal` 权限 →
  查出平台出口 IP 并登记白名单。**若服务器走动态出口或代理，白名单会时灵
  时不灵，须先解决出口固定性。**
- 定金额上限的实际数值（`XM_CARDS_LIMIT_PER_OPERATION` /
  `XM_CARDS_LIMIT_PER_DAY`，单位是 token 本身）。未配齐时领域层 fail closed，
  也就是开卡一律被拒。
- **确认「卡片管理」在导航里的归属**：ADMIN-IA v3 没有这一条，现放在
  「平台治理」下是实现期判断，导航测试里已注明待确认。

**待验收线决定的**

- **批准 ADR-021**（供应商写通道）。不批的话写通道这段代码要另找出路。
- 顺带发现：现有 `registry.connection.set_status` 与 `registry.connector.create`
  两份契约声明为 L2，而内核对 L2 及以上返回 `ADVANCED_CONTROLS_REQUIRED`
  并拒绝执行——**建议核一下这两个 Action 是否实际可用**。
- 补跑 PgStore 的集成测试（需测试库）。

**后续切片**

- 真实验证一次性跑掉契约里的七项验证清单，据此把
  `contracts/connectors/infini.card.v1.md` 从草案改为冻结。
- 卡消费接入成本核算（流水投影已经落库，`cards` → `finance` 喂数）。
- Foundation-B / XM-0030 落地后重估 Action 的风险等级（现在全是 L1，
  是平台能力所限而非风险判断——详见下方「实现期发现」第二条）。

## 实现期发现（写测试时才暴露的，都不是风格问题）

**一、Action 的返回值不进审计。** Handler 返回的东西只进 `Result.Value` 回给
调用方；审计的 resource_type / resource_id / before / after 必须用
`action.RecordResource` / `RecordAfter` 显式塞进 ctx 由内核取走。第一版开卡
Handler 漏了这一步，审计只知道「有人执行了 cards.card.issue」，出事时无法
定位到具体哪张卡。这一层**只有走真内核才测得到**。

**二、L2 及以上的 Action 在当前平台无法执行。** 内核的
`RiskLevel.RequiresAdvancedControls()` 对 L2/L3/L4 返回 true，而 Advanced
Controls 属 Foundation-B / XM-0030 尚未实现，执行时直接返回
`ADVANCED_CONTROLS_REQUIRED`。开卡按性质本该高于 L1，但声明成 L2 会让它变成
永远跑不起来的摆设，因此定 L1，护栏由幂等键 + 金额上限 + 审计承担。

**三、限额的精度闸。** `money.ParseMinorUnits` 对超出标度的精度做四舍五入，
于是 `0.30000001` 会被舍成 `0.300000` 与上限比较并放行，而发给上游的仍是
原始文本——一个比校验值更大的数。金额小到可以忽略，但「校验对象与执行对象
不是同一个值」不能接受，因此加了一道精度闸：小数位超出比较标度直接拒绝。

**四、开卡是异步的。** `/v2/cards/apply` 返回申请单而非可用的卡，要轮询到
`status=active`。这一条是在核对文档时发现的，直接改变了设计——同步作业从
「可选的优化」变成「功能闭环的必要环节」。
