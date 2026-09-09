# XM-WORKBENCH-TRUNCATION：工作台取满上限时说出来

- **status:** implemented，未上线。**纯前端**，只改「我的待处理」这一块。
- **branch:** `ai/claude/XM-WORKBENCH-TRUNCATION`，**基于
  `ai/claude/XM-WORKBENCH-JOBS`**。合入顺序：JOBS 在前。
- **来源：** XM-WORKBENCH-JOBS 自己留的 follow_up。

## 为什么

「我的待处理」两条数据源都有取数上限，而界面**一个字都没说**：

- 已放弃的后台任务：`WORK_JOBS_LIMIT = 20`。
- 活跃告警：这条更隐蔽——query 原本**不传 limit**，吃后端的默认值
  （`httpapi` 的 `defaultAlertLimit = 200`）。那个数只写在服务端，前端看不见它，
  **也就根本无从判断这一屏是不是被截断了**。

后果不是"少看几条"：**一张"没有待处理事项"的清单如果其实被截断了，人会据此
收工。** 这比少一条信息严重得多。

## 改了什么

- `lib/workbench.ts`：两个上限成为导出常量（判断"截断了没有"要用同一个数，
  分两处写迟早分叉），新增纯函数 `truncationNote`。
- `pages/OverviewPage.tsx`：活跃告警 query **显式传 `limit`**（这是让判断成立
  的前提，不只是整洁）；列表上方按需渲染一行提示。

## 三处刻意的分寸

1. **只说"可能"。** 恰好等于上限时也可能就是恰好这么多。含糊不好，但假装看到
   的是全部更糟。
2. **只说当前这一格真的可能被截断的那一条。** 在「待审批」下面提"告警取了 200
   条"是噪声——那一格根本不显示告警。
3. **没截断就一个字不说**，而不是显示一句"没有截断"。

## 测试

- `lib/workbench.test.ts` 六条：没到上限时返回 null；告警取满 / 任务取满 /
  两边都取满各自说什么；按当前分类过滤（在 approvals 下返回 null）；
  **超过上限也算截断**（判据是 `>=` 不是 `===`）。
- `router.test.tsx` 两条：铺 20 条已放弃任务时页面出现提示且点名"只取了 20 条"；
  没取满时**不显示**提示。

**做过变异验证**：把 `>=` 改成 `>`（恰好取满不算截断），三条测试如期变红。

门禁：`pnpm -r typecheck` / `test`（1552 条全绿）/ `build`、
`scripts/check-governance.sh` 退出 0、`gitleaks protect --staged` 无泄漏。

## 顺带

划掉了 `XM-WORKBENCH-JOBS` 里两条已经做完的 follow_up：这一条，以及"剩下四类
的 `blockedBy` 还没复核"——那四条已逐条对着代码查过，**全部仍然属实**
（approvals / change-request / refund-reconcile 路由都不存在，凭据只有 rotate
Action、没有到期字段），无需改动。

## follow_ups

- 别的页面也有同样的隐患：任何"不传 limit、吃后端默认值"的列表都无法自证完整。
  这一片只修了工作台。值得对 `listXxx({ signal })` 这种不带 limit 的调用做一次
  全面清点。
