import { useQuery } from "@tanstack/react-query";
import { PageState } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";
import {
  describeCredential,
  listProxyAssets,
  listSubscriptionBatches,
  PROXY_ASSETS_QUERY,
  SUBSCRIPTION_BATCHES_QUERY,
  type MoneyItem,
  type ProxyAssetItem,
  type SubscriptionBatchItem,
  type UpstreamAccountItem,
} from "../api/finance";
import { formatScaledMinorUnits } from "../lib/money";
import type { ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";
import { TokenMappingEditor } from "./TokenMappingEditor";

/** 登记簿金额 → 展示文本。
 *
 *  **直接用 XM-0037d 建的 `formatScaledMinorUnits`，不自己再写一个**：
 *  成本线的金额是 scale-6 微单位，而币种的最小单位是 2 位,
 *  两处各写一份降标度逻辑，迟早有一处的舍入方式与后端 `money.Rescale` 漂开——
 *  同一笔钱在两个页面上差一分，而两处看起来都完全正常。
 *
 *  空金额给「—」而不是 ¥0.00：批次没算出摊销与摊销为零是两件事。 */
export function formatMoneyItem(m: MoneyItem | null | undefined): string {
  if (!m || m.amount_minor === "") return "—";
  return formatScaledMinorUnits(m.amount_minor, m.currency, m.scale);
}

/** 上游账号的展开区：令牌映射 + 订阅批次 + 代理资产。
 *
 *  三块都在这里而不是各开一个页面：它们回答的是同一个问题——
 *  「这个上游账号的成本是怎么算出来的」。计量型看映射，订阅型看批次与代理。 */
export function UpstreamAccountDetail({
  account,
  onDone,
}: {
  account: UpstreamAccountItem;
  onDone: (result: ActionResult) => void;
}) {
  return (
    <div className="flex flex-col gap-4">
      <BasicInfo account={account} />
      <TokenMappingEditor account={account} onDone={onDone} />
      {/* **只有订阅型**才有批次与代理。这里按 access_method 三分而不是照
          `metered` 二分：官方 API 既不计量也没有订阅批次，给它画两张空表
          会让人以为它漏配了批次，然后去找一份根本不存在的订阅单 */}
      <CostBasisNote account={account} />
    </div>
  );
}

/** 这个账号的成本是怎么算出来的（§2.0 的三套口径）。 */
function CostBasisNote({ account }: { account: UpstreamAccountItem }) {
  if (account.access_method === "subscription_account") {
    return <SubscriptionSection account={account} />;
  }
  if (account.metered) {
    return (
      <p className="text-xs text-fg-muted">
        这是计量型账号（按实扣 ÷ 倍率折算成本），没有订阅批次与代理资产。
      </p>
    );
  }
  return (
    <p className="text-xs text-fg-muted">
      这是官方 API 直连账号：成本由原厂计费，v1 的折算口径待定（设计稿 §2.0/§12
      拍板占位后置）。它既不走充值倍率，也没有订阅批次与代理资产。
    </p>
  );
}

function BasicInfo({ account }: { account: UpstreamAccountItem }) {
  const cred = describeCredential(account.credential_ref);
  return (
    <dl className="grid grid-cols-2 gap-x-6 gap-y-2 text-xs md:grid-cols-4">
      <Field label="账号 ID" value={<span className="font-mono break-all">{account.id}</span>} />
      <Field label="系统类型" value={account.system_type || "—"} />
      <Field
        label="凭据"
        value={
          <Badge tone={cred.configured ? "success" : "neutral"} title={cred.hint}>
            {cred.label}
          </Badge>
        }
      />
      <Field
        label="凭据引用"
        // 引用本身可以显示——它不是凭据，是取凭据的地址（ADR-014）
        value={<span className="font-mono break-all">{account.credential_ref || "—"}</span>}
      />
      <Field label="币种" value={account.currency || "—"} />
      <Field
        label="业务日切时区"
        value={account.business_day_tz || "—"}
        hint="展示金额与日期时按它解释，不按浏览器时区（宪法 14 条）"
      />
      <Field label="环境" value={account.environment || "—"} />
      <Field label="更新时间" value={<span className="tabular-nums">{account.updated_at}</span>} />
    </dl>
  );
}

function Field({
  label,
  value,
  hint,
}: {
  label: string;
  value: ReactNode;
  hint?: string;
}) {
  return (
    <div className="min-w-0">
      <dt className="text-fg-muted" title={hint}>
        {label}
      </dt>
      <dd className="text-fg">{value}</dd>
    </div>
  );
}

/** 订阅批次与代理资产。 */
function SubscriptionSection({ account }: { account: UpstreamAccountItem }) {
  const batches = useQuery({
    queryKey: [SUBSCRIPTION_BATCHES_QUERY, account.id],
    queryFn: ({ signal }) => listSubscriptionBatches(account.id, { signal }),
  });
  const proxies = useQuery({
    queryKey: [PROXY_ASSETS_QUERY],
    queryFn: ({ signal }) => listProxyAssets({ signal }),
  });

  return (
    <div className="flex flex-col gap-3">
      <section>
        <h4 className="mb-1 text-xs font-medium text-fg">订阅批次</h4>
        <ApiStateView
          isPending={batches.isPending}
          error={batches.error}
          onRetry={() => void batches.refetch()}
          compact
        >
          {(batches.data ?? []).length === 0 ? (
            <PageState
              kind="empty"
              compact
              title="这个账号还没有订阅批次"
              description="批次记录的是「为这条订阅渠道付了多少钱」，登记走 finance.subscription_batch.register。"
            />
          ) : (
            <BatchTable batches={batches.data ?? []} />
          )}
        </ApiStateView>
      </section>

      <section>
        <h4 className="mb-1 text-xs font-medium text-fg">代理资产</h4>
        <ApiStateView
          isPending={proxies.isPending}
          error={proxies.error}
          onRetry={() => void proxies.refetch()}
          compact
        >
          <ProxyTable
            proxies={(proxies.data ?? []).filter((p) =>
              (batches.data ?? []).some((b) => b.proxy_asset_id === p.id),
            )}
          />
        </ApiStateView>
      </section>
    </div>
  );
}

const TH = "px-2 py-1 text-left text-xs font-medium text-fg-muted whitespace-nowrap";
const TD = "px-2 py-1 align-top text-xs text-fg";

function BatchTable({ batches }: { batches: SubscriptionBatchItem[] }) {
  return (
    <div className="relative max-w-full overflow-x-auto rounded-md border border-edge">
      <table className="w-full border-collapse">
        <caption className="sr-only">订阅批次：付款、摊销与有效期</caption>
        <thead className="border-b border-edge bg-surface-muted">
          <tr>
            {["起止", "有效天数", "实付", "附加费", "已退", "成本基数", "本账号分摊", "每日摊销", "状态"].map(
              (h) => (
                <th key={h} scope="col" className={TH}>
                  {h}
                </th>
              ),
            )}
          </tr>
        </thead>
        <tbody>
          {batches.map((b) => (
            <tr key={b.id} className="border-b border-edge last:border-b-0">
              <td className={`${TD} tabular-nums whitespace-nowrap`}>
                {b.starts_on} → {b.expires_on}
              </td>
              <td className={`${TD} tabular-nums`}>{b.effective_days}</td>
              <td className={`${TD} tabular-nums`}>{formatMoneyItem(b.paid)}</td>
              <td className={`${TD} tabular-nums`}>{formatMoneyItem(b.surcharge)}</td>
              <td className={`${TD} tabular-nums`}>{formatMoneyItem(b.refunded)}</td>
              <td className={`${TD} tabular-nums`}>{formatMoneyItem(b.cost_basis)}</td>
              <td className={`${TD} tabular-nums`} title={`本批次共 ${b.account_count} 个账号分摊`}>
                {formatMoneyItem(b.account_share)}
              </td>
              <td className={`${TD} tabular-nums`}>{formatMoneyItem(b.daily_amortization)}</td>
              <td className={TD}>
                <BatchStatus batch={b} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** 批次状态：退款与终止是两件事，不能压成一个「已结束」。 */
function BatchStatus({ batch }: { batch: SubscriptionBatchItem }) {
  if (batch.refunded_on) {
    return (
      <Badge tone="warning" title={`退款于 ${batch.refunded_on}`}>
        已退款
      </Badge>
    );
  }
  if (batch.terminated_on) {
    return (
      <Badge tone="neutral" title={`终止于 ${batch.terminated_on}；终止之后不再产生摊销`}>
        已终止
      </Badge>
    );
  }
  return <Badge tone="success">生效中</Badge>;
}

function ProxyTable({ proxies }: { proxies: ProxyAssetItem[] }) {
  if (proxies.length === 0) {
    return (
      <PageState
        kind="empty"
        compact
        title="没有关联的代理资产"
        description="代理资产按 proxy_asset_id 与订阅批次关联；没有关联不等于没买过代理。"
      />
    );
  }
  return (
    <div className="relative max-w-full overflow-x-auto rounded-md border border-edge">
      <table className="w-full border-collapse">
        <caption className="sr-only">代理资产：购买渠道、摊销与挂载状态</caption>
        <thead className="border-b border-edge bg-surface-muted">
          <tr>
            {["购买渠道", "起止", "实付", "本账号分摊", "每日摊销", "凭据", "挂载"].map((h) => (
              <th key={h} scope="col" className={TH}>
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {proxies.map((p) => {
            const cred = describeCredential(p.credential_ref);
            return (
              <tr key={p.id} className="border-b border-edge last:border-b-0">
                <td className={TD}>
                  {p.buy_platform || "—"}
                  <p className="break-all text-fg-muted">{p.buy_address}</p>
                </td>
                <td className={`${TD} tabular-nums whitespace-nowrap`}>
                  {p.opened_on} → {p.expires_on}
                </td>
                <td className={`${TD} tabular-nums`}>{formatMoneyItem(p.paid)}</td>
                <td className={`${TD} tabular-nums`}>{formatMoneyItem(p.account_share)}</td>
                <td className={`${TD} tabular-nums`}>
                  {/* mounted=false ⇒ 每日摊销是**已知的 0**，不是未知 */}
                  {p.mounted ? (
                    formatMoneyItem(p.daily_amortization)
                  ) : (
                    <span className="text-fg-muted" title="这份代理今天没在服务，摊销是已知的 0">
                      0(未挂载)
                    </span>
                  )}
                </td>
                <td className={TD}>
                  <Badge tone={cred.configured ? "success" : "neutral"} title={cred.hint}>
                    {cred.label}
                  </Badge>
                </td>
                <td className={TD}>
                  <Badge tone={p.mounted ? "success" : "neutral"}>
                    {p.mounted ? "已挂载" : "未挂载"}
                  </Badge>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
