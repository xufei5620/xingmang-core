# XM-INV-LOT-REASON-CONTRACT: 资格枚举契约化 + 未知值降级 + 分请求独立加载

- **status:** implemented and self-tested locally，**已过一轮复审并整改 6 条
  major**（见下面「复审整改（第二轮）」）。前端 vitest 295 passed / 18 files、
  `npm run typecheck`、`npm run build` 全绿；后端 `go build ./...`、`go vet`
  干净，定向 `go test` 与相关包 `-short` 全绿。变异验证两轮共 35 条，其中第一轮
  2 条、第二轮 1 条**没有变红**，都记在表里没有事后抹平——第二轮那条至今仍是绿的
  （App.tsx 那一行没有测试能碰到），已缩小到最小并写进 risks 12。
- **branch:** `ai/claude/XM-INV-LOT-REASON-CONTRACT`（起点 = RC105 生产提交
  `a265b90`），worktree `K:/发票/wt-XM-INV-FE-REASONS`。
- **commit:** 见本文件末尾「commit」一节（提交后回填）。

---

## 复审整改（第二轮）

复审提了 6 条 major。逐条的判断、改法与「旧实现下真的红过」的证据如下；变异编号见
文末第二张变异表。

### R1 —— 合成状态是手列的，而探针的输入空间取自它本该校验的字段（最重的一条）

`lot_synthetic_statuses`（`missing` / `source_unavailable`）当初是手写进契约的，而
`lotInputSpace(contract.LotEligibilityStatus)` 又从同一个契约字段取输入空间。两件事
合起来，闸对「再加第三个响应期状态」完全无感——**而 RC58 当年加 `source_unavailable`
就是这个形状**：application/service.go 里一行赋值，不动任何迁移。

改法：新增 `backend/internal/eligibilitywire/discover.go`，用 `go/ast` 扫
`application` / `postgresstore` / `httpapi` 三个包的非测试源码，收集两类**代码引入的**
状态字面量——(1) 对 `EligibilityStatus` 字段的字符串字面量赋值；(2) SQL 里
`COALESCE(x.eligibility_status,'…')` 的默认值——减去从迁移 CHECK 发现的持久化集，
就是合成集，与契约双向比对。探针的输入空间改成 `LotStatusInputSpace()`
= 持久化集 ∪ 发现到的合成集，**不再读契约**。

证据（MB8 / MB9）：在 service.go 的新鲜度循环里加一个与 `source_unavailable` 完全同形
的分支写入 `refund_review_probe`，整改后 **3 处同时变红**；随后单独把 httpapi 探针的
输入空间改回 `contract.LotEligibilityStatus`，同一个变异下该探针**立刻恢复全绿**——
输入空间这一改动是承重的，不是顺手整理。

### R2 —— 迁移闸的兜底判据是一个硬编码的约束名子串

原来是 `strings.Contains(text, "eligibility_status_check")`：后续迁移只要不写出这个
当前约束的名字，探针就继续拿 0020 的旧取值作答且永不变红。

改法：判据换成与被校验对象同源的形状判断。按**语句**（不是整文件）切分，凡排在已解析
迁移之后、语句里同时出现 `eligibility_status` 与约束词汇
（`CHECK|CONSTRAINT|REFERENCES|ENUM|CREATE TYPE|CREATE DOMAIN`）却匹配不上 CHECK 模式的，
一律**报错拒绝作答**，而不是给旧答案。同时把 CHECK 模式收紧成必须带 `CHECK (` 前缀，
免得把某个 `WHERE eligibility_status IN (...)` 误当成约束。

按语句切分是必须的：迁移 0026 在同一个文件里既给**别的表**加了 CHECK，又在一个触发器
函数体里读了 `eligibility_status`——整文件判据会把它误报成漂移。

证据（MB10 / MB11 / MB12）：查表外键替换 CHECK（全文不含旧约束名）→ 红；只 `DROP
CONSTRAINT` 不加新约束（最危险的放宽方向）→ 红；只做 UPDATE / CREATE INDEX、外加对
别的表的 CHECK 的良性迁移 → **保持绿**（证明不是「提到这个列就红」的过宽闸）。

### R3 —— 账号面板仍会把「读不到」显示成「你没有绑定」

拆开五路只解决了「被兄弟请求波及」。`getSourceAccounts()` **自己**失败时，首次加载
`sourceAccounts` 依旧是 `[]`，面板照旧渲染三步绑定向导，等于继续叫绑定正常的人去重新
绑定——事故报告里最伤人的那个症状。

改法：`plan.failed` 经 `setFailed` → `DataProvider` 的 `failedRequests` → context 暴露；
新增纯函数 `sourceAccountPanelMode({ accountCount, failed })` 返回
`accounts | unavailable | onboarding`；`SourceAccountStatus` 按它渲染，`unavailable`
时显示「已关联账号暂时无法读取」+ 重试按钮，**明说「不代表你的绑定已失效或被撤销，
也不需要重新绑定」**，并且不渲染向导。

