import { useQuery, useQueryClient } from "@tanstack/react-query";
import { PageHeader, type PlatformTabSpec } from "@xingmang/ui-admin";
import { Badge, EmptyState, Tabs } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";
import { useParams, useSearchParams } from "react-router";
import { listMetrics, listServices } from "../api/platform";
import { platformHasUsers } from "../api/users";
import { BlueprintTabView, blueprintTabForPlatform } from "../blueprints";
import { ApiStateView } from "../components/ApiStateView";
import { ChannelsPanel } from "../components/ChannelsPanel";
import { FinanceSummaryCards } from "../components/FinanceSummaryCards";
import { NewApiChannelsPanel } from "../components/NewApiChannelsPanel";
import { MetricCardGrid } from "../components/MetricCardGrid";
import { METRIC_HISTORY_QUERY_PREFIX } from "../components/MetricSparkline";
import { assuranceSubTab } from "../components/PlatformAssurancePanel";
import { financeSubTab } from "../components/PlatformFinancePanel";
import { PlatformUsersPanel } from "../components/PlatformUsersPanel";
import { RequestsPanel } from "../components/RequestsPanel";
import {
  findPlatform,
  pendingBadge,
  pendingHeadline,
  platformOfMetricKey,
  platformOpens,
  resolvePlatformTab,
  tabsForPlatform,
  DEFAULT_PLATFORM_TAB,
  type PlatformEntry,
} from "../lib/platforms";

/** 平台详情页。页签集合**按平台各不相同**（ADMIN-IA v3 §2.1）。
 *
 *  这是 XM-0042 换掉的那件事：v2 是「全平台统一 6~7 格模板」，而原型给 4 个平台
 *  画的是 4 套不同的页签条（9 / 9 / 5 / 7）。统一模板看着整齐，代价是每个平台都
 *  有几格是空的、又缺几格它真正需要的——服务器需要「域名与证书」，而模板里没有。
 *
 *  页签选择放在 URL 的 `?tab=` 上而不是组件 state：这样某一格是可以贴给同事的
 *  地址，旧的 /channels 书签也才有地方可以重定向过去。子页签同理走 `?sub=`。 */
export function PlatformDetailPage() {
  const params = useParams();
  const serviceType = params.serviceType ?? "";
  const [searchParams, setSearchParams] = useSearchParams();
  const queryClient = useQueryClient();

  // 旧页签名改跳、认不出来的 404，都已经在路由 loader 里处理掉了
  // (见 router 的 platformTabLoader)，到这里剩下的一定是合法值
  const resolution = resolvePlatformTab(serviceType, searchParams.get("tab"));
  const activeTab = resolution.kind === "ok" ? resolution.tab : DEFAULT_PLATFORM_TAB;

  const servicesQuery = useQuery({
    queryKey: ["services"],
    queryFn: ({ signal }) => listServices({ signal }),
  });

  const entry = findPlatform(serviceType, servicesQuery.data ?? []);

  // 换页签用 replace：连点五个页签不该在浏览器里堆五条历史，
  // 否则「后退」变成逐格倒着走，而人想回的是上一个页面
  const selectTab = (value: string) => {
    const next = new URLSearchParams(searchParams);
    next.set("tab", value);
    // 换大页签时把子页签清掉：`?sub=` 属于上一格，带着它跳过去要么无效、
    // 要么恰好撞上新格子里的同名子页签，后者比无效更难发现
    next.delete("sub");
    setSearchParams(next, { replace: true });
  };

  const selectSub = (value: string) => {
    const next = new URLSearchParams(searchParams);
    next.set("sub", value);
    setSearchParams(next, { replace: true });
  };

  // 刷新把三样一起拉：注册状态、指标卡、卡下面那条折线。少刷任何一样，
  // 屏幕上就会有一块停在旧时刻却不声张（与总览页同一条理由）
  const refreshAll = () => {
    void servicesQuery.refetch();
    void queryClient.invalidateQueries({ queryKey: ["metrics"] });
    void queryClient.invalidateQueries({ queryKey: [METRIC_HISTORY_QUERY_PREFIX] });
  };

  const pending = entry && !platformOpens(entry);

  return (
    <section>
      <PageHeader
        title={entry?.spec.label ?? serviceType}
        // 状态与标题同层（§11.2）：「服务器」和「服务器·未接入·M2」在运营眼里
        // 是完全不同的两页，这件事不能等人滚到页面中间才发现
        status={
          pending ? (
            <Badge tone="warning" title={pendingHeadline(entry.spec.plan)}>
              {pendingBadge(entry.spec.plan)}
            </Badge>
          ) : null
        }
        description={entry?.spec.scope}
        onRefresh={refreshAll}
        refreshing={servicesQuery.isFetching}
        lastRefreshedAt={servicesQuery.dataUpdatedAt || undefined}
      />
      <ApiStateView
        isPending={servicesQuery.isPending}
        error={servicesQuery.error}
        onRetry={() => void servicesQuery.refetch()}
      >
        <PlatformBody
          entry={entry}
          serviceType={serviceType}
          activeTab={activeTab}
          activeSub={searchParams.get("sub")}
          onTabChange={selectTab}
          onSubChange={selectSub}
        />
      </ApiStateView>
    </section>
  );
}

