# XM-FINANCE-GLOBAL0：跨平台财务页建成（/finance 从占位毕业）

- **status:** implemented，未上线。纯前端，无后端改动、无新端点、无新 scope。
- **branch:** `ai/claude/XM-0030a-approval-core`
- **来源：** 产品负责人 2026-09-07 指示「管理后端未建的需要全部建立，没完成的功能都完成」。
- **同批：** XM-CHANGES0、XM-DESIGN0（治理段最后三页一起毕业）。

## 改了什么

**`web/apps/admin-web/src/pages/FinancePage.tsx`（新增）** —— 六格页面本体。
**`web/apps/admin-web/src/lib/financeGlobal.ts`（新增）** —— 纯函数：跨平台汇总、
币种一致性判定、可用天数档位排序。与渲染分开，便于单测。

**六格今天各是什么：**

| 子页签 | 状态 | 说明 |
|---|---|---|
| 财务总览 | **真实数据** | 三条端点：`/finance/channels/summary`、`/finance/upstreams/summary`、`/finance/upstream-accounts` |
| 支付通道 | 蓝图态 | 没有支付 Connector，一条端点都没有 |
| 财务对账 | 蓝图态 | 没有对账域 |
| 异常与冻结 | 蓝图态 | 两条阻塞：没有来源，且执行入口未解锁 |
| 开票集成 | **真实数据** | 嵌 `InvoiceConsolePanel`（global 模式），由开票系统自己的控制台承载 |
| 财务配置 | **已冻结的决定** | 逐字给出 ADR-006 的核心规则与四个金额键；不是查询结果 |

## 三个刻意的取舍

1. **跨币种 fail closed。** 两平台币种不一致时**不相加**，整格显示「—」并写明
   「币种不一致，合计给不出」。隐式换算会造出一个没人能复核的数。
2. **一个平台缺当日汇总时，整格给不出。** 不显示另一个平台的数——那会被读成
   全平台合计，比空着更糟。
3. **退款/冻结只取 Sub2API，NewAPI 记为「不适用」而不是缺口。** 并标注冻结不在
   这个数里。「不适用」与「还没接」是两件事，混在一起会让人去催一个不存在的接线。

## 共享文件（由主线统一改，不在本片的三个新文件里）

`ui-admin/navigation.ts`（`built: true`）、`admin-web/router.tsx`（路由）、
`PlaceholderPage.tsx`（**删掉 `governanceSubTabOverride`**——`/finance` 建成后
那一支再也走不到，且与 FinancePage 里的实现逐字重复）。

## tests_run

- `pnpm --config.verify-deps-before-run=false -r run typecheck` ✅
- `pnpm --config.verify-deps-before-run=false -r run test` ✅ 前端 2063 条全绿
- `pnpm --config.verify-deps-before-run=false -r run build` ✅
- `bash scripts/check-governance.sh` ✅
- `gitleaks protect --staged` ✅
- **变异验证**：把 `/finance` 退回 `built: false`，`navigation.test.ts` 三条断言
  同时变红（确认不是恒真）。

## not_run

后端 `go test`：本片没有 Go 改动。

## risks

- 「财务配置」一格把 ADR-006 的四个金额键**抄进了前端**。ADR 改了这里不会自动跟——
  已在页面上写明这是冻结的决定而非查询结果，但仍是一份副本。
  值得后续做成从后端读，登记为 follow_up。

## follow_ups

- **XM-FINANCE-CONFIG-SOURCE**：财务配置四个金额键改为从后端读，消掉前端副本。
- 支付 Connector（M3）落地后回来接「支付通道」「异常与冻结」两格。
- 对账域落地后接「财务对账」。
