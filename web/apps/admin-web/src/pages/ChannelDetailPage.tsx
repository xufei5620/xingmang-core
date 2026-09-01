import { useQuery } from "@tanstack/react-query";
import { PageHeader, PageState, StatTile } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";
import { Link, useParams } from "react-router";
import { listServices } from "../api/platform";
import { listPlatformChannels, type PlatformChannelRow } from "../api/platformChannels";
import { accountRowType, describeAccessMethod, listUpstreamAccounts, listUpstreamSummaries, type UpstreamAccountItem, type UpstreamSummary } from "../api/finance";
import { channelFieldNullReason, SCHEDULING_WRITE_HINT, USAGE_WINDOW_SUB2API_HINT } from "../lib/channelFieldReasons";
import { formatScaledMinorUnits } from "../lib/money";
import { runwayReasonText } from "../lib/runway";
import { ApiStateView } from "../components/ApiStateView";
import { ChannelBindingCard } from "../components/ChannelBindingCard";
import { NotFoundView } from "./NotFoundPage";

export type SupplyPlatform = "sub2api" | "newapi";

export const SUPPLY_PLATFORM_LABELS: Record<SupplyPlatform, string> = {
  sub2api: "Sub2API",
  newapi: "NewAPI",
};

/** 渠道/上游详情只在已有两个上游登记簿平台上提供结构壳。 */
export function isSupplyPlatform(value: string): value is SupplyPlatform {
  return value === "sub2api" || value === "newapi";
}

export function supplyPlatformLabel(platform: string): string {
  return isSupplyPlatform(platform) ? SUPPLY_PLATFORM_LABELS[platform] : platform;
}

export function channelDetailPath(platform: string, channelId: string): string {
  return `/platforms/${encodeURIComponent(platform)}/upstream/detail/${encodeURIComponent(channelId)}`;
}

export function upstreamDetailPath(platform: string, upstreamId: string): string {
  return `/platforms/${encodeURIComponent(platform)}/suppliers/${encodeURIComponent(upstreamId)}`;
}

export function upstreamCreatePath(platform: string): string {
  return `/platforms/${encodeURIComponent(platform)}/suppliers/new`;
}

/** 渠道详情。
 *
 *  2026-09-02 起不再是纯 UI-only 壳：渠道目录本身(`GET /api/v1/platforms/{p}/channels`,
 *  XM-C-MAP0 已批的数据契约)在渠道管理列表页上一直是真实数据，这一片第一次
 *  把它接到详情页——按 `service_id + external_channel_id` 从目录里取出这一行,
 *  并把「上游映射」的确认/解绑操作也从列表页搬过来（ManagedChannelTable.tsx
 *  文件头有完整说明）。
 *
 *  07:20 补充裁定进一步要求"登记簿数据并入行与详情页"：这一页因此比列表页
 *  多留了「容量与调度」一整节——8 个字段（容量/并发、调度、今日统计、用量
 *  窗口、最近使用、创建时间、过期时间、代理）今天在渠道目录契约里完全不
 *  存在，要等并行切片 XM-CHAN-FIELDS0 扩展契约后才有；这里先把字段位置和
 *  说明落地，全部显式标未接入，不是这一片的范围去猜它们的值。
 *
 *  仍然诚实：目录没有的字段（经营核算、渠道保障探测结果、凭据别名明细）
 *  继续显式标「未接入」，不因为搬了个位置就编数据。定位这条渠道需要**恰好
 *  一个已登记且 active 的 service**——这与渠道管理列表页切换 ChannelRef
 *  粒度的判据完全一样（见 `ChannelTable.tsx`），多实例或未登记时说明原因,
 *  不猜一个 service 出来。 */
export function ChannelDetailPage() {
  const { serviceType = "", channelId = "" } = useParams();

  if (!isSupplyPlatform(serviceType)) {
    return (
      <NotFoundView
        pathname={`/platforms/${serviceType || "（空平台）"}/upstream/detail/${channelId}`}
        detail={`平台 ${serviceType || "（空平台）"} 不支持渠道详情`}
      />
    );
  }

  if (!channelId.trim()) {
    return (
      <NotFoundView
        pathname={channelDetailPath(serviceType, channelId)}
        detail="渠道 ID 为空，无法定位详情"
      />
    );
  }

  return <ChannelDetailShell platform={serviceType} channelId={channelId} />;
}

