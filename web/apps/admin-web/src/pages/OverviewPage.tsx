import { useQuery } from "@tanstack/react-query";
import { EmptyState } from "@xingmang/ui-primitives";
import { listMetrics, type MetricItem } from "../api/platform";
import { ApiStateView } from "../components/ApiStateView";
import { MetricCard } from "../components/MetricCard";
import { PageHeader } from "../components/PageHeader";

/** 运营总览：指标卡片网格。
 *
 *  每张卡片都带新鲜度徽章与数据时间——这是规格 §9.1「禁止裸数字冒充实时完整
 *  数据」在看板上的落点，不是装饰。 */
export function OverviewPage() {
  const query = useQuery({
    queryKey: ["metrics"],
    queryFn: ({ signal }) => listMetrics({ signal }),
  });

  return (
    <section>
      <PageHeader
        title="运营总览"
        description="所有数值都带数据时间与新鲜度状态；没有新鲜度就没有数字。"
        onRefresh={() => void query.refetch()}
        refreshing={query.isFetching}
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
        <MetricCard key={`${item.metric_key}@${item.environment}`} item={item} />
      ))}
    </div>
  );
}
