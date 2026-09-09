# XM-INV-UNIT-DISPLAY —— 用户端不再显示后台记账刻度，改显示上游的真实余额

- status: 第一至五轮已随 RC107 上线；第六轮（负责人 09-09 的「原始单位不给用户看」）
  ready-for-review，未推送，未合入发布线
- branch: `ai/claude/XM-INV-UNIT-DISPLAY`（第一至五轮）、
  `ai/claude/XM-INV-UNIT-DISPLAY-USERONLY`（第六轮）
- base: `faadf87`（RC106 上线后的发布线）；第六轮的 base 是 `b3ded69`（RC107）
- commit: 见分支末条
- worktree: `K:/发票/wt-XM-INV-UNIT-DISPLAY`

## 第六轮：原始单位对用户端整个关掉（负责人 2026-09-09）

- 分支：`ai/claude/XM-INV-UNIT-DISPLAY-USERONLY`
- base：`b3ded69`（刚上线的 RC107）
- worktree：`K:/发票/wt-XM-INV-UNIT-USERONLY`

负责人原话：「原始单位不应该给用户看，这个我们后端自己知道就行。」

上一轮（本文档 summary 一节）把用户端两格改成「换算数 + 来源口径」，同时**把原始
数字与单位码留在 `title` 属性里**供逐位核对。这一轮把那条出路也关掉：原始刻度对
用户端整个不可达，不只是不在正文里。

### 改了什么

1. **`web/src/lib/service-units.ts`**：`ServiceUnitView` **删掉 `title` 字段**。
   收紧的方式是结构性的而不是约定性的——调用处没有可放进属性的东西，就不必靠
   「每个改这段的人都记得别放进 title」。三条降级分支（单位合同待建立 / 未识别
   单位 / 数值异常）的 `amount` 统一改成新常量 `unconvertibleDisplayText`
   =「暂无法换算」，不再回落到原始数字（旧版未识别时还带上单位码）。`note` 里的
   三句原因**逐字保留**：它们说的是分类，不含任何原始值，排查的人仍分得清是契约
   的问题还是这一行数据的问题。
2. **`web/src/App.tsx` 的 `ServiceUnitCell`**（用户端两格）：去掉
   `title={view.title}`。现在这两格渲染出来只有 `<strong>` 里的换算数和
   `<span class="service-unit-origin">` 里的来源口径，没有任何承载值的属性
   （`title` / `aria-label` / `data-*` 都没有）。
3. **管理端不动**：`RequireAdmin` 网关内的账本详情（`AccountLedgerDetailDrawer`
   的「期初余额（非现金）」）仍由 `<dd>` 直接打出
   `detail.openingBalance.serviceUnits` 与 `unitCode`，`ServiceUnitConversionHint`
   仍只在后面追加折合值。「后端自己知道就行」里的「知道」落在这里和后端日志上，
   所以这两处不能跟着一起藏。只在两处注释里补了指路的话，行为零改动。

### 顺带解决的一件事

第二轮 follow_up 第 7 条记着一笔可访问性欠账：原始刻度只在 `title` 上，触屏用户
看不到、读屏是否读得出来取决于实现。这一轮把 `title` 整个删掉之后，那条欠账不再是
「可达性不足」而是**故意不可达**，第 7 条随之作废（用户端本来就不该有这个信息）。
需要原始值的人走管理端账本详情或后端日志，那是这一轮明确指定的两条路。

### 测试：既有的「title 里有原始单位」断言改成缺席型

缺席型断言最容易恒真，所以每一条都写清楚它靠什么成立，并配了正向断言证明被测
元素确实渲染过：

- `web/src/App.service-unit-cells.test.tsx`
  - 旧的 `expect(html).toContain('title="30,179,629,498 SUB2_BALANCE_1E8"')` 换成
    「整段渲染结果里不出现原始刻度、不出现单位码」。**判据是形状不是名单**：
    单位码比 `/[A-Z][A-Z0-9]*_[A-Z0-9_]+/`（契约将来多一个码照样拦得住），
    原始刻度**先把非数字字符去掉再比包含关系**，所以它不是 `groupThousands` 的
    副本，那份分组逻辑改了也不会让这道断言悄悄失效。
  - 新增一条只盯属性面的：两格那一块里不许出现
    `/\s(?:title|aria-label|data-[a-z-]+)=/`。切片函数 `unitGrid` 靠类名切，
    切不到会返回空串而空串对任何「不包含」都成立，所以调用处先断言切到了东西。
  - 每条缺席断言旁边都有 `expect(html).toContain(">301.80<")` 一类的正向断言：
    组件被删空、或 items 被过滤掉时，缺席断言必须跟着红。
- `web/src/lib/service-units.test.ts`
  - `view.title` 的断言删掉；三条降级分支的期望从「原始数字 + 单位码」改成
    「暂无法换算」。
  - 新增「返回的任何字段里都没有原始刻度，也没有单位码」：扫的是
    `Object.values(view)` 的每一个字符串字段，**不是手列 `amount` / `note`
    两个名字**——上一版的 `title` 就是这么长出来的，将来再加一个带原始值的字段
    这条会自己红。循环前先断言字段数大于零，否则字段被删光时整条恒真。
  - 新增「三条降级分支的主显示是同一句，区别只在原因」：否则将来只改其中一条
    分支回落到原始值，另外两条各自的断言并不会红。