注意该函数收的是整个 `failed` 列表而不是一个现成的布尔：调用它的组件没有任何测试
（仓库无 RTL），写在那一侧的每一行判断都是没人看得住的判断，所以
`failed.includes("sourceAccounts")` 必须落在被测这一侧。剩下的未覆盖面见 risks 12。

### R4 —— 五路全失败时横幅说「其余数据仍是最新的」

那是假话，而且当时根本没有「其余」；同时服务端自己那句可行动的中文（5xx 的
「开票服务暂时不可用，请稍后重试。」）在拆开五路时被整个丢掉了——改造前它就是横幅正文。

改法：按 `failed.length === userDataRequestKeys.length` 分岔，全失败时改说
「开票数据全部读取失败，请稍后重试。」；两种情况都把**可读的**拒绝原因追加在括号里。
「可读」由形状判定：含中文、单行、不超长——`fetch` 的 `Failed to fetch` 因此被丢弃，
负责人不读英文，让英文占住他唯一能读的那一行比不给原因更糟。

### R5 —— `summary_status` 是被定义的，而且定义的理由是错的

原来靠一句断言「summary 与 lot 是同一列，所以两个清单必须相等」。这句话与代码不符：
摘要走 `COALESCE(eas.eligibility_status,'syncing')`（**不是** lot 那条 `'missing'`），
且 `ListUserEligibilitySummaries` 直接透传、不做来源新鲜度覆盖，所以 `missing` 与
`source_unavailable` 在摘要侧**都不可达**。这道闸既发现不了摘要词汇表的真实漂移，
还会用一句错的事实把想改正的人挡回去。

改法：契约里 `summary_status` 收窄为 4 个持久化值；`TestSummaryStatusesAreDiscovered`
改成与发现结果双向比对（`SummaryStatusInputSpace()` = 持久化集 ∪ 摘要路径上发现的
COALESCE 默认值 ∪ 该路径上的响应期覆盖），并保留一条**有方向的**弱断言
`summary_status ⊆ lot_eligibility_status`。摘要探针的输入空间同样改成发现值。
另加 `TestSummaryStatusScanIsNotLookingAtNothing`：扫描按函数名取范围是不可避免的
手写部分，所以断言那两个函数名仍然存在——「扫到空」和「本来就没有」必须可区分。

顺带把 `http-api.ts` 里最后一处客户端硬拒配对（`status === "source_unavailable"` 却
没有 `SOURCE_NOT_READY` 就整条 throw）拉齐成降级。它今天不炸只是因为不可达，
「不可达」是关于后端的事实、不是这段代码的性质。

证据（MB13 / MB14 / MB15）：把摘要的 COALESCE 默认值改成 `'missing'` → 红；给
`ListUserEligibilitySummaries` 加一个 `source_unavailable` 覆盖 → 红；把契约改回 6 个值
（**旧断言会认为这是对的**）→ 红，报「declared but unreachable: [missing
source_unavailable]」。

### R6 —— 同一个函数里还留着一个未被检视的整请求崩点

`exactObjectKeys` 对摘要 DTO 用的是白名单**键集**：后端 DTO 新增一个字段，旧 bundle
就把整条 `getUserEligibilitySummary` 拒掉，红横幅、同一个成因、同一条请求。放宽了三处
枚举却让同一函数里按同样理由会炸的键校验一个字没动，等于关了门开着窗。

改法（比复审要求的「至少写下来」多做了一步）：把 `exactObjectKeys` 拆成
`requiredObjectKeys`（必填键，**仍然硬拒**）+ `unknownObjectKeys`（返回未知键），
**只对开票资格摘要及其嵌套的源服务单位**改成「忽略未知键 + 置 `eligibilityDegraded`」。
未知键不可能让某个显示出来的数字变错——每个字段都按名读取，金额不变量照跑；而拒绝它
就是原样复现本次事故。

其余 13 个 `exactObjectKeys` 调用点**有意保持严格**（管理员账本、支付候选、分页游标
等：其中几个是金额 DTO，多出一个键更该读成「这不是我以为的那条响应」，且都不在用户
开票页上）。这个取舍与它们各自的触发条件写在 risks 10，不再当成已经收口。

---

## summary

用户端开票中心对上游用户 34 / 12 显示「开票数据暂时无法读取 / 充值记录包含无效的
资金账本状态」，且「已关联账号」面板退回绑定向导。

根因不是一处，是**三处独立的手抄闭集**同时踩中同一个新值。
XM-INV-ELIG-AUTO-RECONCILE 给后端加了第 4 个持久化 `eligibility_status`
(`not_invoiceable_pending_reconciliation`，migration 0020 把 0009 的 3 值 CHECK
扩到 4 值) 与配套 lot `reason_code` (`LEDGER_PENDING_RECONCILIATION`)，前端三份
手抄清单都停在旧值：

