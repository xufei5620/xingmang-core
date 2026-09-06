# XM-INV-LEDGER-RECHARGE-POLICY-START —— 「起点后充值」改按全局策略起点统计

- status: ready-for-review（未合入发布线）
- branch: `ai/claude/XM-INV-LEDGER-RECHARGE-POLICY-START`
- base: `a2c53f0`（RC102，已在生产）

## summary

产品负责人 2026-09-07 在生产账本上看到：用户 `ces584398948@qq.com`（Sub2API
1147）9 月 1 日下午充了两笔，账本却显示「起点后充值 ¥0.00 / 0 笔」。追问：
「不是所有的用户都是按照 2026 年 9 月 1 日 00:00 开始吗？」

**是的，开票起点确实是全局统一的**，而且这一条不靠代码自觉：迁移 0016 的
`enforce_funding_lot_invoice_policy` 在插入时就拒绝任何 `completed_at` 或
`eligibility_cutover_at` 早于策略起点的现金批次。八个账号无一例外。

出问题的是账本里**唯一**一列用了别的口径：`accountLedgerRechargesLateral` 额外
要求 `completed_at >= eas.cutover_at`，而 `cutover_at` 是**每个账号各自**「系统
第一次为它建立可信余额基线」的时刻。生产上这八个时刻横跨六天（8-31 16:00 到
9-06 11:29）。1147 的 cutover 落在 09-01 12:14Z，而那两笔已核验人民币现金完成于
09-01 09:03Z 与 09:07Z——**早于它自己的 cutover 三小时**，于是被这一列排除。

同一行的已消耗/可开票/已开票（`accountLedgerConsumedInvoiceableLateral`）**没有
任何日期过滤**，注释里写明了理由：库级约束已经保证了策略起点，再写一遍是冗余。
于是同一笔钱进得了可开票的分母，却进不了充值那一列。**1147 一旦开始消耗，那一行
就会显示「起点后充值 ¥0，可开票 ¥5」——凭空冒出来的可开票额。**

页面标题写的是「按用户账号聚合查看 **2026-09-01 起**充值、消耗与可开票金额」，
更让人以为口径是统一的。

## 改动

**去掉过滤，而不是把 `cutover_at` 换成策略起点。** 这样两个 LATERAL 成为字面上
同一个谓词；策略边界由库级约束对两者可见的每一行统一保证，再写一份就是第二个会
漂移的地方。标题那句「2026-09-01 起」从此对每个账号都成立。

| 文件 | 改了什么 |
|---|---|
| `postgresstore/accounts_ledger.go` | 汇总 LATERAL 与逐笔清单 `accountLedgerRecharges` 同时去掉 `completed_at>=cutover_at`；两者必须在同一个提交里改，否则汇总会算上清单拒绝显示的笔数 |
| 同上 | `AccountLedgerDetail` 新增 `CutoverAt`；原先它只是个局部变量，用来喂那条过滤，改完就没有出口了 |
| `httpapi/accounts_ledger.go` | 详情响应新增 `cutover_at` |
| 前端 `types.ts` / `http-api.ts` / `mock-api.ts` | 映射 `cutoverAt`；响应类型里设为**可选**，老后端没有它时按空串处理，不让整个详情拒绝解析 |
| 前端 `App.tsx` | 详情抽屉在「期初余额」旁新增「纳入采集时刻」；页面标题改成「2026-09-01 00:00（开票策略起点，对所有账号一致）之后的……」 |

`cutover_at` 本身值得显示——它回答另一个真问题「系统从哪天开始看这个账号」——
所以把它**显式报出来**，而不是让它继续在一列钱里隐身。

## files_changed

- `backend/internal/postgresstore/accounts_ledger.go`
- `backend/internal/postgresstore/accounts_ledger_recharge_window_test.go`（新增）
- `backend/internal/httpapi/accounts_ledger.go`
- `web/src/{types.ts,App.tsx}`、`web/src/lib/{http-api.ts,mock-api.ts}`
- `docs/handoffs/XM-INV-LEDGER-RECHARGE-POLICY-START.md`（本文）

## tests_run

- 新增 `TestRechargesCountLotsCompletedBeforeTheAccountCutover`：造一笔**完成于账号
  cutover 之前三小时**的已核验人民币现金批次（仍晚于全局策略起点），断言它同时
  出现在汇总（2 笔 / ¥1500）与逐笔清单里，且两者笔数一致、按完成时刻升序、
  详情报出 `cutover_at`。
- **做过变异验证**：把 `completed_at>=cutover_at` 加回汇总，两个子测试立刻变红，
  并精确复现生产上那个形态（`count=1 minor=110000`，期望 2 / 150000）。
- 夹具踩到的一处：批次的 `completed_at` **首次观测后不可改**（同一个 0016 触发器
  会拒绝 UPDATE），所以测试改为**直接以早于 cutover 的完成时刻插入**第二笔，
  而不是事后挪动第一笔。
- 既有账本测试（`TestAccountLedgerPageCoversEveryBlockStateAndFilters`、
  `TestAccountLedgerDetail` 等）全部照旧通过——**它们原本就没有覆盖这条过滤**，
  这也是它能一直错到生产的原因。
- 开票后端全量 `go test -p 1 -count=1 ./...`；前端 `tsc --noEmit`、
  `vitest run`（**203 条**）、`npm run build`。

## not_run

- 未部署。随下一版 RC 走。

## risks

- 这一列的数值会**变大**（对 cutover 晚于自身充值的账号而言），运营看历史截图会
  发现对不上。这正是修复的目的，但值得在发版材料里点名，免得被当成新缺陷。
  受影响的是生产上八个账号里 cutover 晚于其充值完成时刻的那些——至少 1147 一个。
- 纯读路径：这两处 LATERAL 只服务两个只读端点，不参与任何开票判定、金额计算或
  资金批次写入。可开票额的算法一个字节没动。

## follow_ups

- 账本页那句「2026-09-01」目前是**写死的文案**，而真正的策略起点在
  `invoice_eligibility_policy` 表里。两者现在恰好一致，但没有任何东西保证它们
  不漂移。应当让页面从 `eligibility_start_at` 取值显示（设置接口已经返回它）。
  未开工。
- NewAPI 那两个账号（48/72）资金批次为零，账本全零。与本片无关，另查。
