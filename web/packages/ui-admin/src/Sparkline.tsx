import { cx } from "@xingmang/ui-primitives";
import {
  buildSparkline,
  DEFAULT_SPARKLINE_BOX,
  describeSparkline,
  sparklineCaveats,
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

/** 部分数据的虚线样式（用户坐标系；vectorEffect 让它不随拉伸变形）。 */
const PARTIAL_DASH = "3 2";

/** 迷你趋势图。纯手写 SVG，零依赖。
 *
 *  三条纪律写进了形状里：
 *  1. 失败样本不进折线，只标一笔断点——失败时后端带回的是上一次成功的值，
 *     把它当成新观测画上去就是伪造读数（宪法 12 条）。
 *  2. 部分数据（is_partial）走**虚线 + 空心方块**，不是换个颜色了事：
 *     只靠色相区分完整性，色觉障碍者读不到，而「偏小的数」画成正常实线
 *     恰恰是最容易被当成真相的那种错（Codex #5）。
 *  3. 颜色一律走 currentColor + 令牌类（text-accent / text-danger / text-warning），
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

  const { segments, partialSegments, failedPoints, partialPoints, lastPoint } = geometry;
  // 失败标记用竖直短线而不是圆点：svg 被横向拉伸（preserveAspectRatio=none）时
  // 圆会变成椭圆，竖线不会变形
  const tick = Math.min(box.height / 4, 6);
  const caveats = sparklineCaveats(samples);

  return (
    <div className={cx("flex flex-col gap-0.5", className)}>
      <svg
        role="img"
        // 标签里带上方向与失败/部分数量：读屏用户拿不到形状，只能拿到这句话
        aria-label={`${label}：${describeSparkline(samples)}`}
        viewBox={`0 0 ${box.width} ${box.height}`}
        preserveAspectRatio="none"
        className="block h-10 w-full text-accent"
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

        <g className="text-warning">
          {partialSegments.map((points) => (
            <polyline
              key={`partial-${points}`}
              points={points}
              fill="none"
              stroke="currentColor"
              strokeWidth={1.5}
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeDasharray={PARTIAL_DASH}
              vectorEffect="non-scaling-stroke"
            />
          ))}
          {/* 空心竖标：与失败的实心竖标同位置不同「填法」，形状上就分得开 */}
          {partialPoints.map((p) => (
            <line
              key={`partial-point-${p.x},${p.y}`}
              x1={p.x}
              x2={p.x}
              y1={p.y - tick}
              y2={p.y + tick}
              stroke="currentColor"
              strokeWidth={2}
              strokeDasharray={PARTIAL_DASH}
              vectorEffect="non-scaling-stroke"
            />
          ))}
        </g>

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

      {/* 看得见的那一句：图上的红断点/黄虚线到底是什么，不能只写在 aria-label 里 */}
      {caveats ? <p className="text-xs text-fg-muted">{caveats}</p> : null}
    </div>
  );
}
