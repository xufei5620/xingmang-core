import { useQuery } from "@tanstack/react-query";
import { FreshnessBadge, FreshnessNote, PageHeader, PageState, StatTile, formatUtcTimestamp } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { Fragment, type ReactNode } from "react";
import { Link, useParams, useSearchParams } from "react-router";

import {
  describePaymentStatus,
  getPlatformOrder,
  type PlatformOrderDetail,
  type PlatformOrderItem,
} from "../api/finance";
import { formatMinorUnits, toIntegerValue } from "../lib/money";
import { ApiStateView } from "../components/ApiStateView";

/** 订单/退款详情页（XM-PAY1，原型 `V_paymentDetail`/`V_refundDetail`）。
 *
 *  两个变体共用同一个组件、同一个后端端点（`GET /platforms/{platform}/
 *  orders/{id}`）——上游没有区分"退款记录"和"处于退款状态的订单"，
 *  一笔落在退款生命周期状态的订单本身就是这一页要展示的全部信息来源；
 *  variant 只决定标题、返回目标与卡片措辞，不发起不同的请求。
 *
 *  找不到时展示原型同款的"未找到"状态，绝不回退到另一条记录（ADMIN-IA
 *  交接文档 §8）。 */

type DetailVariant = "order" | "refund";

const PLATFORM_LABELS: Record<string, string> = { sub2api: "Sub2API", newapi: "NewAPI" };

function platformLabel(platform: string): string {
  return PLATFORM_LABELS[platform] ?? platform;
}

const VARIANT_COPY: Record<DetailVariant, { backLabel: string; backSub: string; title: string; intro: string }> = {
  order: {
    backLabel: "充值订单",
    backSub: "orders",
    title: "订单详情",
    intro: "一笔充值订单从创建到当前状态的完整字段。",
  },
  refund: {
    backLabel: "退款与冲正",
    backSub: "refunds",
    title: "退款详情",
    intro: "一笔处于退款生命周期的订单；上游没有独立的退款记录，这里展示的就是该订单本身。",
  },
};

function amountText(amount: { minor_units: string | null; currency: string }): string {
  if (amount.minor_units === null) return "—";
  return formatMinorUnits(toIntegerValue(amount.minor_units), amount.currency);
}

function amountUnavailableReason(kind: "fee" | "net", order: PlatformOrderItem): string {
  if (kind === "net") {
    return "净现金流公式尚未确定（需先确认手续费承担方与是否有未建模成本），本片刻意留白，不猜";
  }
  return order.fee.minor_units === null
    ? "这笔订单的手续费不适用当前状态，或该连接器不提供第二个金额字段（NewAPI 恒为未知）"
    : "";
}

export function PlatformOrderDetailPage() {
  return <DetailPageBody variant="order" />;
}

export function PlatformRefundDetailPage() {
  return <DetailPageBody variant="refund" />;
}

function DetailPageBody({ variant }: { variant: DetailVariant }) {
  const params = useParams();
  const platform = params.serviceType ?? "";
  const orderId = params.orderId ?? "";
  const copy = VARIANT_COPY[variant];
  const label = platformLabel(platform);
  const backTo = `/platforms/${encodeURIComponent(platform)}?tab=finance&sub=${copy.backSub}`;
  const [searchParams] = useSearchParams();
  const day = searchParams.get("day") ?? "";
  const from = searchParams.get("from") ?? "";
  const to = searchParams.get("to") ?? "";

  const query = useQuery({
    queryKey: ["platform-order-detail", platform, orderId, day, from, to],
    queryFn: ({ signal }) =>
      getPlatformOrder(platform, orderId, {
        ...(day ? { day } : {}),
        ...(from ? { from } : {}),
        ...(to ? { to } : {}),
        signal,
      }),
    enabled: platform.length > 0 && orderId.length > 0,
    retry: false,
  });

  return (
    <section className="min-w-0">
      <Link
        to={backTo}
        aria-label={`返回${copy.backLabel}`}
        className="mb-3 inline-flex min-h-9 items-center rounded-md px-2 text-sm font-medium text-accent hover:bg-accent-soft focus-visible:outline-2 focus-visible:outline-accent"
      >
        <span aria-hidden="true">←</span>
        <span className="ml-1">返回{copy.backLabel}</span>
      </Link>

      {orderId.length === 0 ? (
        <>
          <PageHeader title={copy.title} description={`${label} · 订单 ID 为空`} />
          <PageState
            kind="unavailable"
            title="订单 ID 为空"
            description="链接缺少订单 ID，无法定位记录；请从列表页重新进入详情。"
            action={
              <Link to={backTo} className="text-sm font-medium text-accent hover:underline">
                返回{copy.backLabel}
              </Link>
            }
          />
        </>
      ) : (
        <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
          {query.data ? (
            <LookupResult
              result={query.data}
              variant={variant}
              label={label}
              orderId={orderId}
              backTo={backTo}
              backLabel={copy.backLabel}
            />
          ) : null}
        </ApiStateView>
      )}
    </section>
  );
}

