# XM-INV-UNIT-DISPLAY —— 用户端不再显示后台记账刻度，改显示上游的真实余额

- status: ready-for-review（未推送，未合入发布线；分支只在本地 worktree）
- branch: `ai/claude/XM-INV-UNIT-DISPLAY`
- base: `faadf87`（RC106 上线后的发布线）
- commit: 见分支末条
- worktree: `K:/发票/wt-XM-INV-UNIT-DISPLAY`

## summary

负责人原话：「用户端这里不要显示这种后台代码类的余额，而是转化后的真实余额」。
指的是「按平台计算的开票资格」卡片里两格——「切点前旧余额 · 不可开票」与
「赠送 / 返利 / 管理员额度 · 不可开票」——它们原来渲染成
`30,179,629,498 SUB2_BALANCE_1E8`。那是后端记账用的内部刻度，
上游用户在自己账号页上看到的是 `301.80`。

现在这两格显示 `301.80`，下面一行是来源口径 `SoloV API 余额`，
原始数字与单位码退到 `title` 提示里（`30,179,629,498 SUB2_BALANCE_1E8`）供核对。
管理端账本详情保留原始单位不动，只在后面追加一句 `（折合 999.49 SoloV API 余额）`。

换算规则不写在代码里，写在契约里。`contracts/invoice-eligibility-wire.v1.json`
新增 `service_units` 段，每个单位码一条 `{code, divisor, decimals, display_label,
description}`；后端 `eligibilitywire` 校验它、把它生成进
`web/src/lib/eligibility-wire.generated.ts`，并新增一道**发现闸**：扫
`backend/internal` 与 `agents` 下所有非测试 Go 源码里落在单位码位置上的常量，
与契约做「不多不少」比对。往 `consumption.go` 加一个不进契约的新码，`go test` 变红。

### 三个必须讲清楚的判断

**一、取舍方向：四舍五入，不是截断（与派工单的建议相反）。**
派工单建议「向下截断，避免显示比账本多」，但它自己给出的两个数据点都是四舍五入的结果：

| 原始服务单位 | /1e8 精确值 | 截断 | 四舍五入 | 已知事实 |
| --- | --- | --- | --- | --- |
| 99,948,771,408 | 999.48771408 | 999.48 | 999.49 | 生产已核实上游显示 999.49 |
| 30,179,629,498 | 301.79629498 | 301.79 | 301.80 | 派工单写的期望是 301.80 |

这两格的目的就是「让用户看到跟上游一样的数」，差一分钱会重新制造它要消除的困惑。
而「显示得比账本多」在这里没有风险：两格都标着**不可开票**，谁也支不走，
它是对账信息不是可用额度；真正的可开票金额走 `money()`，那条路不经过这里。
理由写在 `convertServiceUnits` 的注释里，改回截断之前请先看那段。

**二、NEWAPI_QUOTA 的除数 500000 已由主控者在生产核实（2026-09-09）。**
生产 New API 库的 `options` 表里**没有** `QuotaPerUnit` 覆盖，所以生效值就是上游默认的
500000（= 1 美元）；同次核实 `USDExchangeRate=1`，上游显示没有再折算成别的币种。
这两条一起把交付时挂着的两条风险都消掉了。契约的 description 按主控者给的措辞写。

另外两处静态证据仍然成立，其中第一条是一道**现成的护栏**，值得留着：

- 本仓库 `contracts/newapi-source-projection-grants.postgresql.sql:116-117`
  （另外两个文件同形）的投影契约在 `QuotaPerUnit ≠ 500000` 时直接拒绝出数。
  线上一旦有人改了这个值，**采集会先停**，而不是让显示侧悄悄给出错的数。
  这一条也一并写进了 description。
- 上游 `common/constants.go:22` 定义 `QuotaPerUnit = 500 * 1000.0`，
  其前端按 `quota / quotaPerUnit` 显示。

即便如此，本平台显示的仍然只是 `quota / QuotaPerUnit` 这个基准数，不折算、不带货币符号。

