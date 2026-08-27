import { useQuery } from "@tanstack/react-query";
import { EmptyState } from "@xingmang/ui-primitives";
import { listMetrics, METRIC_HISTORY_HOURS, type MetricItem } from "../api/platform";
import { ApiStateView } from "../components/ApiStateView";
import { MetricCard } from "../components/MetricCard";
import { MetricSparkline } from "../components/MetricSparkline";
import { PageHeader } from "../components/PageHeader";
import { OVERVIEW_POLL_INTERVAL_MS, useAutoRefresh } from "../lib/autoRefresh";

/** 运营总览：指标卡片网格 + 24 小时趋势。
 *
 *  每张卡片都带新鲜度徽章与数据时间——这是规格 §9.1「禁止裸数字冒充实时完整
 *  数据」在看板上的落点，不是装饰。
 *
 *  自动轮询只在页面可见时进行（见 lib/autoRefresh），并且**不取代**手动刷新
 *  按钮：出事时人要能立刻要一次新的，而不是等下一个 60 秒。 */
export function OverviewPage() {
  const query = useQuery({
    queryKey: ["metrics"],
    queryFn: ({ signal }) => listMetrics({ signal }),
  });

  useAutoRefresh(() => void query.refetch());

  return (
    <section>
      <PageHeader
        title="运营总览"
        description={`所有数值都带数据时间与新鲜度状态；没有新鲜度就没有数字。折线为近 ${METRIC_HISTORY_HOURS} 小时趋势，每 ${OVERVIEW_POLL_INTERVAL_MS / 1000} 秒自动刷新（页面不可见时暂停）。`}
        onRefresh={() => void query.refetch()}
        refreshing={query.isFetching}
        // dataUpdatedAt 是「最近一次成功取到数据」的时刻，不是最近一次发起请求：
        // 请求失败时这行字不该往前跳，否则人会以为看到的是新数据
        lastRefreshedAt={query.dataUpdatedAt || undefined}
      />
      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        <MetricGrid items={query.data ?? []} />
      </ApiStateView>
    </section>
  );
}

function MetricGrid({ items }: { items: MetricItem[] }) {
  if (items.length === 0) {
    return (
      <EmptyState
        title="暂无指标"
        description="该环境下还没有采集到任何指标观测；接入 Connector 采集任务后会出现在这里"
      />
    );
  }
  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
      {items.map((item) => (
        <MetricCard
          key={`${item.metric_key}@${item.environment}`}
          item={item}
          trend={<MetricSparkline item={item} />}
        />
      ))}
    </div>
  );
}
