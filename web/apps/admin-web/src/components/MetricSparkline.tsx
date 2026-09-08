import { useQuery } from "@tanstack/react-query";
import { Sparkline, type SparklineBox } from "@xingmang/ui-admin";
import { useEffect, useState } from "react";
import { listMetricHistory, METRIC_HISTORY_HOURS, type MetricItem } from "../api/platform";
import { ApiError } from "../api/client";
import { metricLabel, toSparkSamples } from "../lib/metrics";
import { errorCodeNote } from "../lib/labels";

function Note({ children }: { children: string }) {
  return <p className="text-xs text-fg-muted">{children}</p>;
}

/** 历史 query key 的前缀。
 *
 *  导出成常量而不是各处手写 "metric-history"：总览页的定时刷新要按前缀
 *  invalidate 这一批 query（见 OverviewPage），两处字面量写岔了就会变成
 *  「刷新了个不存在的 key」——而且不报错。 */
export const METRIC_HISTORY_QUERY_PREFIX = "metric-history";

/** 指标卡片里的 24 小时迷你趋势图。
 *
 *  两条约束决定了它的形状：
 *  1. **懒加载**：卡片先渲染出来，挂载之后才去拉历史。总览页上有 N 张卡，
 *     首屏就并发 N 个请求会把主查询挤在后面，人先看到的应该是数值本身。
 *  2. **失败不外溢**：趋势是补充信息。历史接口挂了（甚至还没上线）只影响
 *     这一小块，卡片上的数值、新鲜度徽章一切照常——所以这里自己吃掉错误，
 *     不交给外层的 ApiStateView。
 *  3. **刷新由页面统一驱动**：这里不设 refetchInterval，总览页的定时器会按
 *     METRIC_HISTORY_QUERY_PREFIX 一起 invalidate。折线自己定时的话，页面
 *     不可见时它照拉不误，就绕开了 lib/autoRefresh 里「不可见不拉」那条规矩。 */
export function MetricSparkline({
  item,
  hours = METRIC_HISTORY_HOURS,
  box,
}: {
  item: MetricItem;
  /** 窗口长度。后端上限 168 小时（正好七天），概览页的大图用它。 */
  hours?: number;
  /** 画布尺寸。概览页底部那张大图只是同一条折线换个大盒子——
   *  另写一个图表组件的话，两处的失败样本与部分数据画法迟早会漂开。 */
  box?: SparklineBox;
}) {
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);

  const query = useQuery({
    // hours 进 key：同一条指标的 24h 与 168h 是两份数据，
    // 共用一个 key 会让先到的那份把另一份顶掉
    queryKey: [METRIC_HISTORY_QUERY_PREFIX, item.metric_key, item.environment, hours],
    queryFn: ({ signal }) => listMetricHistory(item.metric_key, { signal, hours }),
    enabled: mounted,
    // 补充信息不值得重试：失败就安静地说一句「趋势不可用」
    retry: false,
  });

  if (!mounted || query.isPending) return <Note>趋势加载中…</Note>;

  if (query.error) {
    const err = query.error;
    // 把错误码留在悬停里：不打扰正常阅读，但报障时能直接说清是哪一类失败
    const detail = err instanceof ApiError ? `${err.message}（${errorCodeNote(err.code)}）` : String(err);
    return (
      <>
        <p className="text-xs text-fg-muted" title={detail}>
          趋势不可用
        </p>
        {/* title 只有鼠标悬停读得到，读屏用户什么都拿不到；把同一句话
            再给一遍 sr-only，报障时两拨人说的是同一个错误码 */}
        <span className="sr-only">趋势不可用：{detail}</span>
      </>
    );
  }

  const samples = toSparkSamples(item.metric_key, query.data ?? []);
  return (
    <Sparkline
      samples={samples}
      label={`${metricLabel(item.metric_key)} 近 ${hours} 小时趋势`}
      {...(box ? { box } : {})}
    />
  );
}
