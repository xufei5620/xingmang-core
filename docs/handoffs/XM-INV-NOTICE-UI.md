# XM-INV-NOTICE-UI：申请详情里能看见「那条企业微信通知发出去了没有」

- **status:** implemented，未发布。**纯前端**，只读；不改后端、不改数据库。
- **branch:** `ai/claude/XM-INV-NOTICE-UI`，**基于
  `ai/claude/XM-INV-NOTICE-VIEW`**（它提供 `GET
  /api/v1/admin/invoice-requests/{id}/notices`）。合入顺序：NOTICE-VIEW 在前。
- **来源：** XM-INV-NOTICE-VIEW 自己留的 follow_up——「管理端前端还没有用这条
  路由」；平台侧消息目录里也写着「管理端界面还没有用上这条接口，今天要自己调」。

## 改了什么

管理端申请抽屉（`AdminDrawer`）新增一格「企业微信通知」，显示每条通知的
事件、状态、尝试次数、送达时刻或下次重试时刻、失败短码。

**不设状态闸，这是这一片最要紧的一个决定。** 上面那格「交付」只在申请
`issued` / `issued_awaiting_document` / `refund_attention` 时才加载——它答的是
"发票邮件寄了没有"，没开票就无从谈起。**通知不一样**：`request.submitted`
的那条在申请还是「待审核」时就存在了，而那正是最需要查"推出去了没有"的时候。
所以通知这一格对任何状态的申请都加载。

## 四处刻意的诚实

1. **未知事件与未知状态原样显示。** 后端将来加「审核通过」「开具完成」时，
   界面显示原始键仍然有用；藏起来不是。与卡片推送对未知交易类型的处理同一条。
2. **没有记录时说清这不是故障**：「未配置群机器人地址时不会入队」。通知功能
   默认是关的，空是正常状态。
3. **`delivered_at` 为空就不显示送达时刻**，改显示下次重试时刻。后端已经保证
   未送达时是 `null` 而不是零时刻，客户端把 `null` 与缺失一起归成 `undefined`。
4. **失败短码直接显示。** 它是"为什么没发出去"的唯一线索，而后端保证这一列
   不含 Webhook 地址与上游原文（投递侧四条失败路径都只回我方短码）。

## 文件

- 修改：`web/src/types.ts`（`InvoiceNoticeDelivery`）、
  `web/src/lib/api-contract.ts`（接口加一个方法）、
  `web/src/lib/http-api.ts`（真实实现）、`web/src/lib/mock-api.ts`（演示实现）、
  `web/src/App.tsx`（状态、加载 effect、区块、两个中文映射函数）
- 新增：`web/src/lib/http-api.notices.test.ts`

## 测试

`http-api.notices.test.ts` 五条：**打的是 admin 路径不是 user**（后端没有 user
变体）；`null` 与缺失都归成 `undefined`、失败短码与尝试次数带出来；
`items` 缺失或为 `null` 时返回空数组不抛错；未知状态与未知事件原样带出；
**尝试次数说不通（负数、小数）时整块停掉**，不显示一个编出来的数字
（与既有的邮件交付校验同一条纪律）。

**做过变异验证**：把请求路径改成 `/api/v1/user/...`，「打的是 admin 路径」
那条如期变红。

门禁：`tsc --noEmit` 干净、`vitest run` 全套 14 文件 191 条全绿、
`npm run build` 成功、`gitleaks protect --staged` 无泄漏。
后端未触及，未跑 Go 套件。

## follow_ups

- 只能按申请查，没有「最近失败的通知」全局列表。真要排查"最近有没有发失败
  的"今天还是要查库。加列表就要加分页、筛选与保留期，等有人真的需要再说。
- 抽屉里没有「重新投递」按钮。发件箱会自己按指数退避重试到第 8 次，人工重投
  是另一件事（要新的写接口与审计），且到上限的多半是地址填错或机器人被移出群
  ——那两种情况重投一万次也没用，该去改配置。
