import { MetricCard as MetricCardView } from "@xingmang/ui-admin";
import type { ReactNode } from "react";
import type { MetricItem } from "../api/platform";
import { presentMetric } from "../lib/metrics";

export interface MetricCardProps {
  item: MetricItem;
  /** 趋势图槽位，透传给展示组件。 */
  trend?: ReactNode;
  /** 平台入口槽位（「查看平台 →」），透传给展示组件。 */
  link?: ReactNode;
}

/** 指标卡的应用侧适配层：把 MetricItem 按本应用的口径解释成可显示的文本，
 *  再交给 ui-admin 的展示组件画。
 *
 *  拆成两层是因为这两件事的归属不同：**长什么样**属于设计系统（要能在
 *  Storybook 里独立摆出八种状态，不能拖着 metric_key 注册表和金额格式化一起进去），
 *  **一个 metric_key 该显示成什么**属于应用（口径来自各 connector 的契约，
 *  见 lib/metrics）。合在一起的话，Storybook 里每摆一个状态都要先编一份
 *  后端返回值，摆出来的却仍然只是 presentMetric 的输出。 */
export function MetricCard({ item, trend, link }: MetricCardProps) {
  const shown = presentMetric(item);
  return (
    <MetricCardView
      label={shown.label}
      metricKey={item.metric_key}
      value={shown.primary}
      unavailable={shown.unavailable}
      secondary={shown.secondary}
      freshness={item.freshness}
      source={item.source}
      watermark={item.watermark}
      trend={trend}
      link={link}
    />
  );
}