- 管理端那一组（`describe("管理端账本详情的折合提示")`）**一条没改，仍全绿**。

### 变异验证（逐条 red → 还原 → green）

| # | 变异 | 结果 |
| --- | --- | --- |
| M6-1 | `ServiceUnitCell` 把原始值以 `title` 属性加回 `<strong>` | **red**：`App.service-unit-cells.test.tsx` 5 failed / 7 passed |
| M6-2 | `serviceUnitView` 的 `degraded()` 回落成「原始数字 + 单位码」 | **red**：两个文件合计 7 failed / 40 passed |
| M6-3 | 删掉管理端 `<dd>` 里的 `serviceUnits` / `unitCode` 两行（模拟「顺手把管理端也藏了」） | **green —— 没抓住**，见下 |
| M6-4 | 给 `ServiceUnitConversionHint` 的 `<span>` 加 `data-mutation={value.serviceUnits}` | **red**：1 failed / 11 passed |

M6-1、M6-2、M6-4 还原后全绿（386 passed / 24 files）。

**M6-3 要单独说：它是绿的，也就是说「管理端保留原始单位」这件事目前没有测试盯着。**
原因是既有的（上一轮就写在本文档里的）覆盖缺口：`AccountLedgerDetailDrawer` 在
`useEffect` 里自己取数，`react-dom/server` 不跑 effect，整段渲染只到 loading 态，
所以那两行 `<dd>` 从来没有被渲染测覆盖过。管理端那一组断言测的是
`ServiceUnitConversionHint` 这个组件本身（M6-4 证明这组断言是活的、不是恒真），
不是抽屉里的原始值。派工单第 2 条要求管理端「不动」，所以这一轮没有为了可测而去
重构抽屉；**把这条缺口明确留给主控者决定**，见 follow_ups 新增的第 11 条。

### 门禁（UTC，实测）

| 命令 | 开始 | 结束 | 耗时 |
| --- | --- | --- | --- |
| `web: npm run typecheck` | 2026-09-09T08:55:09Z | 2026-09-09T08:55:12Z | 3s |
| `web: npm test -- --run` | 2026-09-09T08:55:12Z | 2026-09-09T08:55:13Z | 1s |
| `backend: go vet ./...` | 2026-09-09T08:54:48Z | 2026-09-09T08:54:51Z | 3s |
| `backend: go test -p 1 -count=1 ./internal/eligibilitywire/... ./internal/httpapi/...` | 2026-09-09T08:54:57Z | 2026-09-09T08:55:02Z | 5s |

结果：typecheck 通过；`24 passed (24)` / `386 passed (386)`；`go vet` 无输出；
`ok invoice-system/backend/internal/eligibilitywire 2.108s`、
`ok invoice-system/backend/internal/httpapi 0.457s`。后端本轮零改动，跑它是按派工单
要求确认没有被前端改动带塌。全量门禁由主控者跑。

### 偏离（相对本轮派工单）

1. **派工单点名的 `formatServiceUnits` 不存在。** 用户端两格走的是
   `ServiceUnitCell`（`web/src/App.tsx`，改动后在 1063 行附近）加
   `serviceUnitView`（`web/src/lib/service-units.ts`）。按位置和职责认定这两个就是
   派工单说的东西，没有另找也没有新建同名函数。
2. **降级分支的收紧范围比派工单写的宽一档。** 派工单只点了「未识别单位码」那条
   分支，我把三条降级分支（含「单位合同待建立」和「数值异常」）都改成了
   「暂无法换算」。理由：没有单位码的原始数字**仍然是后台记账刻度**，正是负责人
   指的那种数；只改一条会让另外两条继续把它显示给用户，这次改动就白做了。如果只想
   收紧未识别那一条，回退点是 `serviceUnitView` 里的 `degraded()`。
3. **`note` 里的三句原因没动。** 于是未识别时用户看到的是
   「暂无法换算」+「（未识别单位）」，数值异常时是「暂无法换算」+
   「（数值异常，未换算）」，后者读起来略重复。保留是为了不动既有已测文案、也为了
   排查的人还能一眼分清是哪条分支。要合并成一句的话是纯文案改动。
4. **面板副标题「旧余额和非现金额度始终按源服务单位展示」没改。** 它说的是口径
   （不是人民币）而不是刻度，换算之后仍然成立，所以按「不动无关文案」处理。

## 第五轮：发现范围锚在本仓库的 Go 模块上（RC107 容器门禁红）

第二、四轮把两道闸的范围从「手列」改成「从仓库根走整棵树」。**在容器里，仓库根
不是仓库。** RC107 的隔离容器门禁（`verify-postgres.ps1`）把 `backend/` 挂在
`/src`，而 `repoRoot()` 是从本文件自身路径往上数三级算出来的，于是解析成容器的
`/`——发现函数照着走，把 Go 自己的测试用例也解析了：

```
eligibilitywire: parse usr/local/go/test/bombad.go: illegal byte order mark (and 4 more errors)
```

红在 application 包的 `TestUserEligibilitySummaryReasonSetIsExhaustive` 上，
因为那条测试会触发状态扫描。

**这是第二、四轮那个改动的代价，我当时没想到**：把范围从「三个写死的目录」换成
「从根走」，解决了「新目录进不了闸」，同时引入了「根是什么由环境决定」。
两个都是范围问题，方向相反。

