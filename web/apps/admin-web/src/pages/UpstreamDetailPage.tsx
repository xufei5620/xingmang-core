import { useQuery, useQueryClient } from "@tanstack/react-query";
import { PageHeader, PageState, StatTile } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { useState, type ReactNode } from "react";
import { Link, useParams } from "react-router";
import {
  describeAccessMethod,
  describeCredential,
  listChannelSummaries,
  listUpstreamAccounts,
  listUpstreamSummaries,
  UPSTREAM_ACCOUNTS_QUERY,
  UPSTREAM_SUMMARY_QUERY,
  type ChannelSummary,
  type Money,
  type UpstreamAccountItem,
  type UpstreamSummary,
} from "../api/finance";
import { formatScaledMinorUnits } from "../lib/money";
import { runwayReasonText } from "../lib/runway";
import { ActionResultNote, type ActionResult } from "../components/ActionResultNote";
import { ApiStateView } from "../components/ApiStateView";
import { UpstreamAccountDetail } from "../components/UpstreamAccountDetail";
import { NotFoundView } from "./NotFoundPage";
import {
  channelDetailPath,
  isSupplyPlatform,
  supplyPlatformLabel,
  upstreamDetailPath,
  type SupplyPlatform,
} from "./ChannelDetailPage";

/** 上游详情。
 *
 *  2026-09-08 起不再是 UI-only 壳。这一页以前整片写着「上游详情读契约尚未
 *  接入」，而那句话在写下的时候就已经不成立了：成本登记簿
 *  （`GET /api/v1/finance/upstream-accounts`，XM-0037a）与两条窗口汇总
 *  （`/finance/upstreams/summary`、`/finance/channels/summary`，§13）一直都通，
 *  同一批端点已经被渠道管理表、平台概览与渠道详情页读了三处。一句「尚未接入」
 *  比缺功能更糟：它让人不去看本来就有的数，于是上游的余额、消耗与毛利在这一屏
 *  上彻底不可见。
 *
 *  ## 三条端点怎么对上同一个上游
 *
 *  登记簿没有「按 id 取单条」的端点，只有列表；两条汇总也是列表。三者的行 id
 *  **是同一个** `finance.upstream_account.id`（后端 `channelToItem` /
 *  `upstreamToItem` 都取 `s.Account.ID`），所以按 id 在列表里定位是对的，不是
 *  凑合。**列表里没有这个 id 时显示「登记簿里没有这一条」**，不给一张空壳：
 *  一个字段全空的详情页看起来像「这个上游什么都没配」，而事实是它根本不存在。
 *
 *  ## 「这个上游有几条渠道」按后端给的 supplierKey 归并，不在前端重算
 *
 *  归并键 `supplier_key`（`system_type|base_url`，没有 base_url 的账号各自成组）
 *  由 `/finance/upstreams/summary` 逐行返回。前端**只按这个字符串分组**，不自己
 *  拼一遍键——拼错一次的后果是几个互不相干的账号共享一个余额。后端注释写明今天
 *  登记簿的唯一索引让它与账号一一对应，所以这个数今天多半是 1；那是事实，不是
 *  漏做聚合。
 *
 *  ## 写操作在 UpstreamAccountDetail 里，本页只是挂载它
 *
 *  令牌映射、订阅批次、代理资产与四个退款/终止对话框都在
 *  `components/UpstreamAccountDetail.tsx`。那个组件自 `3c97057`（渠道管理合并成
 *  单表、删掉 UpstreamAccountsPanel）之后失去了全部调用方——功能一直在仓库里，
 *  只是没有任何一屏能点到它。这一页是它的新宿主，本片不重写它的任何一行。 */
export function UpstreamDetailPage() {
  const { serviceType = "", upstreamId = "" } = useParams();

  if (!isSupplyPlatform(serviceType)) {
    return (
      <NotFoundView
        pathname={`/platforms/${serviceType || "（空平台）"}/suppliers/${upstreamId}`}
        detail={`平台 ${serviceType || "（空平台）"} 不支持上游详情`}
      />
    );
  }

  if (!upstreamId.trim() || upstreamId === "new") {
    return (
      <NotFoundView
        pathname={upstreamDetailPath(serviceType, upstreamId)}
        detail={
          upstreamId === "new"
            ? "新增上游请到渠道管理页使用「＋ 添加上游」（专用评审蓝图页已于 2026-09-03 按产品负责人裁定下线）"
            : "上游 ID 为空，无法定位详情"
        }
      />
    );
  }

  return <UpstreamDetailShell platform={serviceType} upstreamId={upstreamId} />;
}