**三、换算只改刻度，不改口径。**
换算后的数仍然是非现金 / 切点前的源侧余额，不是人民币，所以页面上不带 ¥ 也不带 $，
并且永远与来源口径标签一起出现。这跟
`backend/internal/httpapi/accounts_ledger.go:105-131` 那段「不给这些单位编造一个
人民币汇率」的说明不冲突：那说的是不许换成钱，这里换的是同一种东西的刻度。

## files_changed

修改：

- `contracts/invoice-eligibility-wire.v1.json` —— 新增 `service_units` /
  `service_units_description`。
- `backend/internal/eligibilitywire/wire.go` —— `Contract.ServiceUnits`、
  `ServiceUnit` 类型、`validateServiceUnits()`、`ServiceUnitCodes()`、
  `RenderTypeScript` 生成单位表与 `ServiceUnitCodeWire`。
- `backend/internal/eligibilitywire/wire_test.go` —— 既有的
  `TestContractRejectsMalformedGroups` 夹具补上 `ServiceUnits`（否则它连
  「well-formed 必须过」那一步都过不去）。
- `web/src/lib/eligibility-wire.generated.ts` —— 由 `-update` 重新生成。
- `web/src/App.tsx` —— `formatServiceUnits` 换成 `ServiceUnitCell` 与
  `ServiceUnitConversionHint` 两个组件；两格调用点与管理端期初余额那一格。
- `web/src/types.ts` —— `ServiceUnitSummary.unitCode` 由手抄的两个字面量改为
  生成的 `ServiceUnitCodeWire | null`。
- `web/src/lib/http-api.ts` —— 两处 `expectedUnit` 的字面量改为经
  `expectedUnitBySource`（类型钉在 `ServiceUnitCodeWire` 上）取值。
- `web/src/styles.css` —— 换算后的数字号 9px → 12px，新增
  `.service-unit-origin` / `.service-unit-converted`。

新增：

- `backend/internal/eligibilitywire/unitcodes.go` —— 单位码发现闸。
- `backend/internal/eligibilitywire/unitcodes_test.go` —— 识别规则测试 + 契约闸。
- `web/src/lib/service-units.ts` —— BigInt 换算与展示视图。
- `web/src/lib/service-units.test.ts` —— 换算精确性与降级。
- `web/src/App.service-unit-cells.test.tsx` —— 两格与管理端提示的渲染文案。
- `docs/handoffs/XM-INV-UNIT-DISPLAY.md` —— 本文件。

## 发现闸怎么保证自己不是恒真的

两张**互不同源**的网，都不从契约取输入：

1. **位置网**：落在单位码位置上的常量字符串——赋值给 `unitCode` / `*UnitCode`
   名字、`UnitCode:` 或 `"unit_code":` 复合字面量值、与 `.UnitCode` 的相等比较、
   名字里带 `unit` 的函数的常量返回。它不需要知道单位码「长什么样」，
   所以能看见 `NEWAPI_QUOTA` 这种毫无形状特征的码。
2. **形状网**：任何常量字符串里出现的 `<大写>_1E<数字>` token，包括 SQL 文本里的
   ——位置网永远够不到那里。

一个既没有形状、又出现在两条规则都不认识的位置上的新码仍然会漏。所以位置网遇到
**看不懂**的表达式时**拒绝作答**（报 file:line），而不是返回一个有缺口的集合，
沿用 `discover.go` 对 eligibility_status 的同一套立场。

刻意**没有**加「这些函数必须还在」的存在性断言（不同于 `SummaryStatusInputSpace`）：
它没用。契约声明的集合非空、比对是双向的，所以一个悄悄什么都扫不到的闸会把每个
声明过的码都报成 missing，自己就红了。

两处为了不误伤既有正确代码而加的规则（派工单没写，但不加闸就会在正确代码上报错）：

- **局部一跳解析**：`agents/sourceagent/payment_v3.go` 把值绕了两步才放进字段
  （`unitValue := unitCodeForSource(...)` → `unit = &unitValue` →
  `{WalletUnitCode: unit}`），中间两个名字都不叫 unit code。