改法：范围锚在**本仓库自己的 Go 模块**上。新增 `DiscoverGoModuleRoots()`——只看
`<root>/go.mod` 与 `<root>/*/go.mod`（深度 0 和 1），把有 `go.mod` 的目录当模块根；
`DiscoverGoPackageDirs()` 只在这些模块树内发现包目录，跳过规则不变。

- 本地检出：找到 `backend`、`agents` 两个模块，与第四轮扫到的集合一致。
- 容器：`/src`、`/backend`、`/agents` 都有 `go.mod`，都会找到；`/usr` 没有，
  于是 `/usr/local/go` 根本不会被走到。
- `deploy/postgres/gosu-build/go.mod` 在深度 3，按这条规则不算本仓库的模块。
  它一个 `.go` 都没有——那是钉构建用的，不承载词汇——所以这次没有损失。
  深度限制本身是必要的：从 `/` 做深层搜索会找到 `/usr/local/go/go.mod`，
  等于绕回原地。

**重复目录只影响计数、不影响集合，这一点核对过了**：容器里同一个包会以
`src/internal/...` 与 `backend/internal/...` 两条路径各出现一次。全仓库搜过
`len(...)` 的用法，只有两处「集合是否为空」的判断和两处错误文案里的计数，
**没有任何按目录计数的断言**；契约闸、覆盖探针、范围断言全部按集合判断
（`Set(...)` / `visited[dir]`）。覆盖探针额外按文件路径去重，免得同一个文件在
失败信息里出现两遍。

**两条覆盖探针原来也从 `root` 起走**——同一个毛病的测试侧版本，在容器里会扫整个
文件系统（还会在 `/proc`、权限不足的目录上直接报错）。两条合并成一个
`sweepForCoverage` 辅助函数，同样锚在模块根上。它与扫描**共用的只有锚**
（go.mod 在哪，这是关于树的事实），**判据仍然独立**：自己走一遍、按文本匹配、
自己决定哪些文件算数。

一处依赖写在这里：范围断言要求 `backend/internal/postgresstore`、`agents/sourceagent`
等路径存在。按主控者描述，容器里 `/backend`、`/agents` 都只读挂着，所以成立。
若将来容器只挂 `/src`，这两条断言会是最先红的地方——那时该补挂载，不是放宽断言。

## 第四轮：状态闸的扫描范围也改成发现式

第三轮修的是两道闸的**判据**，范围只改了单位码那一道。复审接着在
`agents/sourceagent` 里种 `EligibilityStatus: "zz_probe_status"`，状态闸是绿的——
和第二轮在 `backend/cmd` 种单位码那次同款：`ScanStatusLiterals` 仍用手列的
`scannedPackageDirs`（application / postgresstore / httpapi 三个目录）。

改法与单位码侧对齐，并且**复用同一个发现函数**：`ScanStatusLiterals` 改走已导出的
`DiscoverGoPackageDirs()`，`StatusScan` 加 `ScannedDirs`，再配一条判据不同源的覆盖
探针 `TestStatusScanCoversEveryGoFileThatMentionsAStatus`——自己走一遍树、按文本
匹配，要求每个命中文件的目录都在扫描实际走过的目录里。两道闸从此共用同一个
「什么算本仓库的源码」的定义，不会各看各的树。

覆盖探针匹配**两种拼写**是有理由的：`EligibilityStatus` 抓 Go 侧赋值，
`eligibility_status` 抓 SQL 文本——这道闸的 COALESCE 默认值那一半就是从查询串里取值
的，一个文件完全可以只出现列名、不出现 Go 字段名，却引入一个状态。

放宽之后扫到的词汇没有变（契约闸原样绿），但确实多扫了几处，记下它们为什么无害：

- `backend/cmd/eligibility-shadow/report.go:262` 是
  `EligibilityStatus: account.EligibilityStatus`——复制，不引入词汇。
- `backend/internal/domain/types.go:129` 是 `!= "active"` 的比较，而状态闸的位置规则
  只认赋值与复合字面量键、不认比较（这一点与单位码闸不同，那边认比较）。
- `backend/internal/eligibilitywire` 自己也进了扫描范围；里面那些正则字面量
  （`COALESCE\s*\(...` 之类）不匹配 COALESCE 网要找的形状，两条 COALESCE 正则
  命中数都是 0，所以不会触发「读不懂的默认值」那条拒绝。

### 顺带修掉一条「断言成立、对象不对」

范围断言写完先跑变异，结果**范围收窄的变异没让它红**：
`TestStatusScanScopeReachesTheAgents` 问的是 `DiscoverGoPackageDirs()` 的返回值，
而变异改的是 `ScanStatusLiterals` 自己的循环——助手函数没被动，断言当然为真，
只是它证明的不是那道闸。改成断言 `scan.ScannedDirs`（扫描**实际走过**的目录）之后，
同一个变异立刻红。

单位码侧的 `TestUnitCodeScanScopeReachesTheCommands` 是第二轮我按同样思路写的，
有同样的毛病，一并改成断言 `scan.ScannedDirs`，并用 R4-M29 单独验证过。
助手函数不是闸，闸是扫描本身。

## 第三轮：把同款透传洞在 discover.go 一并堵上