function ChannelDetailShell({ platform, channelId }: { platform: SupplyPlatform; channelId: string }) {
  const label = supplyPlatformLabel(platform);
  const backTo = `/platforms/${platform}?tab=upstream`;

  const servicesQuery = useQuery({
    queryKey: ["services"],
    queryFn: ({ signal }) => listServices({ signal }),
  });
  const services = (servicesQuery.data ?? []).filter((s) => s.service_type === platform);
  const singleActive = services.length === 1 && services[0]?.status === "active" ? services[0] : undefined;

  const channelsQuery = useQuery({
    queryKey: ["platform-channels", platform, singleActive?.id],
    queryFn: ({ signal }) => listPlatformChannels(platform, singleActive!.id, { signal }),
    enabled: Boolean(singleActive),
  });

  const accountsQuery = useQuery({
    queryKey: ["finance-upstream-accounts"],
    queryFn: ({ signal }) => listUpstreamAccounts({ signal }),
  });
  const summaryQuery = useQuery({
    queryKey: ["finance", "upstreams", "summary"],
    queryFn: ({ signal }) => listUpstreamSummaries({ signal }),
  });

  const row = channelsQuery.data?.items.find((item) => item.channelRef.externalChannelId === channelId);
  const account = row?.binding
    ? (accountsQuery.data ?? []).find((a) => a.id === row.binding!.upstreamAccountId)
    : undefined;
  const summary = row?.binding
    ? (summaryQuery.data?.items ?? []).find((s) => s.id === row.binding!.upstreamAccountId)
    : undefined;

  return (
    <section className="min-w-0">
      <Link
        to={backTo}
        aria-label={`返回 ${label} 渠道管理`}
        className="mb-3 inline-flex min-h-9 items-center rounded-md px-2 text-sm font-medium text-accent hover:bg-accent-soft focus-visible:outline-2 focus-visible:outline-accent"
      >
        <span aria-hidden="true">←</span>
        <span className="ml-1">返回渠道管理</span>
      </Link>

      <PageHeader
        title="渠道详情"
        description={
          <span className="break-all">
            平台 {label} · 渠道 ID <code className="font-mono">{channelId}</code> · 单渠道经营核算
          </span>
        }
      />

      {!singleActive ? (
        <MissingServiceNotice servicesPending={servicesQuery.isPending} servicesError={servicesQuery.error} count={services.length} />
      ) : (
        <ApiStateView
          isPending={channelsQuery.isPending}
          error={channelsQuery.error}
          onRetry={() => void channelsQuery.refetch()}
        >
          {row ? (
            <ChannelDetailBody
              platform={platform}
              channelId={channelId}
              row={row}
              serviceId={singleActive.id}
              account={account}
              summary={summary}
              onBindingChanged={() => void channelsQuery.refetch()}
            />
          ) : (
            <PageState
              kind="empty"
              title="渠道目录里没有这一条"
              description={`当前 ${label} 渠道目录（共 ${channelsQuery.data?.items.length ?? 0} 条）里没有 ID 为 ${channelId} 的记录；可能已被下线，或链接输入有误。`}
            />
          )}
        </ApiStateView>
      )}
    </section>
  );
}

function MissingServiceNotice({
  servicesPending,
  servicesError,
  count,
}: {
  servicesPending: boolean;
  servicesError: unknown;
  count: number;
}) {
  if (servicesPending) return <PageState kind="loading" />;
  if (servicesError) return <PageState kind="error" message="服务注册表读取失败" />;
  return (
    <PageState
      kind="unavailable"
      title="无法定位这条渠道"
      description={
        count === 0
          ? "当前环境没有登记该平台的 service 实例，渠道目录读不到任何一条渠道。"
          : `当前环境登记了 ${count} 个该平台的 service 实例（或不是 active 状态），无法判定这条渠道属于哪一个——与渠道管理列表页切换到逐渠道视图的判据一致。`
      }
    />
  );
}

