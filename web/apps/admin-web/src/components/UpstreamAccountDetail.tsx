import { useQuery, useQueryClient } from "@tanstack/react-query";
import { PageState } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";
import {
  describeCredential,
  FINANCE_READ_PERMISSION,
  listProxyAssets,
  listSubscriptionBatches,
  PROXY_ASSETS_QUERY,
  SUBSCRIPTION_BATCHES_QUERY,
  SUBSCRIPTION_MANAGE_PERMISSION,
  UPSTREAM_ACCOUNTS_QUERY,
  UPSTREAM_SUMMARY_QUERY,
  type MoneyItem,
  type ProxyAssetItem,
  type SubscriptionBatchItem,
  type UpstreamAccountItem,
} from "../api/finance";
import { appApiConfig } from "../api/config";
import { formatScaledMinorUnits } from "../lib/money";
import type { ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";
import { ProxyAssetDialog } from "./ProxyAssetDialog";
import { SubscriptionBatchDialog } from "./SubscriptionBatchDialog";
import {
  SubscriptionLifecycleDialog,
  subjectFromBatch,
  subjectFromProxy,
} from "./SubscriptionLifecycleDialog";
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
  scopes = appApiConfig.scopes,
}: {
  account: UpstreamAccountItem;
  onDone: (result: ActionResult) => void;
  scopes?: readonly string[];
}) {
  return (
    <div className="flex flex-col gap-4">
      <BasicInfo account={account} />
      <TokenMappingEditor account={account} onDone={onDone} />
      {/* **只有订阅型**才有批次与代理。这里按 access_method 三分而不是照
          `metered` 二分：官方 API 既不计量也没有订阅批次，给它画两张空表
          会让人以为它漏配了批次，然后去找一份根本不存在的订阅单 */}
      <CostBasisNote account={account} onDone={onDone} scopes={scopes} />
    </div>
  );
}