第二轮我把「`discover.go` 的 `isStatusPassthrough` 有同款跨包洞」记成了
follow_up 9。主控者要求就在本分支修掉，不留欠账——**对的**：一个已经知道位置、
知道修法的洞留在 follow_up 里，等于把它交给「以后会有人看」这件事，
而这道闸恰恰是随 RC106 上线、正在生产里跑的那一道。

判据不再各写一份，抽进 `backend/internal/eligibilitywire/passthrough.go`：

```
isCopyOfAReadableValue(info, expr)
  选择器基名必须解析到一个非包对象
  解析成 *types.PkgName（包已导入） -> 不是复制
  什么都解析不到（包没导入）       -> 不是复制
```

两侧各自只保留「这个表达式是不是我关心的那个字段」：
`isUnitCodePassthrough = isUnitCodeExpr && isCopyOfAReadableValue`，
`isStatusPassthrough = isStatusField && isCopyOfAReadableValue`。
`discover.go` 的 `types.Info` 相应补上 `Uses`。

**顺带修正了一处「位置识别」与「透传判断」的混用**：`discover.go` 原来用同一个
`isStatusPassthrough` 兼任两职——左值上判「这是不是一个状态位置」，右值上判
「这是不是一次复制」。第三轮把左值那一职拆成 `isStatusField`（只看名字，因为
写另一个包的状态字段仍然是一次这道闸要管的写入），右值才走共用判据。

**这次差点写出一条恒真的测试，记下来**：跨包 red 用例里那条
`l.EligibilityStatus = *zzelsewhere.EligibilityStatus`，第一版在变异（退回只看名字）
下**仍然绿**——因为老的 `isStatusField` 不解引用，`*x.EligibilityStatus` 压根不算
状态位置，于是它是被**另一条**规则拒掉的，跟透传判据对不对无关。给
`isStatusField` 补上解引用（对齐 `isUnitCodeExpr` 的做法）之后，同一个变异让
四个子用例全红。变异验证的价值就在这儿：不做这一步，这条用例会一直看起来在守着
它其实没守的东西。

## 第二轮（复审意见）

复审判 pass/would_ship，留了 2 条 major、3 条 minor，全部已处理：

| 复审意见 | 处理 |
| --- | --- |
| major：扫描根手列 `{backend/internal, agents}`，`backend/cmd` 整棵树在闸外 | 扫描根不再手列，改为**发现**：`DiscoverGoPackageDirs()` 走整个仓库，凡是含非测试 `.go` 的目录都进闸。另加一道**独立**的覆盖探针，用自己的文本扫描核对「凡提到单位码的非测试 Go 文件都落在扫过的目录里」 |
| major：`isUnitCodePassthrough` 只看名字后缀，跨包选择器被无条件放行 | 改为要求选择器的**基名解析到一个非包对象**。解析到 `*types.PkgName`（包已导入）或解析不到（包没导入）一律拒绝 |
| minor：CSS 与 1987 行的 `.eligibility-unit-grid span` 重复，且引了只用一次的色值；注释「两行结构」不对 | 整条 `.service-unit-origin` 规则删掉（那条规则已经覆盖它），只用一次的灰色随之消失；注释改成三行结构，并写明「想调请改那一条，别再抄」 |
| minor（#7）：risks 3 被划成「已消除」，但全仓库没有任何护栏盯 `USDExchangeRate` | 只改措辞：risks 3 改回开着的状态（「核实一次为 1，无持续护栏」），契约 description 那句补上「时点事实，无持续校验」，补护栏的做法进 follow_ups 10，**本轮不动桥接契约** |
| minor：三条只记文档不改代码 | 见 follow_ups 6/7/8 |

**为什么覆盖探针要单独存在**：扫描根改成发现之后，`TestUnitCodeScanScopeReachesTheCommands`
断言 `backend/cmd/**` 在范围内。但那条断言只覆盖今天这棵树的形状。覆盖探针问的是另一个
问题——**扫描实际走过的目录**（`UnitCodeScan.ScannedDirs`）是否覆盖了**任何提到单位码的
非测试 Go 文件**——而且它用自己的文本扫描得出答案，不复用 `DiscoverGoPackageDirs`。
两者同源就等于自己给自己打分，那正是这一轮要修的病。

变异实测（见下表 R2-M21/R2-M21b）也把两者的分工暴露清楚了：**只**把扫描根退回手列时，
覆盖探针仍然绿——因为今天 `backend/cmd` 里本来就没有单位码，没有东西要它覆盖；
退回手列**并且**在 `backend/cmd` 里种一个码，覆盖探针立刻红并点名那个文件。
所以这两条断言一条都不能省：一条盯范围的形状，一条盯范围与内容的关系。

`agents/` 与 `backend/` 之外没有 Go 源码（`deploy/postgres/gosu-build/` 只有一个
`go.mod`，没有 `.go`），所以这次「widening」在今天的树上只多进了 `backend/cmd/**`
与 `agents/cmd/**`；意义在于以后新增的目录不需要有人记得来改这里。

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
500000（= 1 美元）。契约的 description 按主控者给的措辞写。

这里要把**两种「核实过」分清楚**，交付时我把它们混为一谈了：