1. `mapLot`（主崩点）—— status 白名单 + reason 白名单；
2. `mapEligibilitySummary`（**第二个独立崩点**）—— 摘要 status 走同一个常量，
   且 reason 清单从来就没有 `PENDING_RECONCILIATION`；
3. `mapEligibilityFreeze`（**第三个、静默**）—— 管理员冻结列表同样校验
   `eligibility_status`，今天不炸只是因为这两个账号恰好 0 个开放冻结。

再由 `Promise.all` 放大成整页不可用。

本切片做四件事：

1. **契约化**：`contracts/invoice-eligibility-wire.v1.json` 成为「后端会回哪些值」
   的唯一真相源，由 Go 探针跑**真实 emitter** 的笛卡尔积双向比对，并生成
   `web/src/lib/eligibility-wire.generated.ts` 供前端使用。前端不再手抄
   （原先同一事实在 `http-api.ts` 抄了 4 遍、`types.ts` 又抄了 2 遍）。
2. **运行时降级**：三处响应侧校验从「是已知清单成员」放宽为「是形状合法的枚举码」，
   未知值**可见、不可选、带中文兜底文案（含原值）**，并置 `eligibilityDegraded`。
3. **中文文案**：`LEDGER_PENDING_RECONCILIATION` / `not_invoiceable_pending_reconciliation`
   有正式中文；顺带补齐原先缺文案的 4 个 lot reason（原先 8 个里只有 3 个有）。
4. **分请求独立加载**：`Promise.all` → `Promise.allSettled` + 各自 set / 各自报错。

### 为什么两层都要（这是本切片最重要的判断）

生成契约**防不住**本次这类事故。代码生成防的是「源码漂移」；而本次的实际失败是
**部署时序偏斜**——AUTO-RECONCILE 的后端先上了线，浏览器里跑的还是旧 bundle。
哪怕契约、生成器、双向 golden 全部到位且 CI 全绿，只要后端容器先于前端滚动，旧解析器
仍然 throw、页面仍然整页崩。**只有降级能让「前端比后端旧」这个在任何非原子部署里
都必然周期性出现的窗口，不再等于「用户看不到数据」。**

反过来，纯降级会留下静默债：不崩了，但新状态永远显示成「账本状态待确认（XXX）」，
没人发现该补中文——生产上这两个账号从 9 月初持续至今，足以说明没人会主动去看。

所以：**降级层管运行时，生成层管提交时。不是冗余，删任何一层都会回到某一类事故。**
这一点在生成文件的头注释里也写了，避免下一个人以为有了契约就能把降级删掉。

### 降级没有降低资金安全性

放宽的只是「拒绝一个没见过的名字」的能力，这从来不是安全属性，只是「前端总会先部署」
的一个赌注。金额安全由两侧都成立的既有不变量兜住，且**本切片没有动它们**：

- lot：`availableMinor()` 的 `canInvoice` 要求字面量 `"active"`，所以任何未知状态
  都强制 `expectedAvailable = 0`，服务端若声称有可开金额仍抛
  `INCONSISTENT_ELIGIBILITY_RESPONSE`（对应 `domain/types.go:129`）。
- 摘要：`READY` 是唯一能伴随正金额的 reason，且必须单独出现在 `active` + `verified`
  行上；`!ready && availableMinor !== 0` 仍然抛。

变异 M3 专门证明了这条不是摆设（把 `canInvoice` 的 `=== "active"` 改成
`!== "frozen"` 后，「未知状态声称有可开金额」用例变红）。

---

## files_changed

### 新增

| 文件 | 作用 |
| --- | --- |
| `contracts/invoice-eligibility-wire.v1.json` | 四组枚举 + 状态/原因配对 + 摘要 reason 上限的唯一真相源 |
| `backend/internal/eligibilitywire/wire.go` | 契约加载、CRLF TypeScript 渲染、双向 Diff helper |
| `backend/internal/eligibilitywire/discover.go` | **（第二轮）** 发现层：go/ast 扫代码引入的状态字面量、从迁移解析持久化 CHECK（形状化的「解析不了就拒绝作答」）、给两个 emitter 探针供应输入空间 |
| `backend/internal/eligibilitywire/wire_test.go` | golden 比对（`-update` 重生成）、CRLF 断言、**从 migrations 发现**持久化状态集、契约自校验 |
| `backend/internal/httpapi/user_dto_reason_contract_test.go` | lot emitter 笛卡尔积探针（双向）+ npr 下 5 种 reason 可达性 + 非 active 必带 reason |
| `backend/internal/application/eligibility_summary_reason_contract_test.go` | 摘要 reason 笛卡尔积探针（双向）+ 上限发现 + npr≠frozen |
| `web/src/lib/eligibility-wire.generated.ts` | 由契约生成（CRLF），前端唯一枚举来源 |
| `web/src/lib/eligibility-labels.ts` | 资格状态/原因中文表 + 兜底访问器（从 App.tsx 抽出以便直接断言文案） |
| `web/src/lib/user-data-load.ts` | 五路加载的「谁失败不影响谁」决策，抽成纯函数 + 应用器 |
| `web/src/lib/http-api.eligibility-pending-reconciliation.test.ts` | 58 用例：三处降级、混合 reason、形状拒绝、逐值中文覆盖 |
| `web/src/lib/user-data-load.test.ts` | 12 用例：首次加载与两阶段刷新下的分请求隔离 |

