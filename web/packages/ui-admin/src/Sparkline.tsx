import { cx } from "@xingmang/ui-primitives";
import {
  buildSparkline,
  DEFAULT_SPARKLINE_BOX,
  type SparklineBox,
  type SparkSample,
} from "./sparklineGeometry";

export interface SparklineProps {
  samples: SparkSample[];
  /** 无障碍标签。折线本身读不出来，屏幕阅读器只能靠这句话知道画的是什么。 */
  label: string;
  box?: SparklineBox;
  className?: string;
  /** 样本不足以构成趋势时的占位文案。 */
  emptyText?: string;
}

/** 迷你趋势图。纯手写 SVG，零依赖。
 *
 *  两条纪律写进了形状里：
 *  1. 失败样本不进折线，只标一笔断点——失败时后端带回的是上一次成功的值，
 *     把它当成新观测画上去就是伪造读数（宪法 12 条）。
 *  2. 颜色一律走 currentColor + 令牌类（text-accent / text-danger），
 *     组件里不出现任何色值（规格 §7.7）。 */
export function Sparkline({
  samples,
  label,
  box = DEFAULT_SPARKLINE_BOX,
  className,
  emptyText = "暂无趋势",
}: SparklineProps) {
  const geometry = buildSparkline(samples, box);
  if (!geometry) {
    return (
      <p className={cx("text-xs text-fg-muted", className)} role="status">
        {emptyText}
      </p>
    );
  }

  const { segments, failedPoints, lastPoint } = geometry;
  // 失败标记用竖直短线而不是圆点：svg 被横向拉伸（preserveAspectRatio=none）时
  // 圆会变成椭圆，竖线不会变形
  const tick = Math.min(box.height / 4, 6);

  return (
    <svg
      role="img"
      aria-label={label}
      viewBox={`0 0 ${box.width} ${box.height}`}
      preserveAspectRatio="none"
      className={cx("block h-10 w-full text-accent", className)}
    >
      {segments.map((points) => (
        <polyline
          key={points}
          points={points}
          fill="none"
          stroke="currentColor"
          strokeWidth={1.5}
          strokeLinecap="round"
          strokeLinejoin="round"
          // 线宽不随拉伸变化，否则横向拉开后线会变成一条粗带
          vectorEffect="non-scaling-stroke"
        />
      ))}

      {lastPoint ? (
        <line
          x1={lastPoint.x}
          x2={lastPoint.x}
          y1={lastPoint.y - tick / 2}
          y2={lastPoint.y + tick / 2}
          stroke="currentColor"
          strokeWidth={2}
          strokeLinecap="round"
          vectorEffect="non-scaling-stroke"
        />
      ) : null}

      <g className="text-danger">
        {failedPoints.map((p) => (
          <line
            key={`${p.x},${p.y}`}
            x1={p.x}
            x2={p.x}
            y1={p.y - tick}
            y2={p.y + tick}
            stroke="currentColor"
            strokeWidth={2}
            strokeLinecap="round"
            vectorEffect="non-scaling-stroke"
          />
        ))}
      </g>
    </svg>
  );
}