function LookupResult({
  result,
  variant,
  label,
  orderId,
  backTo,
  backLabel,
}: {
  result: Awaited<ReturnType<typeof getPlatformOrder>>;
  variant: DetailVariant;
  label: string;
  orderId: string;
  backTo: string;
  backLabel: string;
}) {
  if (result.kind === "notFound") {
    return (
      <>
        <PageHeader title="未找到订单" description={`无法识别订单 ID ${orderId}，没有回退到其他订单。`} />
        <PageState
          kind="unavailable"
          title="请检查链接"
          description={`${result.message}该订单可能已删除、未纳管，或链接输入有误；从更早/更晚的日期重新进入详情也可能命中（见列表页的"详情"链接自动带上正确的业务日）。`}
          action={
            <Link to={backTo} className="text-sm font-medium text-accent hover:underline">
              返回{backLabel}
            </Link>
          }
        />
      </>
    );
  }

  return <FoundOrderDetail detail={result.detail} variant={variant} label={label} />;
}

function FoundOrderDetail({
  detail,
  variant,
  label,
}: {
  detail: PlatformOrderDetail;
  variant: DetailVariant;
  label: string;
}) {
  const order = detail.order;
  const status = describePaymentStatus(order.status);
  const copy = VARIANT_COPY[variant];

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <PageHeader
        title={order.order_id}
        status={<Badge tone={status.tone} title={order.status}>{status.label}</Badge>}
        description={
          <span>
            {label} · {copy.intro}
          </span>
        }
        actions={<FreshnessBadge freshness={detail.freshness} />}
      />

      {variant === "refund" ? (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <AmountTile label="原订单金额" amount={order.amount} />
          <AmountTile label="退款金额" amount={order.refund_amount} />
          <AmountTile label="支付手续费" amount={order.fee} reason={amountUnavailableReason("fee", order)} />
          <NetTile />
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <AmountTile label="订单金额" amount={order.amount} />
          <AmountTile label="支付手续费" amount={order.fee} reason={amountUnavailableReason("fee", order)} />
          <NetTile />
          <AmountTile label="退款金额" amount={order.refund_amount} />
        </div>
      )}

      <section className="rounded-lg border border-edge bg-surface p-4">
        <h3 className="mb-3 text-sm font-semibold text-fg">订单信息</h3>
        <dl className="grid grid-cols-1 gap-x-6 gap-y-3 sm:grid-cols-2 lg:grid-cols-3">
          <Fact label="用户">
            <span className="break-all font-mono text-xs">{order.user_ref || "—"}</span>
          </Fact>
          <Fact label="类型">用户充值</Fact>
          <Fact label="支付方式">{order.method || "—"}</Fact>
          <Fact label="创建时间">
            <span className="tabular-nums">{formatUtcTimestamp(order.created_at)}</span>
          </Fact>
          <Fact label="到账时间" hint="payments.read.v1 只提供下单时刻（created_at），未提供到账/完成时刻的独立字段">
            <span className="text-fg-muted">未接入</span>
          </Fact>
          <Fact label="参考号" hint="上游自己生成的业务订单号（out_trade_no），不是支付网关自己的流水号——PAY0 未采集后者">
            <span className="break-all font-mono text-xs">{order.upstream_order_ref || "—"}</span>
          </Fact>
        </dl>
      </section>

      {variant === "refund" ? (
        <p className="rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted">
          退款原因、处理人、完成时间与独立的退款流水号在 payments.read.v1 里没有对应字段（上游没有独立的退款记录，
          只有处于退款生命周期状态的订单本身）；不在这里猜一个"看起来合理"的值。
        </p>
      ) : null}

      <div className="rounded-lg border border-edge bg-surface p-3">
        <FreshnessNote freshness={detail.freshness} />
        <p className="break-words text-xs text-fg-muted">
          来源 {detail.data_source || "—"} · 已搜索窗口 {detail.from} 至 {detail.to}
        </p>
      </div>
    </div>
  );
}

function AmountTile({
  label,
  amount,
  reason,
}: {
  label: string;
  amount: { minor_units: string | null; currency: string };
  reason?: string;
}) {
  const unavailable = amount.minor_units === null;
  return (
    <StatTile
      label={label}
      value={amountText(amount)}
      unavailable={unavailable}
      note={unavailable ? reason || "未接入" : `币种 ${amount.currency || "未知"}`}
      status={unavailable ? <Badge tone="neutral">未接入</Badge> : undefined}
    />
  );
}

function NetTile() {
  return (
    <StatTile
      label="净入账"
      value="—"
      unavailable
      note="净现金流公式尚未确定（需先确认手续费承担方与是否有未建模成本），本片刻意留白，不猜"
      status={<Badge tone="neutral">未接入</Badge>}
    />
  );
}

function Fact({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-fg-muted" title={hint}>
        {label}
      </dt>
      <dd className="mt-0.5 text-sm text-fg">{children}</dd>
    </div>
  );
}