### 修改

| 文件 | 改动 |
| --- | --- |
| `backend/internal/application/service.go` | 把摘要 reason 组装**原样**抽成纯函数 `userEligibilitySummaryReasons`（分支顺序不变；`seen!=5 \|\| !ready` → `!sourceReady` 是德摩根等价），使其可被无 DB 的探针调用 |
| `web/src/lib/http-api.ts` | 三处闭集→形状校验 + degraded 标记；4 处手抄联合→契约类型；reason 文案抽成 Record 表并补齐 8 条中文；摘要长度上限改用契约发现值 |
| `web/src/types.ts` | 引入 wire 类型（宽化）与 exact 类型（供 Record 穷尽性）；`FundingOrder`/`UserEligibilitySummary` 增加 `eligibilityDegraded` |
| `web/src/App.tsx` | `Promise.all`→`allSettled`+应用器；补 npr 徽章与 degraded 徽章、零额度小字；标签表迁出；4 处直接下标→兜底访问器 |
| `web/src/lib/invoice-contract.test.ts` | **推翻**原先钉住崩溃行为的断言（见下） |

第二轮另外修改：

| 文件 | 改动 |
| --- | --- |
| `backend/internal/eligibilitywire/wire_test.go` | 迁移解析移入 `discover.go`；新增 `TestLotSyntheticStatusesAreDiscovered`、`TestSummaryStatusesAreDiscovered`、`TestSummaryStatusScanIsNotLookingAtNothing`；删掉那条「summary 与 lot 必相等」的错误断言 |
| `backend/internal/httpapi/user_dto_reason_contract_test.go` | 输入空间 `contract.LotEligibilityStatus` → `eligibilitywire.LotStatusInputSpace()` |
| `backend/internal/application/eligibility_summary_reason_contract_test.go` | 输入空间 `contract.SummaryStatus` → `eligibilitywire.SummaryStatusInputSpace()` |
| `contracts/invoice-eligibility-wire.v1.json` | `summary_status` 6 → 4 值；补 `summary_status_description` 与合成集「是被发现的」的说明 |
| `web/src/lib/eligibility-wire.generated.ts` | 随契约重新生成（`summaryStatuses` 4 值，仍为 CRLF） |
| `web/src/lib/http-api.ts` | `exactObjectKeys` 拆成必填/未知两半；摘要与其嵌套源服务单位容忍未知键并置 degraded；摘要的 `source_unavailable ⇒ SOURCE_NOT_READY` 硬拒改降级 |
| `web/src/lib/user-data-load.ts` | 全失败/部分失败文案分岔 + 可读原因追加；`setFailed` 设置器；`sourceAccountPanelMode` |
| `web/src/App.tsx` | `failedRequests` 进 context；`SourceAccountStatus` 按面板模式渲染，新增「已关联账号暂时无法读取」态 |

### 被有意推翻的既有断言

`invoice-contract.test.ts` 原有：

```ts
expect(() => mapLot({ ...lot, reason_code: "UNKNOWN" })).toThrow(
  "充值记录包含无效的资金账本状态",
);
```

这条断言**正是在钉住把页面炸掉的行为**。它确实曾是有意为之，但该意图已被两次生产事故
证伪：一次是 migration 0016 加 `EVENT_DEAD`/`POLICY_ANCHOR_BLOCKED` 让冻结列表整页崩
（当时的处置就是引入 `freezeReasonPattern` 形状校验，见 `http-api.ts` 该常量上方注释与
`http-api.eligibility-freeze-reason-tolerance.test.ts`），一次是本次。本切片是把同一份
处方补给 lot 与 summary 两条路，不是发明第三套机制。该测试已改名并重写为：未知但形状
合法 ⇒ 降级渲染；形状非法 ⇒ 仍拒绝。

---

## tests_run

第二轮整改后重跑的结果：