- **委托校验**：`backend/internal/postgresstore/store.go:201` 直接
  `unitCode := expectedUnitForSource(sourceType)`。这类调用不算引入新词汇——
  被调函数自己的常量返回已经被同一次扫描收进去了。但这个推理只在「被调函数确实
  被读过」时成立，所以是**查**的不是**假定**的：`VerifyDelegations()` 在整轮走完后
  核对被调名字确实在扫到的函数集合里，否则报错。

## tests_run

全部在 `K:/发票/wt-XM-INV-UNIT-DISPLAY` 下实测，时间为 UTC。下表是
**NEWAPI_QUOTA description 更新之后的那一轮**（2026-09-09 05:48）；更新前
05:40 那一轮四条同样全绿，两轮结果一致。退出码是把输出落盘后判的，不经管道
（管道 + `head` 会因 SIGPIPE 报出与测试结果无关的失败）。

| 门禁 | 开始 | 结束 | 耗时 | 结果 |
| --- | --- | --- | --- | --- |
| `cd web && npm run typecheck` | 05:48:13 | 05:48:17 | 4s | exit 0 |
| `cd web && npm test -- --run` | 05:48:17 | 05:48:18 | 1s（vitest 自报 666ms） | exit 0，22 文件 / 374 用例全绿 |
| `cd backend && go vet ./...` | 05:48:04 | 05:48:04 | <1s（构建缓存命中；首轮 2s） | exit 0 |
| `cd backend && go test -p 1 -count=1 ./internal/eligibilitywire/... ./internal/httpapi/...` | 05:48:04 | 05:48:07 | 3s | exit 0，两个包 ok |

Go 测试一律加八个代理变量的 unset 前缀（本机既定坑）：
`env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy go test …`

## 变异验证（逐条 red → 还原 → green）

每条都是先改坏实现、跑到红、再还原；还原后用 `diff` 与备份逐字节核对过。

| # | 变异 | 变红的断言 | 结果 |
| --- | --- | --- | --- |
| M1 | `consumption.go` 的 `expectedUnitForSource` 多返回一个 `ZZ_UNDECLARED_CODE` | `TestServiceUnitsMatchTheSource`（报「emitted by code but not declared」并给出 file:line） | red → green |
| M2/M3 | 契约里 `NEWAPI_QUOTA` 改成 `ZZ_NOT_EMITTED` | 同上，**两个方向同时报**：`NEWAPI_QUOTA` 未声明 + `ZZ_NOT_EMITTED` 无人发 | red → green |
| M4 | `isUnitCodeName` 恒 false（位置网失效） | `TestUnitCodeScanFindsEveryPositionalShape`、`TestUnitCodeScanRefusesAPositionItCannotEvaluate`(4)、`TestUnitCodeScanChecksWhatItDelegatesTo`、`TestServiceUnitsMatchTheSource`、`TestBothUnitCodeNetsContributeToTheGate`、`TestNewUnitCodeReachesTheGate` | red → green |
| M5 | 形状网正则改成匹配不到任何东西 | `TestUnitCodeShapeNetIsIndependentOfPosition`、`TestBothUnitCodeNetsContributeToTheGate`、`TestNewUnitCodeReachesTheGate/shaped`。**注意 `TestServiceUnitsMatchTheSource` 仍绿**——形状网塌掉时契约闸自己发现不了，这正是「两张网都要有贡献」那条断言存在的理由 | red → green |
| M6 | 位置网的拒绝路径改成 `return nil`（悄悄跳过） | `TestUnitCodeScanRefusesAPositionItCannotEvaluate` 的 3 个子用例（tuple 那条走 AssignStmt 的另一条错误路径，仍绿，符合预期） | red → green |
| M7 | `VerifyDelegations` 恒 nil | `TestUnitCodeScanChecksWhatItDelegatesTo/callee_is_not_scanned` | red → green |
| M8 | `RenderTypeScript` 不写单位表 | `TestGeneratedModuleCarriesTheUnitTable` + 金样本 `TestGeneratedTypeScriptMatchesContract` | red → green |
| M9 | `validateServiceUnits` 恒 nil | `TestContractRejectsMalformedServiceUnits` 全部 12 个子用例 | red → green |
| M10 | 去掉「空串不算单位码」的守卫 | `TestUnitCodeScanFindsEveryPositionalShape`、`TestUnitCodeScanTreatsCopiesAsIntroducingNothing`、`TestServiceUnitsMatchTheSource` | red → green |
| M11 | 四舍五入改成向下截断 | 10 条：两个已核实数据点、半分进位、跨整数进位、大整数、渲染文案 | red → green |
| M12 | `BigInt(serviceUnits)` 改成过一趟 `Number` | 3 条大整数断言 | red → green（**见下**） |
| M13/M15 | `<strong>` 渲染 `view.title` 而不是 `view.amount`（顺带丢掉 title 属性） | 「两格都显示换算后的余额数」「原始刻度不再作为正文出现」「不带人民币符号」「New API 标签」 | red → green |
| M16 | 去掉来源口径那个 `<span>` | 「每格都带来源口径标签」「单位合同待建立」「未识别单位降级」「New API 标签」 | red → green |
| M17/M18 | 折合提示无条件渲染，并加上 ¥ | 「认识的单位码补一句折合值」「换算不出来时整句不出现」 | red → green |
| M19 | 用户端格子的数字前加 ¥ | 「换算值不带人民币符号」等 3 条 | red → green |

