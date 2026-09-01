import { formatUtcTimestamp, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { Link } from "react-router";

import { describePaymentStatus, type PlatformOrderItem } from "../api/finance";
import { formatMinorUnits, toIntegerValue } from "../lib/money";

/** 「充值订单」表的共用列定义（XM-PAY1）——Sub2API「充值订单」页签与 NewAPI
 *  「资金与订单」页签的订单表用的是同一份列集合（任务口径逐字："订单 / 用户 /
 *  类型 / 支付方式 / 金额 / 手续费 / 净额 / 状态 / 创建 / 到账 / 参考号 /
 *  详情"）：两个平台都只是这个端点的不同 platform 参数，字段形状完全一致，
 *  NewAPI 的手续费/净额恒为「—」是数据本身如此（连接器侧 FeeMinorUnits/
 *  RefundAmountMinorUnits 恒为 nil），不是两份列定义该长得不一样。 */

/** `AmountBody` → 展示文本；缺席（`minor_units === null`）显示「—」，
 *  不是「数值异常」——那是给真正解析不出来的值留的（与
 *  PlatformUserDetailPage.tsx 的同名小工具同一条纪律）。 */
export function amountBodyText(amount: { minor_units: string | null; currency: string }): string {
  if (amount.minor_units === null) return "—";
  return formatMinorUnits(toIntegerValue(amount.minor_units), amount.currency);
}

export function amountCell(amount: { minor_units: string | null; currency: string }, mutedWhenMissing = true) {
  const missing = amount.minor_units === null;
  return <span className={missing && mutedWhenMissing ? "text-fg-muted" : undefined}>{amountBodyText(amount)}</span>;
}

/** 订单所在的 UTC 业务日（YYYY-MM-DD）——`created_at` 已经是后端给的 UTC
 *  RFC3339 时间戳，直接切片就是 parseOrdersWindow 的 `day` 参数认识的格式；
 *  详情页链接带上它，保证同一天内点开总能命中（见 GetPlatformOrderHandler
 *  的顶部注释）。 */
export function dayOf(createdAt: string): string {
  return createdAt.slice(0, 10);
}

/** 筛选用的归一化分桶标签——与资金概览卡、`sub2api.payments.daily`/
 *  `newapi.payments.daily` 的四个键同一空间，但这里不额外发请求：客户端
 *  对已加载的行按同一套判据分组。两个平台共用同一份判据表——NewAPI 的
 *  原始状态是小写（pending/success/failed/expired），upper() 之后与
 *  Sub2API 的判据表完全对得上，不需要按平台分叉。 */
export function bucketLabel(status: string): string {
  const upper = status.toUpperCase().trim();
  if (["PAID", "RECHARGING", "COMPLETED", "SUCCESS"].includes(upper)) return "成功到账";
  if (upper === "PENDING") return "待处理";
  if (["EXPIRED", "CANCELLED", "FAILED"].includes(upper)) return "失败";
  if (
    ["REFUND_REQUESTED", "REFUNDING", "REFUND_PENDING", "REFUND_FAILED", "PARTIALLY_REFUNDED", "REFUNDED"].includes(
      upper,
    )
  ) {
    return "退款与冲正";
  }
  return "未知";
}

export const STATUS_BUCKET_OPTIONS = ["成功到账", "待处理", "失败", "退款与冲正"] as const;

function NetUnavailableCell() {
  return <span className="text-fg-muted">未接入</span>;
}

export const COLUMN_ORDER: DataTableColumn<PlatformOrderItem> = {
  id: "order",
  header: "订单",
  primary: true,
  value: (o) => o.order_id,
  cell: (o) => <span className="font-mono">{o.order_id}</span>,
};

export const COLUMN_USER: DataTableColumn<PlatformOrderItem> = {
  id: "user",
  header: "用户",
  value: (o) => o.user_ref,
  cell: (o) => <span className="font-mono text-xs">{o.user_ref || "—"}</span>,
};

/** 这个端点只返回充值订单——上游没有区分"其它类型"，本列固定给出真实的
 *  静态值，不是一个猜出来的分类（见 payments.read.v1 契约"逐笔订单字段"）。 */
export const COLUMN_TYPE: DataTableColumn<PlatformOrderItem> = {
  id: "type",
  header: "类型",
  value: () => "用户充值",
  cell: () => "用户充值",
};

export const COLUMN_METHOD: DataTableColumn<PlatformOrderItem> = {
  id: "method",
  header: "支付方式",
  value: (o) => o.method,
  cell: (o) => o.method || "—",
};

export const COLUMN_AMOUNT: DataTableColumn<PlatformOrderItem> = {
  id: "amount",
  header: "订单金额",
  numeric: true,
  value: (o) => toIntegerValue(o.amount.minor_units),
  cell: (o) => amountCell(o.amount, false),
};

export const COLUMN_FEE: DataTableColumn<PlatformOrderItem> = {
  id: "fee",
  header: "手续费",
  headerTitle: "只在这笔订单落进成功到账/退款桶时才有值（Sub2API）；NewAPI 恒为「—」，上游没有第二个金额字段可供相减",
  numeric: true,
  value: (o) => toIntegerValue(o.fee.minor_units),
  cell: (o) => amountCell(o.fee),
};

export const COLUMN_NET: DataTableColumn<PlatformOrderItem> = {
  id: "net",
  header: "净额",
  headerTitle: "净现金流公式尚未确定（需先确认手续费承担方与是否有未建模成本），本片刻意留白，不猜",
  numeric: true,
  cell: () => <NetUnavailableCell />,
};

export const COLUMN_STATUS: DataTableColumn<PlatformOrderItem> = {
  id: "status",
  header: "状态",
  // 筛选值刻意用归一化分桶（而不是 describePaymentStatus 的原始状态友好名）：
  // DataTableV2 的筛选/搜索是子串匹配（filterRows），"退款失败"这个原始状态
  // 友好名本身就含有"失败"两个字——如果这一列的可筛选值也用原始状态友好名，
  // 按"失败"筛选会把"退款与冲正"桶里的订单也筛进来。四个桶标签互不为子串，
  // 不会有这个问题；badge 仍然显示精确的原始状态，不受这里的影响。
  value: (o) => bucketLabel(o.status),
  cell: (o) => {
    const shown = describePaymentStatus(o.status);
    return (
      <Badge tone={shown.tone} title={o.status}>
        {shown.label}
      </Badge>
    );
  },
};

export const COLUMN_CREATED: DataTableColumn<PlatformOrderItem> = {
  id: "created",
  header: "创建",
  value: (o) => o.created_at,
  cell: (o) => <span className="text-xs tabular-nums">{formatUtcTimestamp(o.created_at)}</span>,
};

/** 「到账」列：payments.read.v1 只提供下单时刻（created_at），没有独立的
 *  到账/完成时刻字段——恒为「未接入」，不能拿 created_at 冒充到账时间
 *  （两者对一笔尚在处理中的订单可能相差任意长）。 */
export const COLUMN_ARRIVED: DataTableColumn<PlatformOrderItem> = {
  id: "arrived",
  header: "到账",
  headerTitle: "payments.read.v1 只提供下单时刻（created_at），未提供到账/完成时刻的独立字段",
  cell: () => <span className="text-fg-muted">未接入</span>,
};

export const COLUMN_REF: DataTableColumn<PlatformOrderItem> = {
  id: "ref",
  header: "参考号",
  headerTitle: "上游自己生成的业务订单号，不是支付网关自己的流水号——PAY0 未采集后者",
  value: (o) => o.upstream_order_ref,
  cell: (o) => <span className="font-mono text-xs">{o.upstream_order_ref || "—"}</span>,
};

export function detailColumn(platform: string, kind: "orders" | "refunds" = "orders"): DataTableColumn<PlatformOrderItem> {
  return {
    id: "detail",
    header: "",
    headerTitle: kind === "refunds" ? "打开退款详情" : "打开订单详情",
    cell: (order) => (
      <Link
        to={`/platforms/${encodeURIComponent(platform)}/finance/${kind}/${encodeURIComponent(order.order_id)}?day=${dayOf(order.created_at)}`}
        aria-label={`查看订单 ${order.order_id} 的${kind === "refunds" ? "退款" : ""}详情`}
        className="inline-flex size-9 items-center justify-center rounded-md text-lg text-accent hover:bg-accent-soft focus-visible:outline-2 focus-visible:outline-accent"
      >
        <span aria-hidden="true">›</span>
      </Link>
    ),
  };
}

/** 完整的订单表列集合，逐字对齐任务口径的列顺序（含"到账"列——payments.read.v1
 *  没有对应字段，恒显示「未接入」）。 */
export function orderTableColumns(platform: string): DataTableColumn<PlatformOrderItem>[] {
  return [
    COLUMN_ORDER,
    COLUMN_USER,
    COLUMN_TYPE,
    COLUMN_METHOD,
    COLUMN_AMOUNT,
    COLUMN_FEE,
    COLUMN_NET,
    COLUMN_STATUS,
    COLUMN_CREATED,
    COLUMN_ARRIVED,
    COLUMN_REF,
    detailColumn(platform, "orders"),
  ];
}