| 命令 | 结果 |
| --- | --- |
| `cd web && npm run test -- --run`（去代理变量） | 18 files / **295** tests passed |
| `cd web && npm run typecheck` | exit 0 |
| `cd web && npm run build` | exit 0 |
| `cd backend && go build ./...` | exit 0 |
| `cd backend && go vet ./internal/{eligibilitywire,httpapi,application,postgresstore}/...` | exit 0 |
| `go test -p 1 -count=1 -run 'TestUserFundingLot\|TestUserEligibilitySummary\|TestGeneratedTypeScript\|TestLotEligibility\|TestLotPersisted\|TestLotSynthetic\|TestSummaryStatus\|TestContractRejects' ./internal/httpapi/... ./internal/application/... ./internal/eligibilitywire/...` | ok ×3 |
| `go test -p 1 -count=1 -short ./internal/{application,httpapi,eligibilitywire}/...` | ok ×3 |

**门禁顺序天然正确**：`verify.ps1` 的 go test 排在 npm test 之前，所以契约漂移会先在
Go 侧变红，前端根本走不到。

## not_run

- **全量 `go test ./...`**：按纪律只跑定向正则 + 相关包 `-short`。
- **DB 集成测试**（`*_integration_test.go`）：需要真实 Postgres；未连库（硬约束）。
  受影响最相关的是 `internal/application` 与 `internal/postgresstore` 的资格摘要集成
  测试——`ListUserEligibilitySummaries` 的抽取是纯移动，但**没有被集成测试实际覆盖过**，
  见 risks。
- **浏览器端到端 / 组件渲染**：仓库无 RTL / jsdom / Playwright 用户流。
  「面板不再退回向导」是通过 `applyUserDataResults` + `sourceAccountPanelMode` 这两条
  真实代码路径断言的，**不是渲染断言**——`SourceAccountStatus` 里把 context 的
  `failedRequests` 传进去那一行，以及「unavailable 态不渲染三步向导」这件事本身，
  只有 `typecheck` 看着。见 risks 12。
- **迁移未在真实 Postgres 上跑过**（未连库）。第二轮的迁移解析闸是纯文本分析，
  变异用的临时迁移 `0099_*.sql` 已删除，`git status` 干净。
- **未 ssh、未连生产库、未查看任何密钥文件、未推 GitHub。**

---

## 变异表（逐条：变异 → 结果 → 还原 → 复跑）

全部变异均已还原，`git status` 与 `grep TESTONLY` 确认无残留，还原后全量门禁复跑通过。

### 前端

| # | 变异 | 预期 | 实际 |
| --- | --- | --- | --- |
| M1 | `mapLot` status 恢复闭集（**只补 reason_code 的半吊子修法**） | 红 | **8 红**：主用例 + 4 条混合 reason + 不可选 + 2 条未知值 |
| M2 | `mapLot` reason_code 恢复闭集（只补 status） | 红 | **5 红**（含 `invoice-contract.test.ts`） |
| M3 | `canInvoice` 的 `=== "active"` 改成 `!== "frozen"`（削弱金额不变量） | 红 | **6 红**，含「未知状态声称有可开金额仍须拒绝」 |
| M4 | `degraded` 只保留配对违例（未知值不再标记） | 红 | 2 红 |
| M5 | 形状正则放成 `/^.*$/` | 红 | **12 红**（全部形状非法用例） |
| M6 | `mapEligibilitySummary` status 恢复闭集 | 红 | 4 红 |
| M7 | 摘要 reason 长度上限改成 10 | 红 | 1 红 |
| M8 | `mapEligibilityFreeze` status 恢复闭集 | 红 | 1 红 |
| M9 | `PENDING_RECONCILIATION` 文案改成「资金资格已安全冻结」 | 红 | 1 红（文案逐字断言） |
| M10 | 删掉 `LEDGER_SYNCING` 的中文 | 红 | **两层都红**：`typecheck` TS2741 + 逐值探针 |
| M11 | 应用器改成 `setSourceAccounts(plan.sourceAccounts ?? [])` | 红 | **绿（未发现）** → 见下 |
| M12 | `planUserDataLoad` 任一失败即全部 withhold（`Promise.all` 语义） | 红 | **仅 1 红，两阶段用例未红** → 见下 |
| M13 | 摘要失败时合计回落为 0 | 红 | 1 红 |

### 后端

| # | 变异 | 预期 | 实际 |
| --- | --- | --- | --- |
| MB1 | `user_dto.go` 插入新分支 `LEDGER_TESTONLY` | 红 | 红，且**双向同时报**：多出 `LEDGER_TESTONLY`、`LEDGER_SYNCING` 变不可达 |
| MB2 | 契约删掉 `LEDGER_PENDING_RECONCILIATION` | 红 | 红 ×2：emitter 探针 + golden 陈旧 |
| MB3 | 契约 `lot_persisted_statuses` 删掉 npr | 红 | 红 ×2：与 0020 的 CHECK 不符 + persisted∪synthetic 不符 |
| MB4 | 新增 `0099` 迁移，用 `= ANY(ARRAY[...])` 改写该 CHECK（正则解析不到） | **必须报错而不是静默用旧值** | 红，且报「migrations [0099…] 比 0020 新且形状无法解析，请更新 checkPattern」 |
| MB5 | 摘要组装插入新 reason 分支 | 红 | 红 |
| MB6 | 契约 `summary_reason_max_count` 改成 7 | 红 | 红（实际最大 5） |
| MB7 | 生成器写 `\n` 而非 `\r\n` | 红 | 红 ×2：golden + 专设的 CRLF 断言 |

