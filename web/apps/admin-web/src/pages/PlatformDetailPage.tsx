import { useQuery, useQueryClient } from "@tanstack/react-query";
import { PageHeader } from "@xingmang/ui-admin";
import { EmptyState, Tabs } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";
import { useParams, useSearchParams } from "react-router";
import { listMetrics, listServices } from "../api/platform";
import { ApiStateView } from "../components/ApiStateView";
import { ChannelsPanel } from "../components/ChannelsPanel";
import { NewApiChannelsPanel } from "../components/NewApiChannelsPanel";
import { MetricCardGrid } from "../components/MetricCardGrid";
import { METRIC_HISTORY_QUERY_PREFIX } from "../components/MetricSparkline";
import {
  findPlatform,
  normalizeTab,
  pendingHeadline,
  platformOfMetricKey,
  platformOpens,
  PLATFORM_TABS,
  type PlatformEntry,
  type PlatformTabValue,
} from "../lib/platforms";

/** 平台详情页：全平台共用的统一页签模板（ADMIN-IA 二、统一页签模板）。
 *
 *  这一页是 XM-0034 的主要产出。以后接入一个平台 = 把这六格填上，
 *  不再各自发明一套页面结构与命名——「统一命名」这句话的落点就在这里。
 *
 *  页签选择放在 URL 的 `?tab=` 上而不是组件 state：这样某一格是可以贴给同事的
 *  地址，旧的 /channels 书签也才有地方可以重定向过去。 */
export function PlatformDetailPage() {
  const params = useParams();
  const serviceType = params.serviceType ?? "";
  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab = normalizeTab(searchParams.get("tab"));
  const queryClient = useQueryClient();

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
    setSearchParams(next, { replace: true });
  };

  // 刷新把三样一起拉：注册状态、指标卡、卡下面那条折线。少刷任何一样，
  // 屏幕上就会有一块停在旧时刻却不声张（与总览页同一条理由）
  const refreshAll = () => {
    void servicesQuery.refetch();
    void queryClient.invalidateQueries({ queryKey: ["metrics"] });
    void queryClient.invalidateQueries({ queryKey: [METRIC_HISTORY_QUERY_PREFIX] });
  };

  return (
    <section>
      <PageHeader
        title={entry?.spec.label ?? serviceType}
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
          onTabChange={selectTab}
        />
      </ApiStateView>
    </section>
  );
}

function PlatformBody({
  entry,
  serviceType,
  activeTab,
  onTabChange,
}: {
  entry: PlatformEntry | undefined;
  serviceType: string;
  activeTab: PlatformTabValue;
  onTabChange: (value: string) => void;
}) {
  if (!entry) {
    return (
      <EmptyState
        title="未知平台"
        description={`平台目录与服务注册表里都没有 ${serviceType}；地址可能已过期或拼写有误`}
      />
    );
  }

  // 未接入的平台给一整屏占位，而不是六个空页签：页签摆在那里等于承诺点进去有东西，
  // 而这里一格都还没有。§12 惯例要的是「显示为未接入」，不是「显示成接入了但空」
  // 判据是「点进去有没有东西」而不是「注册表里有没有」——与导航共用同一个
  // 判据，否则会出现导航亮着链接、点进来却是一屏「未接入」（见 platformOpens）
  if (!platformOpens(entry)) {
    // 这一句话的范围已经由页头的 description 给出（对所有平台都一样），
    // 占位里不再重复一遍——同一句话在同一屏出现两次，读的人会以为是两件事
    return (
      <EmptyState
        title={pendingHeadline(entry.spec.plan)}
        description={`接入后本页按统一页签模板展开：${PLATFORM_TABS.map((tab) => tab.label).join(" / ")}`}
      />
    );
  }

  return (
    <Tabs
      value={activeTab}
      onValueChange={onTabChange}
      items={PLATFORM_TABS.map((tab) => ({
        value: tab.value,
        label: tab.label,
        content: tabContent(tab.value, entry),
      }))}
    />
  );
}

/** 一格页签的内容。
 *
 *  没实现的格子渲染诚实占位：一句话说明将来放什么，以及归哪个任务。
 *  空白页签会让人以为「这个平台没有告警」——而事实是这块还没建。 */
function tabContent(tab: PlatformTabValue, entry: PlatformEntry): ReactNode {
  const { spec } = entry;
  switch (tab) {
    case "overview":
      return <PlatformOverview serviceType={spec.serviceType} label={spec.label} />;
    case "trends":
      return (
        <EmptyState
          title="指标趋势尚未实现"
          description="将显示本平台全部 metric 的历史曲线。历史观测接口（XM-0024）已就绪，页面待建；ADMIN-IA 未给该页签指派任务号。"
        />
      );
    case "resources":
      // 两个平台的「资源」都是渠道，但**指标形状不同**（sub2api 是余额+令牌，
      // newapi 是启停+错误率+延迟），所以是两个组件而不是一个带参数的通用表：
      // 硬凑成一张表要么列对不上，要么长出一堆各平台各半空的列。
      switch (spec.serviceType) {
        case "sub2api":
          // Sub2API 的资源就是渠道，页面已有（原 /channels 整体迁入）
          return <ChannelsPanel />;
        case "newapi":
          return <NewApiChannelsPanel />;
        default:
          return (
            <EmptyState
              title="渠道/资源尚未实现"
              description={`将显示 ${spec.label} 的资源清单（渠道／账号／模型，按该平台的语义）。`}
            />
          );
      }
    case "connection":
      return (
        <EmptyState
          title="连接与凭据尚未实现"
          description="将显示 Connection 状态、CredentialRef（永不显示明文）与 Kill Switch。ADMIN-IA 未给该页签指派任务号。"
        />
      );
    case "alerts":
      return (
        <EmptyState
          title="告警尚未实现"
          description="将显示全局告警中心按本平台过滤的视图，随 XM-0033 告警中心上线。"
        />
      );
    case "operations":
      return (
        <EmptyState
          title="操作尚未实现"
          description="将列出本平台可用的 Action；L2 及以上标注「需审批（F-B）」，随 XM-0030 Action Advanced Controls 上线。"
        />
      );
  }
}

/** 概览页签：本平台的指标卡。
 *
 *  按 metric_key 的平台前缀过滤，所以这一格对任何平台都成立，不是 Sub2API 专用：
 *  下一个平台的指标一开始上报，它的概览就自动有内容。 */
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
      {/* ADMIN-IA 给概览的内容契约是「关键指标卡 + 新鲜度 + 活动告警数」。
          前两样在卡片里，第三样还没有——缺了就说缺了，不装作契约已经满足 */}
      <p className="text-xs text-fg-muted">活动告警数随 XM-0033 告警中心上线后补上。</p>
    </div>
  );
}
