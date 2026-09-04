# XM-CARD0/1/2/3：Infini 卡服务接入（连接器、领域层、Action、同步作业、管理端、双账号）

## status

**已上线生产**（2026-09-04，release `7b63f7d`，迁移到 000028，`/api/v1/cards` 返回 403 说明路由已挂载、healthz/readyz 200）。

凭据尚未填写，首次真实开卡未做——产品负责人上线后自行验证。

**真实端点验证第一轮已通过**（2026-09-04，只读接口，零花费）：签名口径、
Date 格式、IP 白名单、`card_alias` 回显、时间格式、`mask` 掩码形态六项确认。
**PgStore 已对真实 Postgres 跑过**（迁移 000026 + 10 条集成测试）。

仍未验证的只剩「要花钱才能验」的四项，其中头号是 `card_alias` 能否在开卡时
设置——整个幂等方案建立在这个假设上。

**产品负责人 2026-09-04 决定：不在本片做首次真实开卡，先合入上线，由本人
上线后自行验证。** 因此本片交付的是「配好就能用」的功能，而不是「已经用过
一次」的功能——下面 not_run 与 risks 里那四项在第一次真实开卡时才会有答案，
其中 `card_alias` 若不能在申请时设置，幂等对账要改设计（见 risks）。

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
- `(本次)` — feat(cards): 卡号明文落库、用途登记、成员邮箱下拉

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

### 敏感数据（口径 2026-09-04 变更，产品负责人决定）

**明文卡号、CVV、有效期现在落库**（迁移 000027）。原设计是只存掩码、
明文只在 reveal 的一次性响应里出现；产品负责人在看过三个选项与代价后
选择「明文直接落库」，换取「打开页面直接看到卡号」。

三个后果已写进迁移文件头与连接器契约：平台成为持卡数据存储方（备份、
导出、DB 权限进入敏感范围）；「谁看了哪张卡」的审计链失效；明文靠 reveal
拉取，因此 `card.reveal` 权限与 IP 白名单成了同步链路的硬依赖。

保留的部分：`RevealedCard` 的 `String()`/`GoString()` 仍打码，明文不进日志、
不进错误文本；读端点只把明文回给持有 `card.reveal` 的调用方，只有
`card.read` 的仍看到掩码——这是「谁能看卡号」剩下的唯一一道闸。

明文**只拉一次**：卡首次 active 时同步作业调一次 reveal 存下来，之后不再调。
有测试钉住「刷新卡状态不会把明文冲掉」——冲掉的话每轮同步都要重拉。

### 金额

卡余额与流水金额经 `money` 包换算成整数最小单位，币种不认识时报错而不猜 2 位。
申请金额与 `card_balance` **保持文本形态不换算**：单位是 token（USDT/USDC），
而 `money.CurrencyScale` 只登记法币，猜错的那 10000 倍不会有任何症状。
台账里两份都存（文本供发送与审计，整数供今日累计求和）。

限额以申请金额本身的单位计，**不做汇率换算**——「1 USDT = 1 USD」这种假设
一旦写进代码就再也没人会去质疑它。上限未配齐时 fail closed。

### 双账号

平台同时管理两个 Infini 账号，账号维度贯穿表结构、台账、限额、对账、
Action 与界面。**账号是内部运营维度**：只体现在管理后端，以后开放外部用户时
不暴露——那一侧的归属维度是 `owner_ref`，两者正交。

最要紧的两条纪律：**未配置的账号 fail closed**（不回落到「第一个」），
**对账只在操作自己的账号里查**（跨账号认下一张 alias 相同的卡，等于把别的
账号的卡记到这笔操作头上）。两条都有专门的测试。

幂等键**不**按账号分：它标识的是哪一笔业务操作，同键在两个账号上各来一次
等于让去重失效。

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

**迁移**：`db/migrations/000026_infini_cards.{up,down}.sql`（三张表）、
`000027_infini_card_pan`（卡面明文列）、`000028_infini_card_usage`（用途登记列）

**契约**：`contracts/connectors/infini.card.v1.md`、
`contracts/actions/cards.card.{issue,reveal,usage.set}.v1.json`

**架构**：`docs/adr/ADR-021-供应商写通道.md`

