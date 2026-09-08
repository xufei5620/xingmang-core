# XM-INV-LOT-REASON-CONTRACT: 资格枚举契约化 + 未知值降级 + 分请求独立加载

- **status:** implemented and self-tested locally. 前端 vitest 273 passed / 18
  files、`npm run typecheck`、`npm run build` 全绿；后端 `go build ./...`、
  `go vet` 干净，定向 `go test` 与相关包 `-short` 全绿。逐条变异验证已完成
  （22 条，见文末变异表），其中 2 条变异**没有变红**，暴露了两个恒真/覆盖不足
  的断言，已补测试后重跑变红——过程记在表里，没有事后抹平。
- **branch:** `ai/claude/XM-INV-LOT-REASON-CONTRACT`（起点 = RC105 生产提交
  `a265b90`），worktree `K:/发票/wt-XM-INV-FE-REASONS`。
- **commit:** 见本文件末尾「commit」一节（提交后回填）。

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

| 命令 | 结果 |
| --- | --- |
| `cd web && npm run test -- --run`（去代理变量） | 18 files / 273 tests passed |
| `cd web && npm run typecheck` | exit 0 |
| `cd web && npm run build` | exit 0（1588 modules） |
| `cd backend && go build ./...` | exit 0 |
| `cd backend && go vet ./internal/{eligibilitywire,httpapi,application}/...` | exit 0 |
| `go test -p 1 -count=1 -run 'TestUserFundingLot\|TestUserEligibilitySummary\|TestGeneratedTypeScript\|TestLotEligibility\|TestLotPersisted\|TestContractRejects\|TestUserInvoiceRequestDTO' ./internal/httpapi/... ./internal/application/... ./internal/eligibilitywire/...` | ok ×3 |
| `go test -p 1 -count=1 -short ./internal/{application,httpapi,eligibilitywire}/...` | ok ×3 |

**门禁顺序天然正确**：`verify.ps1` 的 go test 排在 npm test 之前，所以契约漂移会先在
Go 侧变红，前端根本走不到。

## not_run

- **全量 `go test ./...`**：按纪律只跑定向正则 + 相关包 `-short`。
- **DB 集成测试**（`*_integration_test.go`）：需要真实 Postgres；未连库（硬约束）。
  受影响最相关的是 `internal/application` 与 `internal/postgresstore` 的资格摘要集成
  测试——`ListUserEligibilitySummaries` 的抽取是纯移动，但**没有被集成测试实际覆盖过**，
  见 risks。
- **浏览器端到端**：仓库无 RTL / Playwright 用户流；「面板不再退回向导」是通过
  `applyUserDataResults` 这一真实代码路径断言的，不是渲染断言。
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
6. **`lot` 与 `summary` 的 status 列表在契约里是两个字段**，由
   `TestLotEligibilityStatusIsPersistedPlusSynthetic` 断言二者必须相等（它们是同一列）。
   若将来两者真的分化，要显式改这条断言而不是让它们悄悄漂开。
7. **`descriptions` 的行为变化**：`SOURCE_REFUND` / `LEDGER_SYNCING` / `LEDGER_FROZEN` /
   `SOURCE_NOT_READY` 的 lot 从前显示「钱包充值（按已消费现金开票）」，现在显示各自的
   不可开票原因。这是有意修正（一个退款冻结的 lot 自称「按已消费现金开票」是误导），
   但**是用户可见的措辞变化**，发版说明里要提。
8. **`web/src/lib/eligibility-wire.generated.ts` 必须保持 CRLF。** 仓库
   `core.autocrlf=true`（index 存 LF、工作树 CRLF），`.gitattributes` 没给 `*.ts` 定 eol。
   生成器显式写 CRLF；若有人「顺手」给 `.gitattributes` 加 `*.generated.ts eol=lf`，
   会让这一个文件与树里其余 `.ts` 不一致，且 golden 每次假红。`TestGeneratedTypeScriptUsesCRLF`
   是**独立于 golden 的第二条断言**，专门防「渲染器和文件同时退化成 LF 时 golden 仍绿」。

## commit

见分支 `ai/claude/XM-INV-LOT-REASON-CONTRACT` 的单次提交（提交哈希在本文件提交后由
`git log` 可查；本文件随该提交一并提交）。