- **`QuotaPerUnit` 有持续护栏。** 本仓库
  `contracts/newapi-source-projection-grants.postgresql.sql:159-160`
  （另外两个文件同形）的 cfg 检查在 `QuotaPerUnit ≠ 500000` 时直接拒绝出数：
  线上一旦有人改了这个值，**采集会先停**，而不是让显示侧悄悄给出错的数。
  上游 `common/constants.go:22` 也把它定义为 `500 * 1000.0`。所以这一条是
  「核实过 + 有人盯着」。
- **`USDExchangeRate` 只有时点事实，没有护栏。** 同次核实它是 1，上游显示没有
  再折算。但同一道 cfg 检查只覆盖 `QuotaPerUnit` / `Price` / `TopupGroupRatio`
  三个键，`USDExchangeRate` 不在其中，本仓库其他任何地方也不提它
  （`grep -rn USDExchangeRate` 只命中我自己写的这两处文档与 description）。
  折算一开，上游页面上的数与本平台的基准数就分叉，而**显示侧发现不了**。
  见 risks 3 与 follow_ups 10。

本平台显示的始终只是 `quota / QuotaPerUnit` 这个基准数，不折算、不带货币符号。

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
  `.service-unit-converted`（管理端折合提示）。来源口径那个 span **不另写规则**，
  它落在既有的 `.eligibility-unit-grid span` 上。
- `backend/internal/eligibilitywire/discover.go`（第三、四轮）—— 第三轮：
  `isStatusPassthrough` 改用共用判据，左值那一职拆成 `isStatusField`（并补上
  解引用），`types.Info` 加 `Uses`。第四轮：删掉手列的 `scannedPackageDirs`，
  `ScanStatusLiterals` 改走 `DiscoverGoPackageDirs()`，`StatusScan` 加
  `ScannedDirs`。
- `backend/internal/eligibilitywire/discover_test.go`（第三、四轮）—— 第三轮：
  跨包状态选择器的拒绝用例与 `item.EligibilityStatus` 的对照用例。第四轮：
  状态侧的范围覆盖探针与 agents 范围断言。
- `backend/internal/eligibilitywire/unitcodes_test.go`（第四轮）——
  `TestUnitCodeScanScopeReachesTheCommands` 改成断言扫描实际走过的目录，
  而不是发现函数的返回值。

新增：

- `backend/internal/eligibilitywire/unitcodes.go` —— 单位码发现闸：范围发现
  （`DiscoverGoModuleRoots` + `DiscoverGoPackageDirs`，第五轮锚到模块根）、
  两张识别网、拒绝路径、委托校验。
- `backend/internal/eligibilitywire/unitcodes_test.go` —— 识别规则测试、范围覆盖
  探针、契约闸；第五轮加模块锚定测试与两条探针共用的 `sweepForCoverage`。
- `backend/internal/eligibilitywire/passthrough.go`（第三轮）—— 两道闸共用的
  「这是不是一次可读的复制」判据 `isCopyOfAReadableValue` 与 `leftmostIdent`。
- `web/src/lib/service-units.ts` —— BigInt 换算与展示视图。
- `web/src/lib/service-units.test.ts` —— 换算精确性与降级。
- `web/src/App.service-unit-cells.test.tsx` —— 两格与管理端提示的渲染文案。
- `docs/handoffs/XM-INV-UNIT-DISPLAY.md` —— 本文件。

## 发现闸怎么保证自己不是恒真的

**范围**：整个仓库里含非测试 `.go` 的目录，由 `DiscoverGoPackageDirs()` 走出来，
不手列（第一版手列 `{backend/internal, agents}`，`backend/cmd` 因此整棵在闸外）。
唯一手写的是**跳过的目录名**：`.git` / `node_modules` / `vendor` / `testdata`，
四个都写在一处、都不是本仓库自己的 Go 源码，且后两个由覆盖探针盯着——
它们底下一旦冒出提到单位码的 `.go`，探针会红而不是默默放过。

**内容**：两张**互不同源**的网，都不从契约取输入：

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

第二轮补上的第三条（复审 major 2）：**「复制」必须是从这次扫描看得见的值复制**。

`out.UnitCode = item.UnitCode` 这种写法，第一版只看点号右边的名字就判成「复制、
不引入词汇」。于是 `m.UnitCode = zzelsewhere.WalletUnitCode`——另一个包的变量，
内容这次扫描根本没读过——也被放行了。判据改成：选择器的**基名**必须在这里解析到一个
**非包**对象。

- `payload.UnitCode`、`*payload.WalletUnitCode`、`unitCode`：基名是本包的变量或参数
  → 复制，放行。
- `somepkg.WalletUnitCode`：基名解析成 `*types.PkgName` → 拒绝。
- `zzelsewhere.WalletUnitCode` 且 `zzelsewhere` 压根没导入：基名什么都解析不到 → 也拒绝。
  「我看不出这是什么」不能和「这没问题」共用一个答案。

用**标识符解析**而不是基名的**类型**，是这条规则唯一不显然的地方，也是随手写会踩的坑：
stub importer 让所有跨包类型都是 invalid，按类型判会把 `item.UnitCode`（`item` 的结构体
来自别的包）也拒掉——而那是 postgresstore / application 里到处都是的正确代码。
`TestUnitCodeScanStillAcceptsInPackageCopies` 专门钉这一半。

同一类漏洞在 `discover.go` 的 `isStatusPassthrough`（eligibility_status 那道闸）里
**依然存在**：它也只看名字后缀。这次没动它——那道闸随 RC106 上线，改它是另一件事——
已记进 follow_ups 9。