**前端**：`api/cards.ts`、`components/CardsPanel.tsx`（+ 测试）、
`components/CardDetailDialog.tsx`、`pages/CardsPage.tsx`、`router.tsx`、
`ui-admin/navigation.ts`（+ 测试）

## tests_run

- `go test -p 1 ./...`（**带真实测试库** `XM_TEST_DATABASE_URL`）—— 53 个包全绿
- `go vet ./...`、`go build ./...` —— 干净
- `bash scripts/check-governance.sh` —— 退出码 0
- admin-web `vitest run` —— 104 文件 / 1483 用例全绿
- ui-admin `vitest run` —— 17 文件 / 261 用例全绿
- PgStore 用途登记的四个集成测试做了变异验证：去掉 `SetCardUsage` 的账号谓词、
  让 UPSERT 覆盖登记列，两次都被断言咬住（写在实现之后的测试立刻就过，
  本身不证明它会咬人）
- 两个前端包 `tsc --noEmit` —— 干净

新增测试约 90 条。**全部走替身或 httptest，没有一次真实上游调用，没有花钱。**

## not_run

- **真实端点验证**（被密钥与 IP 白名单阻塞）：签名口径、字段形状、金额单位、
  时间格式、`card_alias` 能否当幂等信标，全部只在文档层面成立。
- **首次真实开卡**（会花钱）：`card_alias` 能否在申请时设置、异步轮询到
  active 的实际耗时、申请单终态取值、冻结后的 status 取值、余额 minor units
  与控制台是否对得上。产品负责人 2026-09-04 决定由本人上线后自行验证。
  已确认的输入：`product_id` 三个取值（1 Lite / 2 Pro / 102 AI，无产品列表
  端点）、费用只在 apply 响应的 `total_fee` 里、文档未写最低充值额。
- **本地只跑过 fake 模式**：`XM_CARDS_MODE=real` 的完整链路（凭据解析 →
  签名 → 上游 → 台账 → 投影 → 页面）没有端到端跑过。off 模式的界面表现
  已补（未启用说明，见 commit 29e2480）。
- Storybook 构建：本片没有新增 ui-admin 组件（卡片页的组件都在 admin-web 内）。
- 前端门禁走的是 node 直调 tsc/vitest（worktree 无 node_modules，按既定配方
  用 junction 镜像主检出），不是 `pnpm -r run`。

## 上线清单（人工执行）

AI 不部署生产（宪法红线）。合入后按这个顺序：

1. **迁移**：000026 / 000027 / 000028 三个，走既有迁移流程。000027 加的是卡面
   明文列——上线前确认数据库备份的存放与访问权限已按「含持卡数据」对待
   （契约「敏感数据」一节列了三个后果）。
2. **凭据**：在管理端「密钥引用」页填两个账号各两条：
   `secret://infini-chris/{api-key-id,api-secret}`、
   `secret://infini-linfeng/{api-key-id,api-secret}`。引用由账号 id 推出，
   不需要配环境变量。**不要把明文写进 .env 或提交进仓库。**
3. **环境变量**（`deploy/compose/.env`）：
   - `XM_CARDS_MODE=real`（取值只有 off / fake / real——**没有 live**。
     写错不是被忽略，是 api 与 worker 直接启动失败：2026-09-04 上线时
     照着「live」配，两个进程崩溃重启，生产控制台下线约 4 分钟。
     默认 off，不配就整组不挂载）
   - `XM_CARDS_ACCOUNTS=CHRIS,LINFENG`
   - 四个 `XM_CARDS_<账号>_LIMIT_PER_{OPERATION,DAY}=unlimited`
     （留空 = 未配置 = 开卡被拒，这是有意的 fail closed）
4. **IP 白名单**：Infini 后台确认服务器出口 IP 在列。开发机那两个临时 IP
   （电信家宽动态）上线后删掉。服务器出口 IP 用 `curl checkip` 在服务器上确认，
   不要假设它等于入站 IP。
5. **首次真实开卡**：最小金额试一张，核对 `total_fee` 的真实费率、轮询到
   active 的耗时（据此调 `XM_CARDS_SYNC_INTERVAL`，现在是拍脑袋的 300 秒）、
   卡面明文是否落库、freeze 后的 status 取值、余额单位是否与控制台一致。
   把结果回填进 `contracts/connectors/infini.card.v1.md` 的验证清单。

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
6. ~~PgStore 未对真实库跑过~~ —— **已消除**：迁移 000026 在真实 Postgres 上
   跑通，PgStore 有 10 条集成测试（幂等去重、跨账号隔离、今日累计的三态覆盖、
   流水去重键、owner_ref 保留语义）。