function ChannelDetailBody({
  platform,
  channelId,
  row,
  serviceId,
  account,
  summary,
  onBindingChanged,
}: {
  platform: SupplyPlatform;
  channelId: string;
  row: PlatformChannelRow;
  serviceId: string;
  account: UpstreamAccountItem | undefined;
  summary: UpstreamSummary | undefined;
  onBindingChanged: () => void;
}) {
  const label = supplyPlatformLabel(platform);
  const access = account ? describeAccessMethod(account.access_method) : undefined;

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-5">
        <StatTile label="来源平台" value={label} note="由路由平台段确定，不从名称推断" />
        <StatTile
          label="上游分组"
          value={account?.upstream_group || "未接入"}
          {...(!account?.upstream_group ? { unavailable: true, status: <Badge tone="neutral">{row.binding ? "未知" : "未映射"}</Badge> } : {})}
          note={row.binding ? "来自绑定上游账号的登记簿" : "等待渠道绑定与分组目录契约"}
        />
        <StatTile
          label="今日供给成本"
          value="—"
          unavailable
          status={<Badge tone="neutral">未接入</Badge>}
          note={row.economicsState || "服务端尚未按渠道拆分经营字段"}
        />
        <StatTile
          label="今日我方计费"
          value="—"
          unavailable
          status={<Badge tone="neutral">未接入</Badge>}
          note="本渠道令牌产生的计费消耗"
        />
        <StatTile
          label="今日毛利"
          value="—"
          unavailable
          status={<Badge tone="neutral">未接入</Badge>}
          note="我方计费消耗 − 供给成本"
        />
      </div>

      <ChannelBindingCard serviceId={serviceId} row={row} onDone={onBindingChanged} />

      <div className="grid min-w-0 grid-cols-1 gap-4 lg:grid-cols-2">
        <DetailSection title="渠道与映射" hint="一行 = 一个账号/Key 到一个上游分组的映射">
          <DetailList>
            <Fact label="渠道 ID">
              <span className="break-all font-mono text-xs">{channelId}</span>
            </Fact>
            <Fact label="平台">
              <Badge tone="info">{label}</Badge>
            </Fact>
            <Fact label="渠道名称 / 账号">{row.name || "未命名渠道"}</Fact>
            <Fact
              label="类型"
              hint="订阅账号 / 上游渠道；row.kind（XM-CHAN-FIELDS0）优先，没有则按绑定账号的接入方式派生（2026-09-02 07:20/07:25 裁定）"
            >
              {row.binding ? (
                <Badge tone="neutral">
                  {row.kind === "subscription"
                    ? "订阅账号"
                    : row.kind === "upstream"
                      ? "上游渠道"
                      : accountRowType(account?.access_method ?? "").label}
                </Badge>
              ) : (
                <Badge tone="neutral">未映射</Badge>
              )}
            </Fact>
            {row.vendor || account ? (
              <Fact label="来源上游">{row.vendor || account?.upstream_name || account?.base_url || "未接入"}</Fact>
            ) : (
              <UnavailableFact label="来源上游" hint={row.binding ? "已绑定但登记簿里找不到这个账号" : "还没有绑定上游账号"} />
            )}
            {account?.upstream_group ? (
              <Fact label="上游分组实际名">{account.upstream_group}</Fact>
            ) : (
              <UnavailableFact label="上游分组实际名" />
            )}
            <Fact
              label="倍率 / 上游倍率"
              hint="独立展示，不并入充值比例，也不在前端重复乘算；row.rateMultiplier/upstreamMultiplier（XM-CHAN-FIELDS0）优先，没有则退回登记簿"
            >
              {(() => {
                // rateMultiplier/upstreamMultiplier 是数字，group_rate/recharge_ratio
                // 是十进制字符串——显式判 null，不判假值（数字倍率理论上可以是 0）
                const rate = row.rateMultiplier ?? account?.group_rate ?? null;
                const upstreamRate = row.upstreamMultiplier ?? account?.recharge_ratio ?? null;
                if (rate === null && upstreamRate === null) return "未接入";
                return `${rate !== null ? `${rate}×` : "—"}${upstreamRate !== null ? ` / ${upstreamRate}×` : ""}`;
              })()}
            </Fact>
            {access ? (
              <Fact label="接入方式" hint={access.hint}>
                {access.label}
              </Fact>
            ) : (
              <UnavailableFact label="接入方式" />
            )}
            <UnavailableFact label="Key / 账号别名" hint="只显示脱敏别名；完整凭据永不进入页面响应" />
            {typeof row.models?.count === "number" ? (
              <Fact label="可用模型">{row.models.count} 个（仅数量，验证详情见渠道保障 M1.5）</Fact>
            ) : (
              <UnavailableFact label="可用模型" hint="要渠道保障（M1.5）上线后才有" />
            )}
            <Fact label="登记状态">
              {account ? <Badge tone={account.status === "active" ? "success" : "neutral"}>{account.status}</Badge> : "未接入"}
            </Fact>
          </DetailList>
        </DetailSection>

        <DetailSection
          title="容量与调度"
          hint="XM-CHAN-FIELDS0 已交付渠道目录契约；每个未接入字段的原因按平台各不相同，见各字段说明。调度即使有值也只做只读展示，写操作另立 XM-SCHED0"
        >
          <DetailList>
            {row.capacity ? (
              <Fact label="容量 / 并发">
                {row.capacity.used} / {row.capacity.limit}
              </Fact>
            ) : (
              <UnavailableFact label="容量 / 并发" hint={channelFieldNullReason("capacity", platform)} />
            )}
            <Fact
              label="调度"
              hint={
                row.scheduling
                  ? SCHEDULING_WRITE_HINT
                  : `${channelFieldNullReason("scheduling", platform)}；${SCHEDULING_WRITE_HINT}`
              }
            >
              {row.scheduling ? (
                <>
                  {row.scheduling.enabled ? "已开启" : "已关闭"} · 优先级 {row.scheduling.priority}
                </>
              ) : (
                "未接入"
              )}
            </Fact>
            {row.today ? (
              <Fact label="今日统计">
                {/* successRate 是 0-1 小数，显示前乘 100；Sub2API 端这个子字段
                    恒为 null，即使 requests/cost 是真的，不能默认成 0 */}
                {row.today.requests} 次 ·{" "}
                {row.today.successRate === null ? "成功率未接入" : `${(row.today.successRate * 100).toFixed(1)}%`} ·{" "}
                {formatScaledMinorUnits(row.today.costMinor, row.today.currency, row.today.scale)}
              </Fact>
            ) : (
              <UnavailableFact label="今日统计" hint={`请求数 · 成功率 · 消耗。${channelFieldNullReason("today", platform)}`} />
            )}
            {(() => {
              const kind = row.binding ? (row.kind ?? (account ? accountRowType(account.access_method).value : null)) : null;
              if (kind === "upstream") {
                return (
                  <Fact label="用量窗口" hint="上游渠道没有用量窗口这个概念">
                    不适用
                  </Fact>
                );
              }
              if (!row.usageWindow) {
                return <UnavailableFact label="用量窗口" hint={channelFieldNullReason("usageWindow", platform)} />;
              }
              return (
                <Fact label="用量窗口" hint={platform === "sub2api" ? USAGE_WINDOW_SUB2API_HINT : undefined}>
                  {Math.round(row.usageWindow.usedRatio * 100)}%
                  {row.usageWindow.resetsAt ? `，重置于 ${row.usageWindow.resetsAt}` : ""}
                </Fact>
              );
            })()}
            {row.lastUsedAt ? (
              <Fact label="最近使用">{row.lastUsedAt}</Fact>
            ) : (
              <UnavailableFact label="最近使用" hint={channelFieldNullReason("lastUsed", platform)} />
            )}
            {row.createdAt ? (
              <Fact label="创建时间">{row.createdAt}</Fact>
            ) : (
              <UnavailableFact label="创建时间" hint={channelFieldNullReason("createdAt", platform)} />
            )}
            {row.expiresAt ? (
              <Fact label="过期时间">{row.expiresAt}</Fact>
            ) : (
              <UnavailableFact label="过期时间" hint={channelFieldNullReason("expiresAt", platform)} />
            )}
            {row.proxy ? (
              <Fact label="代理">{row.proxy}</Fact>
            ) : (
              <UnavailableFact label="代理" hint={channelFieldNullReason("proxy", platform)} />
            )}
          </DetailList>
        </DetailSection>

        <DetailSection title="经营核算" hint="成本、计费与毛利按渠道单独核算">
          <DetailList>
            <UnavailableFact label="统计区间" />
            <UnavailableFact label="上游供给成本" hint={row.economicsState} />
            <UnavailableFact label="我方计费消耗" hint={row.economicsState} />
            <UnavailableFact label="毛利" hint={row.economicsState} />
            <UnavailableFact label="毛利率" />
            <UnavailableFact label="币种 / 计费单位" />
            <UnavailableFact label="成本口径" hint="计量型按实扣 ÷ 充值比例；订阅型按批次日摊；官方 API 口径待定" />
            <UnavailableFact label="成本观测时间" />
            <UnavailableFact label="计费观测时间" />
            <Fact label="数据来源">{row.economicsState || "服务端未提供"}</Fact>
          </DetailList>
        </DetailSection>

        <DetailSection title="余额与预计补充" hint="余额只作为上游账号共享引用，不在多个渠道重复合计">
          <DetailList>
            {summary?.runway.balance ? (
              <Fact label="上游共享余额">
                {formatScaledMinorUnits(summary.runway.balance.amountMinor, summary.runway.balance.currency, summary.runway.balance.scale)}
              </Fact>
            ) : (
              <UnavailableFact label="上游共享余额" hint={summary ? (runwayReasonText(summary.runway) ?? undefined) : "未绑定上游账号，或汇总还没有这一条"} />
            )}
            <UnavailableFact label="本渠道近 7 日日均消耗" hint="经营字段未按渠道拆分" />
            {summary?.runway.dailyAverage ? (
              <Fact label="上游全部渠道日均">
                {formatScaledMinorUnits(summary.runway.dailyAverage.amountMinor, summary.runway.dailyAverage.currency, summary.runway.dailyAverage.scale)}
              </Fact>
            ) : (
              <UnavailableFact label="上游全部渠道日均" />
            )}
            {summary?.runway.days !== undefined && summary?.runway.days !== null ? (
              <Fact label="预计可用天数">约 {summary.runway.days} 天</Fact>
            ) : (
              <UnavailableFact label="预计可用天数" hint={summary ? (runwayReasonText(summary.runway) ?? undefined) : undefined} />
            )}
            <UnavailableFact label="预计补充时间" />
            <Fact label="充值比例">{account?.recharge_ratio || "未接入"}</Fact>
            <Fact label="充值成本率">{account?.recharge_cost_rate || "未接入"}</Fact>
            <Fact label="余额观测时间">{summary?.runway.balanceObservedAt || "未接入"}</Fact>
            <Fact label="可用天数覆盖窗口">
              {summary ? `覆盖 ${summary.runway.coveredDays}/${summary.runway.windowDays} 天` : "未接入"}
            </Fact>
          </DetailList>
        </DetailSection>

        <DetailSection title="渠道保障" hint="模型检测、成功率与延迟属于独立保障观测">
          <DetailList>
            <UnavailableFact label="最近保障状态" />
            <UnavailableFact label="24h 成功率" />
            <UnavailableFact label="最近模型检测" />
            <UnavailableFact label="已验证模型数" />
            <UnavailableFact label="已配置模型数" />
            <UnavailableFact label="最近检测时间" />
          </DetailList>
        </DetailSection>
      </div>

      <ReadOnlyTableSection
        title="可用模型"
        hint="模型名称、验证结果与最近检测时间"
        columns={["模型", "可用状态", "最近检测", "证据"]}
        description="模型清单属于渠道保障（M1.5）读契约；当前不把空列表解释成「没有可用模型」。"
      />

      <ReadOnlyTableSection
        title="凭据边界"
        hint="只显示引用与状态"
        columns={["类型", "账号别名", "密钥引用（CredentialRef）", "状态", "最近验证"]}
        description="凭据只经 CredentialRef；密码、私钥、Token 与完整 API Key 不进入页面响应。"
      />

      <p className="text-xs text-fg-muted">
        渠道基本信息、上游映射与共享余额来自渠道目录与登记簿的只读 Query；经营核算（成本/计费/毛利）
        与渠道保障探测结果仍等待对应的读契约，未知值不会被折算为 0、1 或「正常」。
      </p>
    </div>
  );
}

function DetailSection({
  title,
  hint,
  children,
}: {
  title: string;
  hint?: string;
  children: ReactNode;
}) {
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

function ReadOnlyTableSection({
  title,
  hint,
  columns,
  description,
}: {
  title: string;
  hint?: string;
  columns: readonly string[];
  description: string;
}) {
  return (
    <section className="min-w-0 rounded-lg border border-edge bg-surface p-4 shadow-sm">
      <header className="mb-3 flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="text-sm font-semibold text-fg">{title}</h3>
        {hint ? <p className="text-xs text-fg-muted">{hint}</p> : null}
      </header>
      <div className="max-w-full overflow-x-auto rounded-md border border-edge">
        <table className="w-full min-w-max border-collapse text-sm">
          <caption className="sr-only">{`${title}（只读结构预览，尚无数据）`}</caption>
          <thead className="border-b border-edge bg-surface-muted">
            <tr>
              {columns.map((column) => (
                <th key={column} scope="col" className="px-3 py-2 text-left text-xs font-medium text-fg-muted">
                  {column}
                </th>
              ))}
            </tr>
          </thead>
        </table>
      </div>
      <PageState kind="unavailable" description={description} compact />
    </section>
  );
}