function UpstreamDetailShell({ platform, upstreamId }: { platform: SupplyPlatform; upstreamId: string }) {
  const label = supplyPlatformLabel(platform);
  const queryClient = useQueryClient();
  const [result, setResult] = useState<ActionResult | null>(null);
  // 2026-09-02 起「上游管理」不再是独立页签：登记簿字段并入了渠道管理页
  // 表格的行与渠道详情页（07:20 补充裁定）——返回目标跟着从 ?tab=suppliers
  // 改成 ?tab=upstream，不带锚点：已经没有独立区块可滚，落地在页顶即可
  const backTo = `/platforms/${platform}?tab=upstream`;

  // 登记簿与供给汇总用 api/finance 导出的那两个 key 常量，**不另写字面量数组**：
  // 挂在下面的 UpstreamAccountDetail 写完之后作废的正是这两个 key，各写各的
  // 会变成「写成功了但这一页的数没变」。
  const accountsQuery = useQuery({
    queryKey: [UPSTREAM_ACCOUNTS_QUERY],
    queryFn: ({ signal }) => listUpstreamAccounts({ signal }),
  });
  const upstreamSummaryQuery = useQuery({
    queryKey: [UPSTREAM_SUMMARY_QUERY],
    queryFn: ({ signal }) => listUpstreamSummaries({ signal }),
  });
  // 渠道汇总用平台通用的那个 key：与平台概览、渠道管理表同一份缓存，一屏一次请求。
  const channelSummaryQuery = useQuery({
    queryKey: ["finance", "channels", "summary"],
    queryFn: ({ signal }) => listChannelSummaries({ signal }),
  });

  const accounts = accountsQuery.data ?? [];
  const account = accounts.find((item) => item.id === upstreamId);
  const upstreamSummaries = upstreamSummaryQuery.data?.items ?? [];
  const summary = upstreamSummaries.find((item) => item.id === upstreamId);

  const afterWrite = (written: ActionResult) => {
    setResult(written);
    // 写的是登记簿与订阅口径；两条汇总也一起作废——摊销改了之后本期成本会变。
    for (const queryKey of [
      [UPSTREAM_ACCOUNTS_QUERY],
      [UPSTREAM_SUMMARY_QUERY],
      ["finance", "channels", "summary"],
    ]) {
      void queryClient.invalidateQueries({ queryKey });
    }
  };

  return (
    <section className="min-w-0">
      <Link
        to={backTo}
        aria-label={`返回 ${label} 上游管理`}
        className="mb-3 inline-flex min-h-9 items-center rounded-md px-2 text-sm font-medium text-accent hover:bg-accent-soft focus-visible:outline-2 focus-visible:outline-accent"
      >
        <span aria-hidden="true">←</span>
        <span className="ml-1">返回上游管理</span>
      </Link>

      <PageHeader
        title="上游详情"
        {...(account
          ? {
              status: (
                <Badge tone={account.status === "active" ? "success" : "neutral"}>{account.status}</Badge>
              ),
            }
          : {})}
        description={
          <span className="break-all">
            平台 {label} · 上游 ID <code className="font-mono">{upstreamId}</code> · 多渠道汇总视角
          </span>
        }
      />

      {result ? <ActionResultNote result={result} onDismiss={() => setResult(null)} /> : null}

      <ApiStateView
        isPending={accountsQuery.isPending}
        error={accountsQuery.error}
        onRetry={() => void accountsQuery.refetch()}
      >
        {!account ? (
          <PageState
            kind="empty"
            title="上游登记簿里没有这一条"
            description={`成本登记簿（共 ${accounts.length} 条）里没有 ID 为 ${upstreamId} 的上游账号；可能已被删除，或链接输入有误。登记簿没有按 ID 取单条的端点，这一页按 ID 在列表里定位。`}
          />
        ) : account.system_type !== platform ? (
          // 平台段与登记簿里的 system_type 对不上：**不按 URL 的平台渲染它**。
          // 把一个 sub2api 账号画在 NewAPI 的标题下，页面上每一个数都还是对的，
          // 唯独归属是错的——那是最难被发现的一种错。
          <PageState
            kind="empty"
            title={`这个上游不属于 ${label}`}
            description={`ID ${upstreamId} 在登记簿里的系统类型是 ${account.system_type}，不是 ${platform}。`}
          />
        ) : (
          <UpstreamDetailBody
            platform={platform}
            account={account}
            accounts={accounts}
            summary={summary}
            upstreamSummaries={upstreamSummaries}
            summaryWindow={
              upstreamSummaryQuery.data
                ? { from: upstreamSummaryQuery.data.from, to: upstreamSummaryQuery.data.to }
                : null
            }
            summaryPending={upstreamSummaryQuery.isPending}
            summaryError={upstreamSummaryQuery.error}
            channels={channelSummaryQuery.data?.items ?? []}
            channelsPending={channelSummaryQuery.isPending}
            channelsError={channelSummaryQuery.error}
            onChannelsRetry={() => void channelSummaryQuery.refetch()}
            onDone={afterWrite}
          />
        )}
      </ApiStateView>
    </section>
  );
}

