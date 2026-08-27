import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { Link } from "react-router";
import { listAlerts } from "../api/alerts";
import { listMetrics, METRIC_HISTORY_HOURS, type MetricItem } from "../api/platform";
import { METRIC_HISTORY_QUERY_PREFIX } from "../components/MetricSparkline";
import { AlertSummaryCard } from "../components/AlertSummaryCard";
import { ApiStateView } from "../components/ApiStateView";
import { MetricCardGrid } from "../components/MetricCardGrid";
import { PageHeader } from "../components/PageHeader";
import { OVERVIEW_POLL_INTERVAL_MS, useAutoRefresh } from "../lib/autoRefresh";
import { PLATFORM_CATALOG, platformOfMetricKey } from "../lib/platforms";

/** 运营总览：指标卡片网格 + 24 小时趋势。
 *
 *  每张卡片都带新鲜度徽章与数据时间——这是规格 §9.1「禁止裸数字冒充实时完整
 *  数据」在看板上的落点，不是装饰。
 *
 *  自动轮询只在页面可见时进行（见 lib/autoRefresh），并且**不取代**手动刷新
 *  按钮：出事时人要能立刻要一次新的，而不是等下一个 60 秒。
 *
 *  XM-0034 起每张卡片多一个平台入口：总览回答「哪里不对劲」，平台页回答
 *  「这个系统到底怎么了」，两问之间不该让人回到导航上重新找一遍。 */
export function OverviewPage() {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["metrics"],
    queryFn: ({ signal }) => listMetrics({ signal }),
  });
  // 告警走**独立**的 query，不并进 metrics：两者的失败必须能分开显示。
  // 合成一条的话，指标端点挂掉会把告警卡一起换成错误态，而那正是最需要
  // 看见告警的时候（规格 §9.2 把告警列为总览的固定一项）。
  const alertsQuery = useQuery({
    queryKey: ["alerts", "active"],
    queryFn: ({ signal }) => listAlerts({ signal }),
  });

  // 手动刷新与自动刷新走同一条路：卡片数值和它下面那条折线必须一起更新。
  // 折线挂在独立的 ['metric-history', ...] key 上，只 refetch ['metrics'] 的话
  // 主数字会走、折线永远停在首次加载那一刻——页头却写着「每 60 秒刷新」，
  // 于是界面自己说了一句假话（Codex #4）
  const refreshAll = () => {
    void query.refetch();
    void alertsQuery.refetch();
    void queryClient.invalidateQueries({ queryKey: [METRIC_HISTORY_QUERY_PREFIX] });
  };

  useAutoRefresh(refreshAll);

  return (
    <section>
      <PageHeader
        title="运营总览"
        description={`所有数值都带数据时间与新鲜度状态；没有新鲜度就没有数字。折线为近 ${METRIC_HISTORY_HOURS} 小时趋势，每 ${OVERVIEW_POLL_INTERVAL_MS / 1000} 秒自动刷新（页面不可见时暂停）。`}
        onRefresh={refreshAll}
        refreshing={query.isFetching || alertsQuery.isFetching}
        // dataUpdatedAt 是「最近一次成功取到数据」的时刻，不是最近一次发起请求：
        // 请求失败时这行字不该往前跳，否则人会以为看到的是新数据
        lastRefreshedAt={query.dataUpdatedAt || undefined}
      />
      {/* 告警卡在指标网格**上方**：它回答的是「现在有什么要处理」，
          比任何一个数值都优先。它有自己的加载/错误态，指标端点挂掉时
          这张卡照常显示。 */}
      <div className="mb-4">
        <ApiStateView
          isPending={alertsQuery.isPending}
          error={alertsQuery.error}
          onRetry={() => void alertsQuery.refetch()}
        >
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            <AlertSummaryCard alerts={alertsQuery.data ?? []} />
          </div>
        </ApiStateView>
      </div>

      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        <MetricCardGrid
          items={query.data ?? []}
          emptyTitle="暂无指标"
          emptyDescription="该环境下还没有采集到任何指标观测；接入 Connector 采集任务后会出现在这里"
          renderLink={platformLink}
        />
      </ApiStateView>
    </section>
  );
}

/** 指标卡片上的平台入口。
 *
 *  只有指标键的平台前缀能在平台目录里找到时才给链接：给一个目录里没有的平台
 *  挂入口，点进去只会是一屏「未知平台」，还不如没有这个入口。 */
function platformLink(item: MetricItem): ReactNode {
  const serviceType = platformOfMetricKey(item.metric_key);
  const spec = PLATFORM_CATALOG.find((platform) => platform.serviceType === serviceType);
  if (!spec) return null;
  return (
    <Link
      to={`/platforms/${spec.serviceType}`}
      // 可见文案对所有卡片都一样，但读屏用户是按链接列表逐条听的，
      // 一连五个「查看平台」分不出彼此——平台名补在无障碍名称里
      aria-label={`查看平台 ${spec.label}`}
      className="text-xs font-medium text-accent hover:underline"
    >
      查看平台 →
    </Link>
  );
}