## tests_run

全部在 `K:/发票/wt-XM-INV-UNIT-DISPLAY` 下实测，时间为 UTC。下表是
**第二轮改动之后**的那一轮。退出码是把输出落盘后判的，不经管道（管道 + `head`
会因 SIGPIPE 报出与测试结果无关的失败）。

| 门禁 | 开始 | 结束 | 耗时 | 结果 |
| --- | --- | --- | --- | --- |
| `cd web && npm run typecheck` | 07:34:50 | 07:34:53 | 3s | exit 0 |
| `cd web && npm test -- --run` | 07:34:53 | 07:34:54 | 1s（vitest 自报 632ms） | exit 0，22 文件 / 374 用例全绿 |
| `cd backend && go vet ./...` | 07:34:28 | 07:34:29 | 1s | exit 0 |
| `cd backend && go test -p 1 -count=1 ./internal/eligibilitywire/... ./internal/httpapi/...` | 07:34:29 | 07:34:33 | 4s | exit 0，两个包 ok |
| 加跑：`go test -run TestUserEligibilitySummaryReasonSetIsExhaustive ./internal/application/...` | 07:34:39 | 07:34:40 | 1s | exit 0（容器里红的就是这条；本机不复现容器路径，只能确认没被改坏） |

上表是第五轮改动之后那一次。此前跑过：第一轮 05:40、除数定稿后 05:48、第二轮代码后
06:11、`USDExchangeRate` 措辞后 06:17、第三轮后 06:25、第四轮后 06:41，每次四条全绿。
第三到五轮只动 Go 侧，前端两条属于回归确认。

**本机跑不出容器那条红**：`repoRoot()` 在这里解析到真正的仓库根，`/usr` 那种情况
不存在。所以第五轮的证据是 `t.TempDir()` 里造的假仓库根（见 R5-M30/M31/M32），
真正的确认要等主控者在 RC107 上重跑容器门禁。

`eligibilitywire` 这个包的用时随轮次在涨（0.8s → 2.0s）：范围从 3 个目录变成 23 个，
每个都要 parse + 类型检查，而且状态闸与单位码闸各走一遍。仍是秒级，但如果以后再加
第三道闸，值得让它们共用一次解析。

前两轮的记录（结果都是四条全绿）：第一轮 05:40，NEWAPI_QUOTA description 更新后
05:48（typecheck 4s / web test 1s / vet <1s / go test 3s）。

Go 测试一律加八个代理变量的 unset 前缀（本机既定坑）：
`env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy go test …`

## 变异验证（逐条 red → 还原 → green）

每条都是先改坏实现、跑到红、再还原；还原后用 `diff` 与备份逐字节核对过。
R2-* / R3-* / R4-* / R5-* 是第二到五轮新加的。

### 第五轮

| # | 变异 | 变红的断言 | 结果 |
| --- | --- | --- | --- |
| R5-M30 | 退回「从仓库根走整棵树」 | `TestGoPackageDiscoveryStaysInsideTheModules`，报出 `outside`、`outside/deeper`、`vendorish/nested` 都被扫进来了。**这就是 RC107 容器里发生的事**，只是那里的 `outside` 叫 `/usr` | red → green |
| R5-M31 | 模块根改成深层搜索（不限深度 0/1） | 同一条断言，报 `got [mod mod vendorish/nested]`——深层搜索会把嵌套模块也当成本仓库的模块，从 `/` 出发就等于找到 `/usr/local/go/go.mod` | red → green |
| R5-M32 | 找不到模块时安静返回空而不是报错 | `TestGoPackageDiscoveryRefusesARootWithNoModules`。空集合会让下游每道闸都拿空集合去比，是「绿得没有意义」的典型形状 | red → green |

三条都跑在 `t.TempDir()` 造的假仓库根上，因为「不在任何 go.mod 之下的 Go 源码」
这种情况在真实检出里造不出来——真实树里哪儿都在某个模块底下。

### 第四轮

| # | 变异 | 变红的断言 | 结果 |
| --- | --- | --- | --- |
| R4-M27 | 在 `agents/sourceagent/cutover.go` 里种 `EligibilityStatus: "zz_probe_status"` | `TestLotSyntheticStatusesAreDiscovered`（报「introduced by code but not declared: [zz_probe_status]」并点名 `agents/sourceagent/cutover.go:617 in zzProbeBuildLot`）+ `TestLotEligibilityStatusIsPersistedPlusSynthetic`。**这正是复审那次全绿的场景** | red → green |
| R4-M28 | 把 `ScanStatusLiterals` 的范围收回手列的三个目录（**不**种状态） | `TestStatusScanCoversEveryGoFileThatMentionsAStatus`（点名 `agents/sourceagent/cutover.go`、`backend/cmd/eligibility-shadow/report.go`、`backend/internal/domain/types.go`、`backend/internal/eligibilitywire/discover.go`）+ `TestStatusScanScopeReachesTheAgents` | red → green |
| R4-M29 | 让 `ScanUnitCodes` 自己的循环跳过 `backend/cmd`（发现函数不动） | `TestUnitCodeScanScopeReachesTheCommands` | red → green |