/** 这个账号的成本是怎么算出来的（§2.0 的三套口径）。 */
function CostBasisNote({
  account,
  onDone,
  scopes,
}: {
  account: UpstreamAccountItem;
  onDone: (result: ActionResult) => void;
  scopes: readonly string[];
}) {
  if (account.access_method === "subscription_account") {
    return <SubscriptionSection account={account} onDone={onDone} scopes={scopes} />;
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
function SubscriptionSection({
  account,
  onDone,
  scopes,
}: {
  account: UpstreamAccountItem;
  onDone: (result: ActionResult) => void;
  scopes: readonly string[];
}) {
  const queryClient = useQueryClient();
  const canRead = scopes.includes(FINANCE_READ_PERMISSION);
  const hasWrite = scopes.includes(SUBSCRIPTION_MANAGE_PERMISSION);
  const canManage = canRead && hasWrite;
  const batches = useQuery({
    queryKey: [SUBSCRIPTION_BATCHES_QUERY, account.id],
    queryFn: ({ signal }) => listSubscriptionBatches(account.id, { signal }),
    enabled: canRead,
  });
  const proxies = useQuery({
    queryKey: [PROXY_ASSETS_QUERY],
    queryFn: ({ signal }) => listProxyAssets({ signal }),
    enabled: canRead,
  });

  if (!canRead) {
    return (
      <p className="rounded-md border border-warning bg-warning/10 px-3 py-2 text-xs text-warning">
        授权状态不一致：缺少 {FINANCE_READ_PERMISSION}，不能在看不见现状时开放订阅维护表单。
      </p>
    );
  }

  const afterWrite = (result: ActionResult) => {
    onDone(result);
    for (const queryKey of [
      [SUBSCRIPTION_BATCHES_QUERY, account.id],
      [PROXY_ASSETS_QUERY],
      [UPSTREAM_ACCOUNTS_QUERY],
      [UPSTREAM_SUMMARY_QUERY],
    ]) {
      void queryClient.invalidateQueries({ queryKey });
    }
  };

  const batchPage = batches.data;
  const proxyPage = proxies.data;
  const proxyPageStatus = proxies.isError ? "error" : proxies.isSuccess ? "success" : "pending";
  const linkedProxies = (proxyPage?.items ?? []).filter((proxy) =>
    (batchPage?.items ?? []).some((batch) => batch.proxy_asset_id === proxy.id),
  );

  return (
    <div className="flex flex-col gap-3">
      <section>
        <div className="mb-1 flex flex-wrap items-center justify-between gap-2">
          <h4 className="text-xs font-medium text-fg">订阅批次</h4>
          <SubscriptionBatchDialog
            account={account}
            proxyPage={proxyPage}
            proxyPageStatus={proxyPageStatus}
            disabled={!canManage}
            onDone={(runId) => afterWrite({ title: "订阅批次已登记", runId })}
          />
        </div>
        <ApiStateView
          isPending={batches.isPending}
          error={batches.error}
          onRetry={() => void batches.refetch()}
          compact
        >
          {(batchPage?.items ?? []).length === 0 ? (
            <PageState
              kind="empty"
              compact
              title="这个账号还没有订阅批次"
              description="批次记录的是「为这条订阅渠道付了多少钱」，登记走 finance.subscription_batch.register。"
            />
          ) : (
            <BatchTable
              batches={batchPage?.items ?? []}
              account={account}
              canManage={canManage}
              onDone={afterWrite}
            />
          )}
        </ApiStateView>
        <PageEvidence page={batchPage} />
      </section>

      <section>
        <div className="mb-1 flex flex-wrap items-center justify-between gap-2">
          <h4 className="text-xs font-medium text-fg">代理资产</h4>
          <ProxyAssetDialog
            account={account}
            disabled={!canManage}
            onDone={(runId) => afterWrite({ title: "代理资产已登记", runId })}
          />
        </div>
        <ApiStateView
          isPending={proxies.isPending}
          error={proxies.error}
          onRetry={() => void proxies.refetch()}
          compact
        >
          <ProxyTable proxies={linkedProxies} account={account} canManage={canManage} onDone={afterWrite} />
        </ApiStateView>
        <PageEvidence page={proxyPage} />
      </section>
      {!canManage ? (
        <p className="rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted">
          维护控件已锁定：当前客户端缺少 {SUBSCRIPTION_MANAGE_PERMISSION}。前端仅作 UX 锁定，
          服务端 Action 权限、HUMAN 主体与环境检查仍是最终裁决。
        </p>
      ) : null}
      <p className="text-xs text-fg-muted">
        当前 read DTO 未提供剩余未摊销成本，也未提供代理 IP、协议、地区与健康状态；这些格明确未接入，页面不估算、不编造。
      </p>
    </div>
  );
}

function PageEvidence({
  page,
}: {
  page: { truncated: boolean; limit: number; as_of: string } | undefined;
}) {
  if (!page) return null;
  return (
    <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-fg-muted">
      <span>{page.as_of ? `派生金额截至 ${page.as_of}` : "派生金额观测日期未返回"}</span>
      {page.truncated ? (
        <span className="text-warning">列表已截断（limit {page.limit}），当前只显示返回页</span>
      ) : null}
    </div>
  );
}

const TH = "px-2 py-1 text-left text-xs font-medium text-fg-muted whitespace-nowrap";
const TD = "px-2 py-1 align-top text-xs text-fg";

function BatchTable({
  batches,
  account,
  canManage,
  onDone,
}: {
  batches: SubscriptionBatchItem[];
  account: UpstreamAccountItem;
  canManage: boolean;
  onDone: (result: ActionResult) => void;
}) {
  return (
    <div className="relative max-w-full overflow-x-auto rounded-md border border-edge">
      <table className="w-full border-collapse">
        <caption className="sr-only">订阅批次：付款、摊销与有效期</caption>
        <thead className="border-b border-edge bg-surface-muted">
          <tr>
            {[
              "起止",
              "有效天数",
              "实付",
              "附加费",
              "已退",
              "成本基数",
              "本账号分摊",
              "每日摊销",
              "状态",
              "操作",
            ].map((h) => (
              <th key={h} scope="col" className={TH}>
                {h}
              </th>
            ))}
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
              <td className={TD}>
                {/* 退款与终止是两个独立的会计事件，各给一个有名字的按钮。
                    刻意不合成一个「更多」菜单：不可逆的动作藏在二级菜单里，
                    等于把「这一步撤不回来」这句话也藏进去了 */}
                <div className="flex flex-wrap items-center gap-2">
                  <SubscriptionLifecycleDialog
                    subject={subjectFromBatch(b)}
                    action="refund"
                    businessDayTz={account.business_day_tz}
                    disabled={!canManage}
                    onDone={onDone}
                  />
                  <SubscriptionLifecycleDialog
                    subject={subjectFromBatch(b)}
                    action="terminate"
                    businessDayTz={account.business_day_tz}
                    disabled={!canManage}
                    onDone={onDone}
                  />
                </div>
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

function ProxyTable({
  proxies,
  account,
  canManage,
  onDone,
}: {
  proxies: ProxyAssetItem[];
  account: UpstreamAccountItem;
  canManage: boolean;
  onDone: (result: ActionResult) => void;
}) {
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
            {["购买渠道", "起止", "实付", "本账号分摊", "每日摊销", "凭据", "挂载", "操作"].map((h) => (
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
                <td className={TD}>
                  <div className="flex flex-wrap items-center gap-2">
                    <ProxyAssetDialog
                      account={account}
                      proxy={p}
                      disabled={!canManage}
                      onDone={(runId) => onDone({ title: "代理资产已更新", runId })}
                    />
                    <SubscriptionLifecycleDialog
                      subject={subjectFromProxy(p)}
                      action="refund"
                      businessDayTz={account.business_day_tz}
                      disabled={!canManage}
                      onDone={onDone}
                    />
                    <SubscriptionLifecycleDialog
                      subject={subjectFromProxy(p)}
                      action="terminate"
                      businessDayTz={account.business_day_tz}
                      disabled={!canManage}
                      onDone={onDone}
                    />
                  </div>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
