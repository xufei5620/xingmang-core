import { PageState } from "@xingmang/ui-admin";
import type { ReactNode } from "react";
import { ChannelProfitView } from "./ChannelProfitView";
import { InvoiceConsolePanel } from "./InvoiceConsolePanel";
import { NewApiFinanceOverview } from "./NewApiFinanceOverview";
import { Sub2ApiFinanceOverview } from "./Sub2ApiFinanceOverview";
import { Sub2ApiOrdersPanel } from "./Sub2ApiOrdersPanel";
import { Sub2ApiRefundsPanel } from "./Sub2ApiRefundsPanel";

/** 支付与财务(原型 `V["s2/finance"]` / `V["newapi/finance"]`)。
 *
 *  子页签条按 IA v3 定稿（Sub2API 5 格 / NewAPI 3 格，NewAPI 的「开票」由
 *  CR-0005 补上），每一格给出它将来放什么、现在被什么挡着。有真实数据可接
 *  的格先接；「开票」两边都已接（CR-0005 第一阶段：嵌入开票系统管理端）。
 *
 *  §9.8 有一条硬口径必须在界面上说出来:
 *
 *	「用户充值」不是当期收入；用户发生使用消费时才确认使用收入。
 *
 *  这句话不是注解，是这一页最容易被读错的地方——把充值当收入，整条利润线
 *  从第一步就错了。所以它印在「资金概览」上，而不是只写在文档里。 */

/** 一格纯占位。写清楚放什么、被什么挡着——「敬请期待」什么也没说明。 */
function pending(title: string, description: string): ReactNode {
  return <PageState kind="unavailable" title={title} description={description} />;
}

/** Sub2API 的 5 个子页签（IA v3 §2.2 逐字）。 */
function sub2apiFinanceSubTab(subId: string): ReactNode | undefined {
  switch (subId) {
    case "overview":
      return <Sub2ApiFinanceOverview />;
    case "orders":
      return <Sub2ApiOrdersPanel />;
    case "refunds":
      return <Sub2ApiRefundsPanel />;
    case "profit":
      // XM-SUB2API-PROFIT：这一格曾经等的是「第 5 片把渠道与上游账号对上号」。
      // 那件事已经由 finance/channels/summary 做完了——端点无条件挂载，一次
      // 返回两个平台的行（`system_type`），NewAPI 侧同名子页早就在读它。
      // 所以这里不是新做一张表，是把已经验证可用的那张按平台参数化。
      //
      // **供数不走 `finance/profit-daily`**：那条是逐行台账
      //（upstream_account × business_day × token_id），比这一页需要的粒度细
      // 一档，且没有任何前端在读它。见 docs/handoffs/slices/XM-SUB2API-PROFIT.md。
      return <ChannelProfitView platform="sub2api" />;
    case "invoices":
      // CR-0005 第一阶段：这一格的阻塞点曾经是**契约**（同库还是同步，要等
      // CR-0002 冻结），产品负责人 2026-09-02 指令改走嵌入式迁入——不等
      // CR-0002，直接嵌入开票系统自己的管理端（鉴权、审批、双人复核、审计
      // 全部仍由它执行，平台不新增任何数据通道，也不展示任何开票数字）
      return <InvoiceConsolePanel mode="sub2api" />;
    default:
      return undefined;
  }
}

/** NewAPI 的 3 个子页签。第 3 格「开票」是 CR-0005 补的
 *  （2026-09-02 产品负责人指令推翻 ADMIN-IA §8.2 #2「不补开票」的裁定），
 *  内容是与 Sub2API 同一种嵌入，只是按 newapi 过滤。 */
function newapiFinanceSubTab(subId: string): ReactNode | undefined {
  switch (subId) {
    case "orders":
      return <NewApiFinanceOverview subId="orders" />;
    case "profit":
      return <NewApiFinanceOverview subId="profit" />;
    case "invoices":
      return <InvoiceConsolePanel mode="newapi" />;
    default:
      return undefined;
  }
}

/** 按平台与子页签 id 取内容。认不出的返回 undefined，由调用方回落到通用占位。 */
export function financeSubTab(
  serviceType: string,
  subId: string,
): ReactNode | undefined {
  if (serviceType === "newapi") return newapiFinanceSubTab(subId);
  return sub2apiFinanceSubTab(subId);
}