R4-M28 与单位码侧的 R2-M21 有个差别值得记：单位码那次，**只**收窄范围而不种码时覆盖
探针仍绿（`backend/cmd` 里本来没有单位码）；状态这次，只收窄范围覆盖探针就红了——
因为三个手列目录之外**本来就有**四个文件提到状态。也就是说这道闸的手列范围一直是
「当时恰好够用」，不是「够用」。

R4-M29 是为了验证第四轮顺手改的那条断言。改之前它问的是
`DiscoverGoPackageDirs()`，这个变异只动扫描自己的循环、不动发现函数，
所以改之前它**是绿的**；改成断言 `scan.ScannedDirs` 之后才红。

### 第三轮

| # | 变异 | 变红的断言 | 结果 |
| --- | --- | --- | --- |
| R3-M25 | `isStatusPassthrough` 退回只看名字（`return isStatusField(expr)`） | `TestScanRefusesACrossPackageStatusSelector` 4 个子用例全红 | red → green |
| R3-M26 | 把共用判据 `isCopyOfAReadableValue` 的包/非包判断取反 | 13 处，**两道闸同时红**：状态侧 3 条（含既有的 `TestScanStillSeesBareLiteralsAndPassthroughs`）、单位码侧 4 条、加四道契约/发现断言。这一条同时证明了「共用」是真的共用，不是抄了两份 | red → green |

R3-M25 第一次跑只红了 3 个子用例，「跨包 + 解引用」那条仍绿——它被老的
`isStatusField`（不解引用）以另一条理由拒掉，与透传判据对不对无关，
是一条恒真断言。给 `isStatusField` 补上解引用后重跑，四条全红。

### 第二轮

| # | 变异 | 变红的断言 | 结果 |
| --- | --- | --- | --- |
| R2-M20 | 在 `backend/cmd/bootstrap-sources/main.go` 里种两个码（`ZZ_CMD_CREDIT` 无形状 + `ZZ_CMD_QUOTA_1E6` 有形状） | `TestServiceUnitsMatchTheSource`，两个码都报「emitted by code but not declared」。**这正是复审那次全绿的场景** | red → green |
| R2-M21 | 把扫描范围退回手列的 `{backend/internal, agents}` | `TestUnitCodeScanScopeReachesTheCommands`。**覆盖探针此时仍绿**——今天 `backend/cmd` 里本来就没有单位码，没有东西要它覆盖 | red → green |
| R2-M21b | 退回手列范围**并且**在 `backend/cmd` 里种码（复审的原始场景） | `TestUnitCodeScanCoversEveryGoFileThatMentionsAUnitCode`（点名 `backend/cmd/bootstrap-sources/main.go`）+ `TestUnitCodeScanScopeReachesTheCommands` | red → green |
| R2-M22 | `isUnitCodePassthrough` 退回只看名字（`return true`） | `TestUnitCodeScanRefusesACrossPackageUnitCodeSelector` 4 个子用例全红 | red → green |
| R2-M23 | 把包/非包的判断取反 | 上面 4 条中的 3 条，加 `TestUnitCodeScanStillAcceptsInPackageCopies`、`TestUnitCodeScanTreatsCopiesAsIntroducingNothing`、覆盖探针、契约闸、两网断言——共 9 处 | red → green |
| R2-M24 | 去掉 `<span className="service-unit-origin">` 的类名 | 「每格都带来源口径标签」（改成按结构断言之后才抓得到） | red → green |

R2-M21 与 R2-M21b 一起说明了为什么两条范围断言都要留：一条盯范围的**形状**
（`backend/cmd` 在不在里面），一条盯范围与**内容**的关系（有码的文件是不是都扫到了）。
只留后者，今天这棵树上退回手列是绿的。

CSS 那条改动（删重复规则）没有对应的变异：它不改任何渲染文本，行为上等价，
`.eligibility-unit-grid span` 那条规则接管了颜色字号行高。能钉住的只有类名还在，
R2-M24 钉的就是这个。

`USDExchangeRate` 那条（复审 #7）**只改措辞，没有对应变异**：改的是契约的
`description` 与交接文档，`description` 不进生成物（`git diff --stat` 对
`eligibility-wire.generated.ts` 为空），也没有任何断言读它。真正能把这条风险
钉住的是 follow_ups 10 那道 cfg 护栏，那要动桥接契约，本轮不做。
支撑措辞的事实是实测的：`grep -rn USDExchangeRate` 在本仓库只命中我自己写的
文档与 description，投影契约的 cfg 检查（`newapi-source-projection-grants.postgresql.sql:159-167`）
只覆盖 `QuotaPerUnit` / `Price` / `TopupGroupRatio`。

### 第一轮

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
   第二轮按复审要求给后一句补了「时点事实，无持续校验」——它原来读起来像
   `QuotaPerUnit` 那句一样有护栏撑着，实际没有。
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
   这一条能划掉，是因为它**既核实过又有护栏**：投影契约的 cfg 检查在
   `QuotaPerUnit ≠ 500000` 时拒绝出数，值被改了采集会先停。
