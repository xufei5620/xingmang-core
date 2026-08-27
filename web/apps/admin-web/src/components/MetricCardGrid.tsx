import { EmptyState } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";
import type { MetricItem } from "../api/platform";
import { MetricCard } from "./MetricCard";
import { MetricSparkline } from "./MetricSparkline";

export interface MetricCardGridProps {
  items: MetricItem[];
  /** 空态文案由调用方给：全局总览要说「这个环境还没采到任何指标」，
   *  平台页要说「这个平台还没有指标」。同一句话套两个场景，必然有一句是错的。 */
  emptyTitle: string;
  emptyDescription: string;
  /** 每张卡片的平台入口。不传就不渲染——平台详情页自己已经是终点了。 */
  renderLink?: (item: MetricItem) => ReactNode;
}

/** 指标卡片网格。
 *
 *  抽成共享组件而不是各画一遍：全局运营总览与平台详情的「概览」页签画的是同一种
 *  东西——同一批卡片、同一条趋势、同一套新鲜度徽章。复制一份的话，下一次调整
 *  新鲜度呈现时只会改到其中一处，而两处都在对人说「这是最新的数」。 */
export function MetricCardGrid({
  items,
  emptyTitle,
  emptyDescription,
  renderLink,
}: MetricCardGridProps) {
  if (items.length === 0) {
    return <EmptyState title={emptyTitle} description={emptyDescription} />;
  }
  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
      {items.map((item) => (
        <MetricCard
          key={`${item.metric_key}@${item.environment}`}
          item={item}
          trend={<MetricSparkline item={item} />}
          {...(renderLink ? { link: renderLink(item) } : {})}
        />
      ))}
    </div>
  );
}
