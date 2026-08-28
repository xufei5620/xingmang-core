import { useQuery } from "@tanstack/react-query";
import { PageState } from "@xingmang/ui-admin";
import type { ReactNode } from "react";
import { listMetrics } from "../api/platform";
import { platformOfMetricKey } from "../lib/platforms";
import { ApiStateView } from "./ApiStateView";
import { MetricCardGrid } from "./MetricCardGrid";
import { Sub2ApiFinanceOverview } from "./Sub2ApiFinanceOverview";

/** 支付与财务(原型 `V["s2/finance"]` / `V["newapi/finance"]`)。
 *
 *  本片只建**框架**：子页签条按 IA v3 定稿（Sub2API 5 格 / NewAPI 2 格）,
 *  每一格给出它将来放什么、现在被什么挡着。有真实数据可接的格先接。
 *
 *  §9.8 有一条硬口径必须在界面上说出来:
 *
 *	「用户充值」不是当期收入；用户发生使用消费时才确认使用收入。
 *
 *  这句话不是注解，是这一页最容易被读错的地方——把充值当收入，整条利润线
 *  从第一步就错了。所以它印在「资金概览」上，而不是只写在文档里。 */

/** 资金概览：能接的先接——本平台的收入类指标卡是真实的。 */
function RevenueRuleNote() {
  return (
    <p
      role="status"
      className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
    >
      「用户充值」不是当期收入：用户发生<strong>使用消费</strong>时才确认使用收入。
      这两个数在这一页上永远分开列，不相加。
    </p>
  );
}

function FinanceOverview({ serviceType }: { serviceType: string }) {
  const query = useQuery({
    queryKey: ["metrics"],
    queryFn: ({ signal }) => listMetrics({ signal }),
  });
  const items = (query.data ?? []).filter(
    (metric) =>
      platformOfMetricKey(metric.metric_key) === serviceType &&
      /revenue|cost|balance|recharge/.test(metric.metric_key),
  );

  return (
    <div className="flex flex-col gap-3">
      <RevenueRuleNote />
      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        <MetricCardGrid
          items={items}
          emptyTitle="暂无本平台的资金类指标"
          emptyDescription={`该环境下还没有 ${serviceType}.* 的收入/成本/余额观测；采集任务跑起来后会出现在这里`}
        />
      </ApiStateView>
      <PageState
        kind="unavailable"
        compact
        title="区间成功到账 / 待处理 / 失败 / 退款与冲正"
        description="这四格要逐笔订单才算得出来，随支付 Connector 接入（M3）上线。现在显示的是已有的指标卡，不是订单汇总。"
      />
    </div>
  );
}

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
      return (
        <div className="flex flex-col gap-3">
          <PageState
            kind="unavailable"
            title="开票"
            description="申请单号、用户、开票金额、类型、抬头、状态，以及开票详情页。"
          />
          {/* 这一格的阻塞点是**契约**，不是工期，必须说清楚 */}
          <p className="rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted">
            阻塞点是契约而不是工期：开票线（Codex）的独立系统与本页签是同库还是同步,
            要等 <strong>CR-0002</strong> 冻结时一并定（ADMIN-IA §8.3）。
            平台侧只展示与发起 Action,<strong>不得实现开票资格算法</strong>（ADR-006）。
          </p>
        </div>
      );
    default:
      return undefined;
  }
}

/** NewAPI 的 2 个子页签。**刻意不补齐成 5 格**——裁定 #2 维持原型。 */
function newapiFinanceSubTab(subId: string, serviceType: string): ReactNode | undefined {
  switch (subId) {
    case "orders":
      return <FinanceOverview serviceType={serviceType} />;
    case "profit":
      return pending(
        "利润核算",
        "逐渠道的我方计费、上游成本、毛利与毛利率。数据来自 XM-0037 成本台账，接线随第 5 片一并做。",
      );
    default:
      return undefined;
  }
}

/** 按平台与子页签 id 取内容。认不出的返回 undefined，由调用方回落到通用占位。 */
export function financeSubTab(
  serviceType: string,
  subId: string,
): ReactNode | undefined {
  if (serviceType === "newapi") return newapiFinanceSubTab(subId, serviceType);
  return sub2apiFinanceSubTab(subId);
}