function PlatformBody({
  entry,
  serviceType,
  activeTab,
  activeSub,
  onTabChange,
  onSubChange,
}: {
  entry: PlatformEntry | undefined;
  serviceType: string;
  activeTab: string;
  activeSub: string | null;
  onTabChange: (value: string) => void;
  onSubChange: (value: string) => void;
}) {
  if (!entry) {
    return (
      <EmptyState
        title="未知平台"
        description={`平台目录与服务注册表里都没有 ${serviceType}；地址可能已过期或拼写有误`}
      />
    );
  }

  const tabs = tabsForPlatform(serviceType);
  if (tabs.length === 0) {
    // Registry 里登记了、但 ADMIN-IA 还没给它定页签。不编一套模板套上去：
    // 编出来的六格页签会让人以为我们已经想清楚这个平台该怎么管
    return (
      <EmptyState
        title="这个平台还没有页签定义"
        description={`${serviceType} 在服务注册表里有登记，但 ADMIN-IA 还没给它定义页签集合。要接入它，先在 docs/architecture/ADMIN-IA.md 里补一行，再改 ui-admin 的 navigation.ts。`}
      />
    );
  }

  return (
    <Tabs
      value={activeTab}
      onValueChange={onTabChange}
      items={tabs.map((tab) => ({
        value: tab.value,
        label: tab.label,
        content: (
          <TabBody tab={tab} entry={entry} activeSub={activeSub} onSubChange={onSubChange} />
        ),
      }))}
    />
  );
}

/** 一格页签的内容。有子页签的先展开子页签条，再往里放内容。 */
function TabBody({
  tab,
  entry,
  activeSub,
  onSubChange,
}: {
  tab: PlatformTabSpec;
  entry: PlatformEntry;
  activeSub: string | null;
  onSubChange: (value: string) => void;
}) {
  if (tab.subTabs.length === 0) return tabContent(tab, entry);

  const active = activeSub === null || activeSub === "" ? tab.subTabs[0]?.id : activeSub;
  if (!tab.subTabs.some((sub) => sub.id === active)) {
    // 与未知 `?tab=` 同一条规矩：不静默回落第一格（交接文档 §8）。
    // 这里用 EmptyState 而不是整页的 NotFoundView——页头已经是平台名了，
    // 再叠一个「页面不存在」的标题，人会以为整个平台都没了
    return (
      <EmptyState
        title="没有这个子页签"
        description={`「${tab.label}」下没有名为 ${activeSub} 的子页签；地址可能已过期或拼写有误。本格现有：${tab.subTabs.map((s) => s.label).join(" / ")}。`}
      />
    );
  }

  return (
    <Tabs
      value={active}
      onValueChange={onSubChange}
      items={tab.subTabs.map((sub) => ({
        value: sub.id,
        label: sub.label,
        content: subTabContent(tab, sub.id, entry) ?? (
          <EmptyState
            title={`「${sub.label}」尚未实现`}
            description={pendingNote(entry, tab)}
          />
        ),
      }))}
    />
  );
}

/** 有内容的子页签走各自的面板;没有的返回 undefined,由调用方回落到通用占位。
 *
 *  做成一个解析器而不是在 TabBody 里堆 switch:页签内容是**按片交付**的,
 *  每一片只往这里加一行,不必碰渲染逻辑。 */
function subTabContent(
  tab: PlatformTabSpec,
  subId: string,
  entry: PlatformEntry,
): ReactNode | undefined {
  switch (tab.value) {
    case "model":
      // 渠道保障:UI 蓝图态。布局与文案照原型,数据一行都没有——
      // 交接文档 §9.7 明写「真实探针不能提前冒充已上线」
      return assuranceSubTab(subId);
    case "finance":
      return financeSubTab(entry.spec.serviceType, subId);
    default:
      return undefined;
  }
}

/** 未实装页签的一句话。
 *
 *  说清楚两件事：这一格现在为什么空（本片只搬导航），以及它归哪个阶段。
 *  空白页签会让人以为「这个平台没有告警」——而事实是这块还没建。 */
function pendingNote(entry: PlatformEntry, tab: PlatformTabSpec): string {
  const stage = tab.stage ?? (entry.spec.plan.kind === "milestone" ? entry.spec.plan.milestone : "");
  const suffix = stage ? `阶段 ${stage}。` : "";
  return `XM-0042 只重构了导航与路由：这一格的位置、命名与地址已经定下来，内容按实施计划的后续切片实现。${suffix}`;
}

