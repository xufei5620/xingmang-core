import { PageState } from "@xingmang/ui-admin";
import type { ReactNode } from "react";
import { InvoiceConsolePanel } from "./InvoiceConsolePanel";
import { NewApiFinanceOverview } from "./NewApiFinanceOverview";
import { Sub2ApiFinanceOverview } from "./Sub2ApiFinanceOverview";

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
      return pending(
        "充值订单",
        "订单号、用户、类型、支付方式、订单金额、手续费、净入账、支付流水。随支付 Connector 接入（M3）上线；平台侧只展示与发起 Action，不实现支付逻辑。",
      );
    case "refunds":
      return pending(
        "退款与冲正",
        "退款号、原订单、原金额、退款金额、原因、处理人。退款是写操作，必须走 Action 且 L2 以上需审批（Foundation-B），这一格上线之后也不会有「直接退款」的按钮。",
      );
    case "profit":
      return pending(
        "利润核算",
        "逐渠道的使用收入、上游成本、毛利与毛利率。数据来自 XM-0037 成本台账（finance.profit-daily 端点已有），接线随第 5 片「渠道管理 + 上游管理」一并做——那一片才会把渠道与上游账号对上号。",
      );
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