### 两条没变红的变异（重要，未事后抹平）

**M11（应用器把「自己那一路失败」也清空）没红**，因为我原先所有用例里
`sourceAccounts` 那一路都是成功的——**测试根本没覆盖「账号请求自己失败」**，而这恰恰是
最直接复现向导回退的场景。补 `keeps the previously loaded accounts when the accounts
request itself fails` 后重跑 → 变红 → 还原 → 绿。

**M12（恢复 `Promise.all` 全有全无语义）只红了 1 条**，两阶段用例全绿。原因：一旦第一阶段
已把面板填好，「withhold 新值」和「保留旧值」在观测上**不可区分**（旧值本来就是对的）。
真实事故发生在**首次加载**——没有好的旧值可退，`sourceAccounts` 停在初始 `[]` 才渲染成
向导。补两条首次加载用例（断言「另一路失败时其余面板仍被填充」，这是正向断言，旧实现下
必然为空）后重跑 → 3 红 → 还原 → 绿。

> 侦察简报提醒过「断言失败后 `sourceAccounts` 为 `[]`」是恒真的（初始值也是 `[]`）。
> 我据此写了两阶段用例，但两阶段用例挡得住 M11 类（显式清空）、挡不住 M12 类（整体
> withhold）。**两种形状都需要**：首次加载区分 withhold，两阶段区分清空。

---

## 变异表（第二轮：复审整改）

全部变异均已还原。还原后 `git diff` 与整改完成时的快照**逐字节比对通过**（唯一差异是
后来有意做的 `sourceAccountPanelMode` 签名重构），全量门禁复跑通过。

### 后端

| # | 变异 | 预期 | 实际 |
| --- | --- | --- | --- |
| MB8 | service.go 新鲜度循环里加第三个响应期状态 `refund_review_probe`（与 `source_unavailable` 完全同形） | 红 | **3 红**：合成集发现、persisted∪synthetic、lot emitter 探针。**这正是复审亲手跑过、整改前全绿的那条** |
| MB9 | 在 MB8 仍生效的前提下，把 httpapi 探针输入空间改回 `contract.LotEligibilityStatus` | 该探针恢复绿（证明输入空间是承重的） | **绿**——同一个新状态，探针不再报。输入空间取自被校验字段 = 自己同意自己 |
| MB10 | 新增迁移 0099：查表 + 外键替换 CHECK，全文不含 `eligibility_status_check` | **必须拒绝作答而不是给旧答案** | 红 ×6（含依赖它的 httpapi 探针），报「比 0020 新且形状无法解析」 |
| MB11 | 新增迁移 0099：只 `DROP CONSTRAINT`，不加新约束（最危险的放宽方向） | 红 | 红 |
| MB12 | 新增迁移 0099（良性）：`UPDATE` + `CREATE INDEX` 用到该列，另对**别的表**加 CHECK（0026 已有的形状） | **绿**（不许假红） | 绿 |
| MB13 | 摘要查询 `COALESCE(eas.eligibility_status,'syncing')` → `'missing'` | 红 | 红，报「reachable but undeclared: [missing]」 |
| MB14 | 给 `ListUserEligibilitySummaries` 加一个 `source_unavailable` 响应期覆盖 | 红 | 红，报「reachable but undeclared: [source_unavailable]」 |
| MB15 | 契约 `summary_status` 改回 6 值（**旧断言认为这才是对的**） | 红 | 红 ×2：发现式比对 + golden 陈旧 |

### 前端