**M12 抓到了我自己的一条恒真断言，值得记。** 第一版「大整数不走浮点」用的输入是
23 位的 `12345678901234567890123`：改用 `Number` 之后**它照样绿**——丢掉的精度落在
显示位数以下，两条路都给出 `123,456,789,012,345.68`。也就是说那条断言当时是靠
「恰好看不出来」成立的，不是靠实现对。换成 26 位的
`12345678901234567890123456`（精确 `…678.90` vs 浮点 `…682.45`）与全 9 的
`99999999999999999999999`（精确进位成 `1,000,000,000,000,000.00` vs 浮点
`999,999,999,999,999.92`）之后，同一个变异变红。测试注释里写了这段经过。

## 偏离（相对派工单）

1. **取舍方向**：派工单建议向下截断，实现用四舍五入。理由见上「判断一」，
   代码注释里也写了。这是唯一一处与明确建议相反的决定。
2. **NEWAPI_QUOTA 的 description**：交付时我把派工单的「主控者核实中」换成了两处静态
   证据。核实结果回来后（2026-09-09）按主控者给的措辞重写为「除数 = New API 的
   QuotaPerUnit；生产 options 无覆盖，取默认 500000（2026-09-09 核实）」，
   **后面多留了两句**：投影契约在 `QuotaPerUnit ≠ 500000` 时拒绝出数这道护栏，
   以及同次核实的 `USDExchangeRate=1`。多留的两句是为了让「值改了会发生什么」
   与「上游会不会再折算」在契约里各有一句话可查；要精简就删这两句，
   主控者给的那一句原样保留着。除数值 `500000` 从头到尾没变过。
3. **多改了三处手抄点**：派工单在「事实」里列了 `web/src/lib/http-api.ts:694`
   等硬编码点，但「要做的」里只要求后端/代理侧的发现闸。我顺手把
   `types.ts` 的 `unitCode` 联合与 `http-api.ts` 两处 `expectedUnit` 接到了生成表上
   （纯类型/取值改动，行为不变，typecheck 覆盖）。**「来源 → 单位码」这条映射本身
   仍是前端的一份判断**，见 follow_ups。
4. **契约校验只做了派工单点名的四项**（code 非空唯一、divisor 正整数、decimals 0–8、
   label 非空中文），没有额外要求 `description` 非空。
5. **发现闸多做了两件事**（局部一跳解析、委托校验），派工单没写。不加这两条，
   闸会在 `payment_v3.go` 与 `store.go` 的既有正确代码上报错。

## risks