function tabContent(tab: PlatformTabSpec, entry: PlatformEntry): ReactNode {
  const { spec } = entry;
  switch (tab.value) {
    case "overview":
      return <PlatformOverview serviceType={spec.serviceType} label={spec.label} />;
    case "upstream":
      // 两个平台的渠道表**指标形状不同**(sub2api 是余额+令牌，newapi 是启停+
      // 错误率+延迟)，所以是两个组件而不是一个带参数的通用表：硬凑成一张表
      // 要么列对不上，要么长出一堆各平台各半空的列。
      //
      // 原型把 v2 的「渠道/资源」拆成了「渠道管理」(一行=一个账号/一把 Key)与
      // 「上游管理」（按上游供应商汇总）两格。现有面板是前者，原样挂在这里；
      // 收窄语义与单渠道毛利核算属于第 5 片（依赖 XM-0037 成本线）
      switch (spec.serviceType) {
        case "sub2api":
          return <ChannelsPanel />;
        case "newapi":
          return <NewApiChannelsPanel />;
        default:
          return <EmptyState title={`「${tab.label}」尚未实现`} description={pendingNote(entry, tab)} />;
      }
    case "users":
      // 逐用户资金明细。邮箱在**连接器**层就打了码,平台不持有明文;
      // 逐用户充值/消费在 v1 上游契约里给不出,面板里逐格说明(原型 warnbar)
      return platformHasUsers(spec.serviceType) ? (
        <PlatformUsersPanel platform={spec.serviceType} />
      ) : (
        <EmptyState title={`「${tab.label}」尚未实现`} description={pendingNote(entry, tab)} />
      );
    case "usage":
      // 数据来自生产上已在运行的外挂请求审计系统，平台只是带权限与审计的
      // 只读网关——正文永不落平台库。脱敏、`request.content.read` 与查看审计
      // 属于第 8 片
      return <RequestsPanel platform={spec.serviceType} />;
    default: {
      // 有蓝图规格的平台页签走蓝图（UI 第 6 片，目前只有服务器的 7 格）：
      // 页签结构与列头照原型，数字一个不显示。没有的仍是那句「尚未实现」。
      const blueprint = blueprintTabForPlatform(spec.serviceType, tab.value);
      if (blueprint) return <BlueprintTabView tab={blueprint} />;
      return <EmptyState title={`「${tab.label}」尚未实现`} description={pendingNote(entry, tab)} />;
    }
  }
}

/** 概览页签：本平台的指标卡。
 *
 *  按 metric_key 的平台前缀过滤，所以这一格对任何平台都成立，不是 Sub2API 专用：
 *  下一个平台的指标一开始上报，它的概览就自动有内容。
 *
 *  裁定 #3 砍掉了「指标趋势」页签之后，历史曲线的落点就是这里的卡片
 *  (MetricCard 里的 Sparkline)——功能没丢，只是不再单列一格。 */
function PlatformOverview({ serviceType, label }: { serviceType: string; label: string }) {
  const query = useQuery({
    queryKey: ["metrics"],
    queryFn: ({ signal }) => listMetrics({ signal }),
  });

  const items = (query.data ?? []).filter(
    (item) => platformOfMetricKey(item.metric_key) === serviceType,
  );

  return (
    <div className="flex flex-col gap-3">
      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        <MetricCardGrid
          items={items}
          emptyTitle="暂无本平台指标"
          emptyDescription={`该环境下还没有 ${serviceType}.* 的指标观测；${label} 的采集任务跑起来后会出现在这里`}
        />
      </ApiStateView>
      {/* 成本三卡只挂在**计量型上游**那两个平台上（XM-0037d）。
          CPA 与服务器没有上游账号，给它们挂一组恒为「未接入」的成本卡，
          等于把一句「这里本来就没有这个概念」显示成一处缺口。 */}
      {FINANCE_CARD_PLATFORMS.has(serviceType) ? (
        <FinanceSummaryCards systemType={serviceType} label={label} />
      ) : null}
      {/* ADMIN-IA 给概览的内容契约是「关键指标卡 + 新鲜度 + 活动告警数」。
          前两样在卡片里，第三样还没有——缺了就说缺了，不装作契约已经满足 */}
      <p className="text-xs text-fg-muted">活动告警数随第 3 片（运营工作台）一并补上。</p>
    </div>
  );
}

/** 挂成本三卡的平台。
 *
 *  与 `finance.upstream_account.system_type` 的取值对齐（`sub2api` / `newapi`）：
 *  卡片按 systemType 过滤渠道，platform 的 serviceType 与它同名不是巧合，
 *  是登记簿刻意用了同一套标识。第三个取值 `official` 没有对应的平台页
 *  （官方 API 直连的成本口径 v1 占位后置），所以不在这里。 */
const FINANCE_CARD_PLATFORMS: ReadonlySet<string> = new Set(["sub2api", "newapi"]);