3. **上游按 `USDExchangeRate` 折算显示币种的风险仍然开着。**
   2026-09-09 核实一次为 1，**无持续护栏**：折算一开，上游显示与本平台的基准数
   会分叉，显示侧发现不了。
   第二轮把这一条从「已消除」改了回来——它当时是靠一次观测划掉的，而
   `QuotaPerUnit` 那条靠的是护栏，两者不是一回事。全仓库唯一盯 New API 配置的
   地方是 `newapi-source-projection-grants.postgresql.sql` 的 cfg 检查，它只覆盖
   `QuotaPerUnit` / `Price` / `TopupGroupRatio`；`USDExchangeRate` 在本仓库其余
   任何代码、SQL、schema 里都不出现。补护栏的做法见 follow_ups 10，本轮不动
   桥接契约。
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
   无覆盖，取默认 500000。同次也看了 `USDExchangeRate=1`，但那只是时点事实，
   风险没关，见 risks 3 与本节第 10 条。
4. 清掉 `source_readiness_integration_test.go` 里的 `NEWAPI_CREDIT_1E6` fixture，
   或者把测试源码也纳入发现闸的扫描范围（对应 risks 4）。
5. 起前端看一眼这两格的实际排版（对应 risks 6）。

第二轮复审记下、但**这一轮不改代码**的三条：

6. **`contracts/source-agent-batch.v3.schema.json:82` 的 `$defs/unitCode` 是词表的第二份
   手抄**：`{ "enum": ["SUB2_BALANCE_1E8", "NEWAPI_QUOTA"] }`，被同文件 6 处 `$ref` 引用，
   与 `invoice-eligibility-wire.v1.json` 的 `service_units` 各说各的。新增第三个单位码时
   两处都得改，而只有一处有闸盯着。做法有两条：让 `eligibilitywire` 也校验这个 schema 的
   enum 与契约一致（最小改动），或者由契约生成它。没有在这一轮做，是因为那是**批次协议**
   的契约、有自己的版本纪律（v1/v2/v3 三份并存），动它要先想清楚改的是不是 v3 一份。
7. ~~**title 上的原始刻度，触屏与读屏取不到**~~ —— **作废（第六轮，2026-09-09）**：
   负责人定了「原始单位不应该给用户看」，`title` 已整个删掉。原来这条想的是
   「怎么让原始刻度对所有用户都可达」，现在的答案是**它对用户端一个都不该可达**。
   需要原始值的人走管理端账本详情或后端日志。
8. **欠费账号这两格会显示 `0.00`**：`service_units` 的协议形状是非负整数
   （`^(0|[1-9][0-9]{0,77})$`，前后端与 agents 三处同形），负余额在协议里另走
   `deficit_service_units` + `balance_negative`（见 XM-INV-NEGATIVE-DEFICIT）。
   所以欠费账号在这两格上本来就是 0，换算之后显示 `0.00`。这是**既有行为**，不是这次
   引入的；但换算之后它更像一个「确实是零」的断言了，如果产品认为欠费要看得出来，
   得让摘要接口把 deficit 也带上，属于协议层改动。
9. ~~**`discover.go` 的 `isStatusPassthrough` 有和第二轮 major 2 同款的洞**~~ ——
   **已完成（第三轮）**：判据抽进 `passthrough.go` 的 `isCopyOfAReadableValue()`，
   两道闸共用一份；`discover.go` 的 `types.Info` 补上 `Uses`，左值那一职拆成
   `isStatusField`。跨包 red 用例与 `item.EligibilityStatus` 对照用例都补齐，
   变异 R3-M25 / R3-M26 见变异章节。
10. **给 `USDExchangeRate` 补一道护栏**（对应 risks 3）：把它加进
    `contracts/newapi-source-projection-grants.postgresql.sql` 的 cfg 检查，
    和 `QuotaPerUnit` / `Price` 同样断言等于 1，值一变就拒绝出数。
    **这不是改一行 SQL**。cfg 检查读的那几个键同时是 `configuration_hash` 的输入：
    `quota_per_unit||'|'||price||'|'||topup_group_ratio_semantics||'|NEWAPI_QUOTA|rc.25|v4'`，
    这个式子在 `newapi-source-projection-grants.postgresql.sql` 里 1 处、
    `newapi-economic-projection-grants.postgresql.sql` 里 3 处（实测 `grep -c`），
    四处都得一起改，改完按主控者的说法要**重钉两处哈希、重装桥接**
    （那两处哈希在哪、怎么重装，属于发布流程，我没有核对）。
    所以**本轮不动桥接契约**，留给 XM-INV-ADMIN-CREDITS 切片一起做。
    在那之前，risks 3 就是开着的：核实过一次，没人盯着。

第六轮新增的一条：

11. **「管理端保留原始单位」没有测试盯着**（对应第六轮变异 M6-3）：删掉
    `AccountLedgerDetailDrawer` 里那两行 `<dd>` 输出，整套 386 条测试仍然全绿。
    根因是 `react-dom/server` 不跑 `useEffect`，抽屉渲染只到 loading 态，所以它
    取数之后的那一段从来没被覆盖过——这是上一轮就记在案的既有缺口，第六轮把它
    从「没测到」变成了「有明确要求却没测到」：现在用户端**必须**藏、管理端
    **必须**留，两边方向相反，只有一边有闸。三条路可选：
    (a) 把那一格抽成一个可导出的小组件（和 `ServiceUnitConversionHint` 同样的
    做法），直接渲染测；(b) 给前端引入 jsdom/RTL，让抽屉的 effect 真跑起来，
    收益不止这一处；(c) 接受缺口，靠人审。第六轮派工单要求管理端「不动」，
    所以没有自行选 (a)——这三条里选哪条由主控者定。