1. **未识别单位码的降级分支，在真实 HTTP 路径上目前到不了。**
   `mapServiceUnitSummary`（`http-api.ts:698`）与 `mapAccountLedgerDetail`
   （同文件 1140 附近）都在更早的位置对 `unit_code` 做严格相等校验，
   不匹配直接抛 `UNIT_CONTRACT_MISMATCH` / `INVALID_ACCOUNT_LEDGER_RESPONSE`。
   所以渲染层这条降级现在是纵深防御（以及 `mock-api.ts` 那条路可达），
   不是「后端先上线一版」时真正会走到的路。要让它真可达，得放宽那两处校验——
   那是**改动钱相关不变量**的决定，不在这次派工范围内，没有动。测试是对导出的纯函数
   与组件直接打的，注释里写明了这一点。
2. ~~**New API 的除数没有在生产实例上核对过。**~~ **已消除（2026-09-09，主控者核实）**：
   生产 `options` 表无 `QuotaPerUnit` 覆盖，生效值就是默认 500000。
3. ~~**上游可能按 `USDExchangeRate` 折算显示币种。**~~ **已消除（同次核实）**：
   生产 `USDExchangeRate=1`，上游没有折算。若将来有人把它改成别的值，
   本平台显示的基准数会与上游页面上的数分叉——这不是显示侧能自己发现的，
   届时要一并调整 display_label 的口径措辞。
4. **发现闸只扫非测试源码。**
   `backend/internal/postgresstore/source_readiness_integration_test.go:28` 的 fixture
   用了 `NEWAPI_CREDIT_1E6` 这个单位码，没有任何生产路径会发它，契约也没有它。
   把测试文件纳入扫描会让闸红在这条 fixture 上。这一条留着，见 follow_ups。
5. **管理端的接线没有渲染测试。** `AccountLedgerDetailDrawer` 在 `useEffect` 里
   自己取数，`react-dom/server` 不跑 effect，整段只渲染到 loading 态（仓库没有
   jsdom / RTL）。测的是导出的 `ServiceUnitConversionHint` 组件本身；
   「抽屉里确实用了它」只有 typecheck 保证。
6. **视觉没有截图核对。** 卡片里的字号从 9px 提到 12px（原来那串数字很长才需要 9px），
   新增两个 span。没有起前端跑浏览器确认换行/溢出。
7. **`web/node_modules` 是指向主检出的目录联结（junction）。**
   `K:\发票\wt-XM-INV-UNIT-DISPLAY\web\node_modules` → `K:\发票\invoice-system\web\node_modules`。
   前端门禁靠它才能跑。**在这个 worktree 里 `rm -rf web/node_modules` 会顺着链接删掉
   主检出的依赖**，删的时候用 `cmd /c rmdir` 而不是 `rm -rf`。
   重建：`New-Item -ItemType Junction -Path "…\wt-XM-INV-UNIT-DISPLAY\web\node_modules" -Target "…\invoice-system\web\node_modules"`。
8. **没跑全量。** 只跑了派工单指定的四条门禁；全量 `go test ./...`、`npm run build`、
   镜像门禁都没跑（派工单说全量由主控者跑）。

## follow_ups

1. 把「来源 → 单位码」这条映射也搬进契约（给 `service_units` 条目加 `source` 字段），
   消掉 `http-api.ts` 里剩下的两处三元判断。这次没做是因为派工单把条目形状定死了。
2. 决定 `mapServiceUnitSummary` 的 `UNIT_CONTRACT_MISMATCH` 要不要从抛错改成降级
   （对应 risks 1）。改了这一条，渲染层那条降级才真正在线上有意义。
3. ~~在生产 New API 实例上核对 `options.QuotaPerUnit`~~ —— **已完成（2026-09-09，主控者）**：
   无覆盖，取默认 500000；`USDExchangeRate=1`。契约 description 与 risks 2/3 已相应更新。
4. 清掉 `source_readiness_integration_test.go` 里的 `NEWAPI_CREDIT_1E6` fixture，
   或者把测试源码也纳入发现闸的扫描范围（对应 risks 4）。
5. 起前端看一眼这两格的实际排版（对应 risks 6）。