/** 金额展示。给不出就「—」，不按 0 兜底（宪法 13 条，同 lib/money 的 fail closed）。 */
function moneyText(money: Money | null | undefined): string {
  if (!money) return "—";
  return formatScaledMinorUnits(money.amountMinor, money.currency, money.scale);
}

/** 这个上游名下的全部账号：按后端给的 `supplier_key` 归并。
 *
 *  **键只从汇总行里取，不在前端拼一遍**（后端 `SupplierKeyOf` 是
 *  `system_type|base_url`，没有 base_url 时退回 `account|<id>`）。汇总读不到时
 *  返回 null 而不是「就它自己一个」：那两件事在屏幕上必须长得不一样。 */
function supplierGroupIds(
  upstreamId: string,
  summaries: readonly UpstreamSummary[],
): string[] | null {
  const self = summaries.find((item) => item.id === upstreamId);
  if (!self || !self.supplierKey) return null;
  return summaries.filter((item) => item.supplierKey === self.supplierKey).map((item) => item.id);
}

function UpstreamDetailBody({
  platform,
  account,
  accounts,
  summary,
  upstreamSummaries,
  summaryWindow,
  summaryPending,
  summaryError,
  channels,
  channelsPending,
  channelsError,
  onChannelsRetry,
  onDone,
}: {
  platform: SupplyPlatform;
  account: UpstreamAccountItem;
  accounts: readonly UpstreamAccountItem[];
  summary: UpstreamSummary | undefined;
  upstreamSummaries: readonly UpstreamSummary[];
  summaryWindow: { from: string; to: string } | null;
  summaryPending: boolean;
  summaryError: unknown;
  channels: readonly ChannelSummary[];
  channelsPending: boolean;
  channelsError: unknown;
  onChannelsRetry: () => void;
  onDone: (result: ActionResult) => void;
}) {
  const label = supplyPlatformLabel(platform);
  const access = describeAccessMethod(account.access_method);
  const cred = describeCredential(account.credential_ref);
  const groupIds = supplierGroupIds(account.id, upstreamSummaries);
  const groupAccounts = groupIds ? accounts.filter((item) => groupIds.includes(item.id)) : [account];
  const groupChannels = groupIds ? channels.filter((item) => groupIds.includes(item.id)) : [];
  const selfChannel = channels.find((item) => item.id === account.id);
  // 「本期」是后端定的窗口，不是前端挑的：不给 from/to 时后端只取今天。
  const windowNote = summaryWindow
    ? `窗口 ${summaryWindow.from} ~ ${summaryWindow.to}（业务日）`
    : "窗口未知：汇总尚未返回";
  // 汇总读不到与「汇总里没有这一条」是两件事，副行上要分得开。
  const summaryMissingNote = summaryError
    ? "供给汇总读取失败"
    : summaryPending
      ? "供给汇总加载中"
      : "供给汇总里没有这个上游";
  const channelsMissingNote = channelsError
    ? "逐渠道汇总读取失败"
    : channelsPending
      ? "逐渠道汇总加载中"
      : "逐渠道汇总里没有这个上游";
  const groupedAccountsWithGroup = groupAccounts.filter((item) => item.upstream_group);

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <p
        role="status"
        className="rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted"
      >
        本页读三条只读契约：成本登记簿 /finance/upstream-accounts、供给汇总
        /finance/upstreams/summary、逐渠道汇总 /finance/channels/summary。凭据只显示引用与状态，
        平台永不回显明文（ADR-014）；下方的写操作一律走 Action，前端不直接写库（宪法 2、3 条）。
      </p>

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-5">
        <StatTile label="接入平台" value={label} note="由当前平台路由确定" />
        <StatTile
          label="接入渠道"
          value={groupIds ? String(groupIds.length) : "—"}
          {...(groupIds ? {} : { unavailable: true, status: <Badge tone="neutral">未接入</Badge> })}
          note={
            groupIds
              ? "按后端 supplier_key 归并的账号数；登记簿的唯一索引今天让它与账号一一对应"
              : summaryMissingNote
          }
        />
        <StatTile
          label="上游余额"
          value={moneyText(summary?.runway.balance)}
          {...(summary?.runway.balance ? {} : { unavailable: true, status: <Badge tone="neutral">未知</Badge> })}
          note={
            summary
              ? (runwayReasonText(summary.runway) ??
                (summary.runway.balanceObservedAt
                  ? `余额观测于 ${summary.runway.balanceObservedAt}`
                  : "上游账号余额或订阅有效期"))
              : summaryMissingNote
          }
        />
        <StatTile
          label="本期总消耗"
          value={moneyText(summary?.usageRevenue)}
          {...(summary?.usageRevenue ? {} : { unavailable: true, status: <Badge tone="neutral">给不出</Badge> })}
          note={summary ? `该上游下所有渠道的我方计费消耗 · ${windowNote}` : summaryMissingNote}
        />
        <StatTile
          label="本期总利润"
          value={moneyText(summary?.grossProfit)}
          {...(summary?.grossProfit ? {} : { unavailable: true, status: <Badge tone="neutral">给不出</Badge> })}
          note={summary ? `我方计费 − 供给成本 · ${windowNote}` : summaryMissingNote}
        />
      </div>

      <div className="grid min-w-0 grid-cols-1 gap-4 lg:grid-cols-2">
        <DetailSection title="上游资料" hint="由平台登记，不从渠道名称或网址反推">
          <DetailList>
            <Fact label="上游 ID">
              <span className="break-all font-mono text-xs">{account.id}</span>
            </Fact>
            <TextFact
              label="上游实际名"
              value={account.upstream_name}
              hint="登记簿的 upstream_name；空串 = 还没登记"
            />
            <TextFact
              label="上游网址"
              value={account.base_url}
              hint="登记簿的 base_url，也是供应商归并键的一半"
            />
            <Fact label="接入平台">
              <Badge tone="info">{label}</Badge>
            </Fact>
            <UnavailableFact
              label="上游账号"
              hint="登记簿没有账号名字段：账号身份在凭据里，平台永不持有（ADR-014）。这里不拿网址或名称冒充账号"
            />
            <Fact label="账号密码状态" hint={cred.hint}>
              <Badge tone={cred.configured ? "success" : "neutral"}>{cred.label}</Badge>
            </Fact>
            <TextFact
              label="联系人"
              value={account.upstream_contact}
              hint="登记簿的 upstream_contact，一个自由文本字段"
            />
            <Fact label="状态">
              <Badge tone={account.status === "active" ? "success" : "neutral"}>{account.status}</Badge>
            </Fact>
            <TextFact label="环境" value={account.environment} />
            <Fact label="接入方式" hint={access.hint}>
              {access.label}
            </Fact>
            <TextFact label="上游分组" value={account.upstream_group} hint="登记簿记下的分组实际名" />
            <TextFact label="登记簿更新时间" value={account.updated_at} />
          </DetailList>
        </DetailSection>

        <DetailSection title="充值比例与成本口径" hint="比例用于解释计量型渠道的上游成本">
          <DetailList>
            <TextFact
              label="当前充值比例"
              value={account.recharge_ratio}
              hint="规范存储量（除数）"
              missing="登记簿没有配倍率——订阅型账号本就没有充值比例"
            />
            <TextFact
              label="充值成本率"
              value={account.recharge_cost_rate}
              hint="成本率是充值比例的展示投影（1 ÷ 倍率），不在前端重复计算，也绝不用它反算成本"
              missing="没有充值比例就没有成本率，后端不编一个 1.000000 冒充「没打折」"
            />
            <TextFact
              label="分组倍率"
              value={account.group_rate}
              hint="独立展示，不并入充值比例，前端绝不拿它去乘任何金额（§10.2）"
              missing="这条账号没有配分组倍率——多数账号的正常状态，不是 1"
            />
            <UnavailableFact
              label="最近一笔比例"
              hint="登记簿只存当前值，没有比例历史表；改倍率的过程留在 finance.recharge_ratio.set 的 ActionRun 与审计里，本页不跨读那条线"
            />
            <UnavailableFact
              label="比例更新时间"
              hint={`登记簿只有整行的 updated_at（${account.updated_at || "未返回"}），没有单给倍率的时间戳——拿整行时间冒充倍率时间，会让「今天没改过倍率」看起来像改过`}
            />
            <Fact label="计量 / 订阅口径" hint="由后端 metered 判定，前端不按 access_method 再判一次">
              {account.metered ? "计量：按实扣 ÷ 倍率折算" : "非计量：订阅摊销或原厂计费"}
            </Fact>
            <TextFact label="币种 / 额度单位" value={account.currency} />
            <TextFact
              label="业务日切时区"
              value={account.business_day_tz}
              hint="展示金额与日期按它解释，不按浏览器时区（宪法 14 条）"
            />
            <TextFact
              label="成本观测时间"
              value={summary?.observed.costObservedAt ?? ""}
              hint="窗口内最旧的成本观测时刻"
              missing={summary ? "窗口内没有已知成本行" : summaryMissingNote}
            />
            <TextFact
              label="收入观测时间"
              value={summary?.observed.revenueObservedAt ?? ""}
              hint="窗口内最旧的收入观测时刻"
              missing={summary ? "窗口内没有已知收入行" : summaryMissingNote}
            />
            <TextFact
              label="数据来源"
              value={summary?.observed.source ?? ""}
              missing={summaryMissingNote}
            />
            <TextFact
              label="毛利率"
              value={selfChannel?.grossMargin ?? ""}
              hint="逐渠道汇总的 gross_margin"
              missing={selfChannel ? "收入 ≤ 0，后端不给毛利率——没有收入谈不上毛利率" : channelsMissingNote}
            />
          </DetailList>
        </DetailSection>

        <DetailSection title="余额与预计补充" hint="余额按上游账号共享，不能在渠道行重复累加">
          <DetailList>
            <TextFact
              label="总余额"
              value={moneyText(summary?.runway.balance)}
              blank="—"
              missing={summary ? (runwayReasonText(summary.runway) ?? "后端未给余额") : summaryMissingNote}
            />
            <UnavailableFact
              label="订阅最近到期"
              hint="不在这里合成一个「最近到期」：每一笔订阅批次的起止就在上方「账号明细 · 订阅批次」表里逐条列着，把它们压成一个日期只会丢掉「还有哪几笔」"
            />
            <TextFact
              label="窗口内日均消耗"
              value={moneyText(summary?.runway.dailyAverage)}
              blank="—"
              {...(summary
                ? { hint: `按近 ${summary.runway.windowDays} 个完整业务日算（实到 ${summary.runway.coveredDays} 天）` }
                : {})}
              missing={summary ? (runwayReasonText(summary.runway) ?? "后端未给日均") : summaryMissingNote}
            />
            <TextFact
              label="预计可用天数"
              value={
                summary?.runway.days === null || summary?.runway.days === undefined
                  ? ""
                  : `约 ${summary.runway.days} 天`
              }
              missing={summary ? (runwayReasonText(summary.runway) ?? "后端未给天数") : summaryMissingNote}
            />
            <UnavailableFact
              label="预计补充时间"
              hint="后端没有这个字段。拿余额除日均外推一个日期是前端造数：那个日期看起来像承诺，而余额采集本身都还没接通"
            />
            <TextFact
              label="余额观测时间"
              value={summary?.runway.balanceObservedAt ?? ""}
              missing={summary ? "还没读到过这个上游的余额" : summaryMissingNote}
            />
            <TextFact
              label="覆盖窗口"
              value={summary ? `覆盖 ${summary.runway.coveredDays}/${summary.runway.windowDays} 天` : ""}
              missing={summaryMissingNote}
            />
            <Fact label="覆盖完整度" hint="两侧都覆盖满且币种单一时，金额才是可断言的（宪法 12 条）">
              {summary ? (
                <>
                  <Badge tone={summary.coverage.complete ? "success" : "warning"}>
                    {summary.coverage.complete ? "完整" : "不完整"}
                  </Badge>
                  <span className="ml-1 text-xs text-fg-muted">
                    收入 {summary.coverage.revenueKnownRows}/{summary.coverage.rowCount} 行 · 成本{" "}
                    {summary.coverage.costKnownRows}/{summary.coverage.rowCount} 行
                    {summary.coverage.mixedCurrency ? " · 币种混杂" : ""}
                  </span>
                </>
              ) : (
                <span className="text-fg-muted">{summaryMissingNote}</span>
              )}
            </Fact>
          </DetailList>
        </DetailSection>

        <DetailSection
          title="联系人与支持"
          hint="登记簿只有一个自由文本的 upstream_contact，没有拆分字段，也不记录联系历史"
        >
          <DetailList>
            <TextFact label="联系人" value={account.upstream_contact} />
            <UnavailableFact
              label="主要沟通渠道"
              hint="登记簿没有这个字段；沟通渠道今天只可能写在 upstream_contact 那一行自由文本里"
            />
            <UnavailableFact
              label="邮箱"
              hint="同上：没有独立的邮箱字段，本页不从自由文本里正则抠一个出来冒充结构化数据"
            />
            <UnavailableFact label="电话" hint="同上，没有独立的电话字段" />
            <UnavailableFact label="最近联系" hint="平台不记录与上游的联系历史，没有任何一张表存这件事" />
            <UnavailableFact label="支持等级" hint="登记簿没有 SLA / 支持等级字段" />
          </DetailList>
        </DetailSection>
      </div>

      <section className="min-w-0 rounded-lg border border-edge bg-surface p-4 shadow-sm">
        <header className="mb-3 flex flex-wrap items-baseline justify-between gap-2">
          <h3 className="text-sm font-semibold text-fg">账号明细</h3>
          <p className="text-xs text-fg-muted">令牌映射 · 订阅批次 · 代理资产；写操作走 Action</p>
        </header>
        {/* 这一整块就是 components/UpstreamAccountDetail：它自 3c97057 起没有
            调用方，四个退款/终止对话框因此在整个后台里点不到。本页只挂载，
            不重写它的任何一行——重写一遍等于把已有测试覆盖的那份逻辑复制成两份 */}
        <UpstreamAccountDetail account={account} onDone={onDone} />
      </section>

      <TableSection
        title="上游全部分组"
        hint="只列平台登记簿记下的分组；上游侧的分组目录没有读契约，未登记的分组不会出现在这里"
        columns={["分组实际名", "分组倍率", "接入状态", "接入平台", "Key / 账号", "可用模型"]}
        empty={
          groupedAccountsWithGroup.length === 0 ? (
            <PageState
              kind="empty"
              compact
              title="登记簿里这些账号都没有填分组"
              description="没填不等于上游那边没有分组：上游侧的分组目录没有读契约，这张表只反映平台登记簿。"
            />
          ) : undefined
        }
      >
        {groupedAccountsWithGroup.map((item) => (
          <tr key={item.id} className="border-b border-edge last:border-b-0">
            <td className={TD}>{item.upstream_group}</td>
            <td className={`${TD} tabular-nums`}>{item.group_rate || "未配置"}</td>
            <td className={TD}>
              <Badge tone={item.status === "active" ? "success" : "neutral"}>{item.status}</Badge>
            </td>
            <td className={TD}>{supplyPlatformLabel(item.system_type)}</td>
            <td className={`${TD} tabular-nums`}>{item.token_mappings.length}</td>
            <td className={TD}>
              <span className="text-fg-muted">—</span>
              <Badge tone="neutral" className="ml-1">
                未接入
              </Badge>
            </td>
          </tr>
        ))}
      </TableSection>

      <ApiStateView isPending={channelsPending} error={channelsError} onRetry={onChannelsRetry} compact>
        <TableSection
          title="关联渠道"
          hint="同一上游下的 Sub2API / NewAPI 渠道分别核算，再汇总到本页"
          columns={["平台", "渠道", "分组 / Key", "供给成本", "我方计费", "毛利", "状态"]}
          empty={
            groupChannels.length === 0 ? (
              <PageState
                kind="empty"
                compact
                title="逐渠道汇总里没有这个上游的行"
                description={
                  groupIds
                    ? "两条汇总是同一批账号的两个投影，正常情况下都该有；只有一边有，说明两次请求落在了不同的窗口或环境。"
                    : "供给汇总还没返回，没法按 supplier_key 归并出这个上游名下有哪些账号。"
                }
              />
            ) : undefined
          }
        >
          {groupChannels.map((item) => {
            const row = accounts.find((a) => a.id === item.id);
            return (
              <tr key={item.id} className="border-b border-edge last:border-b-0">
                <td className={TD}>{supplyPlatformLabel(item.systemType)}</td>
                <td className={TD}>
                  {isSupplyPlatform(item.systemType) ? (
                    <Link
                      to={channelDetailPath(item.systemType, item.id)}
                      className="text-accent hover:underline focus-visible:outline-2 focus-visible:outline-accent"
                    >
                      {item.name || item.id}
                    </Link>
                  ) : (
                    item.name || item.id
                  )}
                </td>
                <td className={TD}>
                  {row?.upstream_group || "未分组"} · {item.tokenCount} 个令牌
                </td>
                <td className={`${TD} tabular-nums`}>{moneyText(item.supplyCost)}</td>
                <td className={`${TD} tabular-nums`}>{moneyText(item.usageRevenue)}</td>
                <td className={`${TD} tabular-nums`}>{moneyText(item.grossProfit)}</td>
                <td className={TD}>
                  <Badge tone={item.status === "active" ? "success" : "neutral"}>{item.status}</Badge>
                </td>
              </tr>
            );
          })}
        </TableSection>
      </ApiStateView>

      <TableSection
        title="上游凭据"
        hint="只显示引用与配置状态"
        columns={["类型", "账号别名", "密钥引用（CredentialRef）", "状态", "最近轮换", "最近验证"]}
      >
        <CredentialRow
          kind="账号级"
          alias={account.upstream_name || account.base_url || account.id}
          credentialRef={account.credential_ref}
        />
        {account.token_mappings.map((mapping) => (
          <CredentialRow
            key={`${mapping.upstream_token_id}:${mapping.own_account_id}`}
            kind="令牌级"
            alias={mapping.own_account_id}
            credentialRef={mapping.credential_ref}
          />
        ))}
      </TableSection>

      <p className="text-xs text-fg-muted">
        上游账号密码、API Key 与 Token 永不回显；表里那一列是取凭据的地址（CredentialRef），不是凭据本身（ADR-014）。
        「最近轮换」在设置 → 凭据管理（凭据元数据的 updated_at 与 version），本页不跨读那条端点；
        「最近验证」平台今天根本不采集——凭据管理只回答「此刻读不读得到」，那不是一个时刻。
      </p>

      <p className="text-xs text-fg-muted">
        上游总利润 = 该上游下所有渠道的我方计费消耗 − 对应渠道供给成本。这三个数由后端按业务日窗口算好，
        本页只做展示与按 supplier_key 的归并，不在前端相加，也不重复套用充值比例。
      </p>
    </div>
  );
}