## follow_ups

**挡住上线的（产品负责人）**

- ~~申请 API key、权限与 IP 白名单~~ —— 已完成（两个账号，三个 IP：
  服务器 38.147.105.28、开发机直连出口 115.224.87.144、代理出口
  192.220.24.14）。**开发机那两个是临时的，上线后应从白名单撤掉**——
  家宽是动态 IP，长期留着等于白名单里挂了一条随时失效的规则。
- ~~定金额上限的实际数值~~ —— 已定：**两档都不设限**（产品负责人 2026-09-04：
  内部自用、Infini 账户余额本身就是硬顶、充值可 redeem 拉回）。配置值写
  `unlimited`，见 `deploy/compose/.env.example`。留空仍然 fail closed——
  忘了配和故意不限是两件事。以后要加护栏，最值钱的是单笔档（手滑挡板）。
- **凭据在管理端「密钥引用」页填写**，不再走环境变量——引用由账号 id 推出，
  值经 `credential.secret.upsert` / `rotate` 写入（那条路径写的就是
  SecretProvider 读的文件）。
- **确认「卡片管理」在导航里的归属**：ADMIN-IA v3 没有这一条，现放在
  「平台治理」下是实现期判断，导航测试里已注明待确认。

**待验收线决定的**

- **批准 ADR-021**（供应商写通道）。不批的话写通道这段代码要另找出路。
- 顺带发现：现有 `registry.connection.set_status` 与 `registry.connector.create`
  两份契约声明为 L2，而内核对 L2 及以上返回 `ADVANCED_CONTROLS_REQUIRED`
  并拒绝执行——**建议核一下这两个 Action 是否实际可用**。
- ~~补跑 PgStore 的集成测试~~ —— 已完成（见 tests_run）。

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

**五、`.env.local` 会把两个路由用例判成失败。** 本地起页面时建的
`web/apps/admin-web/.env.local` 被 vitest 一并读入，改掉了
`router.test.tsx` 两个用例断言的身份头与环境口径。这在更早一次全量门禁里
出现过一次又"自愈"（那次刚好删了该文件），当时在提交信息里记成"疑似资源
竞争"——**那个判断是错的**，现象是确定性的：有该文件必挂两条，无则 153 全过。
本地预览改用 vitest 不读的 `.env.preview.local` + `--mode preview`。

**六、UI 的两个 bug 只有把页面跑起来才会露头。** 一是表格 `rowKey` 用了
`card_id`，两个账号下同 id 的卡被折成一行（另一个账号的卡直接从页面上消失）；
二是开卡表单的账号下拉用 `useState(accounts[0] ?? "")` 初始化，而首屏
`accounts` 还是空数组，于是账号永远提交空值。两个都编译通过、都有测试覆盖
相邻逻辑，跑起来才发现。

**七、上线当天的两个事故，都不是代码逻辑问题。**

其一：`launch.yaml` 的 `platform-api` 段里 `XM_CARDS_MODE` 写了两次（本该给
worker 的一段落错了服务）。YAML 同一映射内重复键，PyYAML 默认「后者覆盖
前者」静默通过，而 docker compose 的 Go 解析器**拒绝整个文件**——于是全部
本地门禁绿灯、合并推送照常，直到生产 preflight 才炸。补了
`scripts/check-compose.py`：在此之前**没有任何一处门禁解析过 compose 文件**。
跑得起来却没覆盖到的门禁比没有门禁更危险，它给出的是虚假的绿。

顺带说，这个 bug 还藏着第二个后果：`platform-worker` 一个卡片变量都拿不到。
如果重复键没先把栈拦下来，表现会是「装好了但开卡永远停在 pending」——
比崩溃难查得多。严格解析器替我们挡了一次。

其二：上线指令里把模式值写成 `live`，而代码只接受 `off / fake / real`。
api 与 worker 因 `cards_config_invalid` 崩溃重启，**生产控制台下线约 4 分钟**。
`.env.example` 里写的是对的，错的是照着记忆写的散文指令——**配置值要从
`.env.example` 抄，不要凭印象写**。修正后一条 `docker compose up -d` 即恢复。
