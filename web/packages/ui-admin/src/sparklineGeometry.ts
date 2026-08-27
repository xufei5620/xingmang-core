/** 迷你趋势图的几何计算。
 *
 *  独立成模块而不是塞进组件里：路径字符串是纯函数的产物，能直接断言，
 *  不必先把 SVG 渲染出来再去数属性。零依赖——一个折线图不值得引入图表库
 *  （VERSIONS.lock 锁死依赖，规格 §5.3）。 */

/** 一个时间序列样本。 */
export interface SparkSample {
  /** 横轴位置：观测时刻的毫秒时间戳。 */
  at: number;
  /** 主数值；null 表示这个样本没有可信数值。 */
  value: number | null;
  /** 这次同步是否失败。失败样本不参与折线，只在原位标一笔。 */
  failed: boolean;
}

/** 画布尺寸（用户坐标系，不是 CSS 像素）。 */
export interface SparklineBox {
  width: number;
  height: number;
  /** 内边距。线宽与端点标记要占地方，贴边画会被裁掉一半。 */
  padding: number;
}

export interface SparkPoint {
  x: number;
  y: number;
}

export interface SparklineGeometry {
  /** 折线段的 points 属性值。失败/缺值处折线断开，所以可能有多段。 */
  segments: string[];
  /** 失败样本的位置。 */
  failedPoints: SparkPoint[];
  /** 最后一个成功样本的位置，用来标出「当前值」落在哪。 */
  lastPoint: SparkPoint | null;
}

/** 少于两个成功样本就没有「趋势」可言——一个点连不成线，
 *  画一条水平线只会让人以为「很平稳」，那是编出来的结论。 */
export const MIN_TREND_POINTS = 2;

/** 默认画布。宽高比按「一眼看形状」选，不追求精读数值（精确值在卡片主位）。 */
export const DEFAULT_SPARKLINE_BOX: SparklineBox = { width: 240, height: 40, padding: 4 };

/** 保留两位小数：路径字符串因此稳定可断言，也不会长出一串浮点尾巴。 */
function round2(n: number): number {
  return Math.round(n * 100) / 100;
}

function isPlottable(s: SparkSample): boolean {
  return !s.failed && s.value !== null && Number.isFinite(s.value);
}

function numericValue(s: SparkSample): number | null {
  return s.value !== null && Number.isFinite(s.value) ? s.value : null;
}

/** 能画进折线的样本数（失败样本不算——失败时后端带回的是上一次成功的值，
 *  把它当成一次新观测画上去就是伪造了一个读数）。 */
export function plottableCount(samples: SparkSample[]): number {
  return samples.filter(isPlottable).length;
}

/** 计算折线几何；样本不足以构成趋势时返回 null，由调用方显示占位文案。
 *
 *  纵轴定义域取**全部**有数值的样本（含失败样本带回的旧值），这样失败标记
 *  一定落在框内，不需要钳制——钳制会把一个越界的值画在边上，看起来像正常读数。 */
export function buildSparkline(
  samples: SparkSample[],
  box: SparklineBox = DEFAULT_SPARKLINE_BOX,
): SparklineGeometry | null {
  if (plottableCount(samples) < MIN_TREND_POINTS) return null;

  // 契约说升序，这里再排一次：真收到乱序数据，画出来是锯齿状的纯噪音，
  // 而排序的代价只有一次 O(n log n)
  const ordered = [...samples].sort((a, b) => a.at - b.at);

  const innerW = Math.max(0, box.width - box.padding * 2);
  const innerH = Math.max(0, box.height - box.padding * 2);

  const times = ordered.map((s) => s.at);
  const tMin = Math.min(...times);
  const tMax = Math.max(...times);
  // 时间戳全相同（上游没给观测时刻）时退化成按序号等距排布：
  // 除以 0 会得到 NaN，而 NaN 进了路径字符串整条线都不会渲染
  const xOf = (s: SparkSample, index: number): number =>
    tMax === tMin
      ? box.padding + (ordered.length === 1 ? innerW / 2 : (index / (ordered.length - 1)) * innerW)
      : box.padding + ((s.at - tMin) / (tMax - tMin)) * innerW;

  const values = ordered.map(numericValue).filter((v): v is number => v !== null);
  const vMin = Math.min(...values);
  const vMax = Math.max(...values);
  // 值恒定时画在垂直中线：贴着上沿或下沿会被误读成「顶到极值了」
  const yOf = (v: number): number =>
    vMax === vMin ? box.padding + innerH / 2 : box.padding + (1 - (v - vMin) / (vMax - vMin)) * innerH;

  const segments: string[] = [];
  const failedPoints: SparkPoint[] = [];
  let current: string[] = [];
  let lastPoint: SparkPoint | null = null;

  // 孤立的成功样本（前后都是失败）也要留下痕迹：单点 polyline 什么都不画，
  // 所以把坐标写两遍，配合 round linecap 渲染成一个点。丢掉它等于宣称
  // 那个时刻没有采集成功过
  const flush = () => {
    if (current.length >= MIN_TREND_POINTS) segments.push(current.join(" "));
    else if (current.length === 1) segments.push(`${current[0]} ${current[0]}`);
    current = [];
  };

  ordered.forEach((s, index) => {
    const x = round2(xOf(s, index));
    const v = numericValue(s);

    if (isPlottable(s) && v !== null) {
      const point = { x, y: round2(yOf(v)) };
      current.push(`${point.x},${point.y}`);
      lastPoint = point;
      return;
    }

    // 失败或缺值：折线在此断开。断开本身就是信息——
    // 把两端直接连起来等于宣称中间那段数据是连续的
    flush();
    if (v !== null) failedPoints.push({ x, y: round2(yOf(v)) });
  });
  flush();

  return { segments, failedPoints, lastPoint };
}