const TD = "px-3 py-2 align-top text-xs text-fg";

function CredentialRow({ kind, alias, credentialRef }: { kind: string; alias: string; credentialRef: string }) {
  const cred = describeCredential(credentialRef);
  return (
    <tr className="border-b border-edge last:border-b-0">
      <td className={TD}>{kind}</td>
      <td className={`${TD} break-all`}>{alias || "—"}</td>
      <td className={`${TD} break-all font-mono`}>{credentialRef || "—"}</td>
      <td className={TD}>
        <Badge tone={cred.configured ? "success" : "neutral"} title={cred.hint}>
          {cred.label}
        </Badge>
      </td>
      <td className={TD}>
        <span className="text-fg-muted">—</span>
        <Badge tone="neutral" className="ml-1">
          未接入
        </Badge>
      </td>
      <td className={TD}>
        <span className="text-fg-muted">—</span>
        <Badge tone="neutral" className="ml-1">
          未接入
        </Badge>
      </td>
    </tr>
  );
}

function DetailSection({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <section className="min-w-0 rounded-lg border border-edge bg-surface p-4 shadow-sm">
      <header className="mb-3 flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="text-sm font-semibold text-fg">{title}</h3>
        {hint ? <p className="text-xs text-fg-muted">{hint}</p> : null}
      </header>
      {children}
    </section>
  );
}

