import { useQuery } from "@tanstack/react-query";
import { Sparkline } from "@xingmang/ui-admin";
import { useEffect, useState } from "react";
import { listMetricHistory, METRIC_HISTORY_HOURS, type MetricItem } from "../api/platform";
import { ApiError } from "../api/client";
import { metricLabel, toSparkSamples } from "../lib/metrics";

function Note({ children }: { children: string }) {
  return <p className="text-xs text-fg-muted">{children}</p>;
}

/** 指标卡片里的 24 小时迷你趋势图。
 *
 *  两条约束决定了它的形状：
 *  1. **懒加载**：卡片先渲染出来，挂载之后才去拉历史。总览页上有 N 张卡，
 *     首屏就并发 N 个请求会把主查询挤在后面，人先看到的应该是数值本身。
 *  2. **失败不外溢**：趋势是补充信息。历史接口挂了（甚至还没上线）只影响
 *     这一小块，卡片上的数值、新鲜度徽章一切照常——所以这里自己吃掉错误，
 *     不交给外层的 ApiStateView。 */
export function MetricSparkline({ item }: { item: MetricItem }) {
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);

  const query = useQuery({
    queryKey: ["metric-history", item.metric_key, item.environment, METRIC_HISTORY_HOURS],
    queryFn: ({ signal }) => listMetricHistory(item.metric_key, { signal }),
    enabled: mounted,
    // 补充信息不值得重试：失败就安静地说一句「趋势不可用」
    retry: false,
  });

  if (!mounted || query.isPending) return <Note>趋势加载中…</Note>;

  if (query.error) {
    const err = query.error;
    // 把错误码留在悬停里：不打扰正常阅读，但报障时能直接说清是哪一类失败
    const detail = err instanceof ApiError ? `${err.message}（错误码 ${err.code}）` : String(err);
    return (
      <p className="text-xs text-fg-muted" title={detail}>
        趋势不可用
      </p>
    );
  }

  const samples = toSparkSamples(item.metric_key, query.data ?? []);
  return (
    <Sparkline
      samples={samples}
      label={`${metricLabel(item.metric_key)} 近 ${METRIC_HISTORY_HOURS} 小时趋势`}
    />
  );
}