| # | 变异 | 预期 | 实际 |
| --- | --- | --- | --- |
| MF14 | `sourceAccountPanelMode` 删掉 `unavailable` 分支（读不到 ⇒ 仍显示向导） | 红 | 1 红（首次加载 + 账号自身失败） |
| MF15 | `applyUserDataPlan` 里 `setFailed(plan.failed)` → `setFailed([])` | 红 | 3 红 |
| MF16 | 删掉「全失败」文案分岔（全失败也说「其余数据仍是最新的」） | 红 | 2 红 |
| MF17 | `readableReason` 去掉中文形状判定（任何 message 都往外抛） | 红 | 5 红（含 `Failed to fetch` 被渲染进横幅） |
| MF18 | 摘要键集改回 `exactObjectKeys`（严格） | 红 | 2 红 |
| MF19 | 嵌套源服务单位不传 unknown sink（只放宽外层） | 红 | 1 红 |
| MF20 | `requiredObjectKeys` 去掉必填键校验 | 红 | **仅 1 红** → 见下 |
| MF21 | 摘要 `source_unavailable ⇒ SOURCE_NOT_READY` 改回硬 throw | 红 | 1 红 |
| MF22 | `mapLot` 状态改回闭集（回归复核第一轮的 M1） | 红 | 2 红 |
| MF23 | `mapEligibilitySummary` 状态改回闭集（回归复核第一轮的 M6） | 红 | 3 红 |
| MF24 | App.tsx `failed: failedRequests` → `failed: []` | 红 | **绿（未发现）** → 见下 |

### 两条值得单独说的

**MF20 只红了 1 条。** 去掉必填键校验后，只有我这一轮新写的
「still rejects a summary that is missing a field this client reads」变红——也就是说
`exactObjectKeys` 的**必填键那一半，在其余 13 个调用点没有任何测试覆盖**。这是既有欠账，
不是本轮引入的，但既然量到了就记在 risks 11，不当没看见。

**MF24 至今仍是绿的（唯一没变红的变异）。** 仓库没有 RTL / jsdom 渲染测试，
`SourceAccountStatus` 里那一行 `failed: failedRequests` 没有任何测试能碰到，`typecheck`
也不报（它是个合法的空数组）。我没有装 RTL（离线环境加依赖是另一件事），而是把判断
本身搬进被测函数：`sourceAccountPanelMode` 收整个 `failed` 列表而不是一个现成布尔，
于是 `includes("sourceAccounts")` 这条判断被 MF14 覆盖住，未覆盖面缩到「有没有把
context 里的列表传进去」这一处。**这一处仍然是裸的**，见 risks 12。

---

## risks / follow_ups

1. **前端修好 ≠ 账本修好。** 上游 12 / 34 仍处于
   `not_invoiceable_pending_reconciliation`、仍不可开票。本切片只让页面正常渲染并把
   原因说清楚（且明说「完成后自动恢复」，避免用户误以为被冻结而提工单）。账本本身
   （finalized_through 仍在推进、34 有投影任务在跑、历史上曾进入自我维持热重试循环）
   是**另一条独立的线**，别把「页面不崩了」当成「问题解决了」。
2. **`ListUserEligibilitySummaries` 的抽取没有集成测试实际跑过**（无 DB）。改动是纯移动、
   分支顺序未变、`!sourceReady` 与原 `seen!=5 || !ready` 德摩根等价，新增的笛卡尔积单测
   覆盖了 4×6×2×2×2×2 = 576 种输入；但合入前建议在有 DB 的环境跑一次
   `./internal/application/... ./internal/postgresstore/...` 的集成测试确认。
3. **`user_dto.go` 的 if/else 链本次一个字没动**（有意）。`source_unavailable` 压过一切
   （RC58）、`NO_POST_START_CONSUMPTION` 压过 `LEDGER_SYNCING` 这些顺序都有生产事故背书；
   把它重构成 table-driven 会改变优先级，是**独立的一刀**，不能和本次修复混在一个提交里。
4. **`source_unavailable ⇒ SOURCE_NOT_READY` 从前端硬 throw 改成降级。** 该配对仍然是要求，
   红应该红在 Go 侧（`TestUserFundingLotSourceUnavailableAlwaysMapsToSourceNotReady`
   已钉住，本次未动）。若将来后端再次违反，用户会看到「账本状态待确认」徽章而非白页——
   这是有意的取舍：RC58 证明了在客户端硬拒会把后端排序 bug 放大成整页事故。
5. **`lot_status_reason_pairs` 目前是「发现后写死在契约里」**，由探针双向比对维持。它比
   reason 全集更细，因此也更容易在后端合理改动时变红。这是有意的（它是「只补 reason 白名单
   救不了页面」的机器化证据），但如果将来觉得噪音过大，正确做法是删掉这一组、保留 reason
   全集比对，**不是**把比对改成单向。
6. **`lot` 与 `summary` 的 status 列表在契约里是两个字段，而且它们本来就不相等。**
   ~~由 `TestLotEligibilityStatusIsPersistedPlusSynthetic` 断言二者必须相等（它们是同一
   列）。~~ **第二轮更正**：这句话是错的，那条断言已删除。摘要走
   `COALESCE(eas.eligibility_status,'syncing')`、且 `ListUserEligibilitySummaries`
   直接透传不做新鲜度覆盖，所以摘要只可能是 4 个持久化值，`missing` 与
   `source_unavailable` 都不可达。现在由 `TestSummaryStatusesAreDiscovered` 与发现结果
   双向比对，另加一条有方向的 `summary_status ⊆ lot_eligibility_status`。