function DetailList({ children }: { children: ReactNode }) {
  return <dl className="grid min-w-0 grid-cols-1 gap-x-6 gap-y-3 sm:grid-cols-2">{children}</dl>;
}

function Fact({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-fg-muted" title={hint}>
        {label}
      </dt>
      <dd className="mt-0.5 min-w-0 text-sm text-fg">{children}</dd>
    </div>
  );
}

/** 有值就显示值，没值就显示**为什么没值**。
 *
 *  这些契约里的空串一律是「还没登记 / 后端给不出」，不是「值就是空的」——所以
 *  缺省一路走「未接入」那支，并把原因带在 `missing` 上。一句「—」而不说为什么，
 *  与从前整页的「未接入」是同一个毛病：读的人分不清是没配、没采到，还是坏了。
 *
 *  `blank` 给那些本身就有格式化占位的字段用（金额的 `—`）：`moneyText` 给不出时
 *  返回的是 `—` 而不是空串，那一支同样是缺失，不能显示成一个值。 */
function TextFact({
  label,
  value,
  hint,
  missing,
  blank,
}: {
  label: string;
  value: string;
  hint?: string;
  missing?: string;
  blank?: string;
}) {
  const empty = value === "" || (blank !== undefined && value === blank);
  if (empty) return <UnavailableFact label={label} hint={missing ?? hint} />;
  return (
    <Fact label={label} hint={hint}>
      <span className="break-all">{value}</span>
    </Fact>
  );
}