7. **`descriptions` 的行为变化**：`SOURCE_REFUND` / `LEDGER_SYNCING` / `LEDGER_FROZEN` /
   `SOURCE_NOT_READY` 的 lot 从前显示「钱包充值（按已消费现金开票）」，现在显示各自的
   不可开票原因。这是有意修正（一个退款冻结的 lot 自称「按已消费现金开票」是误导），
   但**是用户可见的措辞变化**，发版说明里要提。
8. **`web/src/lib/eligibility-wire.generated.ts` 必须保持 CRLF。** 仓库
   `core.autocrlf=true`（index 存 LF、工作树 CRLF），`.gitattributes` 没给 `*.ts` 定 eol。
   生成器显式写 CRLF；若有人「顺手」给 `.gitattributes` 加 `*.generated.ts eol=lf`，
   会让这一个文件与树里其余 `.ts` 不一致，且 golden 每次假红。`TestGeneratedTypeScriptUsesCRLF`
   是**独立于 golden 的第二条断言**，专门防「渲染器和文件同时退化成 LF 时 golden 仍绿」。

### 第二轮新增

9. **发现层扫的是三个包，扫到的每一个非持久化字面量都会被要求解释。** 如果有人在
   `application` / `postgresstore` / `httpapi` 里写了一个 `EligibilityStatus = "..."`
   或一条新的 `COALESCE(x.eligibility_status,'...')`，而那个值并不真的会出现在响应里
   （比如只是内部中间态），`TestLotSyntheticStatusesAreDiscovered` 会红。**正确的处置是
   在契约里承认它、或者把那个字面量挪出扫描范围，不是把它加进某个豁免名单**——扫描范围
   一旦有豁免，就又变回手列的了。失败信息里带 `文件:行号`，不用 grep。
10. **`exactObjectKeys` 的其余 13 个调用点仍然是严格键集，这是有意的，但它们有同一个
    失败模式。** 触发条件：后端给对应 DTO 新增一个字段，且后端先于前端 bundle 上线 →
    那一条响应被整条拒掉（`INVALID_ELIGIBILITY_RESPONSE`，文案「服务返回的 X 字段超出
    安全白名单。」）。受影响的是管理员账本列表/详情、账本充值明细、消耗时间线、分页游标、
    资格冻结记录等。保留严格的理由：其中几个是金额 DTO，多出一个未知键更该读成「这不是
    我以为的那条响应」；而且都不在用户开票页上，炸的是管理端一个列表而不是用户的整页。
    **这不是「已经收口」，是一个写下来的取舍**：下一个给这些 DTO 加字段的人应当知道自己
    会复现同一个横幅，处方现成（照 `mapEligibilitySummary` 改成忽略未知键 + degraded）。
11. **`exactObjectKeys` 的必填键校验在那 13 个调用点没有测试覆盖**（变异 MF20 只红了本轮
    新写的那一条）。既有欠账，本轮没有扩大也没有修；补的话是独立一刀。
12. **`SourceAccountStatus` 里 `failed: failedRequests` 这一行没有任何测试覆盖**
    （变异 MF24 至今是绿的）。仓库没有 RTL / jsdom，`typecheck` 也接受一个空数组。
    判断逻辑本身已经搬进被测的 `sourceAccountPanelMode`，裸露的只剩「有没有把 context
    里的列表传进去」。**要真正闭合，需要引入一次渲染测试**（RTL + jsdom，或 Playwright
    一条用户流），那是一个需要加依赖的独立决定，不该塞进本次修复。在那之前，改动
    `SourceAccountStatus` 的人请自己确认这一行还在。
13. **摘要键集放宽后，「未知键」不再是拒绝理由，但仍会置 `eligibilityDegraded`。** 也就是
    说后端加字段会让用户看到降级徽章而不是红横幅。这是有意的（可见、不可选、不崩），
    但意味着**降级徽章会因为一次纯粹的部署时序而出现**，运营看到时不要当成账本异常。

## commit

- 第一轮实现：`a3d7a84c36805820448fed1b2c42a0a1bca045c2`（父提交 `a265b90` =
  RC105 生产提交）。16 files changed, 2454 insertions(+), 176 deletions(-)。
- 第一轮哈希回填：`3db69940958bc55e86428012a2af4d91608fda23`（`docs`）。
- **第二轮复审整改：`d558fe0`**（父提交 `3db6994`）。12 files changed,
  1410 insertions(+), 185 deletions(-)。按要求是**追加提交**，没有 amend / rebase。
  本文件的哈希回填是紧随其后的 `docs` 提交，因为哈希在提交前不存在。

分支 `ai/claude/XM-INV-LOT-REASON-CONTRACT`，worktree `K:/发票/wt-XM-INV-FE-REASONS`。