function UnavailableFact({ label, hint }: { label: string; hint?: string }) {
  return (
    <Fact label={label} hint={hint}>
      <span className="text-fg-muted">—</span>
      <Badge tone="neutral" className="ml-1">
        未接入
      </Badge>
    </Fact>
  );
}

/** 表头恒在、表体可空的只读表。
 *
 *  空态**不换掉表头**：列头是这一格的规格，一句「暂无数据」会把「这张表将来有
 *  哪几列」一起吞掉——这一页原来的三张表就是只有列头，那部分是对的，留住。 */
function TableSection({
  title,
  hint,
  columns,
  empty,
  children,
}: {
  title: string;
  hint?: string;
  columns: readonly string[];
  empty?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <section className="min-w-0 rounded-lg border border-edge bg-surface p-4 shadow-sm">
      <header className="mb-3 flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="text-sm font-semibold text-fg">{title}</h3>
        {hint ? <p className="text-xs text-fg-muted">{hint}</p> : null}
      </header>
      <div className="max-w-full overflow-x-auto rounded-md border border-edge">
        <table className="w-full min-w-max border-collapse text-sm">
          <caption className="sr-only">{title}</caption>
          <thead className="border-b border-edge bg-surface-muted">
            <tr>
              {columns.map((column) => (
                <th key={column} scope="col" className="px-3 py-2 text-left text-xs font-medium text-fg-muted">
                  {column}
                </th>
              ))}
            </tr>
          </thead>
          {empty ? null : <tbody>{children}</tbody>}
        </table>
      </div>
      {empty}
    </section>
  );
}
